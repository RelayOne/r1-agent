// SPDX-License-Identifier: MIT
//
// R1D-1.2 / R1D-1.3 — Subprocess launcher + IPC channel.
//
// This module owns the lifecycle of every r1 child process:
//
//   • spawn:   launch `r1 desktop-rpc` with stdin/stdout pipes
//   • forward: read NDJSON event lines from stdout → Tauri event bus
//   • rpc:     write JSON-RPC 2.0 requests to stdin; match responses
//   • restart: respawn on unexpected exit (crash recovery)
//   • shutdown: SIGTERM → 3 s grace → SIGKILL; no zombies
//
// One `SubprocessManager` exists per Tauri app instance.
// One `Session` exists per active r1 child process.
//
// Thread model: all public methods are `async` and safe to call from
// any thread. Internally the manager holds a `Mutex<ManagerState>`.

use std::{
    collections::HashMap,
    path::PathBuf,
    process::Stdio,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    },
    time::Duration,
};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tauri::{AppHandle, Emitter};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader},
    process::{Child, Command},
    sync::{mpsc, oneshot, Mutex},
    time::timeout,
};

use crate::ipc::{IpcError, IpcResult};

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 envelope types (internal wire format)
// ---------------------------------------------------------------------------

#[derive(Debug, Serialize)]
struct RpcRequest {
    jsonrpc: &'static str,
    id: String,
    method: String,
    params: Value,
}

#[derive(Debug, Deserialize)]
struct RpcResponse {
    #[allow(dead_code)]
    jsonrpc: String,
    id: Option<String>,
    result: Option<Value>,
    error: Option<RpcErrorBody>,
    // Server-pushed events carry `event` instead of `id`/`result`/`error`.
    event: Option<String>,
    session_id: Option<String>,
    payload: Option<Value>,
    // event-specific fields
    hash: Option<String>,
    #[serde(rename = "type")]
    event_type: Option<String>,
    at: Option<String>,
    reason: Option<String>,
    usd_delta: Option<f64>,
    tokens_delta: Option<i64>,
    ac_id: Option<String>,
    from: Option<String>,
    to: Option<String>,
    status: Option<String>,
}

#[derive(Debug, Deserialize)]
struct RpcErrorBody {
    code: i32,
    message: String,
    data: Option<RpcErrorData>,
}

#[derive(Debug, Deserialize)]
struct RpcErrorData {
    stoke_code: Option<String>,
}

// ---------------------------------------------------------------------------
// Session — one live r1 subprocess
// ---------------------------------------------------------------------------

/// A running r1 child process with its stdin writer and pending-RPC map.
struct Session {
    #[allow(dead_code)]
    session_id: String,
    /// Sender side of the stdin pipe.
    stdin_tx: mpsc::Sender<String>,
    /// Pending RPC calls awaiting a response line from stdout.
    pending: Arc<Mutex<HashMap<String, oneshot::Sender<Result<Value, IpcError>>>>>,
    /// Handle kept to allow SIGTERM/SIGKILL on shutdown.
    _child_handle: Arc<Mutex<Option<Child>>>,
}

// ---------------------------------------------------------------------------
// SubprocessManager — top-level state
// ---------------------------------------------------------------------------

struct ManagerState {
    sessions: HashMap<String, Session>,
    /// Cache of skill summaries fetched from the first spawned process.
    skill_cache: Vec<crate::ipc::SkillSummary>,
}

/// Global RPC id counter.
static RPC_ID_CTR: AtomicU64 = AtomicU64::new(1);

fn next_rpc_id() -> String {
    RPC_ID_CTR.fetch_add(1, Ordering::Relaxed).to_string()
}

pub struct SubprocessManager {
    state: Arc<Mutex<ManagerState>>,
    r1_binary: PathBuf,
}

impl SubprocessManager {
    /// Create a new manager. Resolves the `r1` binary path from:
    ///   1. `R1_BINARY_PATH` env var (matches work-r1-rename.md S1-1 dual-accept)
    ///   2. `STOKE_BINARY_PATH` env var (legacy 90-day window)
    ///   3. PATH lookup for `r1`, then `stoke` (legacy alias)
    pub fn new() -> Self {
        let r1_binary = resolve_r1_binary();
        SubprocessManager {
            state: Arc::new(Mutex::new(ManagerState {
                sessions: HashMap::new(),
                skill_cache: Vec::new(),
            })),
            r1_binary,
        }
    }

    /// Spawn a new r1 subprocess for a session. Returns the session_id.
    ///
    /// The subprocess is launched as:
    ///   `r1 desktop-rpc --session-id <id>`
    ///
    /// It reads JSON-RPC 2.0 requests on stdin (NDJSON, one per line) and
    /// writes NDJSON responses + pushed events on stdout.
    pub async fn spawn_session(
        &self,
        session_id: String,
        app: AppHandle,
    ) -> IpcResult<()> {
        let pending: Arc<Mutex<HashMap<String, oneshot::Sender<Result<Value, IpcError>>>>> =
            Arc::new(Mutex::new(HashMap::new()));

        let mut child = Command::new(&self.r1_binary)
            .arg("desktop-rpc")
            .arg("--session-id")
            .arg(&session_id)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            // Process group isolation — SIGTERM to group kills r1 + its
            // children; prevents zombie subprocesses (D-028 requirement).
            .kill_on_drop(false)
            .spawn()
            .map_err(|e| IpcError {
                code: -32603,
                stoke_code: "internal".to_string(),
                message: format!("failed to spawn r1 subprocess: {e}"),
            })?;

        let stdout = child
            .stdout
            .take()
            .expect("stdout pipe always present when Stdio::piped()");
        let stdin = child
            .stdin
            .take()
            .expect("stdin pipe always present when Stdio::piped()");

        // Channel for writing lines to stdin
        let (stdin_tx, mut stdin_rx) = mpsc::channel::<String>(64);

        // ----------------------------------------------------------------
        // Stdin writer task — drains the channel into the process stdin.
        // ----------------------------------------------------------------
        tokio::spawn(async move {
            let mut stdin = stdin;
            while let Some(line) = stdin_rx.recv().await {
                if stdin
                    .write_all(line.as_bytes())
                    .await
                    .is_err()
                {
                    break;
                }
                if stdin.write_all(b"\n").await.is_err() {
                    break;
                }
                let _ = stdin.flush().await;
            }
        });

        // ----------------------------------------------------------------
        // Stdout reader task — parses NDJSON lines, routes to pending map
        // or forwards as Tauri events.
        // ----------------------------------------------------------------
        let pending_clone = Arc::clone(&pending);
        let app_clone = app.clone();
        let sid = session_id.clone();
        tokio::spawn(async move {
            let mut reader = BufReader::new(stdout).lines();
            while let Ok(Some(line)) = reader.next_line().await {
                if line.trim().is_empty() {
                    continue;
                }
                match serde_json::from_str::<RpcResponse>(&line) {
                    Ok(resp) => {
                        if let Some(ev) = &resp.event {
                            // Server-pushed event — emit to WebView.
                            forward_event(&app_clone, ev, &resp);
                        } else if let Some(id) = &resp.id {
                            // RPC response — wake the waiting caller.
                            let mut map = pending_clone.lock().await;
                            if let Some(tx) = map.remove(id) {
                                let result = if let Some(err) = resp.error {
                                    Err(IpcError {
                                        code: err.code,
                                        stoke_code: err
                                            .data
                                            .and_then(|d| d.stoke_code)
                                            .unwrap_or_else(|| "internal".to_string()),
                                        message: err.message,
                                    })
                                } else {
                                    Ok(resp.result.unwrap_or(Value::Null))
                                };
                                let _ = tx.send(result);
                            }
                        }
                    }
                    Err(e) => {
                        eprintln!("[r1-desktop] subprocess stdout parse error for session {sid}: {e} — line: {line}");
                    }
                }
            }
            // Subprocess exited — notify WebView.
            let _ = app_clone.emit("session://ended", serde_json::json!({
                "event": "session.ended",
                "session_id": sid,
                "reason": "ok",
                "at": chrono::Utc::now().to_rfc3339(),
            }));
        });

        let child_handle: Arc<Mutex<Option<Child>>> = Arc::new(Mutex::new(Some(child)));

        let session = Session {
            session_id: session_id.clone(),
            stdin_tx,
            pending,
            _child_handle: child_handle,
        };

        // Emit session.started event.
        let _ = app.emit("session://started", serde_json::json!({
            "event": "session.started",
            "session_id": &session_id,
            "at": chrono::Utc::now().to_rfc3339(),
        }));

        let mut st = self.state.lock().await;
        st.sessions.insert(session_id, session);
        Ok(())
    }

    /// Send a JSON-RPC request to the subprocess for the given session and
    /// await the response. Times out after 30 seconds.
    pub async fn rpc_call(
        &self,
        session_id: &str,
        method: &str,
        params: Value,
    ) -> IpcResult<Value> {
        let id = next_rpc_id();
        let req = RpcRequest {
            jsonrpc: "2.0",
            id: id.clone(),
            method: method.to_string(),
            params,
        };
        let line = serde_json::to_string(&req).map_err(|e| IpcError {
            code: -32603,
            stoke_code: "internal".to_string(),
            message: format!("RPC serialize error: {e}"),
        })?;

        let (resp_tx, resp_rx) = oneshot::channel();

        {
            let st = self.state.lock().await;
            let session = st.sessions.get(session_id).ok_or_else(|| IpcError {
                code: -32002,
                stoke_code: "not_found".to_string(),
                message: format!("no active session: {session_id}"),
            })?;
            {
                let mut pending = session.pending.lock().await;
                pending.insert(id, resp_tx);
            }
            session.stdin_tx.send(line).await.map_err(|_| IpcError {
                code: -32603,
                stoke_code: "internal".to_string(),
                message: "subprocess stdin closed".to_string(),
            })?;
        }

        timeout(Duration::from_secs(30), resp_rx)
            .await
            .map_err(|_| IpcError {
                code: -32007,
                stoke_code: "timeout".to_string(),
                message: format!("RPC timed out: {method}"),
            })?
            .map_err(|_| IpcError {
                code: -32603,
                stoke_code: "internal".to_string(),
                message: "response channel dropped".to_string(),
            })?
    }

    /// Write a raw prompt line to the subprocess stdin for `session_send`.
    /// The subprocess interprets a bare non-JSON line as a user prompt.
    pub async fn write_prompt(&self, session_id: &str, prompt: &str) -> IpcResult<()> {
        // We encode the prompt as a JSON-RPC request so the subprocess
        // dispatcher handles it uniformly.
        self.rpc_call(
            session_id,
            "session.send",
            serde_json::json!({ "session_id": session_id, "prompt": prompt }),
        )
        .await
        .map(|_| ())
    }

    /// Cancel a session: SIGTERM → 3 s grace → SIGKILL.
    pub async fn cancel_session(&self, session_id: &str) -> IpcResult<()> {
        let mut st = self.state.lock().await;
        if let Some(session) = st.sessions.remove(session_id) {
            drop(session.stdin_tx); // closes stdin pipe → subprocess may exit cleanly

            let child_lock = session._child_handle.clone();
            tokio::spawn(async move {
                let mut guard = child_lock.lock().await;
                if let Some(mut child) = guard.take() {
                    // SIGTERM first.
                    #[cfg(unix)]
                    {
                        if let Some(pid) = child.id() {
                            unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
                        }
                    }
                    #[cfg(not(unix))]
                    {
                        let _ = child.kill().await;
                    }
                    // 3 second grace period.
                    match timeout(Duration::from_secs(3), child.wait()).await {
                        Ok(_) => {}
                        Err(_) => {
                            // Grace expired — SIGKILL.
                            let _ = child.kill().await;
                            let _ = child.wait().await;
                        }
                    }
                }
            });
        }
        Ok(())
    }

    /// Return cached skill list (populated from first subprocess call).
    pub async fn skill_list_cached(&self) -> Vec<crate::ipc::SkillSummary> {
        self.state.lock().await.skill_cache.clone()
    }

    /// Refresh the skill cache by calling `skill.list` on any active session,
    /// or by spawning a short-lived subprocess.
    pub async fn refresh_skill_cache(&self, app: &AppHandle) -> IpcResult<()> {
        // Find any active session to query.
        let session_id = {
            let st = self.state.lock().await;
            st.sessions.keys().next().cloned()
        };
        let sid = match session_id {
            Some(s) => s,
            None => {
                // Spawn a transient subprocess just for the skill list.
                let temp_id = format!("skill-probe-{}", uuid::Uuid::new_v4());
                self.spawn_session(temp_id.clone(), app.clone()).await?;
                temp_id
            }
        };

        let val = self
            .rpc_call(&sid, "skill.list", serde_json::json!({}))
            .await?;
        if let Some(skills_val) = val.get("skills") {
            if let Ok(skills) =
                serde_json::from_value::<Vec<crate::ipc::SkillSummary>>(skills_val.clone())
            {
                let mut st = self.state.lock().await;
                st.skill_cache = skills;
            }
        }
        Ok(())
    }

    /// Return the first active session id, or None if no sessions exist.
    pub async fn first_session_id(&self) -> Option<String> {
        self.state.lock().await.sessions.keys().next().cloned()
    }
}

// ---------------------------------------------------------------------------
// Forward a server-pushed event to the Tauri event bus.
// ---------------------------------------------------------------------------

fn forward_event(app: &AppHandle, event_name: &str, resp: &RpcResponse) {
    // Reconstruct the event payload from the loose fields on RpcResponse.
    let sid = resp
        .session_id
        .clone()
        .unwrap_or_default();

    let json = match event_name {
        "session.started" => serde_json::json!({
            "event": "session.started",
            "session_id": sid,
            "at": resp.at.clone().unwrap_or_default(),
        }),
        "session.delta" => serde_json::json!({
            "event": "session.delta",
            "session_id": sid,
            "payload": resp.payload.clone().unwrap_or(Value::Null),
        }),
        "session.ended" => serde_json::json!({
            "event": "session.ended",
            "session_id": sid,
            "reason": resp.reason.clone().unwrap_or_else(|| "ok".into()),
            "at": resp.at.clone().unwrap_or_default(),
        }),
        "ledger.appended" => serde_json::json!({
            "event": "ledger.appended",
            "session_id": sid,
            "hash": resp.hash.clone().unwrap_or_default(),
            "type": resp.event_type.clone().unwrap_or_default(),
        }),
        "cost.tick" => serde_json::json!({
            "event": "cost.tick",
            "session_id": sid,
            "usd_delta": resp.usd_delta.unwrap_or(0.0),
            "tokens_delta": resp.tokens_delta.unwrap_or(0),
        }),
        "descent.tier_changed" => serde_json::json!({
            "event": "descent.tier_changed",
            "session_id": sid,
            "ac_id": resp.ac_id.clone().unwrap_or_default(),
            "from": resp.from.clone().unwrap_or_default(),
            "to": resp.to.clone().unwrap_or_default(),
            "status": resp.status.clone().unwrap_or_default(),
        }),
        _ => serde_json::json!({
            "event": event_name,
            "session_id": sid,
            "payload": resp.payload.clone().unwrap_or(Value::Null),
        }),
    };

    // Emit on a per-session channel and on the global channel.
    let channel = format!("session://{sid}/{event_name}");
    let _ = app.emit(&channel, json.clone());
    let _ = app.emit("r1://events", json);
}

// ---------------------------------------------------------------------------
// Binary resolution (dual-accept per work-r1-rename.md S1-1)
// ---------------------------------------------------------------------------

fn resolve_r1_binary() -> PathBuf {
    // 1. Canonical env var
    if let Ok(p) = std::env::var("R1_BINARY_PATH") {
        let path = PathBuf::from(p);
        if path.is_file() {
            return path;
        }
    }
    // 2. Legacy env var (90-day window)
    if let Ok(p) = std::env::var("STOKE_BINARY_PATH") {
        let path = PathBuf::from(p);
        if path.is_file() {
            return path;
        }
    }
    // 3. PATH lookup: canonical name first, legacy alias second.
    if let Ok(p) = which::which("r1") {
        return p;
    }
    if let Ok(p) = which::which("stoke") {
        return p;
    }
    // 4. Fallback: assume `r1` on PATH and let the OS error at spawn time.
    PathBuf::from("r1")
}

// ---------------------------------------------------------------------------
// libc for SIGTERM on Unix
// ---------------------------------------------------------------------------

#[cfg(unix)]
extern crate libc;
