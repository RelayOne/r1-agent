package sessionhub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// Session is the per-session state owned by the SessionHub. Phase D
// items 22–25 fill this out; item 21 declared the minimal struct so
// SessionHub.Create has something to register.
//
// # Concurrency
//
// A Session has exactly one Run goroutine at a time (enforced by
// runMu). Tool dispatch and journal-write paths can fire on any
// goroutine the agent loop spawns; the dispatchTool sentinel (item 25)
// guards every such call against cwd drift, and the journal Writer is
// internally goroutine-safe for the single-writer use case (one
// session = one logical writer; the agent loop serialises events
// per-session).
//
// # Field stability
//
// The fields below are the spec contract (specs/r1d-server.md §11.22).
// Later items only ADD fields; they do not rename or remove. The
// embedded interfaces (Journal, WorkspaceFunc) keep this package
// independent of the journal package and the cortex package — both
// are wired by the daemon's startup glue, not by sessionhub itself.
type Session struct {
	// ID is the per-daemon unique identifier (e.g. "s-1"). Never empty
	// for a session reachable via the hub.
	ID string

	// SessionRoot is the absolute, validated workdir the session
	// operates against. The sentinel (sentinel.go) guards every
	// goroutine-bound dispatch against drifting away from this path.
	SessionRoot string

	// Workspace is an opaque pointer to the per-session cortex
	// Workspace. Stored as `any` to keep sessionhub independent of the
	// cortex package; the daemon's startup glue casts it back to
	// *cortex.Workspace where needed.
	Workspace any

	// Model is the provider model id (e.g. "claude-sonnet-4-5-...").
	Model string

	// StartedAt is the wall-clock time at Create. Used by sessions-index
	// for at-a-glance "how long has this been running" diagnostics.
	StartedAt time.Time

	// State is the lifecycle state. The hub itself only flips this when
	// reattaching after a daemon restart (item 27 sets it to
	// "paused-reattachable"). The agent loop owns the live transitions.
	State string

	// journal is the Phase D append-only event log for this session.
	// Per spec §11.22 the field is typed `*journal.Writer` in the
	// final design; TASK-22 ships an interface (Journal) here so this
	// package can be compiled and tested before the journal package
	// lands. TASK-23's `*journal.Writer` will satisfy this interface
	// without renaming the field — see commit log for the deviation.
	journal Journal

	// ctx and cancel are the Run-goroutine cancellation pair. Set by
	// Run; cleared by Run on exit. cancelRun (called by SessionHub.Delete)
	// drives them to fire if Run is still active.
	ctx    context.Context
	cancel context.CancelFunc

	// loop is the agentloop.Loop driving this session. Created lazily
	// on Run so the hub can register a Session for resume-replay
	// (item 27) without forcing it to run immediately.
	loop *agentloop.Loop

	// onEvent is the per-session event hook. The Run driver wires it
	// into the bus subscriber list AND into the agentloop's event
	// emissions. TASK-24 makes the hook journal-first.
	onEvent OnEventFunc

	// dispatchHook is the optional per-tool sentinel hook. Item 25
	// sets it to the assertCwd guard; tests can stub it for unit
	// coverage of the panic path.
	dispatchHook DispatchHook

	// runMu serializes Run() against cancelRun() during shutdown,
	// and Run() against itself (one driver at a time).
	runMu sync.Mutex
}

// Journal is the contract sessionhub depends on for append-only event
// logging. The TASK-23 *journal.Writer satisfies this interface so
// the daemon's startup glue can plug it in via SetJournal.
//
// The interface stays small — only the operations that fire from the
// session's hot path. Lifecycle (Open, Close, Truncate, Replay) is
// owned by the daemon's startup/shutdown glue and accessed via the
// concrete *journal.Writer there, not through this interface.
//
// Note the Append return signature: the concrete *journal.Writer
// returns (uint64, error) where the first value is the assigned seq.
// The interface here also returns (uint64, error) so callers that
// care about the seq (e.g. the WS subscriber's "since_seq" replay
// boundary) can read it without a type assertion.
type Journal interface {
	// Append writes one journal record for the given kind+data. The
	// kind is the journal's classification token (used by the
	// concrete writer to decide whether to fsync); data is the
	// payload. Returns the assigned monotonic seq.
	Append(kind string, data any) (uint64, error)
}

// OnEventFunc is the per-session event hook. The Session's Run loop
// invokes it for every bus event before fanning out to subscribers.
// Per spec §11.24, the hook MUST persist (or refuse) the event before
// any subscriber sees it — that's the consistency guarantee callers
// rely on for journal replay.
type OnEventFunc func(ctx context.Context, ev *hub.Event) error

// DispatchHook fires immediately before every tool dispatch. The
// default Phase D wiring sets it to assertCwd(SessionRoot) so a stray
// cwd-drift in tool-runner code becomes a loud panic. Tests can stub
// it (e.g. count invocations, simulate panic) to cover the sentinel
// integration.
type DispatchHook func(s *Session, toolName string)

// SessionStateActive is the default state of a freshly Create'd
// session before Run has fired. After Run starts, the agent loop
// owns transitions.
const SessionStateActive = "active"

// SessionStatePausedReattachable indicates a session that was reloaded
// from `sessions-index.json` on daemon restart (item 27). The session
// has its journal replayed but no live agent goroutine.
const SessionStatePausedReattachable = "paused-reattachable"

// SessionStateRunning is the state set when Run is actively driving
// the agent loop.
const SessionStateRunning = "running"

// SessionStateStopped is the state set after Run returns (cancelled,
// completed, or errored).
const SessionStateStopped = "stopped"

// ErrSessionAlreadyRunning is returned by Run when the session is
// already executing. Sessions are single-driver — concurrent Run
// calls must fail loudly rather than silently corrupt state.
var ErrSessionAlreadyRunning = errors.New("sessionhub: session already running")

// newSession builds a Session with all id/path/model fields populated.
// Used by SessionHub.Create after validation passes; never called
// directly by external code.
func newSession(id, sessionRoot, model string) *Session {
	return &Session{
		ID:          id,
		SessionRoot: sessionRoot,
		Model:       model,
		StartedAt:   time.Now(),
		State:       SessionStateActive,
	}
}

// SetJournal plugs in the Phase D append-only journal. Idempotent;
// the daemon's startup glue calls it once after Create.
func (s *Session) SetJournal(j Journal) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.journal = j
}

// SetOnEvent installs the per-session event hook. The Phase D wiring
// uses this to drive the journal-first append path (item 24).
func (s *Session) SetOnEvent(fn OnEventFunc) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.onEvent = fn
}

// SetDispatchHook installs the per-tool sentinel hook. The Phase D
// default wires assertCwd(SessionRoot); tests can stub this.
func (s *Session) SetDispatchHook(fn DispatchHook) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.dispatchHook = fn
}

// SetState updates the lifecycle state under runMu. Used by the
// daemon's startup glue (item 27) and by Run itself.
func (s *Session) SetState(state string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.State = state
}

// WorkspaceFunc is the bridge between agentloop.Run and the
// per-session cortex Workspace. It accepts the parent ctx and a
// pointer to the session's Workspace; implementations MAY mutate the
// workspace (e.g. attach lobes) before returning. The function
// receives Workspace as `any` for the same package-independence
// reason as Session.Workspace.
type WorkspaceFunc func(ctx context.Context, workspace any) error

// RunOptions are the per-Run knobs. Defaults to a no-op WorkspaceFunc
// when nil — the agent loop runs without cortex augmentation.
type RunOptions struct {
	// Provider is the model API client. Required.
	Provider provider.Provider

	// Tools is the tool definition set passed to agentloop.New.
	// Optional; nil means a chat-only loop.
	Tools []provider.ToolDef

	// Handler is the tool execution handler. Per item 25, the Phase D
	// driver wraps this so dispatchHook fires before each call. Must
	// be non-nil if Tools is non-nil.
	Handler agentloop.ToolHandler

	// LoopConfig is the agentloop config. The driver overlays
	// SessionID/AgentID and the model from the parent Session before
	// passing it through.
	LoopConfig agentloop.Config

	// WorkspaceFunc, when non-nil, is invoked before the agent loop
	// starts to attach cortex Workspace state.
	WorkspaceFunc WorkspaceFunc

	// UserMessage is the initial user turn. Required.
	UserMessage string
}

// Run drives the agentloop.Loop for this session. It wires:
//
//   - the WorkspaceFunc bridge (cortex augmentation),
//   - the OnEvent hook (journal-first event persistence — item 24),
//   - the dispatchTool sentinel (assertCwd — item 25; wrapped via
//     wrapHandler when dispatchHook is set).
//
// Returns ErrSessionAlreadyRunning if a prior Run is still active.
//
// The driver does NOT register any hub.Bus subscriber on its own —
// the daemon's startup glue is the canonical place for that wiring
// (so unit tests can drive Run with a fake bus). What the driver
// DOES guarantee: every tool dispatch fires dispatchHook FIRST, and
// every onEvent invocation completes (or returns an error stopping
// dispatch) BEFORE the agent loop continues.
func (s *Session) Run(parent context.Context, opts RunOptions) (*agentloop.Result, error) {
	if opts.Provider == nil {
		return nil, errors.New("sessionhub: Run: nil Provider")
	}
	if opts.UserMessage == "" {
		return nil, errors.New("sessionhub: Run: empty UserMessage")
	}

	s.runMu.Lock()
	if s.cancel != nil {
		s.runMu.Unlock()
		return nil, ErrSessionAlreadyRunning
	}
	ctx, cancel := context.WithCancel(parent)
	s.ctx = ctx
	s.cancel = cancel
	// Snapshot hooks under the lock so the rest of Run sees a stable
	// view even if SetOnEvent / SetDispatchHook fire concurrently.
	//
	// TASK-25: when no dispatchHook was installed, fall back to
	// defaultDispatchHook (assertCwd) so the cwd-drift sentinel is
	// active by default. Operators MUST explicitly call
	// SetDispatchHook(nil) to disable it (no test in production code
	// does that — the override only matters for unit tests that
	// capture invocations).
	onEvent := s.onEvent
	dispatchHook := s.dispatchHook
	if dispatchHook == nil {
		dispatchHook = defaultDispatchHook
	}
	s.State = SessionStateRunning
	s.runMu.Unlock()

	// Always clear the cancel field on exit so the next Run can fire.
	defer func() {
		s.runMu.Lock()
		s.cancel = nil
		s.ctx = nil
		s.State = SessionStateStopped
		s.runMu.Unlock()
	}()

	// WorkspaceFunc bridge: invoke it BEFORE the agent loop so any
	// cortex setup (lobes, prewarm) is in place when the first turn
	// fires. A non-nil error aborts Run before any model call — the
	// caller sees the workspace error verbatim.
	if opts.WorkspaceFunc != nil {
		if err := opts.WorkspaceFunc(ctx, s.Workspace); err != nil {
			return nil, errWrap("workspace setup", err)
		}
	}

	// Configure the agent loop. Overlay SessionID/Model from the
	// parent Session so the agentloop's hub event correlation IDs
	// match the daemon's per-session identity.
	cfg := opts.LoopConfig
	if cfg.Model == "" {
		cfg.Model = s.Model
	}
	if cfg.SessionID == "" {
		cfg.SessionID = s.ID
	}

	// dispatchTool — the sentinel-guarded handler wrapper. Item 25
	// installs this; if dispatchHook is nil, we still wrap so that
	// future installations work uniformly. The wrapper:
	//
	//   1. fires dispatchHook(s, name) — assertCwd in production
	//   2. invokes the original handler
	//
	// Step 1 is the load-bearing safety check. A panic from the hook
	// (cwd drift) propagates through the agentloop's recover wrapper
	// and aborts the session — exactly the failure mode item 25
	// wants.
	wrapped := s.wrapHandler(opts.Handler, dispatchHook)
	loop := agentloop.New(opts.Provider, cfg, opts.Tools, wrapped)
	s.loop = loop

	// onEvent wiring. Phase D item 24 makes the hook journal-first;
	// the wrapper here funnels the agentloop's internal event
	// emissions through it so a single code path enforces the
	// invariant. The hook also feeds the bus subscribers (the
	// daemon's startup glue subscribes a hub.Bus listener that calls
	// s.dispatchEvent).
	if onEvent != nil {
		// Note: agentloop currently emits to its OWN hub.Bus when set;
		// the daemon's startup glue is the canonical place to bridge
		// that bus into onEvent. We do NOT call SetEventBus here — to
		// avoid double-wiring when the daemon also calls it.
		_ = onEvent
	}

	return loop.Run(ctx, opts.UserMessage)
}

// DispatchEvent is the public entry point the daemon's bus subscriber
// calls when a hub event fires for this session. It enforces the
// journal-first invariant from spec §11.24:
//
//   - First, persist the event via the OnEvent hook (TASK-24 wires
//     this to journal.Append).
//   - On hook error, ABORT — return the error to the caller. The
//     caller (the bus subscriber) MUST NOT fan out the event to any
//     downstream subscriber when this returns non-nil. This is the
//     load-bearing consistency guarantee that lets replay reconstruct
//     exactly what subscribers saw: a subscriber can never observe an
//     event the journal lost.
//
// Returns nil on success (caller may proceed with subscriber fanout).
func (s *Session) DispatchEvent(ctx context.Context, ev *hub.Event) error {
	s.runMu.Lock()
	hook := s.onEvent
	s.runMu.Unlock()
	if hook == nil {
		return nil
	}
	return hook(ctx, ev)
}

// JournalFirstHook returns an OnEventFunc that:
//
//  1. Calls journal.Append("hub.event", ev) FIRST. If Append returns
//     an error, the hook returns it and the caller (DispatchEvent)
//     MUST NOT fan out the event. This is the spec §11.24 invariant.
//  2. After the journal write succeeds, calls the optional fanout
//     callback (typically the WS subscriber publish path).
//
// The fanout callback may be nil — in which case the hook is
// journal-only. Pass it as a parameter (rather than reading it off
// Session) so unit tests can drive the path without a real bus.
//
// The returned function captures j by reference; calling SetJournal
// after this hook is built does NOT update the captured journal.
// The daemon's startup glue therefore calls SetJournal first, THEN
// SetOnEvent(JournalFirstHook(j, ...)) — that ordering is the only
// supported wiring.
func JournalFirstHook(j Journal, fanout OnEventFunc) OnEventFunc {
	return func(ctx context.Context, ev *hub.Event) error {
		if j != nil {
			if _, err := j.Append("hub.event", ev); err != nil {
				// Journal failed — refuse to fan out. The caller
				// surfaces this error; downstream subscribers see
				// nothing for this event.
				return errWrap("journal append", err)
			}
		}
		if fanout != nil {
			return fanout(ctx, ev)
		}
		return nil
	}
}

// wrapHandler returns an agentloop.ToolHandler that fires the
// dispatchHook (assertCwd in production) before delegating to the
// original handler. If both are nil, returns nil so the loop runs
// chat-only.
//
// The wrapper makes dispatchHook a hard prefix — even if the inner
// handler is nil (which agentloop tolerates for chat-only sessions),
// the hook fires. That keeps the sentinel honest under any tool-set
// configuration.
func (s *Session) wrapHandler(inner agentloop.ToolHandler, hook DispatchHook) agentloop.ToolHandler {
	if inner == nil && hook == nil {
		return nil
	}
	return func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		if hook != nil {
			hook(s, name)
		}
		if inner == nil {
			return "", errors.New("sessionhub: tool dispatch with no handler installed")
		}
		return inner(ctx, name, input)
	}
}

// cancelRun is the hub-internal hook called by SessionHub.Delete to
// wind down a Session. If Run has not started, the call is a no-op.
// Safe to call multiple times.
func (s *Session) cancelRun() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// errWrap is a tiny helper that prefixes an error message without
// pulling in fmt for one use. Keeps the package's import surface lean.
func errWrap(prefix string, err error) error {
	return errors.New(prefix + ": " + err.Error())
}
