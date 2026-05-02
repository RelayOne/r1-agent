// Package cortex implements a Global-Workspace-Theory (GWT) inspired
// cognitive architecture for the agent. Inspired by mammalian cortex
// dynamics, it coordinates a set of parallel Lobes -- cognitive
// specialists that each receive the full conversation context and
// reason concurrently -- around a shared Workspace. Execution proceeds
// in discrete superstep Rounds: every Lobe runs in parallel against a
// snapshot of the Workspace, a barrier collects their proposals, a
// Spotlight selector elevates the most salient contribution into the
// next-round Workspace, and a Router lets the agent decide how each
// proposal merges back (broadcast, addressed, or dropped). This avoids
// the term used by internal/concern (which handles per-stance context
// projection) and instead uses Lobe/Workspace/Spotlight/Router as the
// load-bearing vocabulary. See specs/research/synthesized/cortex.md for
// the GWT background and specs/cortex-core.md for the build plan.
package cortex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// cortexStopTimeout is the upper bound for the cumulative shutdown wait
// across every LobeRunner.Stop call. Cortex.Stop derives a single
// context.WithTimeout from this constant and hands the resulting
// deadline to each runner so the total stop budget is bounded even
// when several runners are wedged simultaneously.
const cortexStopTimeout = 10 * time.Second

// Config carries cortex construction parameters. Field names mirror
// specs/cortex-core.md §"Cortex — the bundle" exactly. EventBus and
// Provider are required; everything else has a sensible default.
//
//   - EventBus    -- the in-process typed hub used for live UI/log
//     updates; required.
//   - Durable     -- the WAL-backed durable bus used for crash recovery
//     and post-mortem replay; nil selects in-memory mode.
//   - Provider    -- direct AI model client used by the Router and the
//     pre-warm pump; required.
//   - Lobes       -- registered at New(); cannot mutate after Start
//     (TASK-13). May be empty for tests that do not exercise Round.
//   - MaxLLMLobes -- LLM-concurrency cap fed to LobeSemaphore;
//     default 5; hard cap 8 (>8 panics per spec item 12).
//   - PreWarmModel    -- model id used by the cache-prewarm pump;
//     default "claude-haiku-4-5".
//   - PreWarmInterval -- spacing between cache-prewarm fires;
//     default 4*time.Minute.
//   - RoundDeadline   -- soft barrier deadline used by Round.Wait per
//     round; default 2*time.Second.
//   - RouterCfg       -- forwarded to NewRouter; Provider/Model/
//     SystemPrompt may be left zero to inherit Cortex-level defaults.
//   - SessionID       -- optional, surfaced via Cortex.SessionID for
//     telemetry/audit correlation; never validated.
type Config struct {
	SessionID       string
	EventBus        *hub.Bus
	Durable         *bus.Bus
	Provider        provider.Provider
	Lobes           []Lobe
	MaxLLMLobes     int
	PreWarmModel    string
	PreWarmInterval time.Duration
	RoundDeadline   time.Duration
	RouterCfg       RouterConfig
}

// Cortex bundles the parallel-cognition substrate: Workspace + Round +
// Router + LobeSemaphore + BudgetTracker + LobeRunners. One Cortex per
// agentloop.Loop. Lifecycle is tied to the Loop's parent ctx via Start
// (TASK-13) and Stop (TASK-13); New only constructs.
type Cortex struct {
	cfg       Config
	workspace *Workspace
	round     *Round
	router    *Router
	sem       *LobeSemaphore
	tracker   *BudgetTracker
	runners   []*LobeRunner

	// Lifecycle state owned by Start/Stop (TASK-13).
	//
	// ctx/cancel are captured at Start: every spawned goroutine (the
	// pre-warm pump and each LobeRunner) derives from this ctx so a
	// single cancel() winds the world down. Stop is the only writer;
	// Start sets these once under the started CAS.
	//
	// started gates Start with atomic.CompareAndSwap so a concurrent
	// double-Start collapses into a single launch sequence; subsequent
	// Start calls become silent no-ops.
	//
	// stopOnce gates Stop so concurrent shutdown paths converge on a
	// single cancel() + wait sequence; subsequent Stop calls become
	// silent no-ops without re-cancelling an already-cancelled ctx or
	// re-waiting on already-stopped runners.
	ctx      context.Context
	cancel   context.CancelFunc
	started  atomic.Bool
	stopOnce sync.Once
}

// New constructs a Cortex from cfg, validates required fields, applies
// defaults, and builds every sub-system. It does NOT start any
// goroutines — Start (TASK-13) owns the launch sequence.
//
// Validation/defaults (per spec item 12):
//
//   - EventBus and Provider must be non-nil; otherwise a wrapped error
//     is returned.
//   - MaxLLMLobes < 0 is rejected with an error; 0 defaults to 5; >8
//     panics. The hard cap matches LobeSemaphore (which itself panics
//     on capacity > 8) — surfacing the panic here keeps the error
//     message attributable to the cortex layer.
//   - RoundDeadline=0 → 2*time.Second.
//   - PreWarmInterval=0 → 4*time.Minute.
//   - PreWarmModel="" → "claude-haiku-4-5".
//   - RouterCfg.Provider, when blank, inherits cfg.Provider so callers
//     do not have to pass the same provider twice.
//
// On a non-nil Durable bus, Workspace.Replay is invoked before the
// Lobe runners are constructed so any pre-existing Notes are visible
// to the spotlight at construction time. Replay errors are logged at
// slog.Warn but do not fail New — the cortex must remain bootable
// when the WAL is corrupt or partially truncated.
func New(cfg Config) (*Cortex, error) {
	if cfg.EventBus == nil {
		return nil, errors.New("cortex/New: EventBus required")
	}
	if cfg.Provider == nil {
		return nil, errors.New("cortex/New: Provider required")
	}
	if cfg.MaxLLMLobes < 0 {
		return nil, fmt.Errorf("cortex/New: MaxLLMLobes must be >= 0, got %d", cfg.MaxLLMLobes)
	}
	if cfg.MaxLLMLobes == 0 {
		cfg.MaxLLMLobes = 5
	}
	if cfg.MaxLLMLobes > 8 {
		panic(fmt.Sprintf("cortex/New: MaxLLMLobes must be <= 8, got %d", cfg.MaxLLMLobes))
	}
	if cfg.RoundDeadline == 0 {
		cfg.RoundDeadline = 2 * time.Second
	}
	if cfg.PreWarmInterval == 0 {
		cfg.PreWarmInterval = 4 * time.Minute
	}
	if cfg.PreWarmModel == "" {
		cfg.PreWarmModel = "claude-haiku-4-5"
	}

	// Workspace + optional WAL replay. NewWorkspace tolerates a nil
	// durable bus (in-memory mode); Replay short-circuits to nil in
	// that mode, so the call is unconditional.
	ws := NewWorkspace(cfg.EventBus, cfg.Durable)
	if err := ws.Replay(); err != nil {
		slog.Warn("cortex/New: workspace replay failed", "err", err)
	}

	// Router: inherit Provider/Bus from Cortex when the caller left
	// them blank in RouterCfg. NewRouter applies its own model and
	// system-prompt defaults (claude-haiku-4-5, DefaultRouterSystemPrompt).
	rcfg := cfg.RouterCfg
	if rcfg.Provider == nil {
		rcfg.Provider = cfg.Provider
	}
	if rcfg.Bus == nil {
		rcfg.Bus = cfg.EventBus
	}
	router, err := NewRouter(rcfg)
	if err != nil {
		return nil, fmt.Errorf("cortex/New: router: %w", err)
	}

	sem := NewLobeSemaphore(cfg.MaxLLMLobes)
	tracker := NewBudgetTracker()
	r := NewRound()

	runners := make([]*LobeRunner, 0, len(cfg.Lobes))
	for _, l := range cfg.Lobes {
		runners = append(runners, NewLobeRunner(l, ws, sem, cfg.EventBus))
	}

	return &Cortex{
		cfg:       cfg,
		workspace: ws,
		round:     r,
		router:    router,
		sem:       sem,
		tracker:   tracker,
		runners:   runners,
	}, nil
}

// Workspace returns the underlying Workspace for direct read access
// (TUI lanes, web UI, tests). The pointer is stable for the lifetime of
// the Cortex.
func (c *Cortex) Workspace() *Workspace { return c.workspace }

// Spotlight returns the Workspace's Spotlight tracker. Equivalent to
// c.Workspace().Spotlight() but kept as a method for parity with the
// spec interface in §"Cortex — the bundle".
func (c *Cortex) Spotlight() *Spotlight { return c.workspace.Spotlight() }

// Router returns the mid-turn input Router. Exposed so the chat REPL
// (cmd/r1) can call Route directly without reaching through Config.
func (c *Cortex) Router() *Router { return c.router }

// Tracker returns the BudgetTracker. Exposed so the hub.Bus subscriber
// installed by Start (TASK-13) can route EventModelPostCall payloads
// to RecordMainTurn.
func (c *Cortex) Tracker() *BudgetTracker { return c.tracker }

// Round returns the superstep barrier. Exposed so MidturnNote (TASK-14)
// can Open/Wait/Close the per-turn round.
func (c *Cortex) Round() *Round { return c.round }

// SessionID returns the configured session id (zero-value if unset).
// Surfaces in telemetry and audit events emitted by the cortex.
func (c *Cortex) SessionID() string { return c.cfg.SessionID }

// Start launches the cortex lifecycle: it captures a cancellable
// child of parentCtx, performs a single synchronous initial pre-warm
// (best-effort — failures are logged but never abort Start), launches
// the periodic pre-warm pump goroutine, and starts every registered
// LobeRunner against the same context.
//
// Start is idempotent. The first call after construction wins the
// atomic.CompareAndSwap on c.started and runs the launch sequence;
// subsequent calls observe the flag already set and return nil
// without touching ctx, runners, or the pump. This matches the spec
// contract: "Cortex.Start may run after a daemon resume" — re-entry
// must be a no-op, not a panic.
//
// Pre-warm is best-effort by design (spec gotcha #8). The synchronous
// initial fire serves only to seed the cache as early as possible; on
// failure runPreWarmOnce already emits EventCortexPreWarmFailed via
// the bus, and Cortex.Start logs at WARN so operators see the cause
// without bringing down the loop.
//
// The supplied parentCtx becomes the lifetime context for every
// goroutine the cortex spawns; cancelling it (or calling Stop) winds
// the pump and every LobeRunner down. Start does NOT block on
// completion — runners drive their own goroutines and the pre-warm
// pump runs until its ctx is cancelled.
func (c *Cortex) Start(parentCtx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return nil
	}

	c.ctx, c.cancel = context.WithCancel(parentCtx)

	// Synchronous initial pre-warm: best-effort. runPreWarmOnce already
	// emits EventCortexPreWarmFailed on the bus; we log at WARN so the
	// failure surfaces in operator logs without aborting Start.
	if err := runPreWarmOnce(
		c.ctx,
		c.cfg.Provider,
		c.cfg.PreWarmModel,
		"",  // SystemPrompt: not on Config; cache parity handled by callers wiring through agentloop.
		nil, // Tools: not on Config; same parity contract.
		c.cfg.EventBus,
	); err != nil {
		slog.Warn("cortex/Start: initial pre-warm failed",
			"component", "cortex",
			"err", err,
		)
	}

	// Pre-warm pump: runs until c.ctx is cancelled. The pump never
	// terminates on a fire error, only on ctx cancellation, matching
	// the runPreWarmPump contract.
	go runPreWarmPump(c.ctx, c.cfg.PreWarmInterval, func(ctx context.Context) error {
		return runPreWarmOnce(
			ctx,
			c.cfg.Provider,
			c.cfg.PreWarmModel,
			"",
			nil,
			c.cfg.EventBus,
		)
	})

	// Launch every runner against the shared lifetime ctx.
	for _, r := range c.runners {
		r.Start(c.ctx)
	}

	return nil
}

// Stop cancels the cortex's internal context (signalling every
// goroutine spawned by Start to wind down) and then blocks while
// each LobeRunner exits, bounded by a single cumulative
// cortexStopTimeout (10s).
//
// Stop is idempotent via sync.Once: only the first invocation runs
// the cancel + wait sequence. Subsequent calls return nil
// immediately. If Stop is called before Start, the sync.Once still
// fires, but cancel is nil and there are no runners to wait on, so
// the call is a safe no-op.
//
// stopCtx is the caller-supplied shutdown ctx; the 10s deadline is
// derived from it via context.WithTimeout. Cancelling stopCtx aborts
// the wait early; runners that have not yet exited will be observed
// as wedged via slog.Warn inside LobeRunner.Stop.
//
// Individual runner errors are not returned: each LobeRunner.Stop
// already logs its own timeout warning, and the cortex contract is
// "best-effort, bounded shutdown". Callers that need finer-grained
// error reporting should subscribe to the hub event stream.
func (c *Cortex) Stop(stopCtx context.Context) error {
	c.stopOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}

		// Single cumulative deadline shared across all runners. We
		// pass the same derived context into every Stop call so the
		// total stop budget is bounded even when several runners are
		// wedged simultaneously (LobeRunner.Stop has its own per-call
		// lobeStopTimeout, but the cortex-level deadline takes
		// precedence via ctx.Done()).
		deadline, deadlineCancel := context.WithTimeout(stopCtx, cortexStopTimeout)
		defer deadlineCancel()

		for _, r := range c.runners {
			r.Stop(deadline)
		}
	})
	return nil
}
