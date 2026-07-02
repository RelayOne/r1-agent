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

use crate::discovery_state::{DaemonStatusSnapshot, DiscoveryState};
use crate::subprocess::SubprocessManager;

// ---------------------------------------------------------------------------
// Shared error types — re-exported from crate::errors so the bin
// surface keeps the historical `ipc::IpcError` import path working.
// ---------------------------------------------------------------------------

pub use crate::errors::{IpcError, IpcResult};

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
    // Per audit/scan-rust-stubs.md item #6: fall back to the most
    // recent active session when caller omits `session_id`. The
    // previous helper returned `None` unconditionally, so an explicit
    // `not_found` fired even when sessions were live.
    let sid = match params.session_id.clone() {
        Some(s) => s,
        None => any_session_id_maybe(&mgr)
            .await
            .ok_or_else(|| IpcError::not_found("no active session for cost query"))?,
    };
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
    let sid = match params.session_id.clone() {
        Some(s) => s,
        None => any_session_id_maybe(&mgr)
            .await
            .ok_or_else(|| IpcError::not_found("no active session for cost history"))?,
    };
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

/// `app.discovery_status` — snapshot of the daemon-discovery state.
///
/// Distinct from `daemon_status`, which RPCs the daemon for its
/// session-side view. This verb returns the host-side discovery
/// result (whether `discover_or_spawn` succeeded at startup, what
/// transport mode the desktop is using, last error if any). Backs
/// the React DaemonStatus banner per spec desktop-cortex-augmentation
/// §5 lifecycle steps 1-4.
///
/// Tracked: GH issue #145.
#[tauri::command]
pub async fn app_discovery_status(
    state: State<'_, DiscoveryState>,
) -> IpcResult<DaemonStatusSnapshot> {
    Ok(state.snapshot())
}

/// `daemon_install_command` — return the host-OS-appropriate
/// `r1 serve --install ...` command string that the discovery wizard
/// shows in its copy-paste code box (spec
/// desktop-cortex-augmentation §5 lifecycle step 4).
///
/// Wired per audit/scan-rust-stubs.md item #1: the WebView called this
/// verb but no Rust handler existed, panicking the wizard with a
/// missing-handler error. Delegates to `discovery::install_command_for_host_os`.
#[tauri::command]
pub async fn daemon_install_command() -> IpcResult<String> {
    Ok(crate::discovery::install_command_for_host_os())
}

// ---------------------------------------------------------------------------
// Discovery wizard verbs (audit/complete-systems-2026-07-01.md A033)
//
// discovery-wizard-mount.tsx invokes these three commands; before this
// block landed none of them existed on the Rust side, so the wizard
// was unreachable in Tauri builds and Reconnect could never succeed.
// ---------------------------------------------------------------------------

/// Map a typed `DiscoveryError` onto the structured IPC taxonomy so
/// the TS `handleReconnect` catch block sees `{code, stoke_code,
/// message}` instead of an opaque string.
fn discovery_error_to_ipc(err: &crate::discovery::DiscoveryError) -> IpcError {
    use crate::discovery::DiscoveryError as DE;
    match err {
        DE::NotFound => IpcError::not_found("daemon (no usable ~/.r1/daemon.json and sidecar unavailable)"),
        DE::Refused | DE::BadHandshake(_) | DE::SidecarSpawn(_) => {
            IpcError::internal(err.to_string())
        }
    }
}

/// `daemon_config_exists` — true when `~/.r1/daemon.json` is present
/// and parseable. The discovery wizard shows itself only when this
/// returns false (spec desktop-cortex-augmentation §5 lifecycle step
/// 4). A present-but-corrupt file also reports false: the config is
/// unusable, so the wizard's install/reconnect guidance applies.
#[tauri::command]
pub async fn daemon_config_exists() -> IpcResult<bool> {
    Ok(crate::discovery::read_daemon_json().is_ok())
}

/// `daemon_reconnect` — re-run the discovery orchestration
/// (`discovery::discover_or_spawn`) on user demand and publish the
/// result into `DiscoveryState`, exactly as `main.rs setup_discovery`
/// does at startup. Returns the refreshed snapshot on success;
/// surfaces a structured IpcError on failure so the wizard can stay
/// open with an actionable message.
#[tauri::command]
pub async fn daemon_reconnect(app: AppHandle) -> IpcResult<DaemonStatusSnapshot> {
    use tauri::Manager;
    {
        let state: State<'_, DiscoveryState> = app.state();
        state.mark_pending();
    }
    match crate::discovery::discover_or_spawn(&app).await {
        Ok(handle) => {
            let state: State<'_, DiscoveryState> = app.state();
            state.set_handle(handle);
            Ok(state.snapshot())
        }
        Err(err) => {
            let state: State<'_, DiscoveryState> = app.state();
            state.set_error(err.to_string());
            Err(discovery_error_to_ipc(&err))
        }
    }
}

/// `daemon_accept_sidecar` — record that the user accepted the
/// already-auto-spawned bundled sidecar (wizard "Use bundled"
/// button). The sidecar keeps running either way; this stores the
/// consent flag in `DiscoveryState` and returns the current transport
/// mode label ("sidecar" / "external" / "pending" / "disconnected").
#[tauri::command]
pub async fn daemon_accept_sidecar(state: State<'_, DiscoveryState>) -> IpcResult<String> {
    Ok(state.accept_sidecar())
}

/// `transport_reconnect_status` — open a `tauri::ipc::Channel<ReconnectStatus>`
/// the title-bar pill subscribes to. Currently emits a single
/// `Connected` frame and idles; the real run-loop driver
/// (`TransportHandle::run_with`) plugs into this channel once the
/// daemon WS path is wired (audit/scan-rust-stubs.md item #5 / #10).
///
/// Backed by `transport::ReconnectStatus`; the Channel handle is
/// owned by the WebView side via the `subscribe<T>(channel)` Tauri 2
/// pattern.
#[tauri::command]
pub async fn transport_reconnect_status(
    on_status: tauri::ipc::Channel<crate::transport::ReconnectStatus>,
) -> IpcResult<()> {
    // Send the current best-effort status. Until the run-loop is wired
    // to a real socket, we emit `Connected` because the per-session
    // SubprocessManager is the live transport — no reconnect storms
    // possible until that path migrates to the shared WS daemon.
    on_status
        .send(crate::transport::ReconnectStatus::Connected)
        .map_err(|e| IpcError::internal(format!("status channel send: {e}")))?;
    Ok(())
}

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
            // Spec desktop-cortex-augmentation §6.1 (9 new verbs).
            session_lanes_list,
            session_lanes_subscribe,
            session_lanes_unsubscribe,
            session_lanes_kill,
            session_set_workdir,
            daemon_status,
            daemon_shutdown,
            app_popout_lane,
            app_open_folder_picker,
            // Spec §5 (issue #145) — host-side daemon-discovery state.
            app_discovery_status,
            // Wired per audit/scan-rust-stubs.md item #1 — the wizard
            // panicked on the missing-handler error before this
            // handler existed.
            daemon_install_command,
            // Discovery wizard verbs (audit A033) — probe, reconnect,
            // and accept-sidecar backing discovery-wizard-mount.tsx.
            daemon_config_exists,
            daemon_reconnect,
            daemon_accept_sidecar,
            // Wired per audit/scan-rust-stubs.md item #5 — title-bar
            // pill subscribes to a typed status Channel.
            transport_reconnect_status,
        ])
}

// ---------------------------------------------------------------------------
// Spec desktop-cortex-augmentation §6.1 — 9 new IPC verbs.
//
// 7 of these round-trip to the daemon over WS (most). 2 are Tauri-host-
// only (`app_popout_lane`, `app_open_folder_picker`); see §5 of
// IPC-CONTRACT.md. Routing is unified through `daemon_rpc_call` which
// today delegates to the existing SubprocessManager (so behaviour is
// preserved across the Subprocess vs Daemon transport switch in §3 of
// the spec). The transport.rs run-loop replaces the inner body when
// the daemon path lights up — verb signatures stay identical.
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LaneSummary {
    pub lane_id: String,
    pub title: String,
    pub status: String, // pending|running|blocked|done|errored|cancelled
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesListParams {
    pub session_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesListResult {
    pub lanes: Vec<LaneSummary>,
}

/// `session.lanes.list` — list every lane currently known on this
/// session. Round-trips to the daemon (subprocess path today).
#[tauri::command]
pub async fn session_lanes_list(
    params: SessionLanesListParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<SessionLanesListResult> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "session.lanes.list",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesSubscribeParams {
    pub session_id: String,
    /// The Channel handle is provided as a Tauri-side construct; the
    /// caller passes a `tauri::ipc::Channel<LaneEvent>` which the
    /// command handler consumes. This serde-shape carries the runtime
    /// id once Tauri's macro expands it.
    #[serde(default)]
    pub channel_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesSubscribeResult {
    pub subscription_id: String,
}

/// `session.lanes.subscribe` — register a subscription. Forwards every
/// `LaneEvent` the daemon emits for this session through the
/// `tauri::ipc::Channel<LaneEvent>` the WebView passed in (`on_event`
/// kwarg). The host-side LanesState owns the per-subscription
/// forwarder so `session_lanes_unsubscribe` and host-side teardown
/// (window close, daemon disconnect) can drop it deterministically.
///
/// Wired per audit/scan-rust-stubs.md item #2: the previous body
/// dropped the Channel handle, so no LaneEvent ever reached the
/// WebView even though the verb returned a `subscription_id`.
#[tauri::command]
pub async fn session_lanes_subscribe(
    params: SessionLanesSubscribeParams,
    on_event: tauri::ipc::Channel<crate::lanes::LaneEvent>,
    mgr: State<'_, SubprocessManager>,
    state: State<'_, crate::lanes::LanesState>,
) -> IpcResult<SessionLanesSubscribeResult> {
    // Step 1: round-trip to the daemon so it starts pushing lane
    // events for this session over its existing event bus. The result
    // carries the daemon-side subscription id; we use it as the
    // host-side registry key so unsubscribe routing works for both
    // host-fast-path and daemon-side drops.
    let val = mgr
        .rpc_call(
            &params.session_id,
            "session.lanes.subscribe",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    let result: SessionLanesSubscribeResult = from_val(val)?;

    // Step 2: register a host-side LaneSubscription that forwards
    // every LaneEvent emitted by the daemon's session bus to the
    // WebView's Channel. Capacity 1024 matches the per-channel ring
    // budget called out in spec desktop-cortex-augmentation §12 R3.
    let sink: std::sync::Arc<dyn crate::lanes::LaneSink> = std::sync::Arc::new(
        crate::lanes::ChannelSink::new(on_event),
    );
    let subscription = crate::lanes::LaneSubscription::new(
        params.session_id.clone(),
        sink,
        1024,
    );
    state
        .register(result.subscription_id.clone(), subscription)
        .await;

    Ok(result)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesUnsubscribeParams {
    pub subscription_id: String,
    /// Optional session_id so the host knows which subprocess to
    /// route the teardown through if the subscription was registered
    /// before the host-side LanesState was populated.
    #[serde(default)]
    pub session_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesUnsubscribeResult {
    pub ok: bool,
}

/// `session.lanes.unsubscribe` — drop a subscription. Routes via the
/// host LanesState (if registered locally) or the daemon for
/// daemon-side subscriptions.
#[tauri::command]
pub async fn session_lanes_unsubscribe(
    params: SessionLanesUnsubscribeParams,
    mgr: State<'_, SubprocessManager>,
    state: State<'_, crate::lanes::LanesState>,
) -> IpcResult<SessionLanesUnsubscribeResult> {
    // Host-side first: if the subscription was registered by the host
    // forwarder, drop it locally.
    if state.unregister(&params.subscription_id).await {
        return Ok(SessionLanesUnsubscribeResult { ok: true });
    }
    // Otherwise the daemon owns the subscription. Need a session id to
    // route the call.
    let sid = params
        .session_id
        .as_deref()
        .map(|s| s.to_string())
        .or(any_session_id_maybe(&mgr).await)
        .ok_or_else(|| {
            IpcError::not_found(format!("subscription {}", params.subscription_id))
        })?;
    let val = mgr
        .rpc_call(
            &sid,
            "session.lanes.unsubscribe",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesKillParams {
    pub session_id: String,
    pub lane_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionLanesKillResult {
    pub killed_at: String,
}

/// `session.lanes.kill` — operator-initiated lane termination. Round-
/// trips to the daemon; the lanes runtime there emits a
/// `lane.killed` event the host forwarder consumes.
#[tauri::command]
pub async fn session_lanes_kill(
    params: SessionLanesKillParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<SessionLanesKillResult> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "session.lanes.kill",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionSetWorkdirParams {
    pub session_id: String,
    pub workdir: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionSetWorkdirResult {
    pub ok: bool,
    pub workdir: String,
}

/// `session.set_workdir` — bind the session to an absolute workdir.
/// The daemon refuses (returns `conflict`) if any tool call is in
/// flight; that error surfaces verbatim through this verb.
#[tauri::command]
pub async fn session_set_workdir(
    params: SessionSetWorkdirParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<SessionSetWorkdirResult> {
    let val = mgr
        .rpc_call(
            &params.session_id,
            "session.set_workdir",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DaemonStatusResult {
    pub url: String,
    pub mode: String, // "external" | "sidecar"
    pub version: String,
    pub uptime_s: u64,
}

/// `daemon.status` — return current daemon connection metadata. The
/// host caches the values populated at discovery time (mode, url) and
/// only round-trips to the daemon for the live `version` and
/// `uptime_s`.
#[tauri::command]
pub async fn daemon_status(mgr: State<'_, SubprocessManager>) -> IpcResult<DaemonStatusResult> {
    let sid = any_session_id(&mgr).await?;
    let val = mgr
        .rpc_call(&sid, "daemon.status", serde_json::json!({}))
        .await?;
    from_val(val)
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DaemonShutdownParams {
    #[serde(default = "default_true")]
    pub graceful: bool,
}

fn default_true() -> bool {
    true
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DaemonShutdownResult {
    pub shutdown_at: String,
}

/// `daemon.shutdown` — request the daemon to stop. Defaults to
/// graceful (drain in-flight tool calls before exiting).
#[tauri::command]
pub async fn daemon_shutdown(
    params: DaemonShutdownParams,
    mgr: State<'_, SubprocessManager>,
) -> IpcResult<DaemonShutdownResult> {
    let sid = any_session_id(&mgr).await?;
    let val = mgr
        .rpc_call(
            &sid,
            "daemon.shutdown",
            serde_json::to_value(&params).unwrap_or(serde_json::json!({})),
        )
        .await?;
    from_val(val)
}

// --- Tauri-host-only verbs -------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppPopoutLaneParams {
    pub session_id: String,
    pub lane_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppPopoutLaneResult {
    pub window_label: String,
}

/// `app.popout_lane` — open (or focus) a `WebviewWindow` for one
/// lane. Delegates to `popout::open_or_focus_lane_popout`, which
/// builds the window with label `"lane:<session>:<lane>"` and
/// registers it with `PopoutRegistry` so the menu's "Lane Pop-Outs"
/// submenu (item 25) can enumerate live pop-outs.
#[tauri::command]
pub async fn app_popout_lane(
    params: AppPopoutLaneParams,
    app: AppHandle,
    registry: State<'_, crate::popout::PopoutRegistry>,
) -> IpcResult<AppPopoutLaneResult> {
    let label = crate::popout::open_or_focus_lane_popout(
        &app,
        &registry,
        &params.session_id,
        &params.lane_id,
    )
    .await?;
    Ok(AppPopoutLaneResult {
        window_label: label,
    })
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct AppOpenFolderPickerParams {
    #[serde(default)]
    pub title: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppOpenFolderPickerResult {
    pub path: Option<String>,
}

/// `app.open_folder_picker` — open the native OS folder picker.
/// Returns `Some(path)` on selection, `None` on cancel.
///
/// Wired per audit/scan-rust-stubs.md item #3: previously returned
/// `None` unconditionally, so any caller round-tripping folder
/// selection through Rust got `null` and the daemon never received
/// a workdir. Delegates to tauri-plugin-dialog's `pick_folder`
/// callback API; we adapt that to async via a oneshot channel so the
/// command can stay `async fn`.
#[tauri::command]
pub async fn app_open_folder_picker(
    params: AppOpenFolderPickerParams,
    app: AppHandle,
) -> IpcResult<AppOpenFolderPickerResult> {
    use tauri_plugin_dialog::DialogExt;

    let (tx, rx) = tokio::sync::oneshot::channel::<Option<String>>();

    let mut builder = app.dialog().file();
    if let Some(title) = params.title.as_deref() {
        builder = builder.set_title(title);
    }
    builder.pick_folder(move |maybe_path| {
        // Convert FilePath to a string. FilePath on desktop wraps a
        // real PathBuf; we surface the absolute path string.
        let p = maybe_path.and_then(|fp| {
            fp.into_path()
                .ok()
                .and_then(|pb| pb.into_os_string().into_string().ok())
        });
        let _ = tx.send(p);
    });

    let path = rx.await.map_err(|_| {
        IpcError::internal("folder picker callback dropped before completing")
    })?;
    Ok(AppOpenFolderPickerResult { path })
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
    fn discovery_error_maps_not_found_to_taxonomy() {
        use crate::discovery::DiscoveryError;
        let err = discovery_error_to_ipc(&DiscoveryError::NotFound);
        assert_eq!(err.code, -32002);
        assert_eq!(err.stoke_code, "not_found");
    }

    #[test]
    fn discovery_error_maps_refused_and_spawn_to_internal_with_detail() {
        use crate::discovery::DiscoveryError;
        let refused = discovery_error_to_ipc(&DiscoveryError::Refused);
        assert_eq!(refused.code, -32603);
        assert!(refused.message.contains("refused"));

        let spawn = discovery_error_to_ipc(&DiscoveryError::SidecarSpawn("no sidecar binary".into()));
        assert_eq!(spawn.stoke_code, "internal");
        assert!(spawn.message.contains("no sidecar binary"));
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
