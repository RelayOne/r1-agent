# Spec Review — cortex-activation

Spec under review: `specs/cortex-activation.md`
Source map (ground truth): `specs/research/raw/RT-cortex-activation.md`
Reviewer model: opus, effort: high
Date: 2026-06-03

## Result: READY (10/10 failure modes PASS after repair)

Critical issues found: **3** — all build-breaking citation/threading defects. **3 fixed inline.** No scope weakened; checklist grew 11 → 12 items to disentangle a bundled threading hop.

---

## Per-failure-mode verdict (10 documented modes)

### 1. Self-contained checklist items — PASS (after fix)
Each item now names its file, struct, and exact line. The previously-bundled threading work (one item conflating `BuildConfig` + the missing `app.RunConfig` hop + a wrong-file Engine construction) was split into three self-contained items (4 = `app.RunConfig` field + Engine literal; 5 = CLI flags + `BuildConfig` + mapping into `app.RunConfig`; 6 = REPL conduit). A build subagent can now implement each in isolation.

### 2. Library ambiguity — PASS
No generic "the validation library" references. Every package is named explicitly (`internal/cortex`, `internal/memory`, `internal/wisdom`, `internal/hub`, the four lobe packages with the `antitrunclobe` alias). Library Preferences explicitly says "No validation lib."

### 3. Pattern references — PASS
Every code-creating item points to a real existing file to follow: `mcp_serve_runtime.go:118-122` (lobe construction, verified), `cortex_test.go` fixtures (verified), `native_runner.go:375-414`/`:446-473` optional-hook blocks (verified), `--specexec` precedent (verified, with the struct-name correction). All referenced files exist.

### 4. Missing error responses — PASS
Not an HTTP-endpoint spec, but the Error Handling table enumerates all four failure surfaces (`cortex.New` error, `Start` error, wedged lobe, `Stop` over budget) with concrete strategy + user-visible effect. Each maps to a real bounded mechanism (`RoundDeadline` 2s, `cortexStopTimeout` 10s).

### 5. Vague acceptance criteria — PASS
All six Acceptance Criteria use concrete, testable values (exact 4 lobes, `2s` deadline, `byte-identically`, exact injected strings `[BUILD VERIFICATION FAILED …]` / `[CORTEX NOTES — round N]`). No "appropriate/proper/handle correctly" hedging.

### 6. Missing boundaries — PASS
Boundaries section is explicit and strong: no LLM lobes, no `--lobes=all`, no stub provider, no unbounded deadline, no direct hook invocation, no Router wiring, do-not-regress `schema_test.go:39-56`, no live LLM in tests. Each checklist item names the files it touches.

### 7. Dependency ordering — PASS (after fix)
Order is now correct and acyclic: item 1 (`RunSpec` field) → item 2 (native_runner consumes `spec.CortexEnabled`) → item 3 (`Engine`→`buildSpec`→`RunSpec`) → item 4 (`app.RunConfig` field + Engine literal) → items 5/6 (CLI + REPL feed `app.RunConfig`) → items 8-10 (safety/tests) → 11-12 (docs/gate). The fix inserted the missing `app.RunConfig` hop in the right position (item 4, before its two feeders).

### 8. Verification specificity — PASS (after fix)
Every Testing-section item and every checklist item now carries an explicit `VERIFY:` command. The original Testing section listed test cases without per-case VERIFY commands; each now has a named `-run` target (e.g. `go test ./internal/engine/... -run TestNativeCortex_PreEndTurnBlocks`).

### 9. Unresearched assumptions — PASS
Backed by the RT source map. Go version pinned (1.25.5, go.mod:3). The one load-bearing safety assumption (bounded `RoundDeadline`) is verified against real code (`round.go:133`, `cortex.go:183-185`). No unconfirmed library versions or API formats.

### 10. Missing specific values — PASS
Concrete values throughout: `RoundDeadline` default 2s, `cortexStopTimeout` 10s, `PreWarmInterval: time.Hour`, test `RoundDeadline: 60*time.Second`, bounded-wait `200ms`/`2×`. Default-on polarity stated explicitly with the `--no-cortex`/`--cortex=false` kill-switch.

---

## Citations checked (Grep/Read against /home/eric/repos/r1-agent)

ALL of the following RESOLVE to real code (verified):

| Citation | Status |
|---|---|
| `cortex.go:488` static assertion `var _ agentloop.CortexHook = (*Cortex)(nil)` | OK |
| `loop.go:42-45` `CortexHook` interface (MidturnNote + PreEndTurnGate) | OK |
| `loop.go:151` `Cortex CortexHook` field | OK (exact) |
| `loop.go:242-301` defaults(), `:262` `if c.Cortex != nil`, `:283-291` PreEndTurn compose | OK |
| `loop.go:430-431` `New` calls `cfg.defaults()` | OK |
| `loop.go:540` EventModelPostCall emit (eventBus != nil) | OK |
| `loop.go:592-604` PreEndTurnCheckFn invocation + `[BUILD VERIFICATION FAILED …]` | OK |
| `loop.go:684-689` MidturnCheckFn → `[SUPERVISOR NOTE]` block | OK |
| `loop.go:187` EndTurnContinuation / `:630` invocation | OK |
| `cortex.go:167` `New(cfg Config) (*Cortex, error)` | OK |
| `cortex.go:183-185` RoundDeadline default 2s | OK |
| `cortex.go:39` cortexStopTimeout = 10s | OK |
| `cortex.go:549-556` waitCtx fallback; `:557` round.Wait; `:561-568` WARN+drain; `:595` format; `:619-632` PreEndTurnGate | OK |
| `round.go:111-138` Wait, `:133` `time.After(deadline)` arm | OK |
| `native_runner.go:348-354` cfg literal; `:476` agentloop.New; `:375-414`/`:446-473` hook blocks; `:94-110` provider `p` | OK |
| `mcp_serve_runtime.go` lobe slice `:118-122` (was cited `:118-123` — corrected); `memStore` `:112`, `wisStore` `:116`; stub `:35-48`; alias `:22` | OK (range tightened) |
| `chat_interactive_cmd.go:55` field, `:92` flag, `:119` assignment | OK |
| `cortex_test.go` `startStopProvider` `:20-35`, `midturnLobe` `:290` (was `:285` — corrected), `TestMidturnNoteFormat` `:339`, `TestPreEndTurnGateBlocks` `:525` | OK (start line corrected) |
| `schema.go:81-87` parseCortexBlock, `:32-39` LobeFlags, `:24-26` CortexConfig | OK |
| `policy.go:442` parseCortexBlock call / `:446` `p.Cortex = cortex` assignment | OK (clarified call vs assignment) |
| `schema_test.go:39-56` deterministic-on/LLM-off profile | OK |
| `workflow.go:101` Engine struct, `:1667` buildSpec func, `:1668` RunSpec literal | OK (was `:1666-1680` — tightened) |
| `mcp.go:108` `no-cortex` flag spelling | OK |
| `types.go:173` RunSpec struct, `:195` WorktreeDir, `:316` SessionID | OK |
| `main.go:116` SpecExec field, `:1185`/`:1689` flag, `:1465`/`:3415` assignment, `:535` consumption, `:138` runBuild, `:412` app.RunConfig literal | OK — BUT struct mislabeled (see CRITICAL-1) |

### STALE / WRONG citations found (all CRITICAL — would misdirect /build)

**CRITICAL-1 — `BuildConfig` mislabeled as `RunConfig` throughout.**
The struct holding `SpecExec bool` at `cmd/r1/main.go:116` is `BuildConfig` (defined `:99`), NOT `RunConfig`. The real `RunConfig` type is `app.RunConfig` in `internal/app/app.go:50` and is a different struct. The spec's Data Models, Existing Patterns, Business Logic, and checklist items 1/4/9 all said "RunConfig near main.go:116". A build agent would add the field to the wrong struct (or fail to find it). FIXED: relabeled to `BuildConfig` everywhere, with an explicit "NOTE: named BuildConfig, NOT RunConfig" callout.

**CRITICAL-2 — the `app.RunConfig` threading hop was entirely omitted.**
The spec described a 3-hop path `RunConfig → workflow.Engine → RunSpec`, but the real conduit is 4 hops: `BuildConfig` (main.go) → `app.RunConfig` (app.go:50) → `workflow.Engine` (built at app.go:342 from `o.cfg`) → `RunSpec`. `SpecExec` does NOT flow through `app.RunConfig` (it is wrapped separately via `scheduler.WithSpecExec`), so the `--specexec` precedent is misleading for this purpose. Without the `app.RunConfig.CortexEnabled` field the chain is broken and `Engine.CortexEnabled` is always false. FIXED: added the `app.RunConfig` Data-Models row, a dedicated checklist item 4 (add field + set on the `app.go:342` Engine literal), and the explicit 4-hop CLI-path description.

**CRITICAL-3 — Engine construction pointed at the wrong file; REPL conduit wrong.**
Checklist item 4 said "thread into the `workflow.Engine{…}` construction" in `cmd/r1/main.go` — but `workflow.Engine{}` is NOT constructed in main.go; it is built at `internal/app/app.go:342` (and `orchestrate/orchestrator.go:404`). Item 5 said the REPL threads through "`runWorkflow` → `workflow.Engine` construction (`:129-134`)" — but the REPL's `runWorkflow` (`:310`) builds `app.New(app.RunConfig{…})` at `:316` and never constructs a `workflow.Engine` at all (`:129-134` is unrelated session-struct setup). FIXED: corrected the Engine literal site to `app.go:342`, corrected the REPL to add `CortexEnabled: s.cfg.CortexEnabled` to the `app.RunConfig{…}` literal at `chat_interactive_cmd.go:316`, and documented that the CLI and REPL paths converge on the single `app.RunConfig.CortexEnabled` junction.

### Minor citation tightenings (non-critical, fixed for precision)
- `mcp_serve_runtime.go:118-123` → `:118-122` (slice closing brace is `:123`; the four lobe lines are `:119-122`).
- `cortex_test.go:285-325` (midturnLobe) → `:290-325` (struct opens at `:290`, constructor `newMidturnLobe` at `:302`).
- `workflow.go buildSpec :1666-1680` → func `:1667`, literal `:1668`.
- `policy.go:446` clarified as the assignment line (`p.Cortex = cortex`); the `parseCortexBlock` call is `:442`.

---

## Targeted confirmations (task DO step 2)

- **Real provider for the Router, NOT a stub** — CONFIRMED. Boundaries + Business Logic mandate passing the real `p` (native_runner.go:94-110) and explicitly forbid `stubProvider{}` (mcp_serve_runtime.go:35-48). The Router stays dormant because `CortexHook` exposes only MidturnNote/PreEndTurnGate (verified loop.go:42-45); `Cortex.Router()` is never auto-invoked.
- **Deterministic lobes ONLY, no 'all'/LLM mode** — CONFIRMED. Exactly {memoryrecall, walkeeper, rulecheck, antitrunc}, all `KindDeterministic` (verified against lobe `Kind()` methods via RT map). LLM lobes (planupdate/clarifyq/memorycurator, all `KindLLM`) explicitly excluded in Boundaries + Out of Scope. No `--lobes=all` path.
- **Default-on + `--no-cortex` kill-switch** — CONFIRMED. `--cortex` defaults `true`; `--no-cortex` (spelling per mcp.go:108) and `--cortex=false` both disable; kill-switch path leaves `cfg.Cortex == nil` → byte-identical behavior (loop.go:262 no-op composition, verified).
- **Bounded-deadline safety property is explicit** — CONFIRMED. Stated in Overview, Boundaries, Error Handling, Acceptance, and checklist item 8, all citing the real bound: `round.go:133` `time.After(deadline)`, `cortex.go:183-185` 2s default, `PreEndTurnGate` does no waiting (cortex.go:619-632, verified — pure read of `UnresolvedCritical()`).
- **Synthetic-test acceptance needs NO live LLM** — CONFIRMED. All tests use the no-network `startStopProvider`/`fakeProvider` shape (cortex_test.go:20-35, verified) with `PreWarmInterval: time.Hour` to suppress the pre-warm pump. Boundaries explicitly forbid a live-LLM dependency in any added test.

---

## Final verdict: READY

The spec accurately reflects the codebase after repair. The three critical build-breaking defects (mislabeled struct, omitted threading hop, wrong Engine/REPL construction sites) are fixed inline without weakening scope. Every remaining file:line citation was verified to resolve to real code. Default-on safety, deterministic-only lobe set, real-provider Router, kill-switch, and no-live-LLM acceptance are all explicit and correct. Safe to hand to /build.
