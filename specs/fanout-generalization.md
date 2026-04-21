<!-- STATUS: ready -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-3 (bus, event log) -->
<!-- BUILD_ORDER: 14 -->

# Fan-Out Generalization — Implementation Spec

## Overview

Stoke has one place today that spawns N concurrent children with failure tracking: `internal/plan/session_scheduler_parallel.go`. Three upcoming executors (research fan-out in spec-4, delegation hiring in spec-5, code session parallelism already shipped) need the same primitive: spawn N children, track aggregate budget, fail-fast on first error, propagate a trust ceiling, cancel cleanly. This spec extracts that into `internal/fanout/` as a generic reusable package, migrates the session scheduler to consume it, and wires the two executor specs through it. Mirrors CloudSwarm's `SubAgentFanOutWorkflow` shape for behavior parity.

## Stack & Versions

- Go 1.21+ (type parameters required on `FanOut[T Task]`)
- No new third-party deps. `context`, `sync`, `sync/atomic`, `time`, `errors` only.
- Internal deps: `internal/bus` (spec-3), `internal/eventlog` (spec-3), `internal/costtrack` (for budget reservation primitives).

## Existing Patterns to Follow

- Parallel dispatch primitive: `internal/plan/session_scheduler_parallel.go` (see inventory below)
- Bus emission: `eventlog.EmitBus(bus, log, ev)` (spec-3) — dual-publishes to bus + event log
- Streamjson mirroring: spec-2 mirrors `cs.*` and `_stoke.dev/*` bus events
- Costtrack: `internal/costtrack/` — fan-out does NOT implement budget storage; it consults existing Reserve/Reconcile
- Concurrency: `sync.WaitGroup` + buffered semaphore channel; `sync/atomic` for aggregate cost (no mutex in hot path)

## Current Pattern Inventory

### What `session_scheduler_parallel.go` does today

| Concern | File:line | Mechanism |
|---|---|---|
| Parallel dispatch | `session_scheduler_parallel.go:167` | `sem := make(chan struct{}, ss.ParallelSessions)` buffered semaphore + `var wg sync.WaitGroup` |
| Goroutine per child | `session_scheduler_parallel.go:547` | `wg.Add(1); go runOne(id)` inside ready-loop |
| Ready queue (DAG) | `session_scheduler_parallel.go:192-210` | `isReady(id)` checks `dag.Deps[id]` against `completed + failed` sets |
| Result aggregation | `session_scheduler_parallel.go:172-177` | `recordResult` under `resultsMu`; preserves declaration order at the end (`order` slice) |
| Terminal-state bookkeeping | `session_scheduler_parallel.go:181-190` | `recordTerminal` updates `done[id]`, `completed[id]`, `failed[id]` under `stateMu` |
| Fail-fast gate | `session_scheduler_parallel.go:450-457` | `!ss.ContinueOnFailure && len(failed) > 0` → break dispatch loop |
| Context cancellation | `session_scheduler_parallel.go:438-443` | `if ctx.Err() != nil { firstErr = ctx.Err(); break }` in each dispatch iteration |
| Per-child retry | `session_scheduler_parallel.go:251-367` | Inner `for attempt := 1; attempt <= retries` loop — session-level, not per-task |
| Preempt priority | `session_scheduler_parallel.go:219` / `541-543` | `session.Preempt` bypasses `sem` so fix sessions aren't queued |
| Deadlock watchdog | `session_scheduler_parallel.go:78-100`, `479-490` | Background ticker polls `CheckPromotedDispatch` at SLA/2; surfaces stalled promoted sessions |
| Budget tracking | **ABSENT** | Today's scheduler has no aggregate budget cap. That's the gap this spec closes. |
| Trust propagation | **ABSENT** | Today's scheduler has no trust ceiling concept. Spec-5 needs it. |
| Shared-state mutexes | `session_scheduler_parallel.go:104-111` | `stateMu`, `resultsMu`, `sessionsMu` around SOWState flushes, results slice, live sessions list |

Concurrency limit enforced only via semaphore; no backpressure hook. Failures propagate via `failed` map + the `!ContinueOnFailure` short-circuit — there is no context-based cancellation broadcast to running siblings (they finish their current attempt, they don't get cancelled mid-execute).

## Data Models

### `FanOutConfig`

| Field | Type | Default | Constraints |
|---|---|---|---|
| `MaxParallel` | `int` | 0 (serial) | `>= 0`; 0 or 1 → serial execution |
| `FailFast` | `bool` | `false` | When true, cancel siblings on first child error |
| `BudgetLimit` | `float64` | `0` (unlimited) | USD aggregate across all children; `0` disables |
| `TrustCeiling` | `int` | `-1` (inherit) | 0-4 (CloudSwarm L0-L4); propagated via context |
| `TimeoutPerChild` | `time.Duration` | `0` (no per-child timeout) | Applied via `context.WithTimeout` wrapping ctx |
| `OnChildStart` | `func(childID string)` | nil | Fired synchronously at goroutine entry |
| `OnChildComplete` | `func(childID string, result any, err error)` | nil | Fired synchronously after child returns |
| `BusPublisher` | `bus.Publisher` | nil | When non-nil, emits lifecycle events per spec-3 |
| `EventLog` | `eventlog.Log` | nil | When non-nil, mirrors bus events |
| `RunID` | `string` | auto ULID | Identifier for this fan-out invocation; appears in every event |

### `Task` interface

```go
type Task interface {
    ID() string
    Execute(ctx context.Context) (any, error)
    EstimateCost() float64   // upfront USD estimate; 0 means "unknown, don't pre-reserve"
}
```

Generic wrapper signature:

```go
func FanOut[T Task](ctx context.Context, tasks []T, cfg FanOutConfig) []Result
```

Callers implement `Task` on their domain type. Return slice is declaration-ordered.

### `Result`

| Field | Type | Notes |
|---|---|---|
| `Task` | `Task` | original task reference |
| `Value` | `any` | whatever `Execute` returned on success; zero-value on error |
| `Error` | `error` | non-nil when child errored, was cancelled, or exceeded budget |
| `CostUSD` | `float64` | actual cost reported by child (via ctx-stamped cost tracker) |
| `Duration` | `time.Duration` | wall-clock from goroutine entry to return |
| `Cancelled` | `bool` | true when child did not complete its work because parent cancelled (ctx/fail-fast/budget) |

### Context keys (propagated into each child)

- `fanout.runID` — ULID of this fan-out run.
- `fanout.trustCeiling` — int, 0-4. Children must clamp any further delegation to `min(current, ceiling)`.
- `fanout.budgetRemaining` — atomic pointer consulted by children before expensive calls (advisory; fan-out itself also cancels when exceeded).

## API / Package Surface

### `internal/fanout/fanout.go`

```go
package fanout

type Task interface {
    ID() string
    Execute(ctx context.Context) (any, error)
    EstimateCost() float64
}

type Result struct {
    Task      Task
    Value     any
    Error     error
    CostUSD   float64
    Duration  time.Duration
    Cancelled bool
}

type FanOutConfig struct {
    MaxParallel      int
    FailFast         bool
    BudgetLimit      float64
    TrustCeiling     int
    TimeoutPerChild  time.Duration
    OnChildStart     func(childID string)
    OnChildComplete  func(childID string, result any, err error)
    BusPublisher     bus.Publisher
    EventLog         eventlog.Log
    RunID            string
}

func FanOut[T Task](ctx context.Context, tasks []T, cfg FanOutConfig) []Result
```

Helper (non-exported, but documented for tests):

```go
// budgetTracker wraps the aggregate cost accumulator + limit check.
// Uses sync/atomic on a *uint64 (cents) so siblings can report without mutex contention.
type budgetTracker struct {
    limitCents  uint64    // 0 = unlimited
    spentCents  atomic.Uint64
    onExceed    func()    // cancels parent ctx
}

func (b *budgetTracker) Charge(usd float64) bool // returns false if adding exceeds limit
func (b *budgetTracker) Remaining() float64
```

## Business Logic

### Budget aggregation algorithm

```
bt = new budgetTracker(cfg.BudgetLimit * 100)  // cents, integer atomic
cancelCtx, cancel = context.WithCancel(ctx)
bt.onExceed = cancel

# Pre-flight: sum of EstimateCost() must not exceed BudgetLimit when all estimates
# are non-zero. If estimates are zero, skip pre-flight and rely on runtime check.
estimateTotal = sum(t.EstimateCost() for t in tasks)
if cfg.BudgetLimit > 0 and estimateTotal > 0 and estimateTotal > cfg.BudgetLimit:
    return all-tasks-errored with ErrBudgetPreflight

# Per-child on completion:
ok = bt.Charge(result.CostUSD)
if !ok:
    emit bus "fanout.budget_exceeded" {run_id, spent, limit}
    bt.onExceed()   # cancels all siblings
    result.Error = ErrBudgetExceeded
```

`Charge` is a CAS loop: load spent, compute new, if `new > limit` return false without commit, else CAS. No mutex required. Siblings continue until they observe `ctx.Done()`.

### Trust clamping algorithm

```
# At FanOut entry:
parentTrust = ctxTrust(ctx)           # reads existing fanout.trustCeiling, default 4
effective   = parentTrust
if cfg.TrustCeiling >= 0:
    effective = min(parentTrust, cfg.TrustCeiling)

# When spawning each child:
childCtx = context.WithValue(cancelCtx, trustKey, effective)
childCtx, _ = context.WithTimeout(childCtx, cfg.TimeoutPerChild)
```

Children reading `ctxTrust(ctx)` get `effective`. When a child itself calls `FanOut` or `delegation.NewDelegation`, those callees clamp to `min(their-config, ctx-value)`. This is the same rule CloudSwarm uses (`delegation.py:128-155`: `effective_trust = min(requester_trust, provider_trust)`).

### Cancellation propagation

1. Parent ctx cancelled → `cancelCtx` (derived) auto-cancels → every child ctx cancels.
2. Child errors + `FailFast` → main loop calls `cancel()` → siblings observe `ctx.Done()`.
3. Budget exceeded → `bt.onExceed()` calls `cancel()` → same path.
4. Per-child timeout elapses → that child's ctx (derived with `WithTimeout`) cancels independently; parent and siblings unaffected.
5. `TimeoutPerChild == 0` → no per-child timeout; child bounded only by parent ctx + fan-out-level cancellation.

### Dispatch loop (simplified)

```
sem = make(chan struct{}, max(cfg.MaxParallel, 1))
var wg sync.WaitGroup
results = make([]Result, len(tasks))

for i, task in tasks:
    if cancelCtx.Err() != nil: break       # pre-dispatch gate
    sem <- struct{}{}
    wg.Add(1)
    go func(i int, task T):
        defer wg.Done()
        defer func() { <-sem }()
        start := time.Now()
        childCtx := childContext(cancelCtx, i, cfg)
        if cfg.OnChildStart != nil: cfg.OnChildStart(task.ID())
        emit bus "fanout.child.start"
        value, err := task.Execute(childCtx)
        cost := readCostFromCtx(childCtx)
        results[i] = Result{Task: task, Value: value, Error: err, CostUSD: cost, Duration: time.Since(start), Cancelled: childCtx.Err() != nil && err == childCtx.Err()}
        bt.Charge(cost)
        if cfg.OnChildComplete != nil: cfg.OnChildComplete(task.ID(), value, err)
        emit bus "fanout.child.complete"
        if err != nil and cfg.FailFast: cancel()
    (i, task)

wg.Wait()
emit bus "fanout.complete"
return results
```

`results[i]` is indexed by slice position — no post-hoc sort needed. Writes to distinct indices don't race.

### Failure modes table

| Failure | Detection | Effect on other children | Result.Error |
|---|---|---|---|
| Single child returns error | `err != nil` from `task.Execute` | If `FailFast`: cancel siblings; else: continue | wrapped child error |
| Parent ctx cancelled | `cancelCtx.Err() != nil` | All children receive ctx cancel | `context.Canceled` / `DeadlineExceeded` |
| Per-child timeout elapses | child-ctx `DeadlineExceeded` | Only that child affected | `context.DeadlineExceeded` |
| Budget pre-flight fails | `sum(estimates) > BudgetLimit` | No children ever spawned | `ErrBudgetPreflight` on every Result |
| Budget exceeded at runtime | `bt.Charge` returns false | cancel() → siblings cancelled | `ErrBudgetExceeded` + wrapped ctx cancel |
| Task.Execute panics | deferred recover in worker | Only that child (if `!FailFast`) | `ErrTaskPanic` wrapping recovered value |
| All children succeed | `wg.Wait()` completes cleanly | n/a | nil for every Result |

### Observability events

Emitted through `internal/bus` and mirrored to `internal/eventlog` per spec-3 (single call via `eventlog.EmitBus`). Mirrored to streamjson per spec-2.

| Event type | Payload |
|---|---|
| `fanout.start` | `{run_id, n, max_parallel, fail_fast, budget_limit, trust_ceiling}` |
| `fanout.child.start` | `{run_id, child_id, index}` |
| `fanout.child.complete` | `{run_id, child_id, cost_usd, duration_ms, err}` (err is string or empty) |
| `fanout.budget_exceeded` | `{run_id, spent_usd, limit_usd}` |
| `fanout.complete` | `{run_id, n_success, n_error, n_cancelled, aggregate_cost_usd, duration_ms}` |

All events carry `cs.` or `_stoke.dev/` prefix as per spec-2 streamjson conventions. Default prefix `_stoke.dev/fanout.*`.

## Migration — `session_scheduler_parallel.go`

Keep `runParallel` method and its exported triggers (`ParallelSessions`, `ContinueOnFailure`, `OnProgress`, `OnSessionStart`) unchanged. Refactor the internals:

| Old (current) | New (via fanout) |
|---|---|
| `sem := make(chan struct{}, ss.ParallelSessions)` | `cfg.MaxParallel = ss.ParallelSessions` |
| `!ss.ContinueOnFailure` short-circuit | `cfg.FailFast = !ss.ContinueOnFailure` |
| `runOne(id)` goroutine body | wrapped in a `sessionTask` type implementing `Task` |
| `recordResult` + per-result `ss.OnProgress` | `cfg.OnChildComplete = func(id, v, err) { ... ss.OnProgress(v.(SessionResult)) }` |
| `wg.Wait()` + declaration-order reorder | `FanOut` returns `[]Result` in input order; unwrap `.Value` to `SessionResult` |
| DAG `isReady` / ready-queue dispatch loop | **KEEP** outside fanout — fanout handles flat parallelism only; the scheduler feeds fanout a "currently-ready" batch per iteration |
| `sessionsMu`, `stateMu`, `recordTerminal` | **KEEP** as-is; these are scheduler concerns, not fanout concerns |
| Deadlock watchdog, promoted-session tracking | **KEEP** as-is; domain-specific |
| Preempt bypass | **KEEP** as-is; implemented at scheduler layer by calling `FanOut` twice: once for regular sessions (bounded), once for preempt sessions (unbounded `MaxParallel=0` with no sem) |

**Design call:** fanout does NOT know about DAGs. The scheduler keeps its DAG ready-queue logic and hands fanout one batch of siblings per outer iteration. Fanout replaces only the inner parallel block.

## Research Executor Integration (spec-4)

Research subagent spawn in `internal/executor/research.go`:

```go
tasks := make([]SubagentTask, len(queries))
for i, q := range queries {
    tasks[i] = SubagentTask{Query: q, ParentID: parentID}
}
budgetPerChild := parentBudget / float64(len(queries))
results := fanout.FanOut(ctx, tasks, fanout.FanOutConfig{
    MaxParallel:     min(len(queries), 5),
    FailFast:        false,                          // research wants partial results
    BudgetLimit:     parentBudget,
    TrustCeiling:    ctxTrust(ctx),                  // inherit parent's effective trust
    TimeoutPerChild: 90 * time.Second,
    BusPublisher:    busPub,
    EventLog:        log,
    RunID:           "research-" + parentID,
})
```

`SubagentTask.EstimateCost()` returns a Haiku-class budget (~$0.01 per subagent). `Execute` runs a single agentloop turn with clamped tools and returns a `ResearchChunk`. Parent collects `.Value` from each non-errored Result and merges.

## Delegation Executor Integration (spec-5)

When multiple candidate agents match a spec (e.g. three translation agents), delegation can fan out "find + hire" in parallel and take the first successful hire. Mode selected via `cfg.FailFast=true` + `cfg.MaxParallel=N`:

```go
tasks := make([]HireTask, len(candidates))
for i, c := range candidates:
    tasks[i] = HireTask{Candidate: c, Spec: spec, ParentTrust: dctx.DelegatorTrust}
results := fanout.FanOut(ctx, tasks, fanout.FanOutConfig{
    MaxParallel:  len(candidates),
    FailFast:     true,                                // first success wins; losers get ctx cancel
    BudgetLimit:  totalBudget,                          // winner's reservation refunded on losers (see saga)
    TrustCeiling: min(dctx.DelegatorTrust, dctx.ExecutorTrust),  // CloudSwarm rule
    BusPublisher: busPub,
    EventLog:     log,
    RunID:        "delegate-" + dctx.DelegationID,
})
```

Trust clamping: `HireTask.Execute` reads `ctxTrust(ctx)` and writes it into the A2A message `stoke.delegationToken` payload's `EffTrust` field. Loser `HireTask`s (cancelled) emit `cs.delegation.budget_refunded` from their defer block via the saga compensator in spec-5.

Budget splitting: reservation is total; per-child is advisory via `FanOutConfig.BudgetLimit / N`. Only the winner's reservation settles; losers refund through saga.

## Boundaries — What NOT To Do

- Do NOT implement budget storage, reservation, or reconciliation — spec-5 owns that in `internal/delegation/budget.go`. Fanout only **consults** a limit and tracks spent via `sync/atomic`.
- Do NOT serialize children behind DAG dependencies — that's the scheduler's job. Fanout treats input tasks as independent siblings.
- Do NOT delete or rewrite `internal/plan/session_scheduler_parallel.go`. Refactor internals; keep exported surface.
- Do NOT move the deadlock watchdog or preempt logic into fanout — those are session-specific.
- Do NOT introduce a new bus event subsystem. Publish through the existing `internal/bus` + `internal/eventlog.EmitBus` helper per spec-3.
- Do NOT wrap tasks in another queue/scheduler — children MUST start concurrently (up to `MaxParallel`). No FIFO blocking on a single channel receive.
- Do NOT mutate `FanOutConfig` after passing it to `FanOut` — treat as value-copy.

## Error Handling

| Failure | Strategy | Caller Sees |
|---|---|---|
| `len(tasks) == 0` | return empty slice, nil error | no-op |
| `MaxParallel < 0` | treated as 1 (serial) + log warning | `fanout.warn` bus event |
| Pre-flight budget fail | all Results get `ErrBudgetPreflight`; no children start | `fanout.budget_exceeded` published once |
| Task.Execute panics | recover, wrap as `ErrTaskPanic` in Result.Error | normal Result; other siblings unaffected unless FailFast |
| Bus publish fails | log to stderr, continue | best-effort observability |
| Eventlog append fails | same as bus — log, continue | correctness not gated on event log |

## Benchmarks / Expected Throughput

Against current `session_scheduler_parallel.go` baseline (measured on 20-session SOW, 4 concurrent):

| Metric | Before (current) | After (via fanout) | Delta |
|---|---|---|---|
| Wall-clock for 20 independent sessions, 4-way | ~T | ~T (±3%) | neutral — same primitives |
| Budget pre-flight overhead | n/a | < 1ms | new |
| Per-event emit (bus + eventlog) | 0 | ~50-200µs | new but necessary |
| Memory per child | session-sized | +~200B (Result struct) | negligible |

Fanout adds two atomics per `Charge` + one CAS; against a workload where each child runs for seconds-to-minutes, this is < 0.01% overhead. Benchmark as `BenchmarkFanOut100Siblings` and compare to a hand-rolled WaitGroup baseline; regression gate is < 5%.

## Testing

### `internal/fanout/`
- [ ] Happy: 10 `Task` impls, all succeed → all Results have `Error == nil`, aggregate Cost matches sum
- [ ] Concurrency: 10 tasks, `MaxParallel=3` → at most 3 tasks report `OnChildStart` before any `OnChildComplete` (verified via atomic counter)
- [ ] FailFast: 5 tasks, task #2 errors, `FailFast=true` → tasks #3-5 have `Result.Cancelled == true`
- [ ] FailFast off: 5 tasks, task #2 errors, `FailFast=false` → tasks #3-5 complete normally
- [ ] Budget enforcement runtime: `BudgetLimit=$1`, each task costs $0.40 — first two succeed, third hits cancel, remainder cancelled; `fanout.budget_exceeded` event emitted exactly once
- [ ] Budget preflight: `BudgetLimit=$1`, estimates sum to $5 → all Results have `ErrBudgetPreflight`; `n_success == 0`
- [ ] Trust clamping: parent ctx has trust=4; `cfg.TrustCeiling=2` → each child reads `ctxTrust=2`
- [ ] Trust inheritance: parent ctx has trust=1; `cfg.TrustCeiling=3` → child reads trust=1 (min wins)
- [ ] Context cancellation: cancel parent after 2 complete → remaining see ctx.Done, Result.Cancelled=true
- [ ] Per-child timeout: `TimeoutPerChild=100ms`, child sleeps 500ms → that child errors with DeadlineExceeded, siblings unaffected
- [ ] Panic recovery: a task panics → Result.Error wraps panic value, other tasks still complete
- [ ] Event emission: verify `fanout.start`, `N * fanout.child.start`, `N * fanout.child.complete`, `fanout.complete` arrive on fake bus in order
- [ ] Empty tasks: `FanOut(ctx, []Task{}, cfg)` returns `[]`, no events published
- [ ] Declaration-order preservation: 5 tasks, random sleep durations → returned `[]Result` indexes match input order

### `internal/plan/`
- [ ] Existing `TestSessionSchedulerParallelStillWorks` passes unchanged after migration (golden SOW, same results)
- [ ] `TestSessionSchedulerParallelEmitsFanoutEvents` (new): fake bus receives `fanout.start` + `fanout.complete` wrapping the scheduler batch

### Integration
- [ ] Research executor smoke: `stoke research "foo"` dispatches 3 subagents via fanout, all three `fanout.child.start` events visible, aggregate cost within budget
- [ ] Delegation executor smoke: `stoke delegate --spec X --budget 5 --candidates 3` with `FailFast=true` produces exactly one `cs.delegation.completed` + two `cs.delegation.budget_refunded`

## Acceptance Criteria

- `go test ./internal/fanout/... -run TestBasicFanOut`
- `go test ./internal/fanout/... -run TestFailFast`
- `go test ./internal/fanout/... -run TestBudgetEnforcement`
- `go test ./internal/fanout/... -run TestBudgetPreflight`
- `go test ./internal/fanout/... -run TestTrustClamping`
- `go test ./internal/fanout/... -run TestContextCancellation`
- `go test ./internal/fanout/... -run TestPerChildTimeout`
- `go test ./internal/fanout/... -run TestPanicRecovery`
- `go test ./internal/fanout/... -run TestEventEmission`
- `go test ./internal/plan/... -run TestSessionSchedulerParallelStillWorks`
- `go test ./internal/plan/... -run TestSessionSchedulerParallelEmitsFanoutEvents`
- `go test ./... -race -run TestFanOut`  (race detector clean on all fanout tests)
- `go vet ./internal/fanout/...`
- `go build ./cmd/stoke`

WHEN `cfg.FailFast == true` and any child returns a non-nil error, THE SYSTEM SHALL cancel the fan-out context within 10ms and every non-completed child SHALL observe `ctx.Done()`.

WHEN the aggregate `CostUSD` across completed children exceeds `cfg.BudgetLimit`, THE SYSTEM SHALL emit exactly one `fanout.budget_exceeded` event and cancel all in-flight children.

WHEN `cfg.TrustCeiling >= 0`, THE SYSTEM SHALL propagate `min(parentTrust, cfg.TrustCeiling)` into each child's context under key `fanout.trustCeiling`.

WHEN `session_scheduler_parallel.go` migrates to consume `fanout.FanOut`, the existing public surface (`SessionScheduler.Run`, `ParallelSessions`, `ContinueOnFailure`, `OnProgress`, `OnSessionStart`) SHALL NOT change and all existing tests SHALL pass without modification.

## Implementation Checklist

1. [ ] **Create `internal/fanout/fanout.go`** with the `Task` interface, `Result` struct, `FanOutConfig` struct, `FanOut[T Task]` generic function. Goroutine body: acquire sem, defer release + wg.Done, derive child ctx (cancel + trust + timeout), call `OnChildStart` + emit `fanout.child.start`, run `task.Execute`, compute Result, call `bt.Charge`, call `OnChildComplete` + emit `fanout.child.complete`, if FailFast and err → cancel(). Index writes into pre-sized `results[i]` to avoid mutex on result slice.

2. [ ] **Create `internal/fanout/budget.go`** with `budgetTracker` (atomic uint64 cents + limit + onExceed cancel func). `Charge(usd)` CAS-loop: load, compute new, if over-limit return false without commit, else CAS-commit. Handle `limitCents == 0` as unlimited (always returns true). Expose `Remaining() float64`.

3. [ ] **Create `internal/fanout/context.go`** with typed context helpers: `WithTrustCeiling(ctx, int) context.Context`, `TrustCeiling(ctx) int` (default 4), `WithRunID(ctx, string) context.Context`, `RunID(ctx) string`. Use unexported struct keys to prevent collision.

4. [ ] **Create `internal/fanout/events.go`** with `emitStart(cfg, n)`, `emitChildStart(cfg, id, i)`, `emitChildComplete(cfg, id, cost, dur, err)`, `emitBudgetExceeded(cfg, spent, limit)`, `emitComplete(cfg, summary)`. Each calls `eventlog.EmitBus(cfg.BusPublisher, cfg.EventLog, ev)` when both non-nil; no-op otherwise. Event Type strings: `_stoke.dev/fanout.*` (matches spec-2 prefix convention).

5. [ ] **Create `internal/fanout/errors.go`** with `ErrBudgetPreflight`, `ErrBudgetExceeded`, `ErrTaskPanic`. Use `errors.New` + sentinel values; wrap with `%w` at emit sites so callers can `errors.Is` them.

6. [ ] **Create `internal/fanout/fanout_test.go`** with all 14 tests from the Testing section. Use a `fakeTask` struct with configurable sleep/error/cost so each test configures its own scenario. Use a `fakeBus` that records published events for assertion. Keep each test self-contained — no shared state between tests.

7. [ ] **Create `internal/fanout/fanout_bench_test.go`** with `BenchmarkFanOut10`, `BenchmarkFanOut100`, `BenchmarkFanOut1000` at `MaxParallel=4`. Baseline: hand-rolled WaitGroup identical semantics. Regression gate < 5% wall-clock delta documented in test comment.

8. [ ] **Migrate `internal/plan/session_scheduler_parallel.go`** — inside the dispatch loop, replace the inner `wg.Add(1) + go runOne(id)` block with a batched `fanout.FanOut` call over the ready-this-iteration sessions. `sessionTask` adapter (new private type in `session_task.go`) wraps the existing `runOne` logic as `Task.Execute`. Keep DAG, watchdog, preempt, continuation-append logic outside the fanout call. `OnProgress` hooked via `cfg.OnChildComplete`. Preempt sessions get a separate `FanOut` call with `MaxParallel=0` (unbounded). All existing mutex patterns (`stateMu`, `sessionsMu`, `resultsMu`) preserved as-is.

9. [ ] **Add `internal/plan/session_task.go`** — `sessionTask` struct implementing `fanout.Task`: `ID()` returns session ID; `EstimateCost()` returns `0` (unknown, no pre-reserve); `Execute(ctx)` wraps the current `runOne` body (minus wg/sem boilerplate which fanout now owns). Returns `SessionResult` as the `any`.

10. [ ] **Add `internal/plan/session_scheduler_parallel_test.go`** `TestSessionSchedulerParallelEmitsFanoutEvents`: fake bus, run 3-session SOW, assert `_stoke.dev/fanout.start` + 3× `fanout.child.start` + `fanout.complete` events published with matching `run_id`.

11. [ ] **Document** the new package in `CLAUDE.md` package map under "AGENT BEHAVIOR" (one line: `fanout/  Generic parallel child-task fan-out (budget, trust-clamp, fail-fast, cancellation)`).

12. [ ] **Run** `go build ./cmd/stoke`, `go test ./... -race`, `go vet ./...` — the CI gate. Fix any surfaced races.

13. [ ] **Smoke test** — existing mission SOW with `--parallel 4` completes identically to pre-migration (same pass/fail pattern, same session order in output). No new bus events from scheduler if `BusPublisher == nil` (back-compat).
