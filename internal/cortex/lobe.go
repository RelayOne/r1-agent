package cortex

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// Lobe is the parallel-cognition specialist. Implementations run in a
// dedicated goroutine; they read message history (read-only) and write
// Notes via Workspace.Publish.
//
// Lobe contract:
//   - Run MUST observe ctx.Done(); return nil on graceful shutdown.
//   - Run MAY be called multiple times across daemon restarts; state is
//     externalized to Workspace + bus.WAL.
//   - Run MUST be panic-safe; a Lobe panic is logged + recovered + emits
//     hub.Event{Type:"cortex.lobe.panic"} but does NOT bring down the loop.
//
// Lobes do NOT implement persistence themselves — the runner handles it.
type Lobe interface {
	ID() string          // stable; used as LobeID on Notes
	Description() string // human-readable, for /status
	Kind() LobeKind      // Deterministic | LLM
	Run(ctx context.Context, in LobeInput) error
}

// LobeKind drives semaphore acquisition: LLM Lobes bind against
// LobeSemaphore; Deterministic Lobes run free.
type LobeKind int

const (
	KindDeterministic LobeKind = iota
	KindLLM
)

// LobeInput is the read-only context handed to each Lobe per Round.
type LobeInput struct {
	Round     uint64
	History   []agentloop.Message // current conversation; deep-copied
	Workspace WorkspaceReader     // read-only Workspace handle
	Provider  provider.Provider   // model client (Lobes use as needed)
	Bus       *hub.Bus            // for emitting status events
}

// WorkspaceReader is the read-only subset Lobes get. Forces the contract
// "Lobes WRITE only via Publish; everything else is read-only".
type WorkspaceReader interface {
	Snapshot() []Note
	UnresolvedCritical() []Note
}

// workspaceReader is the private adapter that wraps a *Workspace and
// exposes only the read-only subset declared by WorkspaceReader. Keeping
// this type unexported enforces the spec invariant that Lobes cannot
// reach Workspace.Publish through type assertions.
type workspaceReader struct {
	w *Workspace
}

// Snapshot delegates to (*Workspace).Snapshot.
func (r workspaceReader) Snapshot() []Note { return r.w.Snapshot() }

// UnresolvedCritical delegates to (*Workspace).UnresolvedCritical.
func (r workspaceReader) UnresolvedCritical() []Note { return r.w.UnresolvedCritical() }

// WorkspaceReaderFor wraps a *Workspace in the read-only adapter so
// callers (Cortex, LobeRunner) can hand a WorkspaceReader to Lobes
// without exposing Publish or any other write-side method.
func WorkspaceReaderFor(w *Workspace) WorkspaceReader {
	return workspaceReader{w: w}
}

// lobeStopTimeout is the upper bound LobeRunner.Stop will wait for the
// runner goroutine to exit after the owning context has been cancelled.
// Beyond this point Stop emits a slog.Warn so a wedged Lobe is visible
// in operator logs without bringing down Cortex.
const lobeStopTimeout = 5 * time.Second

// LobeRunner owns the goroutine that drives a single Lobe. The Cortex
// constructs one runner per active Lobe, holds the parent context, and
// signals "begin a new round" by sending on the runner's tick channel.
//
// Lifecycle:
//   - NewLobeRunner(...) builds a runner in the unstarted state.
//   - Start(ctx) launches the goroutine exactly once. It is idempotent;
//     subsequent calls are silent no-ops because Cortex.Start may run
//     after a daemon resume.
//   - The goroutine selects on <-ctx.Done() vs <-r.tick. On tick, it
//     acquires the LobeSemaphore IFF the Lobe is KindLLM, runs
//     lobe.Run(ctx, in) inside a defer-recover, and releases the slot.
//   - Stop(ctx) blocks until the goroutine exits or lobeStopTimeout
//     elapses. Cancellation is the caller's responsibility — Cortex
//     owns the parent context and cancels it before calling Stop.
//
// Concurrency: started uses atomic.CompareAndSwap so Start is racefree;
// stopOnce guards Stop so multiple shutdown paths converge on a single
// wait; stopped is closed exactly once by the goroutine on exit.
//
// The tick channel is buffered (cap 1) so Cortex can fire-and-forget
// without blocking when the Lobe is mid-Run; if a tick is already
// pending, additional ticks are coalesced (TASK-14 only requires
// "begin one round" semantics, not exactly-N delivery).
type LobeRunner struct {
	lobe Lobe
	ws   *Workspace
	sem  *LobeSemaphore
	bus  *hub.Bus

	// tick signals "Cortex has started a new round; please run once".
	// Producers (TASK-14 Cortex.scheduleRound) send non-blockingly; the
	// runner consumes one tick per round inside its main select loop.
	// Buffered with capacity 1 so a producer never blocks while the
	// runner is mid-Run: a second tick before consumption is coalesced.
	tick chan struct{}

	started  atomic.Bool
	stopOnce sync.Once
	stopped  chan struct{}
}

// NewLobeRunner constructs an unstarted LobeRunner bound to the given
// Lobe, writable Workspace, semaphore, and event bus. bus may be nil
// (events are silently dropped); ws may be nil only for tests that do
// not exercise Publish. The returned runner is ready for exactly one
// Start call.
func NewLobeRunner(lobe Lobe, ws *Workspace, sem *LobeSemaphore, bus *hub.Bus) *LobeRunner {
	return &LobeRunner{
		lobe:    lobe,
		ws:      ws,
		sem:     sem,
		bus:     bus,
		tick:    make(chan struct{}, 1),
		stopped: make(chan struct{}),
	}
}

// Tick returns the runner's tick channel. Cortex (TASK-14) calls this
// to signal "begin a new round" by performing a non-blocking send.
// Exposed as a method rather than an exported field so callers cannot
// close the channel — only the runner controls its lifecycle.
func (r *LobeRunner) Tick() chan<- struct{} { return r.tick }

// Start launches the runner goroutine. It is idempotent: only the first
// call after construction launches the goroutine; subsequent calls are
// no-ops. The supplied ctx becomes the lifetime context for every
// lobe.Run invocation; cancelling ctx triggers graceful shutdown.
//
// On entry the runner emits cortex.lobe.started so dashboards can
// confirm wiring without polling. On exit (ctx cancelled or panic) the
// goroutine closes r.stopped, unblocking Stop.
func (r *LobeRunner) Start(ctx context.Context) {
	if !r.started.CompareAndSwap(false, true) {
		return
	}

	r.emitStarted()

	go r.run(ctx)
}

// run is the goroutine body. Defer-close of stopped guarantees Stop
// always observes termination; defer-recover catches any panic from
// lobe.Run and emits cortex.lobe.panic so the orchestrator can decide
// whether to respawn (the Cortex contract: a panicking Lobe must NOT
// bring down the loop).
func (r *LobeRunner) run(ctx context.Context) {
	defer close(r.stopped)
	defer func() {
		if rec := recover(); rec != nil {
			r.emitPanic(rec)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.tick:
			r.runOnce(ctx)
		}
	}
}

// runOnce performs a single Lobe round: acquire (LLM only), build the
// LobeInput, invoke lobe.Run, and release the semaphore. Errors and
// panics from lobe.Run propagate up to the goroutine-level recover so
// callers see a single unified failure surface.
//
// Each runOnce is wrapped in its own defer-recover so a panicking Lobe
// only kills the current round, not the runner — the outer recover in
// run() is the secondary backstop for panics that escape this wrapper
// (e.g. panic during Acquire), which would otherwise terminate the
// goroutine with the user-supplied recovered value.
func (r *LobeRunner) runOnce(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			r.emitPanic(rec)
		}
	}()

	if r.lobe.Kind() == KindLLM && r.sem != nil {
		if err := r.sem.Acquire(ctx); err != nil {
			// Context cancelled during Acquire: drop the round
			// silently. The outer select will observe ctx.Done()
			// on the next iteration and exit.
			return
		}
		defer r.sem.Release()
	}

	in := r.buildInput(ctx)
	_ = r.lobe.Run(ctx, in)
}

// buildInput constructs the per-round LobeInput. The Workspace is
// wrapped in the read-only adapter so the Lobe cannot reach Publish
// through a type assertion; History and Provider are populated by
// TASK-14 wiring and are nil at this layer.
func (r *LobeRunner) buildInput(ctx context.Context) LobeInput {
	_ = ctx
	in := LobeInput{
		Bus: r.bus,
	}
	if r.ws != nil {
		in.Workspace = WorkspaceReaderFor(r.ws)
	}
	return in
}

// Stop blocks until the runner goroutine has exited or lobeStopTimeout
// elapses (whichever comes first), or until the supplied ctx is done.
// Cancellation of the runner is the caller's responsibility — Cortex
// owns the parent context passed to Start and must cancel it before
// calling Stop. Stop is safe to invoke before Start (it returns
// immediately because stopped is never closed and the timeout fires);
// callers should treat that as a programming error.
//
// Stop is idempotent: stopOnce wraps the wait so concurrent shutdown
// paths converge on a single observation.
func (r *LobeRunner) Stop(ctx context.Context) {
	r.stopOnce.Do(func() {
		// If Start was never called, stopped is open and nothing
		// will ever close it; fall through to the timeout branch
		// rather than blocking forever.
		if !r.started.Load() {
			return
		}

		select {
		case <-r.stopped:
			return
		case <-ctx.Done():
			return
		case <-time.After(lobeStopTimeout):
			slog.Warn("cortex: lobe runner stop timeout",
				"lobe_id", r.lobe.ID(),
				"timeout", lobeStopTimeout)
		}
	})
}

// Stopped exposes the runner's stopped channel for tests that need to
// assert clean exit. Production callers use Stop(ctx) instead.
func (r *LobeRunner) Stopped() <-chan struct{} { return r.stopped }

// emitStarted publishes a cortex.lobe.started event. Safe with a nil bus.
func (r *LobeRunner) emitStarted() {
	if r.bus == nil {
		return
	}
	r.bus.EmitAsync(&hub.Event{
		Type: hub.EventCortexLobeStarted,
		Custom: map[string]any{
			"lobe_id":   r.lobe.ID(),
			"lobe_kind": r.lobe.Kind(),
		},
	})
}

// emitPanic publishes a cortex.lobe.panic event with the recovered
// value. Safe with a nil bus. The recovered value is stored as-is in
// Custom["recovered"] so subscribers can format it however they need.
func (r *LobeRunner) emitPanic(rec any) {
	if r.bus == nil {
		return
	}
	r.bus.EmitAsync(&hub.Event{
		Type: hub.EventCortexLobePanic,
		Custom: map[string]any{
			"recovered": rec,
			"lobe_id":   r.lobe.ID(),
		},
	})
}

