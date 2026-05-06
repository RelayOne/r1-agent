// jsonrpc/daemonapi.go — Phase E item 31: daemon-level RPC verbs.
//
// The 11 desktopapi.Handler verbs (TASK-30) cover ledger / memory / cost
// / descent inspection and the high-level session.start/pause/resume
// trio that Tier 2 originally needed. The r1d-server daemon adds a
// second dispatch surface that's specific to the long-lived process —
// the verbs the CLI's `r1 ctl` and the new TUI surface speak directly:
//
//	session.start          (overload — daemon variant takes workdir/model)
//	session.pause
//	session.resume
//	session.cancel         (sigterm + grace + kill)
//	session.send           (write a turn)
//	session.subscribe      (TASK-32 — declared here, body in subscribe.go)
//	session.unsubscribe    (TASK-32)
//	lanes.list
//	lanes.kill
//	cortex.notes
//	daemon.info
//	daemon.shutdown
//	daemon.reload_config
//
// The DaemonAPI interface below is the dependency injection surface for
// the daemon binary: a daemon embeds an implementation, the dispatcher
// picks up the verbs from RegisterDaemonAPI. Tests can stub the
// interface for round-trip coverage without booting a real daemon.
//
// # Why a second interface
//
// desktopapi.Handler is the Tier 2 contract — frozen by IPC-CONTRACT.md.
// Daemon verbs are not part of that contract; they're internal to the
// long-running process. Mixing them onto desktopapi.Handler would
// distort that file's interface freeze for no benefit.
package jsonrpc

import (
	"context"
	"encoding/json"
)

// ----------------------------------------------------------------------
// Request / response shapes
//
// These mirror IPC-CONTRACT.md §2 where there's overlap (session.start
// uses the same SessionStartRequest from desktopapi) but introduce new
// shapes for the daemon-only verbs.
// ----------------------------------------------------------------------

// DaemonSessionStartRequest is the daemon's variant of session.start.
// It accepts an explicit workdir + model (the inspection-only
// desktopapi.SessionStartRequest does not — by design, since Tier 2
// usage was a single global session at scaffold time).
type DaemonSessionStartRequest struct {
	Workdir   string  `json:"workdir"`
	Model     string  `json:"model,omitempty"`
	Prompt    string  `json:"prompt,omitempty"`
	SkillPack string  `json:"skill_pack,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	BudgetUSD float64 `json:"budget_usd,omitempty"`
}

// DaemonSessionStartResponse mirrors desktopapi.SessionStartResponse.
type DaemonSessionStartResponse struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
}

// SessionIDRequest carries a session_id. Used by pause/resume/cancel/send.
type SessionIDRequest struct {
	SessionID string `json:"session_id"`
}

// SessionPauseResponse mirrors desktopapi.SessionPauseResponse.
type SessionPauseResponse struct {
	PausedAt string `json:"paused_at"`
}

// SessionResumeResponse mirrors desktopapi.SessionResumeResponse.
type SessionResumeResponse struct {
	ResumedAt string `json:"resumed_at"`
}

// SessionCancelResponse is the result of session.cancel.
type SessionCancelResponse struct {
	CancelledAt string `json:"cancelled_at"`
	Reason      string `json:"reason,omitempty"`
}

// SessionSendRequest is the params for session.send. Carries a single
// user turn to deliver to the cortex Workspace.
type SessionSendRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	// Role defaults to "user" when empty. Reserved for future
	// system-injection use cases (e.g. operator-issued nudges).
	Role string `json:"role,omitempty"`
}

// SessionSendResponse acknowledges a delivered turn.
type SessionSendResponse struct {
	DeliveredAt string `json:"delivered_at"`
	// Seq, when non-zero, is the journal seq the daemon assigned to
	// the inbound turn (so the caller can correlate with subscriber
	// events that fire as a consequence).
	Seq uint64 `json:"seq,omitempty"`
}

// SessionSubscribeRequest is the params for session.subscribe.
//
// SinceSeq, when non-zero, requests a journal replay starting after
// the given seq BEFORE any live deltas are streamed. The daemon's
// implementation MUST honour the replay-before-live ordering (TASK-32);
// see internal/server/jsonrpc/subscribe.go for the helper that drives
// it.
type SessionSubscribeRequest struct {
	SessionID string `json:"session_id"`
	SinceSeq  uint64 `json:"since_seq,omitempty"`
	// Filter, when non-empty, restricts which event types are
	// delivered. Empty means "all events". Values match the canonical
	// hub.EventType strings (e.g. "lane.delta", "session.delta").
	Filter []string `json:"filter,omitempty"`
}

// SessionSubscribeResponse acknowledges the subscription. The actual
// events flow as `$/event` notifications carrying SubscriptionEvent
// payloads (see SubscriptionEvent below).
type SessionSubscribeResponse struct {
	SubID string `json:"sub"`
}

// SessionUnsubscribeRequest is the params for session.unsubscribe.
type SessionUnsubscribeRequest struct {
	SubID string `json:"sub"`
}

// SessionUnsubscribeResponse acknowledges teardown.
type SessionUnsubscribeResponse struct {
	UnsubscribedAt string `json:"unsubscribed_at"`
}

// SubscriptionEvent is the payload of every `$/event` notification.
// Per spec §11.32: `{sub, seq, type, data}`.
type SubscriptionEvent struct {
	SubID string `json:"sub"`
	Seq   uint64 `json:"seq"`
	Type  string `json:"type"`
	Data  any    `json:"data,omitempty"`
}

// LanesListRequest is the params for lanes.list.
type LanesListRequest struct {
	SessionID string `json:"session_id"`
}

// LaneSummary is one row in lanes.list.
type LaneSummary struct {
	LaneID    string            `json:"lane_id"`
	Kind      string            `json:"kind"`
	ParentID  string            `json:"parent_id,omitempty"`
	Label     string            `json:"label,omitempty"`
	Status    string            `json:"status"`
	StartedAt string            `json:"started_at"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// LanesListResponse is the result of lanes.list.
type LanesListResponse struct {
	Lanes []LaneSummary `json:"lanes"`
}

// LanesKillRequest is the params for lanes.kill.
type LanesKillRequest struct {
	SessionID string `json:"session_id"`
	LaneID    string `json:"lane_id"`
	Reason    string `json:"reason,omitempty"`
}

// LanesKillResponse acknowledges the kill.
type LanesKillResponse struct {
	KilledAt string `json:"killed_at"`
}

// CortexNotesRequest is the params for cortex.notes.
type CortexNotesRequest struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"`
}

// CortexNote is one note.
type CortexNote struct {
	ID       string `json:"id"`
	LaneID   string `json:"lane_id,omitempty"`
	Severity string `json:"severity"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	At       string `json:"at"`
}

// CortexNotesResponse is the result of cortex.notes.
type CortexNotesResponse struct {
	Notes []CortexNote `json:"notes"`
}

// DaemonInfoResponse is the result of daemon.info.
type DaemonInfoResponse struct {
	PID          int    `json:"pid"`
	Version      string `json:"version"`
	StartedAt    string `json:"started_at"`
	SocketPath   string `json:"socket_path,omitempty"`
	HTTPPort     int    `json:"http_port,omitempty"`
	SessionCount int    `json:"session_count"`
}

// DaemonShutdownRequest is the params for daemon.shutdown.
type DaemonShutdownRequest struct {
	// GraceSeconds, when > 0, gives running sessions that long to
	// drain before SIGKILL. Zero = use daemon default.
	GraceSeconds int `json:"grace_seconds,omitempty"`
}

// DaemonShutdownResponse acknowledges the shutdown request.
type DaemonShutdownResponse struct {
	AcceptedAt string `json:"accepted_at"`
}

// DaemonReloadConfigRequest is the params for daemon.reload_config.
type DaemonReloadConfigRequest struct {
	// Path, when non-empty, is an explicit config file to load. Empty
	// means "re-read the file the daemon was started with".
	Path string `json:"path,omitempty"`
}

// DaemonReloadConfigResponse acknowledges the reload.
type DaemonReloadConfigResponse struct {
	ReloadedAt string `json:"reloaded_at"`
	// Source is the absolute path of the config that was applied.
	Source string `json:"source,omitempty"`
}

// ----------------------------------------------------------------------
// DaemonAPI interface
// ----------------------------------------------------------------------

// DaemonAPI is the dependency injection surface for the daemon-only
// verbs. The r1d-server daemon embeds an implementation; tests stub
// it. Methods are split by namespace to keep the interface readable
// and to make incremental implementation feasible (TASK-31 lands the
// signatures, later phases swap the bodies).
//
// Errors should be *stokerr.Error or desktopapi.ErrNotImplemented for
// stubbed verbs; the dispatcher's ErrorFromGo translates both.
type DaemonAPI interface {
	// session.* (daemon variant)
	DaemonSessionStart(ctx context.Context, req DaemonSessionStartRequest) (DaemonSessionStartResponse, error)
	DaemonSessionPause(ctx context.Context, req SessionIDRequest) (SessionPauseResponse, error)
	DaemonSessionResume(ctx context.Context, req SessionIDRequest) (SessionResumeResponse, error)
	DaemonSessionCancel(ctx context.Context, req SessionIDRequest) (SessionCancelResponse, error)
	DaemonSessionSend(ctx context.Context, req SessionSendRequest) (SessionSendResponse, error)
	DaemonSessionSubscribe(ctx context.Context, req SessionSubscribeRequest) (SessionSubscribeResponse, error)
	DaemonSessionUnsubscribe(ctx context.Context, req SessionUnsubscribeRequest) (SessionUnsubscribeResponse, error)

	// lanes.*
	DaemonLanesList(ctx context.Context, req LanesListRequest) (LanesListResponse, error)
	DaemonLanesKill(ctx context.Context, req LanesKillRequest) (LanesKillResponse, error)

	// cortex.*
	DaemonCortexNotes(ctx context.Context, req CortexNotesRequest) (CortexNotesResponse, error)

	// daemon.*
	DaemonInfo(ctx context.Context) (DaemonInfoResponse, error)
	DaemonShutdown(ctx context.Context, req DaemonShutdownRequest) (DaemonShutdownResponse, error)
	DaemonReloadConfig(ctx context.Context, req DaemonReloadConfigRequest) (DaemonReloadConfigResponse, error)
}

// RegisterDaemonAPI wires every DaemonAPI method onto the dispatcher.
// nil dispatcher / handler panics for the same reason as
// RegisterDesktopAPI.
//
// NOTE on session.start collision: when a daemon ALSO mounts the
// desktopapi.Handler verbs (TASK-30) AND the DaemonAPI verbs, the
// daemon variant of session.start wins (last Register call). This is
// intentional — the daemon's session.start accepts workdir/model
// (which the desktopapi variant doesn't), so a real daemon must use
// the daemon variant. Test setups can flip the ordering.
func RegisterDaemonAPI(d *Dispatcher, h DaemonAPI) {
	if d == nil {
		panic("jsonrpc: RegisterDaemonAPI: nil dispatcher")
	}
	if h == nil {
		panic("jsonrpc: RegisterDaemonAPI: nil daemon handler")
	}

	d.Register("session.start", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req DaemonSessionStartRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionStart(ctx, req)
	})
	d.Register("session.pause", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req SessionIDRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionPause(ctx, req)
	})
	d.Register("session.resume", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req SessionIDRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionResume(ctx, req)
	})
	d.Register("session.cancel", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req SessionIDRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionCancel(ctx, req)
	})
	d.Register("session.send", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req SessionSendRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionSend(ctx, req)
	})
	d.Register("session.subscribe", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req SessionSubscribeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionSubscribe(ctx, req)
	})
	d.Register("session.unsubscribe", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req SessionUnsubscribeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonSessionUnsubscribe(ctx, req)
	})

	d.Register("lanes.list", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req LanesListRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonLanesList(ctx, req)
	})
	d.Register("lanes.kill", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req LanesKillRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonLanesKill(ctx, req)
	})

	d.Register("cortex.notes", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req CortexNotesRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonCortexNotes(ctx, req)
	})

	d.Register("daemon.info", func(ctx context.Context, params json.RawMessage) (any, error) {
		return h.DaemonInfo(ctx)
	})
	d.Register("daemon.shutdown", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req DaemonShutdownRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonShutdown(ctx, req)
	})
	d.Register("daemon.reload_config", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req DaemonReloadConfigRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DaemonReloadConfig(ctx, req)
	})
}
