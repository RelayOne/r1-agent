// jsonrpc/daemonapi_impl.go — production DaemonAPI implementation.
//
// Phase E item 31 declared the DaemonAPI interface and its dispatcher
// adapters; Phase F (this file) lands the FIRST production handler that
// actually creates Sessions through SessionHub instead of returning the
// scaffold's NotImplemented. Until this file, the only DaemonAPI impl in
// the repo was `fakeDaemon` in daemonapi_test.go — useful for round-trip
// coverage of the dispatcher, but never reachable from `r1 serve`.
//
// # Surface area
//
// HubHandler implements every method on DaemonAPI. The ones that map
// cleanly onto SessionHub (start, list-via-DaemonInfo, cancel-as-delete)
// are fully wired. The ones that depend on Session methods that don't
// yet exist (pause/resume as distinct lifecycle states; multi-turn send
// delivery) return a stokerr-tagged BLOCKED error that names the
// missing dependency, so the dispatcher surfaces a proper RPC fault to
// the caller instead of crashing the daemon. Each such stub is
// annotated with a `BLOCKED:` marker and a reference to the dependency
// that owns the missing piece — that's the single source of truth for
// "what's left to wire" reviewers will look for.
//
// # Why a thin handler
//
// The handler is intentionally a glue layer: it does decoding, hub
// lookup, and error wrapping — nothing more. The real logic lives in
// SessionHub and (eventually) Session.Pause/Resume/Cancel/Send. Keeping
// HubHandler thin means a future change that adds, say, Session.Pause
// only needs to touch ONE call site here, and the signature contract
// (DaemonAPI) does not change.
//
// # Errors
//
// All errors are *stokerr.Error so the dispatcher's ErrorFromGo path
// emits structured RPC error codes:
//
//   - ErrValidation  — bad input (empty session_id, etc.)
//   - ErrNotFound    — session_id does not exist in the hub
//   - ErrInternal    — sub-handler is BLOCKED on a missing dependency
//
// The BLOCKED surface uses ErrInternal because there is no dedicated
// taxonomy code for "feature pending". A reviewer can grep `BLOCKED:`
// to find the unwired set in one shot.
package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// HubHandler is the production DaemonAPI implementation backed by a
// SessionHub. The daemon's serve loop constructs a single HubHandler at
// startup and passes it to RegisterDaemonAPI.
//
// Concurrency: HubHandler is goroutine-safe — every operation delegates
// to SessionHub (sync.Map-backed) or to *Session (runMu-backed). The
// handler holds no mutable state of its own beyond the immutable hub
// pointer + clock.
type HubHandler struct {
	// Hub is the per-daemon session registry. Required (NewHubHandler
	// panics on nil).
	Hub *sessionhub.SessionHub

	// PID is the daemon's process id. Surfaced via DaemonInfo. Set by
	// the daemon's startup glue from os.Getpid().
	PID int

	// Version is the build version string surfaced via DaemonInfo.
	Version string

	// StartedAt is the daemon's boot time, surfaced via DaemonInfo. Set
	// by the daemon's startup glue.
	StartedAt time.Time

	// SocketPath / HTTPPort populate the corresponding DaemonInfo fields
	// when non-zero. Either or both may be empty depending on transport.
	SocketPath string
	HTTPPort   int

	// nowFn is an injectable clock for deterministic StartedAt /
	// CancelledAt / etc. Defaults to time.Now when nil.
	nowFn func() time.Time
}

// NewHubHandler constructs a production HubHandler. nil hub is a
// programmer error (the daemon must always have a hub).
func NewHubHandler(hub *sessionhub.SessionHub) *HubHandler {
	if hub == nil {
		panic("jsonrpc: NewHubHandler: nil hub")
	}
	return &HubHandler{Hub: hub}
}

// now returns the current time using nowFn when set (tests inject a
// frozen clock) or time.Now otherwise.
func (h *HubHandler) now() time.Time {
	if h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now()
}

// SetClock installs a clock function for deterministic timestamps.
// Tests use this to assert exact CancelledAt strings.
func (h *HubHandler) SetClock(fn func() time.Time) {
	h.nowFn = fn
}

// DaemonSessionStart creates a new Session in the hub.
//
// Validation flow:
//
//  1. Reject an empty Workdir up front with ErrValidation. SessionHub
//     also rejects empty workdirs but the duplicated check here gives
//     a clearer error code (validation vs. invalid_workdir wrapping).
//  2. Delegate to hub.Create which performs the full workdir-policy
//     check (absolute, exists, writable, not under ~/.r1/). On
//     sessionhub.ErrInvalidWorkdir, wrap with ErrValidation so the
//     caller sees a structured RPC error.
//  3. On success, return the minted session id + StartedAt timestamp.
//
// Note: the returned Session is NOT started. The agent loop runs only
// when a follow-up call (currently session.send, eventually a separate
// session.start_run verb) drives Session.Run. This matches the spec
// §11.21 contract: Create registers the session; Run is a separate
// concern owned by the daemon's execution glue.
func (h *HubHandler) DaemonSessionStart(ctx context.Context, req DaemonSessionStartRequest) (DaemonSessionStartResponse, error) {
	if strings.TrimSpace(req.Workdir) == "" {
		return DaemonSessionStartResponse{}, stokerr.Validationf("session.start: workdir is required")
	}
	s, err := h.Hub.Create(sessionhub.CreateOptions{
		Workdir: req.Workdir,
		Model:   req.Model,
	})
	if err != nil {
		// Map sessionhub error codes to stokerr taxonomy. ErrInvalidWorkdir
		// is a validation failure; ErrSessionExists / ErrSingleSessionExceeded
		// are conflicts; anything else is internal.
		switch {
		case errors.Is(err, sessionhub.ErrInvalidWorkdir):
			return DaemonSessionStartResponse{}, stokerr.Wrap(stokerr.ErrValidation, "session.start: invalid workdir", err)
		case errors.Is(err, sessionhub.ErrSessionExists):
			return DaemonSessionStartResponse{}, stokerr.Wrap(stokerr.ErrConflict, "session.start: session already exists", err)
		case errors.Is(err, sessionhub.ErrSingleSessionExceeded):
			return DaemonSessionStartResponse{}, stokerr.Wrap(stokerr.ErrConflict, "session.start: single-session mode active", err)
		default:
			return DaemonSessionStartResponse{}, stokerr.Wrap(stokerr.ErrInternal, "session.start: hub.Create failed", err)
		}
	}
	return DaemonSessionStartResponse{
		SessionID: s.ID,
		StartedAt: s.StartedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonSessionPause suspends a session by setting its pause flag.
// The agent loop's per-turn observer waits on the resume signal
// before invoking the provider; until Resume fires, no model calls
// happen and no tools dispatch. Idempotent — pausing an
// already-paused session is a no-op.
func (h *HubHandler) DaemonSessionPause(ctx context.Context, req SessionIDRequest) (SessionPauseResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return SessionPauseResponse{}, err
	}
	sess.Pause()
	return SessionPauseResponse{
		PausedAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonSessionResume clears the pause flag and signals waiters.
// Idempotent — resuming a non-paused session is a no-op.
func (h *HubHandler) DaemonSessionResume(ctx context.Context, req SessionIDRequest) (SessionResumeResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return SessionResumeResponse{}, err
	}
	sess.Resume()
	return SessionResumeResponse{
		ResumedAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonSessionCancel cancels a session.
//
// We DO have a working cancellation primitive already: SessionHub.Delete
// invokes the session's cancelRun and removes it from the registry. So
// for cancel-equivalent semantics ("cancel and forget") this handler
// actually works. We delegate to hub.Delete here.
//
// Note: the spec leaves ambiguous whether session.cancel should also
// DELETE the session (current behavior) or keep the entry around in a
// "cancelled" state for inspection. The safest default for now is
// delete-and-forget; a follow-up that adds SessionStateCancelled +
// state-only transition can swap the body without changing the wire
// contract.
func (h *HubHandler) DaemonSessionCancel(ctx context.Context, req SessionIDRequest) (SessionCancelResponse, error) {
	if _, err := h.lookupSession(req.SessionID); err != nil {
		return SessionCancelResponse{}, err
	}
	if err := h.Hub.Delete(req.SessionID); err != nil {
		return SessionCancelResponse{}, stokerr.Wrap(stokerr.ErrInternal, "session.cancel: hub.Delete failed", err)
	}
	return SessionCancelResponse{
		CancelledAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonSessionSend delivers a user turn onto the session's inbox.
// Returns ErrValidation when text is empty or the session is not
// running (inbox closed); ErrUnavailable when the inbox is full.
// Otherwise returns the assigned delivery time. Multi-turn delivery
// is enabled by Session.Send pushing onto an inbox channel the
// agent loop drains between provider calls.
func (h *HubHandler) DaemonSessionSend(ctx context.Context, req SessionSendRequest) (SessionSendResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return SessionSendResponse{}, err
	}
	if strings.TrimSpace(req.Text) == "" {
		return SessionSendResponse{}, stokerr.Validationf("session.send: text is required")
	}
	turn := sessionhub.InboundTurn{Role: req.Role, Text: req.Text}
	if err := sess.Send(turn); err != nil {
		// Map the typed errors to taxonomy codes so callers can
		// distinguish "session ended" (closed) from "back-pressured"
		// (full).
		if errors.Is(err, sessionhub.ErrSessionInputClosed) {
			return SessionSendResponse{}, stokerr.Validationf("session.send: session is not running (inbox closed)")
		}
		if errors.Is(err, sessionhub.ErrSessionInputFull) {
			return SessionSendResponse{}, stokerr.Conflictf("session.send: inbox full; retry shortly")
		}
		return SessionSendResponse{}, stokerr.Wrap(stokerr.ErrInternal, "session.send", err)
	}
	return SessionSendResponse{
		DeliveredAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonSessionSubscribe is the SUBSCRIBE-side acknowledgement. The
// actual event fanout happens in the WS layer's per-connection bridge
// (see ws/handler.go's OnConnect hook + jsonrpc/subscribe.go's
// Subscription.Replay/Publish). When that bridge is mounted, this
// handler is called solely so the JSON-RPC reply carries the negotiated
// SubID; when it is NOT mounted, this handler must still return
// something coherent so a misconfigured daemon doesn't silently swallow
// the call.
//
// BLOCKED: the WS bridge is what actually stamps the SubID, and that
// bridge runs in a different code path (ServeHTTP, not
// Dispatcher.Dispatch). Until the daemon's startup glue wires a
// per-connection subscriber registry that this handler can consult,
// the dispatcher-level surface is honest about the missing piece.
func (h *HubHandler) DaemonSessionSubscribe(ctx context.Context, req SessionSubscribeRequest) (SessionSubscribeResponse, error) {
	if _, err := h.lookupSession(req.SessionID); err != nil {
		return SessionSubscribeResponse{}, err
	}
	// BLOCKED: depends on per-conn subscriber registry wired through
	// the WS handler's OnConnect bridge.
	return SessionSubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
		"session.subscribe: BLOCKED on WS bridge per-conn subscriber registry (follow-up)")
}

// DaemonSessionUnsubscribe — paired with subscribe; same gating.
//
// BLOCKED: paired with subscribe.
func (h *HubHandler) DaemonSessionUnsubscribe(ctx context.Context, req SessionUnsubscribeRequest) (SessionUnsubscribeResponse, error) {
	// BLOCKED: depends on per-conn subscriber registry (paired with subscribe).
	return SessionUnsubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
		"session.unsubscribe: BLOCKED on WS bridge per-conn subscriber registry (follow-up)")
}

// DaemonLanesList lists per-session lanes.
//
// BLOCKED on cortex.Workspace lane registry: lanes-protocol fanout
// currently flows through the hub.Bus subscriber path on the WS
// handler; there is no per-session lane index on Session yet.
// Returning an empty list is the correct "no lanes" answer for sessions
// that haven't started a Run; for active sessions a follow-up must
// thread cortex.Workspace's lane registry through Session. For now we
// return an empty list (rather than ErrInternal) because empty IS a
// valid answer pre-Run and the wire surface is otherwise honest.
func (h *HubHandler) DaemonLanesList(ctx context.Context, req LanesListRequest) (LanesListResponse, error) {
	if _, err := h.lookupSession(req.SessionID); err != nil {
		return LanesListResponse{}, err
	}
	// BLOCKED: cortex.Workspace lane registry hookup (follow-up). Empty
	// list is correct for sessions without an active Run.
	return LanesListResponse{Lanes: nil}, nil
}

// DaemonLanesKill kills one lane.
//
// BLOCKED: paired with lanes.list — needs the same cortex.Workspace
// lane-registry hookup.
func (h *HubHandler) DaemonLanesKill(ctx context.Context, req LanesKillRequest) (LanesKillResponse, error) {
	if _, err := h.lookupSession(req.SessionID); err != nil {
		return LanesKillResponse{}, err
	}
	// BLOCKED: depends on cortex.Workspace lane registry (follow-up).
	return LanesKillResponse{}, stokerr.New(stokerr.ErrInternal,
		"lanes.kill: BLOCKED on cortex.Workspace lane registry (follow-up)")
}

// DaemonCortexNotes returns recent cortex notes for a session.
// Source-of-truth is the per-session cortex.Workspace, which holds an
// in-memory Note slice. We project Workspace.Snapshot() into the wire
// shape and trim to req.Limit (most-recent-first). When the session
// has no Workspace attached (Workspace is nil), an empty list is the
// honest answer — no Lobes have published.
//
// The journal-reader path (replay Notes after daemon restart) is a
// separate follow-up; this handler covers the live in-memory case
// which is the common path for dashboards / external UIs polling a
// running session.
func (h *HubHandler) DaemonCortexNotes(ctx context.Context, req CortexNotesRequest) (CortexNotesResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return CortexNotesResponse{}, err
	}
	if sess.Workspace == nil {
		return CortexNotesResponse{Notes: []CortexNote{}}, nil
	}
	ws, ok := sess.Workspace.(interface {
		Snapshot() []cortex.Note
	})
	if !ok {
		return CortexNotesResponse{}, stokerr.New(stokerr.ErrInternal,
			"cortex.notes: session.Workspace does not implement Snapshot")
	}
	notes := ws.Snapshot()
	limit := req.Limit
	if limit <= 0 || limit > len(notes) {
		limit = len(notes)
	}
	// Workspace.Snapshot is oldest-first (append order). Take the
	// last `limit` entries so callers see most recent at the tail.
	start := len(notes) - limit
	out := make([]CortexNote, 0, limit)
	for _, n := range notes[start:] {
		out = append(out, CortexNote{
			ID:       n.ID,
			LaneID:   n.LobeID,
			Severity: string(n.Severity),
			Title:    n.Title,
			Body:     n.Body,
			At:       n.EmittedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return CortexNotesResponse{Notes: out}, nil
}

// DaemonInfo reports daemon-level metadata. This handler is fully wired:
// SessionCount comes from hub.List(), the rest from HubHandler fields
// the daemon's startup glue populated.
func (h *HubHandler) DaemonInfo(ctx context.Context) (DaemonInfoResponse, error) {
	startedAt := ""
	if !h.StartedAt.IsZero() {
		startedAt = h.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return DaemonInfoResponse{
		PID:          h.PID,
		Version:      h.Version,
		StartedAt:    startedAt,
		SocketPath:   h.SocketPath,
		HTTPPort:     h.HTTPPort,
		SessionCount: len(h.Hub.List()),
	}, nil
}

// DaemonShutdown signals the daemon to wind down.
//
// BLOCKED on shutdown-callback wiring: the actual signal-the-serve-loop
// primitive lives in cmd/r1/serve_cmd.go (the signalContext channel).
// The handler would need a callback the daemon installs at startup to
// drive that signal; until that's wired, return ErrInternal so callers
// don't think shutdown succeeded.
func (h *HubHandler) DaemonShutdown(ctx context.Context, req DaemonShutdownRequest) (DaemonShutdownResponse, error) {
	// BLOCKED: depends on a daemon-supplied shutdown callback (follow-up).
	return DaemonShutdownResponse{}, stokerr.New(stokerr.ErrInternal,
		"daemon.shutdown: BLOCKED on daemon shutdown callback (follow-up)")
}

// DaemonReloadConfig re-reads the daemon's config file.
//
// BLOCKED on reload-callback wiring: config-reload primitive lives in
// daemon-config.yaml-aware code that hasn't been threaded through the
// daemon's serve loop yet.
func (h *HubHandler) DaemonReloadConfig(ctx context.Context, req DaemonReloadConfigRequest) (DaemonReloadConfigResponse, error) {
	// BLOCKED: depends on a daemon-supplied reload callback (follow-up).
	return DaemonReloadConfigResponse{}, stokerr.New(stokerr.ErrInternal,
		"daemon.reload_config: BLOCKED on daemon reload callback (follow-up)")
}

// lookupSession is the shared "get + map errors" helper used by every
// per-session verb. Empty session_id is a validation error (rather than
// a not-found) so the operator sees one error per cause.
func (h *HubHandler) lookupSession(id string) (*sessionhub.Session, error) {
	if strings.TrimSpace(id) == "" {
		return nil, stokerr.Validationf("session_id is required")
	}
	s, err := h.Hub.Get(id)
	if err != nil {
		if errors.Is(err, sessionhub.ErrSessionNotFound) {
			return nil, stokerr.Wrap(stokerr.ErrNotFound, fmt.Sprintf("session not found: %s", id), err)
		}
		return nil, stokerr.Wrap(stokerr.ErrInternal, "session lookup failed", err)
	}
	return s, nil
}

// Compile-time guarantee: HubHandler satisfies DaemonAPI.
var _ DaemonAPI = (*HubHandler)(nil)
