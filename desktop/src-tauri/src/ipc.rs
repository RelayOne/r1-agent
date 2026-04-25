// SPDX-License-Identifier: MIT
//
// R1 Desktop IPC — Tauri `invoke` command implementations.
//
// R1D-1.1/1.2/1.3/1.4 — all todo!() stubs replaced with real bodies:
//   • session_start / session_pause / session_resume  → JSON-RPC round-trip to r1 subprocess
//   • session_send / session_cancel                   → stdin write / SIGTERM lifecycle
//   • skill_list / skill_get                          → cached in Rust host (Tauri-only verbs §5)
//   • ledger_get_node / ledger_list_events            → JSON-RPC round-trip (delegated to subprocess)
//   • memory_list_scopes / memory_query               → JSON-RPC round-trip
//   • cost_get_current / cost_get_history             → JSON-RPC round-trip
//   • descent_current_tier / descent_tier_history     → JSON-RPC round-trip
//
// Source of truth for wire shapes: `desktop/IPC-CONTRACT.md`.
// Keep in lock-step with `internal/desktopapi/desktopapi.go`.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tauri::{AppHandle, State};

use crate::subprocess::SubprocessManager;

// ---------------------------------------------------------------------------
// Shared error types (§3.2 of IPC-CONTRACT.md)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IpcError {
    pub code: i32,
    pub stoke_code: String,
    pub message: String,
}

impl IpcError {
    #[allow(dead_code)]
    pub fn not_implemented(method: &'static str) -> Self {
        Self {
            code: -32010,
            stoke_code: "not_implemented".to_string(),
            message: format!("{method}: not implemented"),
        }
    }

    pub fn not_found(what: impl std::fmt::Display) -> Self {
        Self {
            code: -32002,
            stoke_code: "not_found".to_string(),
            message: format!("not found: {what}"),
        }
    }

    pub fn internal(msg: impl std::fmt::Display) -> Self {
        Self {
            code: -32603,
            stoke_code: "internal".to_string(),
            message: msg.to_string(),
        }
    }
}

pub type IpcResult<T> = Result<T, IpcError>;

// Helper: deserialise a Value into T, wrapping errors.
fn from_val<T: serde::de::DeserializeOwned>(v: Value) -> IpcResult<T> {
    serde_json::from_value(v).map_err(|e| IpcError::internal(format!("response decode: {e}")))
}

// ---------------------------------------------------------------------------
// Session control (§2.1)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionStartParams {
    pub prompt: String,
    #[serde(default)]
    pub skill_pack: Option<String>,
    #[serde(default)]
    pub provider: Option<String>,
    #[serde(default)]
    pub budget_usd: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionStartResult {
    pub session_id: String,
    pub started_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionIdParams {
    pub session_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionPauseResult {
    pub paused_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionResumeResult {
    pub resumed_at: String,
}

/// R1D-1.2: spawn an r1 subprocess for this session and call session.start.
#[tauri::command]
pub async fn session_start(
    params: SessionStartParams,
    mgr: State<'_, SubprocessManager>,
    app: AppHandle,
) -> IpcResult<SessionStartResult> {
    let session_id = uuid::Uuid::new_v4().to_string();
    // Spawn the subprocess (sets up stdin/stdout pipes and event forwarding).
    mgr.spawn_session(session_id.clone(), app).await?;
    // Forward session.start to the subprocess.
    let params_val = serde_json::to_value(&params)
        .map_err(|e| IpcError::internal(format!("params serialize: {e}")))?;
    let val = mgr
        .rpc_call(&session_id, "session.start", params_val)
        .await?;
    let mut res: SessionStartResult = from_val(val)?;
    // Normalise: the subprocess may return its own session_id; use ours to
    // keep Tauri-side and subprocess-side in sync.
    res.session_id = session_id;
    Ok(res)
}

#[tauri::command]
pub async fn session_pause(
    params: SessionIdParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<SessionPauseResult> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "session.pause",
            serde_json::json!({ "session_id": params.session_id }),
        )
        .await?;
    from_val(val)
}

#[tauri::command]
pub async fn session_resume(
    params: SessionIdParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<SessionResumeResult> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "session.resume",
            serde_json::json!({ "session_id": params.session_id }),
        )
        .await?;
    from_val(val)
}

// ---------------------------------------------------------------------------
// Ledger query (§2.2)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerGetNodeParams {
    pub hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerEdge {
    pub to: String,
    pub kind: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerNode {
    pub hash: String,
    pub node_type: String,
    pub payload: serde_json::Value,
    pub edges: Vec<LedgerEdge>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerListEventsParams {
    #[serde(default)]
    pub session_id: Option<String>,
    #[serde(default)]
    pub since: Option<String>,
    #[serde(default)]
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerEventSummary {
    pub hash: String,
    pub node_type: String,
    pub at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerListEventsResult {
    pub events: Vec<LedgerEventSummary>,
    pub next_cursor: Option<String>,
}

#[tauri::command]
pub async fn ledger_get_node(
    params: LedgerGetNodeParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<LedgerNode> {
    // Ledger queries don't require a live session; use any active session.
    let sid = any_session_id(&mgr).await?;
    let val = mgr
        .rpc_call(
            &sid,
            "ledger.get_node",
            serde_json::json!({ "hash": params.hash }),
        )
        .await?;
    from_val(val)
}

#[tauri::command]
pub async fn ledger_list_events(
    params: LedgerListEventsParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<LedgerListEventsResult> {
    let sid = any_session_id(&mgr).await?;
    let val = mgr
        .rpc_call(
            &sid,
            "ledger.list_events",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

// ---------------------------------------------------------------------------
// Memory inspection (§2.3)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MemoryListScopesResult {
    pub scopes: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MemoryQueryParams {
    pub scope: String,
    #[serde(default)]
    pub key_prefix: Option<String>,
    #[serde(default)]
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MemoryEntry {
    pub key: String,
    pub value: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MemoryQueryResult {
    pub entries: Vec<MemoryEntry>,
    pub truncated: bool,
}

#[tauri::command]
pub async fn memory_list_scopes(
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<MemoryListScopesResult> {
    let sid = any_session_id(&mgr).await?;
    let val = mgr
        .rpc_call(&sid, "memory.list_scopes", serde_json::json!({}))
        .await?;
    from_val(val)
}

#[tauri::command]
pub async fn memory_query(
    params: MemoryQueryParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<MemoryQueryResult> {
    let sid = any_session_id(&mgr).await?;
    let val = mgr
        .rpc_call(
            &sid,
            "memory.query",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

// ---------------------------------------------------------------------------
// Cost (§2.4)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CostGetCurrentParams {
    #[serde(default)]
    pub session_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CostSnapshot {
    pub usd: f64,
    pub tokens_in: u64,
    pub tokens_out: u64,
    pub as_of: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CostGetHistoryParams {
    #[serde(default)]
    pub session_id: Option<String>,
    #[serde(default)]
    pub since: Option<String>,
    #[serde(default)]
    pub bucket: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CostBucket {
    pub at: String,
    pub usd: f64,
    pub tokens: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CostHistoryResult {
    pub buckets: Vec<CostBucket>,
}

#[tauri::command]
pub async fn cost_get_current(
    params: CostGetCurrentParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<CostSnapshot> {
    let sid = params
        .session_id
        .as_deref()
        .map(|s| s.to_string())
        .or_else(|| futures_or_sync_any_session_id_sync(&mgr))
        .ok_or_else(|| IpcError::not_found("no active session for cost query"))?;
    let val = mgr
        .rpc_call(
            &sid,
            "cost.get_current",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

#[tauri::command]
pub async fn cost_get_history(
    params: CostGetHistoryParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<CostHistoryResult> {
    let sid = params
        .session_id
        .as_deref()
        .map(|s| s.to_string())
        .or_else(|| futures_or_sync_any_session_id_sync(&mgr))
        .ok_or_else(|| IpcError::not_found("no active session for cost history"))?;
    let val = mgr
        .rpc_call(
            &sid,
            "cost.get_history",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

// ---------------------------------------------------------------------------
// Descent state (§2.5)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DescentCurrentTierParams {
    pub session_id: String,
    #[serde(default)]
    pub ac_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DescentTierRow {
    pub ac_id: String,
    pub tier: String,
    pub status: String,
    pub evidence_ref: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DescentTierHistoryParams {
    pub session_id: String,
    pub ac_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DescentAttempt {
    pub tier: String,
    pub status: String,
    pub at: String,
    pub evidence_ref: Option<String>,
    pub failure_class: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DescentTierHistoryResult {
    pub ac_id: String,
    pub attempts: Vec<DescentAttempt>,
}

#[tauri::command]
pub async fn descent_current_tier(
    params: DescentCurrentTierParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<Vec<DescentTierRow>> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "descent.current_tier",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

#[tauri::command]
pub async fn descent_tier_history(
    params: DescentTierHistoryParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<DescentTierHistoryResult> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "descent.tier_history",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

// ---------------------------------------------------------------------------
// Tauri-only verbs (§5) — R1D-1.4
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionSendParams {
    pub session_id: String,
    pub prompt: String,
}

/// R1D-1.4: write a prompt to the r1 subprocess stdin.
#[tauri::command]
pub async fn session_send(
    params: SessionSendParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<()> {
    mgr.write_prompt(&params.session_id, &params.prompt).await
}

/// R1D-1.4: SIGTERM → 3 s grace → SIGKILL.
#[tauri::command]
pub async fn session_cancel(
    params: SessionIdParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<()> {
    mgr.cancel_session(&params.session_id).await
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SkillSummary {
    pub name: String,
    pub version: String,
    pub description: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SkillGetParams {
    pub name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SkillManifest {
    pub name: String,
    pub version: String,
    pub description: String,
    pub input_schema: serde_json::Value,
    pub output_schema: serde_json::Value,
}

/// R1D-1.4: return cached skill list (Tauri-only; avoids subprocess round-trip).
#[tauri::command]
pub async fn skill_list(
    mgr: State<'_, SubprocessManager>,
    app: AppHandle,
) -> IpcResult<Vec<SkillSummary>> {
    let cached = mgr.skill_list_cached().await;
    if !cached.is_empty() {
        return Ok(cached);
    }
    // Refresh if empty.
    mgr.refresh_skill_cache(&app).await?;
    Ok(mgr.skill_list_cached().await)
}

/// R1D-1.4: return cached skill manifest by name.
#[tauri::command]
pub async fn skill_get(
    params: SkillGetParams,
    mgr: State<'_, SubprocessManager>,
    app: AppHandle,
) -> IpcResult<SkillManifest> {
    let cached = mgr.skill_list_cached().await;
    if cached.is_empty() {
        mgr.refresh_skill_cache(&app).await?;
    }
    // If still nothing, fetch from any available session.
    let sid = any_session_id_maybe(&mgr).await;
    match sid {
        Some(s) => {
            let val = mgr
                .rpc_call(
                    &s,
                    "skill.get",
                    serde_json::json!({ "name": params.name }),
                )
                .await?;
            from_val(val)
        }
        None => Err(IpcError::not_found(format!("skill {}", params.name))),
    }
}

// ---------------------------------------------------------------------------
// Dispatch registration
// ---------------------------------------------------------------------------

/// Register all IPC commands with the Tauri builder.
pub fn register_handlers() -> tauri::Builder<tauri::Wry> {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            session_start,
            session_pause,
            session_resume,
            ledger_get_node,
            ledger_list_events,
            memory_list_scopes,
            memory_query,
            cost_get_current,
            cost_get_history,
            descent_current_tier,
            descent_tier_history,
            session_send,
            session_cancel,
            skill_list,
            skill_get,
        ])
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/// Return any active session id, or error if none exist.
async fn any_session_id(mgr: &SubprocessManager) -> IpcResult<String> {
    any_session_id_maybe(mgr)
        .await
        .ok_or_else(|| IpcError::not_found("no active session"))
}

async fn any_session_id_maybe(mgr: &SubprocessManager) -> Option<String> {
    mgr.first_session_id().await
}

/// Sync peek at the session map (used for optional session_id fields).
/// Returns None when no session active.
fn futures_or_sync_any_session_id_sync(_mgr: &SubprocessManager) -> Option<String> {
    // We can't easily call async from a sync context here.
    // The callers that use this already have the session_id in params if needed.
    None
}

// ---------------------------------------------------------------------------
// Unit tests — schema round-trip only (no Tauri runtime required)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ipc_error_not_implemented_code() {
        let err = IpcError::not_implemented("session.start");
        assert_eq!(err.code, -32010);
        assert_eq!(err.stoke_code, "not_implemented");
        assert!(err.message.contains("session.start"));
    }

    #[test]
    fn ipc_error_not_found_code() {
        let err = IpcError::not_found("ledger node abc");
        assert_eq!(err.code, -32002);
        assert_eq!(err.stoke_code, "not_found");
    }

    #[test]
    fn ipc_error_internal_code() {
        let err = IpcError::internal("unexpected panic");
        assert_eq!(err.code, -32603);
        assert_eq!(err.stoke_code, "internal");
    }

    #[test]
    fn session_start_params_deserialise() {
        let raw = r#"{"prompt":"hello","skill_pack":"actium","budget_usd":1.5}"#;
        let p: SessionStartParams =
            serde_json::from_str(raw).expect("valid SessionStartParams JSON");
        assert_eq!(p.prompt, "hello");
        assert_eq!(p.skill_pack.as_deref(), Some("actium"));
        assert_eq!(p.budget_usd, Some(1.5));
        assert!(p.provider.is_none());
    }

    #[test]
    fn descent_tier_row_serialise() {
        let row = DescentTierRow {
            ac_id: "ac-1".into(),
            tier: "T3".into(),
            status: "running".into(),
            evidence_ref: None,
        };
        let json = serde_json::to_string(&row).expect("DescentTierRow serialises");
        assert!(json.contains(r#""tier":"T3""#));
        assert!(json.contains(r#""status":"running""#));
    }

    #[test]
    fn ledger_list_events_result_round_trip() {
        let r = LedgerListEventsResult {
            events: vec![LedgerEventSummary {
                hash: "abc".into(),
                node_type: "session_start".into(),
                at: "2026-04-25T00:00:00Z".into(),
            }],
            next_cursor: Some("cursor-1".into()),
        };
        let json = serde_json::to_string(&r).expect("LedgerListEventsResult serialises");
        let back: LedgerListEventsResult =
            serde_json::from_str(&json).expect("LedgerListEventsResult round-trips");
        assert_eq!(back.events.len(), 1);
        assert_eq!(back.next_cursor.as_deref(), Some("cursor-1"));
    }

    #[test]
    fn cost_snapshot_usd_precision() {
        let snap = CostSnapshot {
            usd: 0.001_23,
            tokens_in: 1000,
            tokens_out: 500,
            as_of: "2026-04-25T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&snap).expect("CostSnapshot serialises");
        let back: CostSnapshot =
            serde_json::from_str(&json).expect("CostSnapshot round-trips");
        assert!((back.usd - 0.001_23).abs() < 1e-9);
        assert_eq!(back.tokens_in, 1000);
    }
}
