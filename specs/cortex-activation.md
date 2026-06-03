<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-06-03 -->
<!-- CREATED: 2026-06-03 -->
<!-- DEPENDS_ON: cortex-core (done) -->
<!-- BUILD_ORDER: 3 -->
<!-- REVIEW: Codex (GPT) cross-model PASS after fix loop (HIGH: Start-error cortex leak, C13). Self-review also caught + fixed a two-workspace bug (lobes wrote to a Workspace MidturnNote never drained) via an additive cortex.Config.Workspace + regression test (C9). Full gate: go build + vet ./... + test ./... all exit 0, cortex default-on. -->
<!-- ITEM 11 (CLAUDE.md key-design note) — OPERATOR ACTION: CLAUDE.md is harness-permission-blocked to agents; one-line note handed to operator. -->
<!-- ITEM 7 (per-lobe policy.Cortex gating) — MVP all-4-deterministic-on (spec-sanctioned); full per-lobe gating is additive future work. schema_test default profile preserved. -->
<!-- HOLISTIC: production-readiness/collision/playwright N/A — internal Go agent-loop wiring. -->

# Cortex Activation — Implementation Spec

## Overview

The `internal/cortex/` substrate is fully built (~9k LOC, `specs/cortex-core.md` STATUS: done) and `*cortex.Cortex` already satisfies `agentloop.CortexHook` (static assertion `internal/cortex/cortex.go:488`). But the hook is **never assigned in any live path**: `agentloop.Config.Cortex` is `nil` everywhere except the MCP backend, `cortex.Start()` is never called outside tests, and the chat REPL's `--cortex`/`CortexEnabled` plumbing dead-ends at `cmd/r1/chat_interactive_cmd.go:119`. This spec performs exactly the wiring that `cortex-core.md`'s Overview anticipated ("Operators … wire a `*cortex.Cortex` into the loop's Config; if they don't, behavior is unchanged"). It activates the cortex by constructing a `*cortex.Cortex` with the **4 deterministic lobes only** (memoryrecall, walkeeper, rulecheck, antitrunc — no LLM provider needed for the lobes), assigning it to `agentloop.Config.Cortex` at the native-loop wiring point, bracketing `Start(ctx)`/`Stop()` around the run, threading the same enablement through the chat REPL, and shipping it **DEFAULT-ON with a `--no-cortex` kill-switch**. The load-bearing safety property for default-on: `MidturnNote`'s Round barrier is bounded by `RoundDeadline` (default 2s, `cortex.go:183-185`, enforced by `round.go:133`'s `time.After(deadline)` arm), so an active cortex can never hang the hot loop. Acceptance is proven by **synthetic integration tests** that assert `MidturnNote` produces formatted notes and `PreEndTurnGate` blocks on a critical note — **no live LLM run**.

## Stack & Versions

- **Go 1.25.5** (`go.mod` line 3). Pure standard library + existing internal packages — no new third-party deps.
- **Existing internal deps reused (already imported somewhere in the touched files or their package set)**:
  - `internal/cortex` — `New`, `Start`, `Stop`, `Workspace`, the 4 deterministic-lobe constructors.
  - `internal/cortex/lobes/{memoryrecall,walkeeper,rulecheck,antitrunc}` — deterministic lobe constructors (lifted from `cmd/r1/mcp_serve_runtime.go`).
  - `internal/hub` (`hub.Bus`, `hub.New()`), `internal/bus` (`bus.Bus` — optional, may be nil).
  - `internal/memory` (`memory.NewStore(memory.Config{})`), `internal/wisdom` (`wisdom.NewStore()`) — credential-free in-memory stores for memoryrecall.
  - `internal/provider` (`provider.Provider`) — the real provider `p` already in scope at `native_runner.go:94-110`.
  - `internal/agentloop` (`agentloop.Config.Cortex`, the `CortexHook` interface at `loop.go:42-45`).

## Existing Patterns to Follow

- **Proven 4-deterministic-lobe construction**: `cmd/r1/mcp_serve_runtime.go:118-122` (the `lobeList := []cortex.Lobe{...}` block inside `buildCortexBackend`; the slice literal opens at `:118` and the four `New…Lobe` lines are `:119-122`). Lift verbatim, substituting the real provider `p`.
- **Native-loop optional-hook blocks**: `internal/engine/native_runner.go:375-414` (`PreEndTurnCheckFn`, the `if spec.WorktreeDir != ""` block opening at `:375`), `:446-473` (`MidturnCheckFn`, `var supervisorFn` opening at `:446`). The cortex block goes alongside these, between the `cfg :=` literal (`:348-354`) and `agentloop.New` (`:476`).
- **Flag + config + thread pattern**: `--specexec` precedent in `cmd/r1/main.go` — flag at `:1185` (and a second registration `:1689`), the field `SpecExec bool` at `:116` lives on the **`BuildConfig`** struct (`:99`, NOT `RunConfig`), assignment `SpecExec: *specExec` into the `BuildConfig{…}` literals at `:1465`/`:3415`, consumption `if cfg.SpecExec` at `:535` inside `runBuild(cfg BuildConfig)` (`:138`). NOTE: `SpecExec` does NOT flow into `app.RunConfig` or `workflow.Engine` (specexec is wrapped at the exec-fn layer via `scheduler.WithSpecExec`). The cortex flag, by contrast, MUST reach `RunSpec`, so it needs the full `BuildConfig → app.RunConfig → workflow.Engine → RunSpec` conduit described in Business Logic. Mirror the flag-registration shape of `--specexec`; invert polarity to default-on.
- **`--no-cortex` spelling precedent**: `cmd/r1/mcp.go:108` (`noCortex := fs.Bool("no-cortex", false, ...)`). Reuse the spelling for consistency.
- **Engine config threading**: `workflow.Engine` struct (`internal/workflow/workflow.go:101`) carries config fields; `Engine.buildSpec` (func at `:1667`, `engine.RunSpec{…}` literal at `:1668`) constructs `engine.RunSpec`. Add `CortexEnabled` to both and assign in `buildSpec`. The `workflow.Engine{…}` literal itself is built in **`internal/app/app.go:342`** from `o.cfg` (an `app.RunConfig`), NOT in `cmd/r1/main.go`.
- **Cortex test fixtures**: `internal/cortex/cortex_test.go` — `startStopProvider` (`:20-35`, no-network provider), `midturnLobe` (`:290-325`, deterministic lobe publishing a predictable Note; constructor `newMidturnLobe` at `:302`), `TestMidturnNoteFormat` (`:339-396`), `TestPreEndTurnGateBlocks` (`:525-568`). Lift these shapes for the new engine-level integration test.

## Library Preferences

- No validation lib; reuse existing `cortex.New` error returns (`EventBus`/`Provider` nil → error; `MaxLLMLobes > 8` → panic).
- Bus: share **one** `*hub.Bus` between loop and cortex (see Business Logic). When `n.EventBus == nil`, allocate a local `hub.New()`.

## Data Models

### `engine.RunSpec` (new field, `internal/engine/types.go` RunSpec struct `:173`)
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `CortexEnabled` | `bool` | per-run kill switch; mirrors `BuildConfig.SpecExec` semantics | `false` zero-value; set `true` by callers (see Business Logic — default-on lives at the flag, not the zero-value) |

### `workflow.Engine` (new field, `internal/workflow/workflow.go` Engine struct `:101`)
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `CortexEnabled` | `bool` | threaded `app.RunConfig` → `Engine` → `buildSpec` → `RunSpec`; the `workflow.Engine{…}` literal is built in **`internal/app/app.go:342`** (NOT `cmd/r1/main.go`) from `o.cfg` (an `app.RunConfig`) | set by the `app.go:342` Engine literal (`CortexEnabled: o.cfg.CortexEnabled`) |

### `app.RunConfig` (new field, `internal/app/app.go:50`)
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `CortexEnabled` | `bool` | the orchestrator-level config struct consumed by `app.go:342` (`wf := workflow.Engine{…}`); populated from `BuildConfig.CortexEnabled` in the CLI `app.RunConfig{…}` literal at `cmd/r1/main.go:412`, AND directly from `s.cfg.CortexEnabled` in the REPL `app.RunConfig{…}` literal at `cmd/r1/chat_interactive_cmd.go:316`. This field is the shared junction where the CLI path and the REPL path converge. | propagated from the CLI/REPL flag (default-on lives at the flag, not the zero-value) |

### `BuildConfig` (new field, `cmd/r1/main.go:99` — the struct holding `SpecExec bool` at `:116`; NOTE: this struct is named `BuildConfig`, NOT `RunConfig`)
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `CortexEnabled` | `bool` | mirrors `BuildConfig.SpecExec` (`:116`); assigned from `--cortex`(default true) ∧ ¬`--no-cortex` in each `BuildConfig{…}` literal (`:1448`, `:3402`, `:5118`); consumed via `runBuild(cfg BuildConfig)` (`:138`), which builds the `app.RunConfig{…}` at `:412` | `true` (default-on) |

### `chatInteractiveConfig.CortexEnabled` (existing, `cmd/r1/chat_interactive_cmd.go:55`)
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `CortexEnabled` | `bool` | flip flag default `false → true`; thread into the REPL `app.RunConfig{…}` literal at `:316` so it reaches RunSpec | `true` (default-on) |

## Business Logic

### Construct the deterministic cortex (native_runner.go, between `:354` and `:476`)
1. Validate gate: only build when `spec.CortexEnabled` is true.
2. Resolve the shared bus: `eventBus := n.EventBus; if eventBus == nil { eventBus = hub.New() }`. Pass this same `eventBus` to `cortex.New` AND keep using it for `loop.SetEventBus` (`:479-481`) so `EventModelPostCall` (`loop.go:540`) reaches the cortex BudgetTracker.
3. Build a **shell** cortex to get a stable `*cortex.Workspace` pointer (the "shell + live" pattern from `mcp_serve_runtime.go:96-140`):
   `shell, _ := cortex.New(cortex.Config{SessionID: spec.SessionID, EventBus: eventBus, Provider: p, RoundDeadline: <default 2s — leave 0 to inherit cortex.go:183-185, or set explicitly>})`; `ws := shell.Workspace()`.
4. Construct the 4 deterministic lobes against `ws` (lift `mcp_serve_runtime.go:112-122` verbatim — `memStore`/`wisStore` at `:112`/`:116`, the lobe slice at `:118-122` — substituting the real `p` only as the cortex Provider; the lobes themselves take no provider):
   - `memStore, _ := memory.NewStore(memory.Config{})` ; `wisStore := wisdom.NewStore()`
   - `memoryrecall.NewMemoryRecallLobe(ws, memStore, wisStore, eventBus)`
   - `walkeeper.NewWALKeeperLobe(eventBus, durableBus, ws, walkeeper.WALFraming{})` (durableBus may be `nil` — MVP passes nil; lobes degrade to no-ops)
   - `rulecheck.NewRuleCheckLobe(durableBus, ws)`
   - `antitrunclobe.NewAntiTruncLobe(ws, "", "")` (plan/spec scanning additive; conversation-history regex still fires)
5. Build the **live** cortex holding the LobeRunners:
   `live, _ := cortex.New(cortex.Config{SessionID: spec.SessionID, EventBus: eventBus, Provider: p, Lobes: lobeList, PreWarmInterval: time.Hour, PreWarmSystemPrompt: systemPrompt, PreWarmTools: toolDefs, RoundDeadline: <same as shell>})`.
   - `PreWarmInterval: time.Hour` suppresses the pre-warm pump's recurring cost in MVP (matches `cortex_test.go:352`); a real `p` is passed only to satisfy the non-nil requirement.
6. `cfg.Cortex = live` (BEFORE `agentloop.New` at `:476`).
7. `if err := live.Start(ctx); err != nil { slog.Warn(...); /* proceed without cortex — never abort the run */ }` and `defer live.Stop(context.Background())` (fresh ctx so cancel doesn't truncate the bounded-10s shutdown, `cortex.go:39`).

### Kill-switch semantics
- When `spec.CortexEnabled == false`: skip the entire construction above; `cfg.Cortex` stays `nil`; `defaults()` composition is a no-op (`loop.go:262`) → behavior **byte-identical to today**.

### Flag → BuildConfig → app.RunConfig → Engine → RunSpec threading (CLI path)
The `--specexec` precedent struct at `cmd/r1/main.go:116` is named **`BuildConfig`** (`:99`), NOT `RunConfig`; the real `app.RunConfig` type lives in **`internal/app/app.go:50`** and `SpecExec` does NOT flow through it (specexec is wrapped separately at the exec-fn layer). The cortex flag therefore needs the FULL four-hop conduit:
- `cmd/r1/main.go`: add `--cortex` (default `true`) and `--no-cortex` (default `false`); set `BuildConfig.CortexEnabled = *cortex && !*noCortex` in each `BuildConfig{…}` literal (`:1448`, `:3402`, `:5118`).
- `cmd/r1/main.go:412` (the `app.RunConfig{…}` literal inside `runBuild`): map `CortexEnabled: cfg.CortexEnabled` from `BuildConfig` into a new `app.RunConfig.CortexEnabled` field (`internal/app/app.go:50`).
- `internal/app/app.go:342`: set `CortexEnabled: o.cfg.CortexEnabled` in the `workflow.Engine{…}` literal (`wf := workflow.Engine{…}`).
- `internal/workflow/workflow.go`: `Engine.buildSpec` (func `:1667`, literal `:1668`) sets `CortexEnabled: e.CortexEnabled` into the returned `engine.RunSpec{…}`.

### Chat-REPL path (separate conduit, same `app.RunConfig` junction)
- `cmd/r1/chat_interactive_cmd.go`: flip the `--cortex` flag default at `:92` from `false` to `true`; the assignment at `:119` already populates `chatInteractiveConfig.CortexEnabled` (`:55`). The REPL's `runWorkflow` (`:310`) builds its own `app.RunConfig{…}` literal at **`:316`** and calls `app.New(...).Run()` — it does NOT construct a `workflow.Engine` directly. Add `CortexEnabled: s.cfg.CortexEnabled` to that `app.RunConfig{…}` literal at `:316` so the REPL flag reaches `app.RunConfig.CortexEnabled` → `app.go:342` Engine → `RunSpec` (today the field dead-ends at `:119`).

## Error Handling
| Failure | Strategy | User Sees |
|---------|----------|-----------|
| `cortex.New` returns error (nil EventBus/Provider) | Should not happen — eventBus is resolved non-nil and `p` is always in scope. Log WARN, proceed with `cfg.Cortex = nil`. | Unchanged loop behavior |
| `live.Start(ctx)` returns error | Log WARN, do NOT abort the run; cortex stays inert (runners never tick → `MidturnNote` returns ""). | Unchanged loop behavior |
| Slow/wedged lobe in a round | Bounded by `RoundDeadline`; `round.go:133` `time.After` fires; `MidturnNote` logs WARN and drains partial notes (`cortex.go:561-568`). Late notes land on a future round. | ≤ `RoundDeadline` (2s) added latency, never a stall |
| `live.Stop` exceeds budget | Bounded by `cortexStopTimeout = 10s` (`cortex.go:39`); Stop returns regardless. | No hang at run teardown |

## Boundaries — What NOT To Do
- Do **NOT** enable LLM lobes or any `--lobes=all` / "all" mode. Register **only** the 4 deterministic lobes {memoryrecall, walkeeper, rulecheck, antitrunc}. The LLM lobes (planupdate `KindLLM`, clarifyq `KindLLM`, memorycurator `KindLLM`) require a provider and are explicitly excluded.
- Do **NOT** pass `stubProvider{}` (the `mcp_serve_runtime.go:35-48` error-on-every-call stub) to the activated cortex — pass the real `p` already in scope at `native_runner.go:94-110`. The stub spams WARN on every pre-warm fire and warms nothing.
- Do **NOT** let the midturn barrier exceed `RoundDeadline`. Do not remove or raise the `time.After(deadline)` arm (`round.go:133`); do not call `Round.Wait` with a zero/unbounded deadline.
- Do **NOT** let default-on hang or measurably slow the loop. Worst-case added latency per turn = `RoundDeadline` (default 2s); `PreEndTurnGate` does no waiting (pure read of `workspace.UnresolvedCritical()`, `cortex.go:619-632`).
- Do **NOT** wire `Cortex.Router()` / mid-turn input routing — the `CortexHook` interface has only `MidturnNote` + `PreEndTurnGate` (`loop.go:42-45`); the Router is not auto-wired and stays dormant (safe). It is out of scope (see Out of Scope).
- Do **NOT** invoke the cortex hooks directly inside the run loop — `defaults()` (`loop.go:242-301`) composes `cfg.Cortex` into `MidturnCheckFn`/`PreEndTurnCheckFn`; assigning `cfg.Cortex` before `agentloop.New` (`:476`) is the only mechanically-required step.
- Do **NOT** regress `internal/config/schema_test.go:39-56` (the policy-block default profile: deterministic-on, LLM-off). That test is consistent with this work — preserve it as-is.
- Do **NOT** require a live LLM in any test added by this spec. All acceptance tests use a no-network fake provider (`startStopProvider` shape, `cortex_test.go:20-35`).

## Testing

### Engine-level synthetic integration test — `internal/engine/native_cortex_test.go` (NEW)
Follow `cortex_test.go` patterns; **no live LLM** — use a fake `provider.Provider` returning canned responses.

- [ ] **PreEndTurnGate blocks end_turn on a critical note** (composed path): Build `cortex.New(cortex.Config{EventBus: hub.New(), Provider: &fakeProvider{}, Lobes: []cortex.Lobe{criticalNoteLobe}, PreWarmInterval: time.Hour, RoundDeadline: 60*time.Second})` where `criticalNoteLobe` publishes a `SevCritical` Note on first tick (model after `midturnLobe`, `cortex_test.go:290-325`, but with `Severity: SevCritical`). `c.Start(ctx)`. Build `agentloop.Config{Cortex: c, MaxTurns: 2}` + a fake provider returning an immediate `end_turn`. Run `agentloop.New(fakeProvider, cfg, nil, handler).Run(ctx, "go")`. Assert the loop did NOT terminate on the first `end_turn` — the composed `PreEndTurnCheckFn` (`loop.go:283-291`) sees the non-empty critical block and injects the `[BUILD VERIFICATION FAILED …]` continuation (`loop.go:597-604`). Then publish a resolving Note (`ws.Publish(Note{Resolves: critID})`) and assert the next `end_turn` is honored. VERIFY: `go test ./internal/engine/... -run TestNativeCortex_PreEndTurnBlocks`.
- [ ] **MidturnNote output reaches the next user message** (formatted notes from deterministic lobes): Give the loop one `tool_use` turn then `end_turn`. Assert the next user message contains a `[SUPERVISOR NOTE] [CORTEX NOTES — round 1]` block (`loop.go:684-689` + `cortex.go:595`). Recover note IDs/titles via `ws.Snapshot()` and assert both lobe IDs + titles appear (pattern: `TestMidturnNoteFormat`, `cortex_test.go:373-384`). VERIFY: `go test ./internal/engine/... -run TestNativeCortex_MidturnReachesMessage`.
- [ ] **PreEndTurnGate returns non-empty on a critical note** (unit-level, asserting formatted output): publish `Note{Severity: SevCritical, LobeID, Title}` to `c.Workspace()`, recover the ID via `ws.Snapshot()`, assert `c.PreEndTurnGate(nil)` is non-empty and contains `"CRITICAL"` + LobeID + Title (pattern: `TestPreEndTurnGateBlocks`, `cortex_test.go:525-568`); publish `Note{Resolves: critID}` and assert it flips back to `""`. VERIFY: `go test ./internal/engine/... -run TestNativeCortex_PreEndTurnGateFormat`.
- [ ] **MidturnNote returns formatted notes** (assert prefix + content): construct as above with a deterministic lobe publishing a `SevInfo`/`SevWarning` Note; `c.Start(ctx)`; `out := c.MidturnNote([]agentloop.Message{}, 0)`; assert `strings.HasPrefix(out, "[CORTEX NOTES — round 1]\n")` and the lobe ID + title appear (pattern: `cortex_test.go:369-384`). VERIFY: `go test ./internal/engine/... -run TestNativeCortex_MidturnFormat`.
- [ ] **Kill-switch leaves the loop byte-identical**: with `RunSpec.CortexEnabled = false`, assert `native_runner` skips cortex construction → `cfg.Cortex == nil` → `defaults()` composition is a no-op (`loop.go:262`). Drive a one-turn `end_turn` loop and assert it ends on the first `end_turn` (no injected continuation, no `[CORTEX NOTES]` block in any message). VERIFY: `go test ./internal/engine/... -run TestNativeCortex_KillSwitch`.

### Safety property test — `internal/engine/native_cortex_test.go` (or `internal/cortex/`)
- [ ] **Bounded midturn wait**: register a lobe whose `Run` blocks indefinitely (never calls `Done` for its round). With `RoundDeadline: 200*time.Millisecond`, assert `c.MidturnNote(...)` returns within, say, `2*RoundDeadline` wall-clock (use `time.Now()` bracket) and does NOT hang — proving the `round.go:133` `time.After(deadline)` upper bound. Assert the return is `""` or a partial block, never a deadlock. VERIFY: `go test ./internal/engine/... -run TestNativeCortex_BoundedMidturn`.

### Flag-parse tests — `cmd/r1/main_test.go`
- [ ] `--cortex` default parses to `BuildConfig.CortexEnabled == true`. VERIFY: `go test ./cmd/r1 -run TestCortexFlag_Default`.
- [ ] `--no-cortex` parses to `BuildConfig.CortexEnabled == false`. VERIFY: `go test ./cmd/r1 -run TestCortexFlag_NoCortex`.
- [ ] `--cortex=false` parses to `BuildConfig.CortexEnabled == false`. VERIFY: `go test ./cmd/r1 -run TestCortexFlag_Explicit`.

### Regression
- [ ] `internal/config/schema_test.go:39-56` still passes unchanged (deterministic-on, LLM-off policy profile). VERIFY: `go test ./internal/config/...`.
- [ ] `internal/agentloop/loop_test.go`, `pre_end_turn_test.go` pass unchanged (cortex-nil path unaffected). VERIFY: `go test ./internal/agentloop/...`.

## Acceptance Criteria
- WHEN `RunSpec.CortexEnabled` is true THE SYSTEM SHALL construct a `*cortex.Cortex` with exactly the 4 deterministic lobes {memoryrecall, walkeeper, rulecheck, antitrunc}, assign it to `agentloop.Config.Cortex` before `agentloop.New` (`native_runner.go:476`), and call `Start(ctx)`/`defer Stop()` — verified by the synthetic integration tests (no live LLM).
- WHEN an unresolved `SevCritical` Note exists THE SYSTEM SHALL cause the composed `PreEndTurnCheckFn` to return non-empty and refuse `end_turn` (inject `[BUILD VERIFICATION FAILED …]` continuation, `loop.go:597-604`) — verified by the PreEndTurnGate-blocks test.
- WHEN a deterministic lobe publishes Notes during a round THE SYSTEM SHALL have `MidturnNote` return a `[CORTEX NOTES — round N]` block that reaches the next user message as a `[SUPERVISOR NOTE]` (`loop.go:684-689`) — verified by the MidturnNote test.
- WHEN every lobe in a round is wedged THE SYSTEM SHALL have `MidturnNote` return within `RoundDeadline` (default 2s, `cortex.go:183-185` / `round.go:133`) and never hang the loop — verified by the bounded-wait test.
- WHEN `RunSpec.CortexEnabled` is false (`--no-cortex` or `--cortex=false`) THE SYSTEM SHALL leave `cfg.Cortex == nil` and behave byte-identically to today — verified by the kill-switch test.
- WHEN `cortex.Start` returns an error THE SYSTEM SHALL log WARN and proceed with the run (cortex inert, `MidturnNote` returns "") — never abort the coding run.

## Implementation Checklist

1. [ ] **Add `RunSpec.CortexEnabled bool`.** In `internal/engine/types.go`, add `CortexEnabled bool` to the `RunSpec` struct (`:173`; place it near `WorktreeDir` at `:195`), with a doc comment: "CortexEnabled, when true, wires the 4 deterministic cortex lobes into the native agentloop via `agentloop.Config.Cortex`. Mirrors BuildConfig.SpecExec; default-on lives at the CLI flag (`--cortex`), not the zero-value." VERIFY: `go build ./internal/engine/...`.

2. [ ] **Construct + assign + bracket the cortex in `native_runner.Run`.** In `internal/engine/native_runner.go`, between the `cfg := agentloop.Config{...}` literal (`:348-354`) and `loop := agentloop.New(p, cfg, toolDefs, handler)` (`:476`), add a `if spec.CortexEnabled { … }` block that: (a) resolves `eventBus := n.EventBus; if eventBus == nil { eventBus = hub.New() }`; (b) builds a shell cortex `shell, err := cortex.New(cortex.Config{SessionID: spec.SessionID, EventBus: eventBus, Provider: p})` and `ws := shell.Workspace()`; (c) builds `memStore, _ := memory.NewStore(memory.Config{})`, `wisStore := wisdom.NewStore()`; (d) builds the lobe list **lifted verbatim from `cmd/r1/mcp_serve_runtime.go:118-122`**: `[]cortex.Lobe{ memoryrecall.NewMemoryRecallLobe(ws, memStore, wisStore, eventBus), walkeeper.NewWALKeeperLobe(eventBus, nil, ws, walkeeper.WALFraming{}), rulecheck.NewRuleCheckLobe(nil, ws), antitrunclobe.NewAntiTruncLobe(ws, "", "") }`; (e) builds the live cortex `live, err := cortex.New(cortex.Config{SessionID: spec.SessionID, EventBus: eventBus, Provider: p, Lobes: lobeList, PreWarmInterval: time.Hour, PreWarmSystemPrompt: systemPrompt, PreWarmTools: toolDefs})`; (f) on any `cortex.New` error, `slog.Warn(...)` and skip (leave `cfg.Cortex` nil); (g) `cfg.Cortex = live`; `if err := live.Start(ctx); err != nil { slog.Warn(...) }`; `defer live.Stop(context.Background())`. Add imports: `internal/cortex`, the 4 lobe packages (alias `antitrunclobe "…/lobes/antitrunc"` per `mcp_serve_runtime.go:22`), `internal/memory`, `internal/wisdom`, `internal/hub` if not present. Use the existing `eventBus` for `loop.SetEventBus` at `:479-481` (replace the `n.EventBus` reference there with the resolved `eventBus`). Pattern: `mcp_serve_runtime.go:96-140`; placement: alongside `:375-414`/`:446-473`. VERIFY: `go build ./internal/engine/... && go vet ./internal/engine/...`.

3. [ ] **Thread `CortexEnabled` through `workflow.Engine` → `RunSpec`.** In `internal/workflow/workflow.go`: add `CortexEnabled bool` to the `Engine` struct (`:101`); in `Engine.buildSpec` (func `:1667`, `engine.RunSpec{…}` literal `:1668`) set `CortexEnabled: e.CortexEnabled` in the returned `engine.RunSpec{…}` literal. VERIFY: `go build ./internal/workflow/...`.

4. [ ] **Add `app.RunConfig.CortexEnabled` + set it on the Engine literal.** In `internal/app/app.go`: add `CortexEnabled bool` to the `RunConfig` struct (`:50`); in the `wf := workflow.Engine{…}` literal (`:342`) set `CortexEnabled: o.cfg.CortexEnabled`. (This is the shared junction that BOTH the CLI path (item 5) and the REPL path (item 6) feed into.) VERIFY: `go build ./internal/app/...`.

5. [ ] **Add `--cortex`/`--no-cortex` flags + `BuildConfig.CortexEnabled` in `cmd/r1/main.go`, DEFAULT-ON, and map it into `app.RunConfig`.** Mirror `--specexec` (flag `:1185`/`:1689`, field `:116`, assignment `:1465`/`:3415`, consumption `:535`) — but note the struct at `:116` is **`BuildConfig`** (`:99`), NOT `RunConfig`. (a) Add field `CortexEnabled bool // enable deterministic cortex lobes (default on)` to the `BuildConfig` struct near `:116`. (b) Register `cortexEnabled := fs.Bool("cortex", true, "Enable parallel-cognition deterministic lobes (default on; --cortex=false or --no-cortex to disable)")` and `noCortex := fs.Bool("no-cortex", false, "Disable the cortex lobes for this run")` in the same flag set(s) where `--specexec` is registered (`:1185`, `:1689`). (c) Assign `CortexEnabled: *cortexEnabled && !*noCortex` in each `BuildConfig{…}` literal (`:1448`, `:3402`, `:5118`). (d) In the `app.RunConfig{…}` literal at `:412` (inside `runBuild`), map `CortexEnabled: cfg.CortexEnabled` from `BuildConfig` into the new `app.RunConfig.CortexEnabled` field (item 4). The `--no-cortex` spelling mirrors `cmd/r1/mcp.go:108`. VERIFY: `go build ./cmd/r1`.

6. [ ] **Activate the dead chat-REPL plumbing, DEFAULT-ON.** In `cmd/r1/chat_interactive_cmd.go`: (a) flip the `--cortex` flag default at `:92` from `false` to `true` (update help text to "default on; --cortex=false to disable"); the assignment at `:119` already populates `chatInteractiveConfig.CortexEnabled` (`:55`). (b) In `runWorkflow` (`:310`), add `CortexEnabled: s.cfg.CortexEnabled` to the `app.RunConfig{…}` literal at **`:316`** (the REPL builds `app.New(app.RunConfig{…})` directly — it does NOT construct a `workflow.Engine`), so the flag reaches `app.RunConfig.CortexEnabled` (item 4) → `app.go:342` Engine → `RunSpec`. (c) Update the doc comment at `:52-55` to drop "Default off; spec 2 owns…" and state "Default on; wired by cortex-activation (this spec)." VERIFY: `go build ./cmd/r1`.

7. [ ] **Consume the existing `policy.Cortex` per-lobe flags (parsed-but-dead).** The policy block is parsed at `internal/config/policy.go:442` (call) / `:446` (assignment `p.Cortex = cortex`) via `parseCortexBlock` (`internal/config/schema.go:81-87`) into `Policy.Cortex.Lobes.*.Enabled` (`schema.go:32-39`) but consumed nowhere. At the §2 wiring point, gate each deterministic lobe's inclusion on its flag when the policy block is present and reaches `native_runner` (thread the relevant `LobeFlags` through `RunSpec` if not already available — MVP may default all-on when the policy block is absent, matching `schema_test.go:39-56`). Preserve the `schema_test.go:39-56` default profile (MemoryRecall/WALKeeper/RuleCheck enabled, LLM lobes disabled) — do not regress it. VERIFY: `go test ./internal/config/...`.

8. [ ] **Safety: document + assert the `RoundDeadline` bound.** Add a code comment at the §2 wiring point citing the default-on safety property: `MidturnNote` waits via `c.round.Wait(waitCtx, roundID, c.cfg.RoundDeadline)` (`cortex.go:557`), `RoundDeadline` defaults to 2s (`cortex.go:183-185`), and the wait always returns within the deadline via the `time.After(deadline)` arm (`round.go:133`); a slow round degrades to a partial/empty note block (`cortex.go:561-568`), never a stall; `PreEndTurnGate` does no waiting (`cortex.go:619-632`). Add the bounded-wait synthetic test from the Testing section (lobe that never finishes its round; assert `MidturnNote` returns within `2*RoundDeadline`). VERIFY: `go test ./internal/engine/... -run TestNativeCortex_BoundedMidturn`.

9. [ ] **Write the synthetic engine-level integration test `internal/engine/native_cortex_test.go`.** Implement all 5 Testing-section cases (PreEndTurnGate-blocks-then-resolves, MidturnNote-reaches-next-user-message, PreEndTurnGate-formatted-output, MidturnNote-formatted-notes, kill-switch-byte-identical) using a no-network fake `provider.Provider` (pattern: `startStopProvider`, `cortex_test.go:20-35`) and a deterministic critical-note lobe (pattern: `midturnLobe`, `cortex_test.go:290-325`, constructor `newMidturnLobe` `:302`, with `SevCritical`). Use `PreWarmInterval: time.Hour` and `RoundDeadline: 60*time.Second` in tests to remove flakiness (per `cortex_test.go:352`). NO live LLM. VERIFY: `go test ./internal/engine/... -run TestNativeCortex`.

10. [ ] **Add flag-parse tests in `cmd/r1/main_test.go`.** Assert `--cortex` default → `BuildConfig.CortexEnabled == true`; `--no-cortex` → false; `--cortex=false` → false (pattern: any existing `BuildConfig` flag-parse test for `--specexec`). VERIFY: `go test ./cmd/r1 -run TestCortexFlag`.

11. [ ] **Update `CLAUDE.md` package-map / key-design notes (own commit per repo doc rule).** Add a one-line key-design-decision noting the cortex is now activated default-on with a `--no-cortex` kill-switch and the `RoundDeadline` safety bound. Keep it factual. VERIFY: `go vet ./...`.

12. [ ] **Full-gate verification.** Run the CI gate end-to-end: `go build ./cmd/r1 && go test ./internal/cortex/... ./internal/engine/... ./internal/config/... ./internal/app/... ./internal/workflow/... ./cmd/r1 && go vet ./...`. All green, no live-LLM dependency, kill-switch path byte-identical to pre-change behavior.

## Out of Scope (explicit)
- **LLM lobes** (planupdate, clarifyq, memorycurator) and any `--lobes=all` mode — require a provider; excluded by design.
- **Router / mid-turn input routing** (`Cortex.Router()`) — not part of the `CortexHook` interface (`loop.go:42-45`); stays dormant. If ever in scope it must be wired at the REPL layer via `EndTurnContinuation` (`loop.go:187`, invoked `loop.go:630`), not here.
- **Durable bus wiring** for walkeeper/rulecheck — MVP passes `nil` (`bus.Bus` optional; lobes degrade to no-ops). A future spec may pass `bus.New(busDir)`.
- **AntiTrunc plan/spec scanning** — MVP passes `"", ""` to `NewAntiTruncLobe`; conversation-history regex scanning still fires. Plan/spec glob wiring is additive future work.
- **Pre-warm cache optimization** — MVP suppresses the pump (`PreWarmInterval: time.Hour`). Cache-aligned pre-warm (real interval + matching system/tools) is a future tuning pass.
