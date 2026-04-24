// SPDX-License-Identifier: MIT
//
// R1 Desktop IPC stubs (Tier 2 Rust host ↔ Tier 1 WebView ↔ Tier 3 r1 Go subprocess).
//
// Source of truth for the wire shape: `desktop/IPC-CONTRACT.md`. Keep
// this file in lock-step. Any signature change here must land in the
// contract doc and in `internal/desktopapi/desktopapi.go` in the same
// commit.
//
// Scaffold status (R1D-1.4): the method dispatch + param/return
// schemas are concrete and typed. The method bodies are `todo!()` — the
// real work (spawn r1, round-trip JSON-RPC, decode response) ships in
// R1D-1.2 / R1D-1.3. Calling any of these from the WebView today will
// panic the host; that is intentional at scaffold time and is caught
// by the Go-side `ErrNotImplemented` sentinel once the wiring is live.

use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------
// Shared envelope + error types
// ---------------------------------------------------------------------

/// Application-level error returned through Tauri `invoke`. Serialises
/// to the `error.data` object in a JSON-RPC 2.0 error response; see
/// `desktop/IPC-CONTRACT.md` §3.2.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IpcError {
    /// Numeric JSON-RPC code (e.g., -32001 for validation).
    pub code: i32,
    /// R1 taxonomy string (mirrors `internal/stokerr` codes). Clients
    /// should pattern-match on this, not on `code`.
    pub stoke_code: String,
    /// Human-readable message.
    pub message: String,
}

impl IpcError {
    pub fn not_implemented(method: &'static str) -> Self {
        Self {
            code: -32010,
            stoke_code: "not_implemented".to_string(),
            message: format!("{method}: stub — implementation lands in a later R1D-* phase"),
        }
    }
}

/// Convenience alias used by every stub.
pub type IpcResult<T> = Result<T, IpcError>;

// ---------------------------------------------------------------------
// Session control (§2.1)
// ---------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
pub struct SessionStartParams {
    pub prompt: String,
    #[serde(default)]
    pub skill_pack: Option<String>,
    #[serde(default)]
    pub provider: Option<String>,
    #[serde(default)]
    pub budget_usd: Option<f64>,
}

#[derive(Debug, Clone, Serialize)]
pub struct SessionStartResult {
    pub session_id: String,
    pub started_at: String, // ISO-8601
}

#[derive(Debug, Clone, Deserialize)]
pub struct SessionIdParams {
    pub session_id: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct SessionPauseResult {
    pub paused_at: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct SessionResumeResult {
    pub resumed_at: String,
}

#[tauri::command]
pub fn session_start(_params: SessionStartParams) -> IpcResult<SessionStartResult> {
    // Dispatch to Go subprocess via JSON-RPC 2.0 `session.start`.
    todo!("R1D-1.2: spawn r1 subprocess and forward session.start")
}

#[tauri::command]
pub fn session_pause(_params: SessionIdParams) -> IpcResult<SessionPauseResult> {
    todo!("R1D-1.2: forward session.pause")
}

#[tauri::command]
pub fn session_resume(_params: SessionIdParams) -> IpcResult<SessionResumeResult> {
    todo!("R1D-1.2: forward session.resume")
}

// ---------------------------------------------------------------------
// Ledger query (§2.2)
// ---------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
pub struct LedgerGetNodeParams {
    pub hash: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct LedgerEdge {
    pub to: String,
    pub kind: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct LedgerNode {
    pub hash: String,
    pub node_type: String,
    pub payload: serde_json::Value,
    pub edges: Vec<LedgerEdge>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct LedgerListEventsParams {
    #[serde(default)]
    pub session_id: Option<String>,
    #[serde(default)]
    pub since: Option<String>,
    #[serde(default)]
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, Serialize)]
pub struct LedgerEventSummary {
    pub hash: String,
    pub node_type: String,
    pub at: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct LedgerListEventsResult {
    pub events: Vec<LedgerEventSummary>,
    pub next_cursor: Option<String>,
}

#[tauri::command]
pub fn ledger_get_node(_params: LedgerGetNodeParams) -> IpcResult<LedgerNode> {
    todo!("R1D-5: forward ledger.get_node")
}

#[tauri::command]
pub fn ledger_list_events(
    _params: LedgerListEventsParams,
) -> IpcResult<LedgerListEventsResult> {
    todo!("R1D-5: forward ledger.list_events")
}

// ---------------------------------------------------------------------
// Memory inspection (§2.3)
// ---------------------------------------------------------------------

#[derive(Debug, Clone, Serialize)]
pub struct MemoryListScopesResult {
    pub scopes: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct MemoryQueryParams {
    pub scope: String,
    #[serde(default)]
    pub key_prefix: Option<String>,
    #[serde(default)]
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, Serialize)]
pub struct MemoryEntry {
    pub key: String,
    pub value: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct MemoryQueryResult {
    pub entries: Vec<MemoryEntry>,
    pub truncated: bool,
}

#[tauri::command]
pub fn memory_list_scopes() -> IpcResult<MemoryListScopesResult> {
    todo!("R1D-6: forward memory.list_scopes")
}

#[tauri::command]
pub fn memory_query(_params: MemoryQueryParams) -> IpcResult<MemoryQueryResult> {
    todo!("R1D-6: forward memory.query")
}

// ---------------------------------------------------------------------
// Cost (§2.4)
// ---------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
pub struct CostGetCurrentParams {
    #[serde(default)]
    pub session_id: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct CostSnapshot {
    pub usd: f64,
    pub tokens_in: u64,
    pub tokens_out: u64,
    pub as_of: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct CostGetHistoryParams {
    #[serde(default)]
    pub session_id: Option<String>,
    #[serde(default)]
    pub since: Option<String>,
    /// One of "minute", "hour", "day". Default "hour".
    #[serde(default)]
    pub bucket: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct CostBucket {
    pub at: String,
    pub usd: f64,
    pub tokens: u64,
}

#[derive(Debug, Clone, Serialize)]
pub struct CostHistoryResult {
    pub buckets: Vec<CostBucket>,
}

#[tauri::command]
pub fn cost_get_current(_params: CostGetCurrentParams) -> IpcResult<CostSnapshot> {
    todo!("R1D-9: forward cost.get_current")
}

#[tauri::command]
pub fn cost_get_history(_params: CostGetHistoryParams) -> IpcResult<CostHistoryResult> {
    todo!("R1D-9: forward cost.get_history")
}

// ---------------------------------------------------------------------
// Descent state (§2.5)
// ---------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
pub struct DescentCurrentTierParams {
    pub session_id: String,
    #[serde(default)]
    pub ac_id: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct DescentTierRow {
    pub ac_id: String,
    /// One of T1..T8.
    pub tier: String,
    /// One of pending | running | passed | failed.
    pub status: String,
    pub evidence_ref: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct DescentTierHistoryParams {
    pub session_id: String,
    pub ac_id: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct DescentAttempt {
    pub tier: String,
    pub status: String,
    pub at: String,
    pub evidence_ref: Option<String>,
    pub failure_class: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct DescentTierHistoryResult {
    pub ac_id: String,
    pub attempts: Vec<DescentAttempt>,
}

#[tauri::command]
pub fn descent_current_tier(
    _params: DescentCurrentTierParams,
) -> IpcResult<Vec<DescentTierRow>> {
    todo!("R1D-3: forward descent.current_tier")
}

#[tauri::command]
pub fn descent_tier_history(
    _params: DescentTierHistoryParams,
) -> IpcResult<DescentTierHistoryResult> {
    todo!("R1D-3: forward descent.tier_history")
}

// ---------------------------------------------------------------------
// Tauri-only verbs (§5 of IPC-CONTRACT.md)
// ---------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
pub struct SessionSendParams {
    pub session_id: String,
    pub prompt: String,
}

#[tauri::command]
pub fn session_send(_params: SessionSendParams) -> IpcResult<()> {
    todo!("R1D-1.4: write prompt to subprocess stdin")
}

#[tauri::command]
pub fn session_cancel(_params: SessionIdParams) -> IpcResult<()> {
    todo!("R1D-1.4: SIGTERM → grace period → SIGKILL")
}

#[derive(Debug, Clone, Serialize)]
pub struct SkillSummary {
    pub name: String,
    pub version: String,
    pub description: String,
}

#[tauri::command]
pub fn skill_list() -> IpcResult<Vec<SkillSummary>> {
    todo!("R1D-1.4: return cached skill list")
}

#[derive(Debug, Clone, Deserialize)]
pub struct SkillGetParams {
    pub name: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct SkillManifest {
    pub name: String,
    pub version: String,
    pub description: String,
    pub input_schema: serde_json::Value,
    pub output_schema: serde_json::Value,
}

#[tauri::command]
pub fn skill_get(_params: SkillGetParams) -> IpcResult<SkillManifest> {
    todo!("R1D-1.4: return cached skill manifest by name")
}

// ---------------------------------------------------------------------
// Dispatch registration
// ---------------------------------------------------------------------

/// Build the Tauri `invoke_handler` for every IPC command defined in
/// this module. The `Builder::invoke_handler` call in `main.rs`
/// delegates here so the list stays in one file. Keep this list in
/// the same order as `desktop/IPC-CONTRACT.md` §2.
///
/// The macro invocation below is what Tauri v2 expects; at scaffold
/// time the surrounding `main.rs` has not been generated yet (see
/// `src-tauri/src/.gitkeep`). The function is documented but not
/// wired — `tauri::generate_handler!` only exists once the Tauri
/// crate is added to the build. R1D-1.1 (`cargo tauri init`) generates
/// `main.rs`; it will then import this module and call
/// `ipc::register_handlers()` on the builder.
///
/// ```ignore
/// // Expected body once Tauri is on the build path:
/// tauri::generate_handler![
///     session_start,
///     session_pause,
///     session_resume,
///     ledger_get_node,
///     ledger_list_events,
///     memory_list_scopes,
///     memory_query,
///     cost_get_current,
///     cost_get_history,
///     descent_current_tier,
///     descent_tier_history,
///     session_send,
///     session_cancel,
///     skill_list,
///     skill_get,
/// ]
/// ```
pub fn register_handlers() {
    // Intentionally empty at scaffold time — see doc comment.
}

// ---------------------------------------------------------------------
// Unit tests — schema round-trip only (no Tauri runtime required)
// ---------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn not_implemented_carries_taxonomy_code() {
        let err = IpcError::not_implemented("session.start");
        assert_eq!(err.code, -32010);
        assert_eq!(err.stoke_code, "not_implemented");
        assert!(err.message.contains("session.start"));
    }

    #[test]
    fn session_start_params_round_trip() {
        let raw = r#"{"prompt":"hello","skill_pack":"actium","budget_usd":1.5}"#;
        let parsed: SessionStartParams = serde_json::from_str(raw).unwrap();
        assert_eq!(parsed.prompt, "hello");
        assert_eq!(parsed.skill_pack.as_deref(), Some("actium"));
        assert_eq!(parsed.budget_usd, Some(1.5));
        assert!(parsed.provider.is_none());
    }

    #[test]
    fn descent_status_values_documented() {
        let row = DescentTierRow {
            ac_id: "ac-1".into(),
            tier: "T3".into(),
            status: "running".into(),
            evidence_ref: None,
        };
        let json = serde_json::to_string(&row).unwrap();
        assert!(json.contains(r#""tier":"T3""#));
        assert!(json.contains(r#""status":"running""#));
    }
}
