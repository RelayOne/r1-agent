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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
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

	// roundCounter is the monotonic source of round ids handed to
	// Round.Open by MidturnNote (TASK-14). It starts at zero so the
	// first AddUint64 returns 1 — Round.Open requires roundID > 0
	// because NewRound's "current" zero-value would otherwise reject
	// it (Open panics on roundID <= current).
	roundCounter atomic.Uint64

	// budgetSubID is the hub.Bus subscriber ID registered by Start
	// (TASK-24) so the BudgetTracker observes main-turn output tokens
	// from EventModelPostCall events. Stop unregisters it via
	// Bus.Unregister(budgetSubID). The empty string means no
	// subscriber is currently registered (Start was never called, or
	// the registration failed silently — neither path ever happens
	// today, but the empty-string guard lets Stop be a safe no-op).
	budgetSubID string
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
		runner := NewLobeRunner(l, ws, sem, cfg.EventBus)
		// Wire the Round barrier so each runner reports completion
		// via Round.Done(roundID, lobeID) from runOnce, letting
		// Cortex.MidturnNote (TASK-14) wait on the per-round barrier.
		runner.SetRound(r)
		runners = append(runners, runner)
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

	// TASK-24: subscribe BudgetTracker to main-turn token usage. The
	// agentloop emits EventModelPostCall after each successful API
	// response with Model.Role="main"; we filter to events that
	// match this Cortex's SessionID (when one is configured) and
	// feed Model.OutputTokens into RecordMainTurn so subsequent
	// RoundOutputBudget calls can derive the 30% cap.
	//
	// SessionID gating semantics: when c.cfg.SessionID is empty
	// (typical for tests and standalone runs that do not propagate a
	// session id), every main-role event is consumed; this matches
	// the spec's "best-effort cross-correlation" stance and keeps
	// tests free of mandatory session-id boilerplate. When SessionID
	// IS set, only events with the same MissionID land — events from
	// other sessions sharing the bus are silently ignored.
	c.budgetSubID = fmt.Sprintf("cortex.budget.%s.%d", c.cfg.SessionID, time.Now().UnixNano())
	c.cfg.EventBus.Register(hub.Subscriber{
		ID:       c.budgetSubID,
		Events:   []hub.EventType{hub.EventModelPostCall},
		Mode:     hub.ModeObserve,
		Priority: 9000,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			if ev == nil || ev.Model == nil {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if ev.Model.Role != "main" {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if c.cfg.SessionID != "" && ev.MissionID != c.cfg.SessionID {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			c.tracker.RecordMainTurn(ev.Model.OutputTokens)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

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

		// TASK-24: unregister the budget subscriber so a Cortex that has
		// been Stopped no longer pulls main-turn events off a bus that
		// outlives it. Empty subID means Start never registered (e.g.
		// Stop-before-Start), so the call is a safe no-op via the empty
		// guard inside Bus.Unregister.
		if c.budgetSubID != "" && c.cfg.EventBus != nil {
			c.cfg.EventBus.Unregister(c.budgetSubID)
			c.budgetSubID = ""
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

// Static assertion: *Cortex satisfies agentloop.CortexHook so callers
// can assign a *Cortex to agentloop.Config.Cortex without an explicit
// adapter. The interface lives in agentloop/ to break the import cycle
// (cortex imports agentloop for Message); see agentloop/loop.go.
var _ agentloop.CortexHook = (*Cortex)(nil)

// MidturnNote runs one cortex superstep and returns a formatted block
// of Notes for injection into the agent's mid-turn supervisor channel.
//
// The dance, per spec item 14:
//
//  1. Allocate a fresh round id from the monotonic counter.
//  2. Open the Round with len(c.runners) expected participants. With
//     zero runners we skip the dance entirely and return "" — Round.Open
//     would still close the done channel immediately, but tests and
//     callers expect a fast no-op when no Lobes are registered.
//  3. Stamp Workspace.SetRound so every Note Published during this
//     round carries roundID. (Note.Round drives Drain filtering.)
//  4. Tick every LobeRunner with TickRound(roundID). The runner stamps
//     currentRoundID and signals its tick channel. After each lobe.Run
//     returns, the runner calls Round.Done(roundID, lobeID).
//  5. Wait on the Round barrier with cfg.RoundDeadline. Per spec
//     gotcha #6, slow Lobes are NOT cancelled — their Notes simply land
//     on a future round. ErrRoundDeadlineExceeded therefore proceeds to
//     drain whatever has arrived; ctx errors do the same so a cancelled
//     parent does not leave the supervisor with a stale block.
//  6. Drain Notes for this round from the Workspace. Drain returns
//     everything with Round >= roundID; we filter to exactly roundID
//     defensively in case a future round has already produced Notes
//     (shouldn't happen given the synchronous shape, but cheap guard).
//  7. Sort by Severity desc (Critical > Warning > Advice > Info), with
//     EmittedAt asc as the tiebreaker so within a severity bucket the
//     reader sees Notes in the order they fired.
//  8. Close the Round so subsequent ids strictly advance.
//  9. Format. Empty result returns "" so the caller's hook composer
//     does not inject a leading "[CORTEX NOTES — round N]\n" with no
//     bullets underneath.
//
// The returned string format:
//
//	[CORTEX NOTES — round %d]
//	- [%s] %s: %s
//	- ...
//
// where the per-line fields are Severity, LobeID, Title.
func (c *Cortex) MidturnNote(messages []agentloop.Message, turn int) string {
	_ = messages // history payload not consumed at this layer; reserved for future per-round LobeInput wiring.
	_ = turn     // turn number not used yet; surfaces in the agentloop.MidturnCheckFn signature for parity.

	// No runners → no round to drive. Skipping the Open call avoids a
	// trivial (immediately-closed) Round entry in the bookkeeping maps
	// and matches the spec contract "" on empty.
	if len(c.runners) == 0 {
		return ""
	}

	roundID := c.roundCounter.Add(1)

	c.round.Open(roundID, len(c.runners))
	c.workspace.SetRound(roundID)

	for _, r := range c.runners {
		r.TickRound(roundID)
	}

	waitCtx := c.ctx
	if waitCtx == nil {
		// Cortex was constructed but never Started. Fall back to a
		// background context so Round.Wait still respects the
		// deadline; tests that exercise MidturnNote without Start
		// rely on this path.
		waitCtx = context.Background()
	}
	if err := c.round.Wait(waitCtx, roundID, c.cfg.RoundDeadline); err != nil {
		// Slow Lobes are NOT cancelled per spec gotcha #6. We log the
		// timeout/cancel cause and proceed to drain whatever Notes did
		// land; late Notes will surface on a future round's Drain.
		slog.Warn("cortex/MidturnNote: round wait did not complete cleanly",
			"component", "cortex",
			"round", roundID,
			"err", err,
		)
	}

	notes, _ := c.workspace.Drain(roundID)

	// Defensive filter: Drain returns Round >= roundID; restrict to
	// exactly roundID so a future round's Notes (shouldn't exist yet,
	// but cheap guard) never pollute this round's block.
	rn := make([]Note, 0, len(notes))
	for _, n := range notes {
		if n.Round == roundID {
			rn = append(rn, n)
		}
	}

	sort.SliceStable(rn, func(i, j int) bool {
		ri, rj := severityRank(rn[i].Severity), severityRank(rn[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return rn[i].EmittedAt.Before(rn[j].EmittedAt)
	})

	c.round.Close(roundID)

	if len(rn) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[CORTEX NOTES — round %d]\n", roundID)
	for _, n := range rn {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", n.Severity, n.LobeID, n.Title)
	}
	return b.String()
}

// PreEndTurnGate returns a non-empty string when there are unresolved
// SevCritical Notes in the Workspace; the agentloop should refuse end_turn
// while the gate is non-empty (TASK-16 wires this into PreEndTurnCheckFn
// composition).
//
// Empty result is the green-light signal: the loop is free to honour the
// model's end_turn. A non-empty block is intentionally formatted so the
// caller can splice it directly into a follow-up user message:
//
//	[CRITICAL CORTEX NOTES — resolve before ending turn]
//	- LobeID: Title
//	- ...
//
// The messages argument mirrors agentloop.PreEndTurnCheckFn for parity;
// this implementation does not consume it (the gate is purely Workspace-
// driven), but the parameter is preserved so future revisions can correlate
// Notes with the message history without breaking the public signature.
func (c *Cortex) PreEndTurnGate(messages []agentloop.Message) string {
	_ = messages // history payload not consumed at this layer; parity with agentloop.PreEndTurnCheckFn signature.

	notes := c.workspace.UnresolvedCritical()
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[CRITICAL CORTEX NOTES — resolve before ending turn]\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "- %s: %s\n", n.LobeID, n.Title)
	}
	return b.String()
}
