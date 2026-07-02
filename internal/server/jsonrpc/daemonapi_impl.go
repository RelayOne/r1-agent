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
// HubHandler implements every method on DaemonAPI, and every verb is
// wired to a real primitive: start via hub.Create, list via DaemonInfo,
// cancel via hub.Delete, pause/resume via Session.Pause/Resume (pause
// flag + resume channel), and send via Session.Send (bounded inbox the
// agent loop drains between provider calls). There are no stubbed
// verbs left in this file.
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
//   - ErrValidation  — bad input (empty session_id, empty send text,
//     send to a not-running session whose inbox is closed)
//   - ErrNotFound    — session_id / subscription does not exist
//   - ErrConflict    — session already exists, single-session mode
//     active, or send inbox full (back-pressure; retry shortly)
//   - ErrInternal    — hub operation failed, or a daemon-supplied
//     callback (shutdownFn, reloadConfigFn, ConnFromContextFunc) was
//     never configured by the serve loop
package jsonrpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/journal"
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

	// mu guards the function pointers below. The handler is
	// constructed by the daemon's startup glue (single goroutine),
	// then read concurrently by RPC handlers; mu makes the
	// install-once-then-read-many pattern explicitly safe.
	mu sync.Mutex

	// shutdownFn is the daemon-supplied callback that signals the
	// serve loop to drain in-flight work and exit. Installed via
	// SetShutdownFunc; nil means daemon.shutdown returns ErrInternal.
	// Called in a fresh goroutine from DaemonShutdown so the WS
	// response flushes first.
	shutdownFn func(graceSeconds int)

	// reloadConfigFn is the daemon-supplied callback for
	// daemon.reload_config. Receives the operator's override path
	// (empty means "re-read the daemon's default config"); returns
	// the absolute path actually applied. Installed via
	// SetReloadConfigFunc; nil means daemon.reload_config returns
	// ErrInternal.
	reloadConfigFn func(path string) (string, error)

	// journalPathFn resolves the per-session journal file the event
	// pipe appends to and subscribe replays from. Installed via
	// SetJournalPathFn (guarded by mu like the other callbacks); nil
	// disables journaling and replay.
	journalPathFn func(sessionID string) string

	// fanout is the live subscription registry: sessionID -> SubID ->
	// *Subscription. Guarded by fanoutMu (its own mutex - publish is
	// hot-path and must not contend with the callback installers).
	// See fanout.go for the publish/subscribe engine.
	fanoutMu sync.Mutex
	fanout   map[string]map[string]*Subscription

	// journals tracks the per-session journal writers opened by
	// attachSessionEventPipe so DaemonSessionCancel can close them.
	journalsMu sync.Mutex
	journals   map[string]*journal.Writer
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
	// Wire the session's event pipe: journal-first persistence plus
	// live subscription fanout (fanout.go). Without this, subscribers
	// would only ever see replayed history (audit A069).
	h.attachSessionEventPipe(s)
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
	h.closeSessionJournal(req.SessionID)
	return SessionCancelResponse{
		CancelledAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonSessionSend delivers a user turn onto the session's inbox.
// Returns ErrValidation when text is empty or the session is not
// running (inbox closed); ErrConflict when the inbox is full.
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

// SubscriptionRegistry is the per-connection storage interface a
// HubHandler needs to register / unregister Subscriptions. The WS
// layer's *ws.Conn satisfies it via RegisterSubscription /
// UnregisterSubscription (see internal/server/ws/handler.go).
//
// Defining this here as a structural interface keeps the jsonrpc
// package free of an import cycle into ws.
type SubscriptionRegistry interface {
	RegisterSubscription(subID string, closer interface{ Close() })
	UnregisterSubscription(subID string) interface{ Close() }
}

// ConnFromContextFunc is the hook the WS layer registers so handlers
// running inside Dispatcher.Dispatch can pull the live conn out of
// the dispatch ctx. The daemon's startup glue sets this once before
// mounting routes; tests can install a fake.
//
// Returns a SubscriptionRegistry-shaped value or nil when the ctx
// did not come from a WS dispatch path (e.g., HTTP-only routes,
// in-process tests).
var ConnFromContextFunc func(ctx context.Context) SubscriptionRegistry

// DaemonSessionSubscribe mints a *Subscription whose sink writes
// JSON-RPC `$/event` notifications back to the caller's conn,
// registers it on the conn (so unsubscribe can find it and connection
// teardown drains it) AND in the HubHandler live fanout (fanout.go),
// then kicks off the journal replay asynchronously so the subscribe
// response reaches the wire before the replay frames.
//
// The prior scaffold sink here wrote to /dev/null (audit A069); the
// sink is now the real per-connection notification writer, and
// PublishSessionEvent delivers live events for the session into every
// registered subscription.
func (h *HubHandler) DaemonSessionSubscribe(ctx context.Context, req SessionSubscribeRequest) (SessionSubscribeResponse, error) {
	if _, err := h.lookupSession(req.SessionID); err != nil {
		return SessionSubscribeResponse{}, err
	}
	if ConnFromContextFunc == nil {
		return SessionSubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
			"session.subscribe: ConnFromContextFunc not configured; daemon serve loop must register the WS bridge at startup")
	}
	reg := ConnFromContextFunc(ctx)
	if reg == nil {
		return SessionSubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
			"session.subscribe: must be invoked over a WS connection (no Conn on ctx)")
	}
	writer, ok := reg.(NotificationWriter)
	if !ok {
		return SessionSubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
			"session.subscribe: connection cannot deliver $/event notifications (no WriteNotification)")
	}
	subID := mintSubID()
	sink := func(sctx context.Context, ev *SubscriptionEvent) error {
		return writer.WriteNotification(sctx, NewNotification("$/event", ev))
	}
	sub := NewSubscription(subID, req.SessionID, sink, req.Filter)
	h.registerFanout(req.SessionID, sub)
	handle := &fanoutSubscription{sub: sub, onClose: func() {
		h.unregisterFanout(req.SessionID, subID)
	}}
	reg.RegisterSubscription(subID, handle)

	// Replay asynchronously: the RPC response (carrying SubID) should
	// hit the wire before replay frames. Live events that fire while
	// replay runs buffer inside the Subscription and flush after, per
	// the replay-before-live contract (spec section 11.32). ctx here
	// is the per-connection dispatch ctx, cancelled on conn teardown.
	replayer := h.replayerFor(req.SessionID)
	sinceSeq := req.SinceSeq
	go func() {
		if err := sub.Replay(ctx, sinceSeq, replayer); err != nil {
			// Best-effort error notification, then tear down; the
			// client re-subscribes with its last seq.
			_ = sink(ctx, &SubscriptionEvent{SubID: subID, Type: "error",
				Data: map[string]string{"message": err.Error()}})
			handle.Close()
		}
	}()
	return SessionSubscribeResponse{SubID: subID}, nil
}

// DaemonSessionInterrupt implements the drop-partial interrupt
// protocol (specs/web-chat-ui.md; docs/HOW-IT-WORKS.md step 7): cancel
// the session's in-flight Run context so the streaming turn aborts
// and the partial assistant message is never persisted, while the
// session itself stays registered for the next turn. Idempotent: an
// idle session reports WasRunning=false and no error.
func (h *HubHandler) DaemonSessionInterrupt(ctx context.Context, req SessionInterruptRequest) (SessionInterruptResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return SessionInterruptResponse{}, err
	}
	wasRunning := sess.Interrupt()
	return SessionInterruptResponse{
		InterruptedAt: h.now().UTC().Format(time.RFC3339Nano),
		WasRunning:    wasRunning,
	}, nil
}

// DaemonSessionUnsubscribe finds the prior subscription on the
// caller's conn and closes it. ErrNotFound when no such SubID is
// registered.
func (h *HubHandler) DaemonSessionUnsubscribe(ctx context.Context, req SessionUnsubscribeRequest) (SessionUnsubscribeResponse, error) {
	if strings.TrimSpace(req.SubID) == "" {
		return SessionUnsubscribeResponse{}, stokerr.Validationf("session.unsubscribe: sub is required")
	}
	if ConnFromContextFunc == nil {
		return SessionUnsubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
			"session.unsubscribe: ConnFromContextFunc not configured")
	}
	reg := ConnFromContextFunc(ctx)
	if reg == nil {
		return SessionUnsubscribeResponse{}, stokerr.New(stokerr.ErrInternal,
			"session.unsubscribe: must be invoked over a WS connection")
	}
	closer := reg.UnregisterSubscription(req.SubID)
	if closer == nil {
		return SessionUnsubscribeResponse{}, stokerr.NotFoundf("session.unsubscribe: sub %q not found", req.SubID)
	}
	closer.Close()
	return SessionUnsubscribeResponse{
		UnsubscribedAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// mintSubID generates a unique subscription handle. Uses a random
// 16-byte hex string — enough collision resistance for per-conn
// subscriptions.
func mintSubID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "sub-" + hex.EncodeToString(b[:])
}

// laneRegistry is the structural slice of cortex.Workspace that the
// lanes.* handlers need. Defining it as an interface keeps this
// package independent of cortex internals and lets tests substitute a
// fake registry. *cortex.Workspace satisfies it via Lanes() / GetLane().
type laneRegistry interface {
	Lanes() []*cortex.Lane
	GetLane(string) (*cortex.Lane, bool)
}

// DaemonLanesList lists per-session lanes by reading the
// cortex.Workspace lane registry. Sessions whose Workspace is nil
// (no Run yet, or no cortex configured) return an empty list rather
// than an error — empty is the honest answer.
func (h *HubHandler) DaemonLanesList(ctx context.Context, req LanesListRequest) (LanesListResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return LanesListResponse{}, err
	}
	if sess.Workspace == nil {
		return LanesListResponse{Lanes: []LaneSummary{}}, nil
	}
	reg, ok := sess.Workspace.(laneRegistry)
	if !ok {
		return LanesListResponse{}, stokerr.New(stokerr.ErrInternal,
			"lanes.list: session.Workspace does not implement Lanes()/GetLane()")
	}
	lanes := reg.Lanes()
	out := make([]LaneSummary, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, LaneSummary{
			LaneID:    l.ID,
			Kind:      string(l.Kind),
			ParentID:  l.ParentID,
			Label:     l.Label,
			Status:    string(l.Status),
			StartedAt: l.StartedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return LanesListResponse{Lanes: out}, nil
}

// DaemonLanesKill kills one lane via cortex.Lane.Kill, which emits
// lane.killed + terminal lane.status events. Idempotent for already-
// terminal lanes (Lane.Kill is a no-op in that case).
func (h *HubHandler) DaemonLanesKill(ctx context.Context, req LanesKillRequest) (LanesKillResponse, error) {
	sess, err := h.lookupSession(req.SessionID)
	if err != nil {
		return LanesKillResponse{}, err
	}
	if strings.TrimSpace(req.LaneID) == "" {
		return LanesKillResponse{}, stokerr.Validationf("lanes.kill: lane_id is required")
	}
	if sess.Workspace == nil {
		return LanesKillResponse{}, stokerr.NotFoundf("lanes.kill: session has no workspace; lane %q not found", req.LaneID)
	}
	reg, ok := sess.Workspace.(laneRegistry)
	if !ok {
		return LanesKillResponse{}, stokerr.New(stokerr.ErrInternal,
			"lanes.kill: session.Workspace does not implement Lanes()/GetLane()")
	}
	lane, found := reg.GetLane(req.LaneID)
	if !found {
		return LanesKillResponse{}, stokerr.NotFoundf("lanes.kill: lane %q not found", req.LaneID)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "killed via daemon RPC"
	}
	if err := lane.Kill(reason); err != nil {
		return LanesKillResponse{}, stokerr.Wrap(stokerr.ErrInternal, "lanes.kill", err)
	}
	return LanesKillResponse{
		KilledAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
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

// DaemonShutdown signals the daemon to wind down. The actual
// signal-the-serve-loop primitive lives in cmd/r1/serve_cmd.go; this
// handler invokes a callback the daemon's startup glue installed via
// SetShutdownFunc. When no callback is wired (test scenarios, embedded
// uses), returns ErrInternal so callers don't think shutdown succeeded.
//
// GraceSeconds is the in-flight-drain budget. Zero falls through to
// the daemon's default. The handler returns AcceptedAt immediately;
// the actual shutdown happens asynchronously so the WS reply gets
// flushed before the listener closes.
func (h *HubHandler) DaemonShutdown(ctx context.Context, req DaemonShutdownRequest) (DaemonShutdownResponse, error) {
	h.mu.Lock()
	cb := h.shutdownFn
	h.mu.Unlock()
	if cb == nil {
		return DaemonShutdownResponse{}, stokerr.New(stokerr.ErrInternal,
			"daemon.shutdown: shutdown callback not configured")
	}
	// Fire the shutdown callback async so the WS response flushes
	// before the listener tears down. Daemon owns the actual drain
	// semantics; we just pass through the operator-supplied grace.
	go cb(req.GraceSeconds)
	return DaemonShutdownResponse{
		AcceptedAt: h.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// DaemonReloadConfig re-reads the daemon's config file by invoking
// the reload callback the daemon installed via SetReloadConfigFunc.
// req.Path, when non-empty, overrides the default config path.
//
// Returns the absolute path of the config that was applied (so the
// operator can confirm which file was read) plus ReloadedAt.
func (h *HubHandler) DaemonReloadConfig(ctx context.Context, req DaemonReloadConfigRequest) (DaemonReloadConfigResponse, error) {
	h.mu.Lock()
	cb := h.reloadConfigFn
	h.mu.Unlock()
	if cb == nil {
		return DaemonReloadConfigResponse{}, stokerr.New(stokerr.ErrInternal,
			"daemon.reload_config: reload callback not configured")
	}
	source, err := cb(req.Path)
	if err != nil {
		return DaemonReloadConfigResponse{}, stokerr.Wrap(stokerr.ErrInternal, "daemon.reload_config", err)
	}
	return DaemonReloadConfigResponse{
		ReloadedAt: h.now().UTC().Format(time.RFC3339Nano),
		Source:     source,
	}, nil
}

// SetShutdownFunc installs the daemon-supplied shutdown callback.
// The callback receives the operator-supplied GraceSeconds (zero
// means "use daemon default"). The daemon's startup glue calls this
// once after constructing the HubHandler.
//
// fn is called in a fresh goroutine by DaemonShutdown so the WS
// response flushes before the listener closes; fn itself should be
// non-blocking (signal the serve loop's shutdown channel and return).
func (h *HubHandler) SetShutdownFunc(fn func(graceSeconds int)) {
	h.mu.Lock()
	h.shutdownFn = fn
	h.mu.Unlock()
}

// SetReloadConfigFunc installs the daemon-supplied config-reload
// callback. fn receives the operator-supplied path (empty means
// "re-read the default config") and returns the absolute path of
// the config that was applied, or an error.
func (h *HubHandler) SetReloadConfigFunc(fn func(path string) (string, error)) {
	h.mu.Lock()
	h.reloadConfigFn = fn
	h.mu.Unlock()
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
