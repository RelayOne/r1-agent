// Package desktopapi defines the Go-side contract between the R1
// subprocess (Tier 3) and the Tauri Rust host (Tier 2) for R1 Desktop.
//
// The contract is expressed as a single Handler interface whose method
// set matches 1:1 with the JSON-RPC 2.0 verbs enumerated in
// `desktop/IPC-CONTRACT.md` §2. A host process (today: r1 one-shot
// mode; later: r1 serve) embeds a Handler and dispatches every
// incoming JSON-RPC request to the matching method.
//
// Scaffold status (R1D-1.4): every method on the stub implementation
// `NotImplemented{}` returns ErrNotImplemented, a stokerr sentinel
// carrying code stokerr.ErrNotImplemented. Callers can test with
// errors.Is(err, desktopapi.ErrNotImplemented) regardless of whether
// the underlying error was wrapped.
//
// See also:
//   - `desktop/IPC-CONTRACT.md` for the wire shape and error taxonomy.
//   - `desktop/src-tauri/src/ipc.rs` for the matching Rust stubs.
//   - `internal/stokerr` for the shared error taxonomy.
package desktopapi

import (
	"context"
	"errors"

	"github.com/RelayOne/r1/internal/stokerr"
)

// ---------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------

// errNotImplementedCode is the stokerr taxonomy string for the stub
// sentinel. Mirrors desktop/IPC-CONTRACT.md §3.2 ("not_implemented") and
// the Rust host's IpcError::not_implemented helper.
const errNotImplementedCode stokerr.Code = "not_implemented"

// ErrNotImplemented is the sentinel returned by the scaffold handler.
//
// It is a *stokerr.Error wrapping the "not_implemented" code so that
// callers can either:
//
//   - type-assert to *stokerr.Error and read .Code (downstream JSON-RPC
//     translation can then emit the -32010 numeric code and the
//     "not_implemented" data.stoke_code), or
//
//   - use errors.Is(err, desktopapi.ErrNotImplemented) to cheaply check
//     for the stub case and short-circuit their logic (e.g., UI renders
//     a "coming soon" state instead of a red error banner).
//
// The sentinel carries no context because it applies uniformly to
// every method. When a real implementation lands for a given verb, its
// error returns become ordinary stokerr.Error values and this sentinel
// stops appearing for that verb.
var ErrNotImplemented = stokerr.New(errNotImplementedCode, "desktopapi: handler method not implemented")

// IsNotImplemented reports whether err is or wraps ErrNotImplemented.
// Convenience over errors.Is for callers that prefer a named helper.
func IsNotImplemented(err error) bool {
	return errors.Is(err, ErrNotImplemented)
}

// ---------------------------------------------------------------------
// Request/response value types
//
// Every shape here mirrors the Rust struct of the same purpose in
// `desktop/src-tauri/src/ipc.rs`. Field names use Go idiom; JSON tags
// pin the wire-name per the contract doc.
// ---------------------------------------------------------------------

// -- Session control ---------------------------------------------------

// SessionStartRequest is the params payload for `session.start`.
type SessionStartRequest struct {
	Prompt    string  `json:"prompt"`
	SkillPack string  `json:"skill_pack,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	BudgetUSD float64 `json:"budget_usd,omitempty"`
}

// SessionStartResponse is the result payload for `session.start`.
type SessionStartResponse struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
}

// SessionIDRequest is the shared shape for pause/resume.
type SessionIDRequest struct {
	SessionID string `json:"session_id"`
}

// SessionPauseResponse is the result payload for `session.pause`.
type SessionPauseResponse struct {
	PausedAt string `json:"paused_at"`
}

// SessionResumeResponse is the result payload for `session.resume`.
type SessionResumeResponse struct {
	ResumedAt string `json:"resumed_at"`
}

// -- Ledger query ------------------------------------------------------

// LedgerGetNodeRequest is the params payload for `ledger.get_node`.
type LedgerGetNodeRequest struct {
	Hash string `json:"hash"`
}

// LedgerEdge describes a single outbound edge of a ledger node.
type LedgerEdge struct {
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// LedgerNode is the result payload for `ledger.get_node`.
type LedgerNode struct {
	Hash     string         `json:"hash"`
	NodeType string         `json:"type"`
	Payload  map[string]any `json:"payload"`
	Edges    []LedgerEdge   `json:"edges"`
}

// LedgerListEventsRequest is the params payload for `ledger.list_events`.
type LedgerListEventsRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Since     string `json:"since,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// LedgerEventSummary is one row in the `ledger.list_events` response.
type LedgerEventSummary struct {
	Hash     string `json:"hash"`
	NodeType string `json:"type"`
	At       string `json:"at"`
}

// LedgerListEventsResponse is the result payload for `ledger.list_events`.
type LedgerListEventsResponse struct {
	Events     []LedgerEventSummary `json:"events"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// -- Memory inspection -------------------------------------------------

// MemoryScope enumerates the five canonical memory-bus scopes. The
// string form of each is the wire value used in JSON.
type MemoryScope string

// Canonical scope names. Keep in sync with `memory/` package.
const (
	MemoryScopeSession     MemoryScope = "Session"
	MemoryScopeWorker      MemoryScope = "Worker"
	MemoryScopeAllSessions MemoryScope = "AllSessions"
	MemoryScopeGlobal      MemoryScope = "Global"
	MemoryScopeAlways      MemoryScope = "Always"
)

// AllMemoryScopes returns every known memory scope in canonical order.
// Used by `memory.list_scopes`.
func AllMemoryScopes() []MemoryScope {
	return []MemoryScope{
		MemoryScopeSession,
		MemoryScopeWorker,
		MemoryScopeAllSessions,
		MemoryScopeGlobal,
		MemoryScopeAlways,
	}
}

// MemoryListScopesResponse is the result payload for `memory.list_scopes`.
type MemoryListScopesResponse struct {
	Scopes []MemoryScope `json:"scopes"`
}

// MemoryQueryRequest is the params payload for `memory.query`.
type MemoryQueryRequest struct {
	Scope     MemoryScope `json:"scope"`
	KeyPrefix string      `json:"key_prefix,omitempty"`
	Limit     int         `json:"limit,omitempty"`
}

// MemoryEntry is one row in the `memory.query` response.
type MemoryEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
}

// MemoryQueryResponse is the result payload for `memory.query`.
type MemoryQueryResponse struct {
	Entries   []MemoryEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

// -- Cost --------------------------------------------------------------

// CostGetCurrentRequest is the params payload for `cost.get_current`.
type CostGetCurrentRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

// CostSnapshot is the result payload for `cost.get_current`.
type CostSnapshot struct {
	USD       float64 `json:"usd"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	AsOf      string  `json:"as_of"`
}

// CostGetHistoryRequest is the params payload for `cost.get_history`.
type CostGetHistoryRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Since     string `json:"since,omitempty"`
	// One of "minute", "hour", "day". Default "hour".
	Bucket string `json:"bucket,omitempty"`
}

// CostBucket is one row in the `cost.get_history` response.
type CostBucket struct {
	At     string  `json:"at"`
	USD    float64 `json:"usd"`
	Tokens int64   `json:"tokens"`
}

// CostHistoryResponse is the result payload for `cost.get_history`.
type CostHistoryResponse struct {
	Buckets []CostBucket `json:"buckets"`
}

// -- Descent state -----------------------------------------------------

// DescentTier names the eight verification tiers T1..T8.
type DescentTier string

// Canonical tier names.
const (
	TierT1 DescentTier = "T1"
	TierT2 DescentTier = "T2"
	TierT3 DescentTier = "T3"
	TierT4 DescentTier = "T4"
	TierT5 DescentTier = "T5"
	TierT6 DescentTier = "T6"
	TierT7 DescentTier = "T7"
	TierT8 DescentTier = "T8"
)

// AllDescentTiers returns every tier in ascending order.
func AllDescentTiers() []DescentTier {
	return []DescentTier{TierT1, TierT2, TierT3, TierT4, TierT5, TierT6, TierT7, TierT8}
}

// DescentStatus names the four tier states.
type DescentStatus string

// Canonical status values.
const (
	StatusPending DescentStatus = "pending"
	StatusRunning DescentStatus = "running"
	StatusPassed  DescentStatus = "passed"
	StatusFailed  DescentStatus = "failed"
)

// DescentCurrentTierRequest is the params payload for `descent.current_tier`.
type DescentCurrentTierRequest struct {
	SessionID string `json:"session_id"`
	ACID      string `json:"ac_id,omitempty"`
}

// DescentTierRow is one row in the `descent.current_tier` response.
type DescentTierRow struct {
	ACID        string        `json:"ac_id"`
	Tier        DescentTier   `json:"tier"`
	Status      DescentStatus `json:"status"`
	EvidenceRef string        `json:"evidence_ref,omitempty"`
}

// DescentTierHistoryRequest is the params payload for `descent.tier_history`.
type DescentTierHistoryRequest struct {
	SessionID string `json:"session_id"`
	ACID      string `json:"ac_id"`
}

// DescentAttempt is one historical tier attempt.
type DescentAttempt struct {
	Tier         DescentTier   `json:"tier"`
	Status       DescentStatus `json:"status"`
	At           string        `json:"at"`
	EvidenceRef  string        `json:"evidence_ref,omitempty"`
	FailureClass string        `json:"failure_class,omitempty"`
}

// DescentTierHistoryResponse is the result payload for `descent.tier_history`.
type DescentTierHistoryResponse struct {
	ACID     string           `json:"ac_id"`
	Attempts []DescentAttempt `json:"attempts"`
}

// -- Lane control (§2.7) -------------------------------------------------

// LaneStatus enumerates the lane lifecycle states (contract §2.7).
type LaneStatus string

// Canonical lane status values. Keep in sync with the Rust host's
// LaneSummary.status comment in desktop/src-tauri/src/ipc.rs.
const (
	LaneStatusPending   LaneStatus = "pending"
	LaneStatusRunning   LaneStatus = "running"
	LaneStatusBlocked   LaneStatus = "blocked"
	LaneStatusDone      LaneStatus = "done"
	LaneStatusErrored   LaneStatus = "errored"
	LaneStatusCancelled LaneStatus = "cancelled"
)

// SessionLanesListRequest is the params payload for `session.lanes.list`.
type SessionLanesListRequest struct {
	SessionID string `json:"session_id"`
}

// LaneSummary is one row in the `session.lanes.list` response. Field
// names mirror the Rust host's LaneSummary struct in
// desktop/src-tauri/src/ipc.rs.
type LaneSummary struct {
	LaneID    string     `json:"lane_id"`
	Title     string     `json:"title"`
	Status    LaneStatus `json:"status"`
	CreatedAt string     `json:"created_at"`
}

// SessionLanesListResponse is the result payload for `session.lanes.list`.
type SessionLanesListResponse struct {
	Lanes []LaneSummary `json:"lanes"`
}

// SessionLanesSubscribeRequest is the params payload for
// `session.lanes.subscribe`. The `channel` named in contract §2.7 is a
// tauri::ipc::Channel<LaneEvent> — a Rust-host runtime handle that the
// host consumes before forwarding the call; only the optional
// serialised channel id crosses the JSON-RPC boundary.
type SessionLanesSubscribeRequest struct {
	SessionID string `json:"session_id"`
	ChannelID string `json:"channel_id,omitempty"`
}

// SessionLanesSubscribeResponse is the result payload for
// `session.lanes.subscribe`.
type SessionLanesSubscribeResponse struct {
	SubscriptionID string `json:"subscription_id"`
}

// SessionLanesUnsubscribeRequest is the params payload for
// `session.lanes.unsubscribe`.
type SessionLanesUnsubscribeRequest struct {
	SubscriptionID string `json:"subscription_id"`
	SessionID      string `json:"session_id,omitempty"`
}

// SessionLanesUnsubscribeResponse is the result payload for
// `session.lanes.unsubscribe`.
type SessionLanesUnsubscribeResponse struct {
	OK bool `json:"ok"`
}

// SessionLanesKillRequest is the params payload for `session.lanes.kill`.
type SessionLanesKillRequest struct {
	SessionID string `json:"session_id"`
	LaneID    string `json:"lane_id"`
}

// SessionLanesKillResponse is the result payload for `session.lanes.kill`.
type SessionLanesKillResponse struct {
	KilledAt string `json:"killed_at"`
}

// -- Workdir binding (spec desktop-cortex-augmentation §7) ---------------

// SessionSetWorkdirRequest is the params payload for `session.set_workdir`.
type SessionSetWorkdirRequest struct {
	SessionID string `json:"session_id"`
	Workdir   string `json:"workdir"`
}

// SessionSetWorkdirResponse is the result payload for `session.set_workdir`.
type SessionSetWorkdirResponse struct {
	OK      bool   `json:"ok"`
	Workdir string `json:"workdir"`
}

// -- Daemon control (§2.8) -----------------------------------------------

// DaemonStatusResponse is the result payload for `daemon.status`.
type DaemonStatusResponse struct {
	URL     string `json:"url"`
	Mode    string `json:"mode"` // "external" | "sidecar"
	Version string `json:"version"`
	UptimeS int64  `json:"uptime_s"`
}

// DaemonShutdownRequest is the params payload for `daemon.shutdown`.
// A nil Graceful means true — the contract default.
type DaemonShutdownRequest struct {
	Graceful *bool `json:"graceful,omitempty"`
}

// DaemonShutdownResponse is the result payload for `daemon.shutdown`.
type DaemonShutdownResponse struct {
	ShutdownAt string `json:"shutdown_at"`
}

// ---------------------------------------------------------------------
// Handler interface — one method per JSON-RPC verb (§2 of contract).
//
// Every method takes a context and a typed request, returns a typed
// response (or an empty struct) plus an error. Implementations must
// return either a concrete *stokerr.Error or ErrNotImplemented; the
// JSON-RPC dispatch layer translates both into a proper
// {code, message, data.stoke_code} response envelope.
// ---------------------------------------------------------------------

// Handler is the Go-side dispatch surface for every IPC verb defined
// in desktop/IPC-CONTRACT.md §2. The Rust host sends JSON-RPC 2.0
// requests over the subprocess's stdin; a Handler-hosting binary
// decodes the envelope, picks the matching method, and writes the
// response back on stdout.
//
// The 18 methods below are the verbs that round-trip to the Go
// subprocess: the core 11 (§2.1–§2.5) plus lane control (§2.7),
// workdir binding (spec desktop-cortex-augmentation §7), and daemon
// control (§2.8). The Tauri-only verbs (session.send, session.cancel,
// skill.list, skill.get, app.popout_lane, app.open_folder_picker)
// live in the Rust host per §5 of the contract and do not appear on
// this interface.
type Handler interface {
	// Session control (§2.1)
	SessionStart(ctx context.Context, req SessionStartRequest) (SessionStartResponse, error)
	SessionPause(ctx context.Context, req SessionIDRequest) (SessionPauseResponse, error)
	SessionResume(ctx context.Context, req SessionIDRequest) (SessionResumeResponse, error)

	// Ledger query (§2.2)
	LedgerGetNode(ctx context.Context, req LedgerGetNodeRequest) (LedgerNode, error)
	LedgerListEvents(ctx context.Context, req LedgerListEventsRequest) (LedgerListEventsResponse, error)

	// Memory inspection (§2.3)
	MemoryListScopes(ctx context.Context) (MemoryListScopesResponse, error)
	MemoryQuery(ctx context.Context, req MemoryQueryRequest) (MemoryQueryResponse, error)

	// Cost (§2.4)
	CostGetCurrent(ctx context.Context, req CostGetCurrentRequest) (CostSnapshot, error)
	CostGetHistory(ctx context.Context, req CostGetHistoryRequest) (CostHistoryResponse, error)

	// Descent state (§2.5)
	DescentCurrentTier(ctx context.Context, req DescentCurrentTierRequest) ([]DescentTierRow, error)
	DescentTierHistory(ctx context.Context, req DescentTierHistoryRequest) (DescentTierHistoryResponse, error)

	// Lane control (§2.7)
	SessionLanesList(ctx context.Context, req SessionLanesListRequest) (SessionLanesListResponse, error)
	SessionLanesSubscribe(ctx context.Context, req SessionLanesSubscribeRequest) (SessionLanesSubscribeResponse, error)
	SessionLanesUnsubscribe(ctx context.Context, req SessionLanesUnsubscribeRequest) (SessionLanesUnsubscribeResponse, error)
	SessionLanesKill(ctx context.Context, req SessionLanesKillRequest) (SessionLanesKillResponse, error)

	// Workdir binding (spec desktop-cortex-augmentation §7)
	SessionSetWorkdir(ctx context.Context, req SessionSetWorkdirRequest) (SessionSetWorkdirResponse, error)

	// Daemon control (§2.8)
	DaemonStatus(ctx context.Context) (DaemonStatusResponse, error)
	DaemonShutdown(ctx context.Context, req DaemonShutdownRequest) (DaemonShutdownResponse, error)
}

// ---------------------------------------------------------------------
// NotImplemented — scaffold stub implementation
// ---------------------------------------------------------------------

// NotImplemented is a Handler whose every method returns ErrNotImplemented.
// Binaries embed it today (R1D-1 scaffold) and swap in real method
// bodies one verb at a time across later R1D-* phases.
//
// The zero value is ready to use.
type NotImplemented struct{}

// compile-time assertion that NotImplemented satisfies Handler.
var _ Handler = (*NotImplemented)(nil)

// SessionStart returns ErrNotImplemented.
func (NotImplemented) SessionStart(_ context.Context, _ SessionStartRequest) (SessionStartResponse, error) {
	return SessionStartResponse{}, ErrNotImplemented
}

// SessionPause returns ErrNotImplemented.
func (NotImplemented) SessionPause(_ context.Context, _ SessionIDRequest) (SessionPauseResponse, error) {
	return SessionPauseResponse{}, ErrNotImplemented
}

// SessionResume returns ErrNotImplemented.
func (NotImplemented) SessionResume(_ context.Context, _ SessionIDRequest) (SessionResumeResponse, error) {
	return SessionResumeResponse{}, ErrNotImplemented
}

// LedgerGetNode returns ErrNotImplemented.
func (NotImplemented) LedgerGetNode(_ context.Context, _ LedgerGetNodeRequest) (LedgerNode, error) {
	return LedgerNode{}, ErrNotImplemented
}

// LedgerListEvents returns ErrNotImplemented.
func (NotImplemented) LedgerListEvents(_ context.Context, _ LedgerListEventsRequest) (LedgerListEventsResponse, error) {
	return LedgerListEventsResponse{}, ErrNotImplemented
}

// MemoryListScopes returns ErrNotImplemented.
func (NotImplemented) MemoryListScopes(_ context.Context) (MemoryListScopesResponse, error) {
	return MemoryListScopesResponse{}, ErrNotImplemented
}

// MemoryQuery returns ErrNotImplemented.
func (NotImplemented) MemoryQuery(_ context.Context, _ MemoryQueryRequest) (MemoryQueryResponse, error) {
	return MemoryQueryResponse{}, ErrNotImplemented
}

// CostGetCurrent returns ErrNotImplemented.
func (NotImplemented) CostGetCurrent(_ context.Context, _ CostGetCurrentRequest) (CostSnapshot, error) {
	return CostSnapshot{}, ErrNotImplemented
}

// CostGetHistory returns ErrNotImplemented.
func (NotImplemented) CostGetHistory(_ context.Context, _ CostGetHistoryRequest) (CostHistoryResponse, error) {
	return CostHistoryResponse{}, ErrNotImplemented
}

// DescentCurrentTier returns ErrNotImplemented.
func (NotImplemented) DescentCurrentTier(_ context.Context, _ DescentCurrentTierRequest) ([]DescentTierRow, error) {
	return nil, ErrNotImplemented
}

// DescentTierHistory returns ErrNotImplemented.
func (NotImplemented) DescentTierHistory(_ context.Context, _ DescentTierHistoryRequest) (DescentTierHistoryResponse, error) {
	return DescentTierHistoryResponse{}, ErrNotImplemented
}

// SessionLanesList returns ErrNotImplemented.
func (NotImplemented) SessionLanesList(_ context.Context, _ SessionLanesListRequest) (SessionLanesListResponse, error) {
	return SessionLanesListResponse{}, ErrNotImplemented
}

// SessionLanesSubscribe returns ErrNotImplemented.
func (NotImplemented) SessionLanesSubscribe(_ context.Context, _ SessionLanesSubscribeRequest) (SessionLanesSubscribeResponse, error) {
	return SessionLanesSubscribeResponse{}, ErrNotImplemented
}

// SessionLanesUnsubscribe returns ErrNotImplemented.
func (NotImplemented) SessionLanesUnsubscribe(_ context.Context, _ SessionLanesUnsubscribeRequest) (SessionLanesUnsubscribeResponse, error) {
	return SessionLanesUnsubscribeResponse{}, ErrNotImplemented
}

// SessionLanesKill returns ErrNotImplemented.
func (NotImplemented) SessionLanesKill(_ context.Context, _ SessionLanesKillRequest) (SessionLanesKillResponse, error) {
	return SessionLanesKillResponse{}, ErrNotImplemented
}

// SessionSetWorkdir returns ErrNotImplemented.
func (NotImplemented) SessionSetWorkdir(_ context.Context, _ SessionSetWorkdirRequest) (SessionSetWorkdirResponse, error) {
	return SessionSetWorkdirResponse{}, ErrNotImplemented
}

// DaemonStatus returns ErrNotImplemented.
func (NotImplemented) DaemonStatus(_ context.Context) (DaemonStatusResponse, error) {
	return DaemonStatusResponse{}, ErrNotImplemented
}

// DaemonShutdown returns ErrNotImplemented.
func (NotImplemented) DaemonShutdown(_ context.Context, _ DaemonShutdownRequest) (DaemonShutdownResponse, error) {
	return DaemonShutdownResponse{}, ErrNotImplemented
}
