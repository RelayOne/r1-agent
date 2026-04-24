// Package fanout is Stoke's generic parallel child-task primitive.
//
// Three upcoming executors (research fan-out in spec-4, delegation hiring in
// spec-5, and the existing session scheduler in internal/plan) need the same
// shape: spawn N children, track aggregate budget, propagate a trust ceiling,
// cancel cleanly on fail-fast or budget exhaustion. That shape lives here as
// a single reusable primitive so those executors can consume it instead of
// each hand-rolling WaitGroup + semaphore + error bookkeeping.
//
// This file implements the full surface described in
// specs/fanout-generalization.md (Task, Result, FanOutConfig, FanOut[T],
// budget tracker, context helpers, sentinel errors, event emission) in one
// place. Migrating session_scheduler_parallel.go onto it is a separate step
// called out in the spec's implementation checklist (items 8-10); this file
// only delivers the missing primitive.
package fanout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/eventlog"
)

// Task is the unit the fan-out dispatches concurrently. Callers implement
// this on their domain type (SubagentTask, HireTask, sessionTask, …).
type Task interface {
	// ID returns a stable identifier used in lifecycle events and hook
	// callbacks. Duplicates are allowed but discouraged.
	ID() string

	// Execute performs the child's work. The supplied context carries any
	// per-child timeout, the parent cancellation signal, the fan-out run
	// id, and the effective trust ceiling. Implementations should return
	// promptly once ctx.Err() becomes non-nil.
	Execute(ctx context.Context) (any, error)

	// EstimateCost returns the caller's upfront USD guess for this child.
	// Return 0 to skip pre-flight budgeting; the runtime charge path still
	// catches overages via FanOutConfig.BudgetLimit.
	EstimateCost() float64
}

// Result is a single child's terminal record. Returned in declaration order
// from FanOut; indices match the input tasks slice.
type Result struct {
	// Task references the original input task (unchanged).
	Task Task
	// Value is whatever Task.Execute returned on success; zero-value on error.
	Value any
	// Error is non-nil when the child errored, was cancelled, panicked, or
	// tripped the budget cap. Sentinel errors (ErrBudgetPreflight,
	// ErrBudgetExceeded, ErrTaskPanic) wrap so errors.Is works.
	Error error
	// CostUSD is the child's reported cost (0 when unreported).
	CostUSD float64
	// Duration is wall-clock time from goroutine entry to return.
	Duration time.Duration
	// Cancelled is true when the child did not complete its own work
	// because the parent cancelled (ctx / fail-fast / budget).
	Cancelled bool
}

// FanOutConfig tunes one invocation of FanOut. All fields are optional; the
// zero value runs tasks serially with no budget, no trust clamp, and no
// observability. Treat as value-copy — FanOut does not mutate it.
type FanOutConfig struct {
	// MaxParallel bounds concurrent children. 0 or 1 means serial. Negative
	// values are clamped to 1 (a warning is emitted when a bus is wired).
	MaxParallel int

	// FailFast cancels still-running siblings on the first child error.
	FailFast bool

	// BudgetLimit is an aggregate USD cap across all children. 0 disables.
	// Exceeding the cap (at pre-flight or at runtime) cancels siblings.
	BudgetLimit float64

	// TrustCeiling, when >= 0, clamps the child ctx trust to
	// min(parentTrust, TrustCeiling). -1 inherits whatever the parent ctx
	// already carries.
	TrustCeiling int

	// TimeoutPerChild, when > 0, wraps each child's ctx with WithTimeout.
	TimeoutPerChild time.Duration

	// OnChildStart fires synchronously at goroutine entry. Nil is fine.
	OnChildStart func(childID string)

	// OnChildComplete fires synchronously after Execute returns. Nil is fine.
	OnChildComplete func(childID string, result any, err error)

	// BusPublisher, when non-nil alongside EventLog, emits lifecycle events
	// via eventlog.EmitBus. When either is nil, emission is a no-op.
	BusPublisher *bus.Bus

	// EventLog is the durable side of the bus/event-log bridge.
	EventLog *eventlog.Log

	// RunID identifies this invocation in emitted events. When empty a
	// fresh ULID is generated.
	RunID string
}

// Sentinel errors. Wrap with %w so callers can errors.Is them.
var (
	// ErrBudgetPreflight fires when the summed EstimateCost() of all tasks
	// exceeds BudgetLimit. No children are started in that case.
	ErrBudgetPreflight = errors.New("fanout: budget preflight rejected")

	// ErrBudgetExceeded fires when the runtime aggregate cost exceeds
	// BudgetLimit. Emitted once; remaining siblings are cancelled.
	ErrBudgetExceeded = errors.New("fanout: budget exceeded at runtime")

	// ErrTaskPanic wraps a recovered panic value from Task.Execute.
	ErrTaskPanic = errors.New("fanout: task panicked")
)

// Event-type prefix for every event this package emits. Matches the spec-2
// streamjson convention (_stoke.dev/* namespace for Stoke-internal events).
const (
	evtStart           bus.EventType = "_stoke.dev/fanout.start"
	evtChildStart      bus.EventType = "_stoke.dev/fanout.child.start"
	evtChildComplete   bus.EventType = "_stoke.dev/fanout.child.complete"
	evtBudgetExceeded  bus.EventType = "_stoke.dev/fanout.budget_exceeded"
	evtBudgetPreflight bus.EventType = "_stoke.dev/fanout.budget_preflight"
	evtComplete        bus.EventType = "_stoke.dev/fanout.complete"
	evtWarn            bus.EventType = "_stoke.dev/fanout.warn"
)

// --- context helpers -------------------------------------------------------

type ctxKey int

const (
	ctxKeyTrust ctxKey = iota + 1
	ctxKeyRunID
)

// defaultTrustCeiling is the assumed parent trust when the ctx has none. 4
// matches CloudSwarm L4 (no delegation restrictions).
const defaultTrustCeiling = 4

// WithTrustCeiling attaches the effective trust ceiling to ctx. Intended for
// tests and for callers that want to seed a parent ctx before calling FanOut.
func WithTrustCeiling(ctx context.Context, ceiling int) context.Context {
	return context.WithValue(ctx, ctxKeyTrust, ceiling)
}

// TrustCeiling returns the ceiling stored on ctx or defaultTrustCeiling when
// none is present.
func TrustCeiling(ctx context.Context) int {
	if v, ok := ctx.Value(ctxKeyTrust).(int); ok {
		return v
	}
	return defaultTrustCeiling
}

// WithRunID attaches the fan-out run id to ctx.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, ctxKeyRunID, runID)
}

// RunID returns the run id on ctx or "" when none is present.
func RunID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRunID).(string); ok {
		return v
	}
	return ""
}

// --- budget tracker --------------------------------------------------------

// budgetTracker is a lock-free aggregate cost accumulator. Stored in integer
// cents (uint64) so sync/atomic.CompareAndSwap is enough; children report via
// Charge without blocking each other.
type budgetTracker struct {
	limitCents uint64 // 0 means unlimited
	spentCents atomic.Uint64
	onExceed   func() // typically the parent cancel func
	fired      atomic.Bool
}

func newBudgetTracker(limitUSD float64, onExceed func()) *budgetTracker {
	var limit uint64
	if limitUSD > 0 {
		// Round to the nearest cent. Costs below half a cent effectively
		// do not count; that is acceptable for LLM spend tracking.
		limit = uint64(limitUSD*100 + 0.5)
	}
	return &budgetTracker{limitCents: limit, onExceed: onExceed}
}

// Charge atomically adds usd to the aggregate. Returns false when the add
// would cross limitCents (and does not commit in that case). A false return
// fires onExceed at most once.
func (b *budgetTracker) Charge(usd float64) bool {
	if b.limitCents == 0 {
		// Unlimited budget: still track spent for Remaining()/telemetry.
		if usd > 0 {
			b.spentCents.Add(uint64(usd*100 + 0.5))
		}
		return true
	}
	if usd <= 0 {
		return true
	}
	delta := uint64(usd*100 + 0.5)
	for {
		cur := b.spentCents.Load()
		next := cur + delta
		if next > b.limitCents {
			if b.fired.CompareAndSwap(false, true) && b.onExceed != nil {
				b.onExceed()
			}
			return false
		}
		if b.spentCents.CompareAndSwap(cur, next) {
			return true
		}
	}
}

// Remaining reports the headroom in USD. Negative values are clamped to 0.
func (b *budgetTracker) Remaining() float64 {
	if b.limitCents == 0 {
		return 0
	}
	spent := b.spentCents.Load()
	if spent >= b.limitCents {
		return 0
	}
	return float64(b.limitCents-spent) / 100.0
}

// Spent reports the running total in USD.
func (b *budgetTracker) Spent() float64 {
	return float64(b.spentCents.Load()) / 100.0
}

// --- event emission --------------------------------------------------------

// emit publishes a single fanout event. No-op when either side of the bridge
// is nil. Errors are swallowed — observability MUST NOT gate correctness
// (spec Error Handling table).
func emit(cfg FanOutConfig, typ bus.EventType, payload map[string]any) {
	if cfg.BusPublisher == nil || cfg.EventLog == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if _, ok := payload["run_id"]; !ok && cfg.RunID != "" {
		payload["run_id"] = cfg.RunID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = eventlog.EmitBus(cfg.BusPublisher, cfg.EventLog, bus.Event{
		Type:    typ,
		Payload: raw,
	})
}

// --- FanOut ----------------------------------------------------------------

// FanOut runs tasks concurrently under cfg and returns one Result per input
// task, in declaration order. Semantics match specs/fanout-generalization.md
// §"Dispatch loop" and §"Failure modes".
//
//   - MaxParallel bounds concurrency via a buffered semaphore. 0/1 serialize.
//   - FailFast cancels still-running siblings on the first child error.
//   - BudgetLimit caps aggregate CostUSD; preflight uses EstimateCost sums.
//   - TrustCeiling is clamped against any ctx-stored ceiling and forwarded
//     into each child ctx.
//   - TimeoutPerChild wraps each child ctx with WithTimeout when > 0.
//   - Panics inside Task.Execute are recovered and surfaced as ErrTaskPanic
//     on that Result; siblings are unaffected unless FailFast is set.
//
// FanOut MUST NOT mutate cfg. It does not retry individual tasks — callers
// needing retry wrap their Execute body. Tasks returning nil errors with
// Cancelled=true are impossible: Cancelled is only set when the child's own
// ctx was cancelled and Execute returned that ctx's error.
func FanOut[T Task](ctx context.Context, tasks []T, cfg FanOutConfig) []Result {
	if len(tasks) == 0 {
		return nil
	}

	// Defaulting & input hygiene.
	if cfg.RunID == "" {
		cfg.RunID = ulid.Make().String()
	}
	parallel := cfg.MaxParallel
	if parallel <= 0 {
		parallel = 1
		if cfg.MaxParallel < 0 {
			emit(cfg, evtWarn, map[string]any{
				"reason": "max_parallel_negative_clamped_to_1",
				"raw":    cfg.MaxParallel,
			})
		}
	}

	// Resolve effective trust: min(parentTrust, cfg.TrustCeiling) when the
	// config opts in (>= 0); otherwise inherit parent unchanged.
	parentTrust := TrustCeiling(ctx)
	effectiveTrust := parentTrust
	if cfg.TrustCeiling >= 0 && cfg.TrustCeiling < parentTrust {
		effectiveTrust = cfg.TrustCeiling
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]Result, len(tasks))

	// --- Pre-flight budget check ------------------------------------------
	// Only runs when every task provides a non-zero estimate AND the sum
	// exceeds BudgetLimit. A single zero estimate falls back to runtime-only
	// enforcement (spec §"Budget aggregation algorithm").
	if cfg.BudgetLimit > 0 {
		var estTotal float64
		allNonZero := true
		for i := range tasks {
			est := tasks[i].EstimateCost()
			if est <= 0 {
				allNonZero = false
				break
			}
			estTotal += est
		}
		if allNonZero && estTotal > cfg.BudgetLimit {
			emit(cfg, evtBudgetPreflight, map[string]any{
				"estimate_usd": estTotal,
				"limit_usd":    cfg.BudgetLimit,
				"n":            len(tasks),
			})
			for i := range tasks {
				results[i] = Result{
					Task:  tasks[i],
					Error: fmt.Errorf("%w: estimate=%.4f limit=%.4f", ErrBudgetPreflight, estTotal, cfg.BudgetLimit),
				}
			}
			return results
		}
	}

	// Budget tracker cancels the derived ctx when the runtime total crosses
	// BudgetLimit. Siblings observe that cancellation via ctx.Done().
	bt := newBudgetTracker(cfg.BudgetLimit, func() {
		emit(cfg, evtBudgetExceeded, map[string]any{
			"spent_usd": float64(0), // backfilled by the charging child's own emit
			"limit_usd": cfg.BudgetLimit,
		})
		cancel()
	})

	emit(cfg, evtStart, map[string]any{
		"n":             len(tasks),
		"max_parallel":  parallel,
		"fail_fast":     cfg.FailFast,
		"budget_limit":  cfg.BudgetLimit,
		"trust_ceiling": effectiveTrust,
	})

	// --- Dispatch ----------------------------------------------------------
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var nSuccess, nError, nCancelled int64
	var aggregateCost uint64 // cents, atomic

	dispatchStart := time.Now()

dispatch:
	for i := range tasks {
		// Pre-dispatch ctx gate so we don't even queue sem slots after a
		// fail-fast or budget cancel. Not doing this can stall on sem send
		// when parallel < N and the ctx is already dead.
		if cancelCtx.Err() != nil {
			// Mark the un-dispatched remainder as cancelled upfront so the
			// returned slice is fully populated with meaningful records.
			for j := i; j < len(tasks); j++ {
				results[j] = Result{
					Task:      tasks[j],
					Error:     cancelCtx.Err(),
					Cancelled: true,
				}
				atomic.AddInt64(&nCancelled, 1)
			}
			break dispatch
		}

		// Acquire a sem slot OR observe cancellation — whichever fires first.
		select {
		case sem <- struct{}{}:
		case <-cancelCtx.Done():
			for j := i; j < len(tasks); j++ {
				results[j] = Result{
					Task:      tasks[j],
					Error:     cancelCtx.Err(),
					Cancelled: true,
				}
				atomic.AddInt64(&nCancelled, 1)
			}
			break dispatch
		}

		wg.Add(1)
		go func(idx int, task T) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()

			// Build the child ctx: cancellation chain + trust ceiling + run id
			// + optional per-child timeout. Cancellation of a per-child timeout
			// does not propagate to siblings because WithTimeout derives from
			// cancelCtx locally.
			childCtx := WithTrustCeiling(cancelCtx, effectiveTrust)
			childCtx = WithRunID(childCtx, cfg.RunID)
			var childCancel context.CancelFunc
			if cfg.TimeoutPerChild > 0 {
				childCtx, childCancel = context.WithTimeout(childCtx, cfg.TimeoutPerChild)
			} else {
				childCtx, childCancel = context.WithCancel(childCtx)
			}
			defer childCancel()

			if cfg.OnChildStart != nil {
				cfg.OnChildStart(task.ID())
			}
			emit(cfg, evtChildStart, map[string]any{
				"child_id": task.ID(),
				"index":    idx,
			})

			// Execute with panic recovery. A panic mapped to ErrTaskPanic
			// preserves the sibling-unaffected invariant when FailFast=false.
			var (
				value   any
				err     error
				panicV  any
				panicked bool
			)
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicV = r
						panicked = true
					}
				}()
				value, err = task.Execute(childCtx)
			}()
			if panicked {
				err = fmt.Errorf("%w: %v", ErrTaskPanic, panicV)
				value = nil
			}

			// Cancelled classification: the child returned the same error
			// its ctx carries (Canceled or DeadlineExceeded). We do not flag
			// Cancelled=true for plain per-child timeouts because those are
			// caller-scoped, not parent-driven — but the spec's Result table
			// says "parent cancelled" → Cancelled=true. We distinguish by
			// comparing to cancelCtx (parent) vs childCtx (per-child).
			cancelled := false
			if err != nil && cancelCtx.Err() != nil && errors.Is(err, cancelCtx.Err()) {
				cancelled = true
			}

			// Cost charging. Tasks report via ctx-stamped trackers in real
			// integrations; the primitive itself cannot read a "cost from
			// ctx" without coupling to costtrack, so we accept the cost via
			// a (Task).EstimateCost()-as-actual fallback ONLY when the task
			// opted into pre-flight (non-zero estimate) and succeeded. For
			// now the Result.CostUSD defaults to 0 on failure and equals the
			// estimate on success; callers wanting precise billing wrap
			// Execute and mutate Result via OnChildComplete bookkeeping.
			cost := 0.0
			if err == nil {
				cost = task.EstimateCost()
			}
			if cost > 0 {
				if !bt.Charge(cost) {
					// Runtime over-budget: wrap a sentinel onto this child's
					// error and record the cent-accurate aggregate via the
					// tracker. cancel() fires inside Charge via onExceed.
					err = fmt.Errorf("%w: spent>%.4f", ErrBudgetExceeded, cfg.BudgetLimit)
					cost = 0 // cost did not apply — it tripped the cap
					cancelled = true
				} else {
					atomic.AddUint64(&aggregateCost, uint64(cost*100+0.5))
				}
			}

			results[idx] = Result{
				Task:      task,
				Value:     value,
				Error:     err,
				CostUSD:   cost,
				Duration:  time.Since(start),
				Cancelled: cancelled,
			}

			switch {
			case err == nil:
				atomic.AddInt64(&nSuccess, 1)
			case cancelled:
				atomic.AddInt64(&nCancelled, 1)
			default:
				atomic.AddInt64(&nError, 1)
			}

			if cfg.OnChildComplete != nil {
				cfg.OnChildComplete(task.ID(), value, err)
			}

			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			emit(cfg, evtChildComplete, map[string]any{
				"child_id":    task.ID(),
				"index":       idx,
				"cost_usd":    cost,
				"duration_ms": time.Since(start).Milliseconds(),
				"err":         errStr,
			})

			if err != nil && cfg.FailFast {
				cancel()
			}
		}(i, tasks[i])
	}

	wg.Wait()

	emit(cfg, evtComplete, map[string]any{
		"n_success":           atomic.LoadInt64(&nSuccess),
		"n_error":             atomic.LoadInt64(&nError),
		"n_cancelled":         atomic.LoadInt64(&nCancelled),
		"aggregate_cost_usd":  float64(atomic.LoadUint64(&aggregateCost)) / 100.0,
		"duration_ms":         time.Since(dispatchStart).Milliseconds(),
	})

	return results
}
