<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-07-02 -->
<!-- CREATED: 2026-07-02 -->
<!-- DEPENDS_ON: (activates the Phase-4 harness substrate landed in fcf8abcd; companion to specs/governance-activation.md precedent) -->
<!-- BUILD_ORDER: 1 -->
<!-- AUDIT: A041 (stance runner), A099 + A100 (concern role-template registry) in audit/complete-systems-2026-07-01.md -->
<!-- HOLISTIC: production-readiness/collision/playwright N/A — internal Go substrate wiring. -->

# Harness Activation — Give the Stance Substrate an Execution Runner (Model Loop + Tool Authorization + Checkpointing + Cost Accounting)

## Overview

Commit `fcf8abcd` landed the V2 harness substrate: `internal/harness` builds
`StanceSession`s (`SpawnStance`, harness.go) with a role system prompt, a
rendered concern field, a resolved model, and an authorized tool list — then
stops. Nothing ever calls a model, no tool loop exists, `PauseStance` can only
time out at its 30s ack deadline because no runner ever reaches a
`CheckpointCheck` safe point, and `TokensUsed`/`CostUSD` are never written
(`InspectStance` reports zeros forever). The old `internal/harness/models`
package (the `Provider` seam) was deleted as dormant in `2111ee7c` because it
had zero importers. Audit finding A041 confirmed the pipeline "stops at
bookkeeping".

This spec activates the substrate:

1. **Provider seam, consumer-side** — re-introduce a minimal `Provider`
   interface (`Chat(ctx, ChatRequest) (*ChatResponse, error)`) defined in
   `internal/harness` itself (where the consumer lives, per Go convention),
   not in a satellite package that can go dormant again.
2. **`StanceRunner`** (`internal/harness/runner.go`) — drives
   `Provider.Chat` with the `SystemPrompt` built by `SpawnStance`, enforces
   `internal/harness/tools` authorization on every returned `ToolCall`, calls
   `sess.CheckpointCheck` between turns (making `PauseStance`/`ResumeStance`
   actually work), updates `TokensUsed`/`CostUSD` on the `StanceSession`
   under `h.mu`, writes a `cost_record` ledger node per model turn (the shape
   `bench.ComputeMetrics` already queries), and emits
   `worker.action.started`/`worker.action.completed` bus events.
3. **Real adapter + mock** — `APIProvider` adapts the existing
   `internal/provider` direct API clients (Anthropic / OpenAI-compat) to the
   seam, computing `CostUSD` via `costtrack.ComputeCost`; `MockProvider`
   stays for tests and offline bench runs.
4. **Bench drives the runner** — `bench.Runner.Run` spawns a dev stance and
   now drives at least one real runner turn through an offline deterministic
   provider, so golden-mission metrics measure real substrate work
   (worker.action events, checkpoint discipline, cost accounting) instead of
   spawn-then-declare-victory.
5. **Concern role templates go live (A099/A100)** —
   `harness.NewWithRoleTemplates` is the production spawn path that
   constructs the `concern.Builder` and calls `templates.RegisterAll`, so
   every stance spawned through it renders its role/face template
   (`cto_snapshot`, `dev_implementing_ticket`, ...) against real ledger
   content. Bench gains an opt-in (`UseFullTemplates`) that exercises this
   end-to-end while keeping the section-less default for fixture weight.

Who: the V2 substrate operator (bench today; mission/supervisor boot next).
Acceptance is proven entirely by Go unit + integration tests under
`internal/harness/` and `internal/bench/` — no live model call required.

## Stack & Versions

- Go 1.25.x (existing module `github.com/RelayOne/r1`).
- stdlib only: `context`, `encoding/json`, `fmt`, `strings`, `sync`, `time`, `testing`.
- Existing internal packages (no new deps): `internal/bus`, `internal/ledger`,
  `internal/concern`, `internal/concern/templates`, `internal/harness/tools`,
  `internal/harness/prompts`, `internal/provider`, `internal/costtrack`,
  `internal/bench`.
- No third-party additions. `go mod tidy` must report no changes.

## Existing Patterns to Follow

- Session lifecycle + cooperative pause: `internal/harness/harness.go`
  (`SpawnStance`, `PauseStance` ack wait, `ResumeStance` channel cycling)
  and `internal/harness/session.go` (`CheckpointCheck`).
- Bus event emission: `SpawnStance`'s `bus.Publish(bus.Event{Type, EmitterID,
  Scope, Payload, CausalRef})` (harness.go).
- Ledger writes: `ledger.AddNode(ctx, ledger.Node{Type, SchemaVersion:1,
  CreatedBy, MissionID, Content})` — same raw-node pattern the governance
  Governor and `bridge.CostBridge.PublishUsage` use for `cost_record`.
- Cost math: `costtrack.ComputeCost(model, in, out, cacheRead, cacheWrite)`.
- Test style: `internal/harness/harness_test.go` `setup(t)` (real ledger+bus
  in temp dirs, minimal concern templates); `internal/bench/golden_test.go`
  `classifyRunErr` skip discipline.
- Provider seam shape: the deleted `internal/harness/models/provider.go`
  (restored consumer-side, context-aware).

## Data / Event Contract

| Runner moment | bus.Event Type | ledger node | Notes |
|---|---|---|---|
| model turn begins | `worker.action.started` | (none) | payload `{action:"model_turn", turn, model}` |
| model turn ends | `worker.action.completed` | `cost_record` | payload `{action:"model_turn", turn, tokens_in, tokens_out, cost_usd, tool_calls}`; node content `{cost_usd, tokens_used, model, stance_id, turn}` (the shape `bench.ComputeMetrics` sums) |
| tool call requested | `worker.action.started` | (none) | payload `{action:"tool_call", tool, turn, authorized}` |
| tool call finished/denied | `worker.action.completed` | (none) | payload `{action:"tool_call", tool, turn, authorized, error?}`; denied calls NEVER reach the executor |
| between turns | (none) | (none) | `sess.CheckpointCheck(ctx)` — pause acks land here |

Authorization: `htools.IsAuthorized(sess.Role, htools.ToolName(call.Name))`
per call. A denied call feeds a `tool denied: ...` result message back to the
model (honest refusal, not silent drop) and increments `ToolCallsDenied`.

Termination: the runner stops when (a) the model returns no tool calls
(`RunOutcome.FinalContent` set), (b) `MaxTurns` is hit
(`RunOutcome.HitMaxTurns`), (c) the stance is terminated
(`RunOutcome.Terminated`), or (d) ctx ends / provider errors (error return).

## Error Handling

| Failure | Strategy |
|---|---|
| `Provider.Chat` error | abort run, return wrapped error; stance stays in its current status for supervisor triage |
| unauthorized tool call | do NOT execute; feed denial message to model; count in `ToolCallsDenied`; emit completed event with `authorized:false` |
| nil `ToolExecutor` with authorized call | feed `tool unavailable: no executor wired` result to model — loop continues honestly |
| `bus.Publish` / `ledger.AddNode` error inside a turn | swallow (fire-and-forget observability, matching Governor `handle` cases) — the run itself must not die on telemetry loss |
| `CheckpointCheck` ctx error | return ctx error (pause-then-cancel is a clean exit) |
| stance not found / not running at Run start | error return |

## Boundaries — What NOT To Do

- Do NOT resurrect `internal/harness/models` as a package — the seam lives
  with its consumer.
- Do NOT auto-terminate the stance at end of run; callers own lifecycle.
- Do NOT make bench default to the full concern-template registry — the
  section-less default stays (its comment documents why); `UseFullTemplates`
  is opt-in.
- Do NOT block the runner on telemetry failures (bus/ledger write errors).
- Do NOT implement tool execution inside the harness — `ToolExecutor` is a
  seam; the native tool runtime stays where it lives today.

## Testing

### `internal/harness/runner_test.go`
- Full integration: SpawnStance → `StanceRunner.Run` with `MockProvider`
  (tool-call turn then final turn) → executor receives ONLY authorized calls
  → `InspectStance` shows accumulated `TokensUsed`/`CostUSD` → bus replay
  shows `worker.action.started`/`completed` pairs → ledger has `cost_record`
  nodes.
- Authorization: dev stance requesting `web_search` (not in dev's set) is
  denied — executor never sees it, denial surfaced to the model and in
  `RunOutcome.ToolCallsDenied`.
- Pause/resume: runner looping on tool-call responses; `PauseStance` returns
  (checkpoint ack), provider call count freezes while paused, `ResumeStance`
  lets the run finish.
- Termination mid-run stops the loop with `Terminated=true`.
- `APIProvider` request/response conversion against a stub
  `provider.Provider` (no network).

### `internal/bench/runner_activation_test.go`
- `Runner.Run` drives ≥1 runner turn: `worker.action.*` events present in
  the bus replay, `cost_record` plumbing feeds `RunResult.CostUSD` /
  `TokensUsed` when the injected mock reports usage.
- `UseFullTemplates`: spawned dev stance's SystemPrompt (captured by the
  mock provider) contains role-template sections rendered from seeded
  mission/task ledger nodes — the A099/A100 end-to-end proof.

### VERIFY commands
```bash
go build ./cmd/r1
go vet ./internal/harness/... ./internal/bench/...
go test ./internal/harness/... ./internal/bench/... ./internal/concern/... -count=1
```

## Acceptance Criteria

1. `StanceRunner.Run` drives `Provider.Chat` with the spawn-built
   SystemPrompt; zero-call dormancy is gone (mock records requests).
2. Unauthorized tool calls never reach the executor; denials are visible in
   events and outcome counts.
3. `PauseStance` no longer times out when a runner is live: pause acks at a
   between-turn checkpoint; resume continues the loop.
4. `TokensUsed`/`CostUSD` accumulate on the session under `h.mu` and surface
   via `InspectStance`; `cost_record` ledger nodes carry the same totals into
   `bench.ComputeMetrics`.
5. `worker.action.started`/`worker.action.completed` events are published
   for every model turn and tool call.
6. `bench.Runner.Run` executes at least one real runner turn per golden
   mission; `TestGoldenBaseline`/`TestGoldenNonRegression` stay green.
7. A production spawn path (`NewWithRoleTemplates`) registers the full
   concern role-template registry; at least one executable path renders role
   templates end-to-end (bench opt-in test).
8. Stale comments in harness.go / session.go referencing a nonexistent
   runner are corrected to reference the real one.

## Implementation Checklist

- [x] `internal/harness/models.go` — `Provider`, `ChatRequest`, `Message`,
      `ToolDef`, `ChatResponse`, `ToolCall` (consumer-side seam).
- [x] `internal/harness/mock_provider.go` — concurrency-safe `MockProvider`
      (response queue, sticky last, optional `ChatFn` override).
- [x] `internal/harness/api_provider.go` — `APIProvider` over
      `internal/provider` clients + `costtrack.ComputeCost`.
- [x] `internal/harness/runner.go` — `StanceRunner`, `RunnerConfig`,
      `ToolExecutor`, `RunOutcome`.
- [x] `internal/harness/harness.go` / `session.go` — comment fixes;
      `NewWithRoleTemplates` production spawn path (A099/A100).
- [x] `internal/bench/runner.go` — `Provider` + `UseFullTemplates` fields;
      Run drives a runner turn; seeded mission/task nodes for the
      full-template path.
- [x] Tests as listed above.
