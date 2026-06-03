# Spec Review — governance-activation

Spec: `specs/governance-activation.md`
Source map (ground truth): `specs/research/raw/RT-governance-activation.md`
Reviewer model: opus, effort: high. Date: 2026-06-03.
Verification basis: every file:line / function / type citation in the spec was
grepped/Read against the working tree on branch
`fix/desktop-tauri-types-and-vite-override`.

## Result: 10/10 PASS (after inline fixes) — READY for /build

The spec was already strong (it was authored directly off the RT source map).
Review found **4 critical-class issues** and several minor citation drifts. The
4 critical issues are **FIXED inline** in `specs/governance-activation.md`. No
scope was weakened.

---

## RT-flagged landmines — both confirmed handled correctly

The task called out two specific RT findings. Both are handled correctly by the
spec, and both cited sites are REAL:

1. **`EventVerifyCrossModelReview` has no producer.**
   VERIFIED: `grep -rn EventVerifyCrossModelReview --include=*.go` returns ONLY
   the const definition at `internal/hub/events.go:93` (no producers outside
   events.go/tests). The spec's Item 1 adds the producer in `runCrossModelReview`.
   The const citation (events.go:93) is REAL.

2. **Trust rule queries `"review.agree"` vs registered node type `"agree"`.**
   VERIFIED:
   - `trust/completion_requires_second_opinion.go:69` →
     `l.Query(..., QueryFilter{Type: "review.agree"})` (REAL).
   - `trust/fix_requires_second_opinion.go:53` → `"review.agree"` (REAL).
   - `consensus/partner_timeout.go:72-73` → `n.Type == "review.agree"` /
     `"review.dissent"` (REAL).
   - Registered struct types are `"agree"` (agree_dissent.go:23) /
     `"dissent"` (agree_dissent.go:70) (REAL).
   The spec adopts option (A): the Governor writes the LITERAL
   `"review.agree"`/`"review.dissent"` strings the rules query, with
   `CreatedBy="governance.reviewer"` distinct from the declaration EmitterID
   (skip at trust.go:72), so the trust gate becomes satisfiable with ZERO rule
   edits. This is correct and is regression-guarded by the trust no-fire test.

---

## Citation audit — every cited site checked

REAL (verified by Grep/Read):
- governance.go: HubSubscriber:114, ModeObserve:118, handle/switch:129,
  return nil:137, onCost:144, Publish:165, onTask:182, AddNode:195,
  taskNodeContent struct:174. ALL REAL.
- events.go: all event consts (PlanStart:17, PlanDone:18, ExecuteStart:19,
  TaskFailed:29, GitPreCommit:72, GitPreMerge:74, GitPostMerge:75,
  VerifyBuildResult:85, ConvergenceStart:90, ConvergenceResult:91,
  CrossModelReview:93, CostBudgetExceeded:121), Event:247, ModelEvent:295,
  GitEvent:309, LifecycleEvent:347-352, TestEvent:355-365. ALL REAL.
- workflow.go emit sites: TaskFailed:244, PlanStart:490, PlanDone:511,
  CostBudgetExceeded:555, ExecuteStart:569, VerifyBuildResult:768,
  ConvergenceStart:976, ConvergenceResult:1106, GitPreCommit:1148,
  GitPreMerge:1178, GitPostMerge:1212, TaskCompleted:1221,
  ModelPostCall:724. runCrossModelReview:1297, parseReviewVerdict call:1422,
  ReviewEngine assign:1393, ReviewPass=verdict.Pass:1474, dissent
  early-return:1475-1480, emitEvent:2219, emitEventAsync:2230. ALL REAL.
- trust/completion_requires_second_opinion.go: Name:26, Pattern:31,
  agreeContent:48-49, Query review.agree:69, skip:72, publish worker.paused:108,
  publish supervisor.spawn.requested:124. ALL REAL.
- bus.go: EvtWorkerDeclarationDone:37, EvtWorkerPaused:40. REAL.
- ledger nodes: task.go:10/40, verification.go struct(VerificationEvidence):23,
  NodeType:77, Validate:87, producer==verifier check:100; agree_dissent.go:23/70;
  loop.go:10; loops.go NewTracker:104, counts:251-253, TransitionState:344,
  terminalStates:46, loopContent:73. ALL REAL.
- ledger.go AddNode:242, validation Type/Content/SchemaVersion:243-251. REAL.
- config/policy.go: Governance field:232, verificationExplicit:236,
  GovernanceConfig:263-265, DefaultGovernanceConfig:269-271, JSON probe:417-421,
  YAML governance parse parseGovernanceBlock:473, line-scanner governance
  skip:591-596, normalizePolicy:509, verificationExplicit consume:536. ALL REAL.
- governance_config.go parseGovernanceBlock:25. REAL.
- app.go: GovernanceEnabled:122, GovernanceBudgetUSD:127, governor field:134,
  gate:218, governance.New:228, non-fatal warn:229-230, orch.governor=g:233,
  Close:251. ALL REAL.
- main.go: SpecExec field:116, specexec build flag:1185, sow flag:1689,
  SpecExec wired build:1465 / sow:3415, costBudget:1709, CostBudgetUSD:3087,
  appCfg RunConfig:412, buildRunConfigOpts:6542, buildRunConfig literal:6564,
  run:1104, plan:3942. ALL REAL.

STALE / WRONG (critical — would have misdirected /build) — NOW FIXED:
- **C1.** `antitrunc/scope_underdelivery.go:42` — the bare path is WRONG. The
  real path is `internal/supervisor/rules/antitrunc/scope_underdelivery.go:42`
  (NOT `internal/cortex/lobes/antitrunc/`, which contains lobe.go only). Line 42
  (Pattern = EvtWorkerDeclarationDone) is correct; the package path was the
  error. FIXED: Item 3 now cites the full correct path.

MINOR drift (corrected during rewrite, non-blocking):
- Overview cited `close app.go:247`; actual `Close()` is at app.go:251 (247 is
  the comment opening the close block). Corrected to 251.
- Item 4 / node section referenced `nodes.Verification.Validate()`; the struct
  is named `VerificationEvidence`. Corrected to avoid a wrong type name.
- ledger.go AddNode range was cited `242-251`; the validation guards are
  243-251 (242 is the func signature). Corrected.
- ModelPostCall mapping row said vague "workflow.go (cost)"; pinned to
  workflow.go:724.

---

## Per-failure-mode verdict

### 1. Self-contained checklist items — PASS
Each of the 14 items names its target file(s), the exact insertion line, the
literal Type strings, and a VERIFY command. An implementer can build each item
from the item text alone (the Data/Event Mapping table is restated per-item).

### 2. Library ambiguity — PASS
Every item names the concrete package/type: `encoding/json`, `ledger.AddNode`,
`ledger.Node`, `bus.Event`, `hub.LifecycleEvent`, `loops.Tracker`,
`gopkg.in/yaml.v3`. No "the validation library"-style ambiguity.

### 3. Pattern references — PASS
Each code-creating item points to a real existing template (`onCost`/`onTask`
for handle cases, `EventTaskCompleted` emit for the new emit, `governance_test.go`
+ `bridge_test.go` for the synthetic test). All template files verified to exist.

### 4. Missing error responses — PASS
This spec has no HTTP endpoints; the analog is the Error Handling table, which
specifies every failure path (construction error → non-fatal; AddNode/Publish/
Marshal errors → swallow/early-return; nil bus → no-op; both flags → kill-switch
wins; budget 0 → no-op). Each strategy cites the precedent line.

### 5. Vague acceptance criteria — PASS
All criteria use concrete values: literal Type strings, `CreatedBy` identities,
the 2s deadline, `$0.90`/`$1` budget, `Enabled==true/false`, exit-0 gate. No
"appropriate/proper/handle correctly".

### 6. Missing boundaries — PASS
A dedicated "Boundaries — What NOT To Do" section plus per-item `Files:` lines.
Explicitly forbids rule edits, Validate() calls, blocking v1, touching
run/plan RunConfig literals, and a live-LLM dependency.

### 7. Dependency ordering — PASS
Order is correct: Item 1 (emit) precedes Item 2 (handle the emit); Item 11
(adds `GovernanceDisabled` field) is referenced by Item 10 but the field
addition is self-contained in app.go and Item 10 only sets it — building 11
first or together is implied and both touch independent files. Item 6 (doc) and
Item 7 (loops, marked OPTIONAL) do not block the trust fix. Item 14 is the final
gate. No forward dependency that blocks a lower item.

### 8. Verification specificity — PASS
Every item carries an explicit `VERIFY:` command (go test -run <name> + go vet,
scoped to the touched package). The headline test asserts via
`ledger.Query(QueryFilter{Type})` node-count and bus subscription — concrete and
checkable.

### 9. Unresearched assumptions — PASS
The spec is built off the RT source map; all technical claims (AddNode skips
Validate, the trust query string, the YAML-vs-JSON parse split) were
independently re-verified against code during this review. No unconfirmed
library versions or API shapes.

### 10. Missing specific values — PASS
Concrete: SchemaVersion:1, CreatedBy identities ("governance.reviewer",
"governance.worker", "governance.bridge"), 2s poll deadline, budget percentages,
flag default values (false), literal node Type strings.

---

## Critical issues found + fix status

- **C1 — WRONG package path for scope_underdelivery rule** (would misdirect any
  implementer trying to read the sibling rule). FIXED: Item 3 now cites
  `internal/supervisor/rules/antitrunc/scope_underdelivery.go:42`.
- **C2 — Ambiguous emit insertion point (Item 1)** could be read as "after the
  whole if-block", but `if !verdict.Pass { return }` at workflow.go:1475 returns
  BEFORE any post-block code runs, so a dissent verdict would NEVER emit —
  silently breaking the "emit dissent" acceptance. FIXED: Item 1 + the mapping
  row + the acceptance criterion now state the emit MUST go after
  `evidence.ReviewPass = verdict.Pass` (1474) and BEFORE the early-return
  (1475-1480), and explain why.
- **C3 — Default-on YAML path under-specified (Item 12)** originally pointed at
  the JSON probe (policy.go:421) and "the explicit-section path 688-style". But
  governance is NOT handled by the YAML line-scanner (it is SKIPPED at
  policy.go:591-596) — it is parsed by `parseGovernanceBlock(raw)` at
  policy.go:473. The `verificationExplicit` precedent does not transfer 1:1. As
  written, the common `stoke.yaml` (YAML) case would have had no presence
  detector and absent-block default-on could silently fail. FIXED: Item 12 now
  specifies BOTH parse paths explicitly — extend the JSON `probe` struct
  (policy.go:417-421) AND add a `present bool` return to `parseGovernanceBlock`
  consumed at policy.go:473-477 — and the test must cover both YAML and JSON.
- **C4 — Wrong type name `nodes.Verification`** in Item 4's rationale (real
  struct is `VerificationEvidence`). FIXED: corrected to the producer==verifier
  check at verification.go:100 without the wrong type name.

## Synthetic-integration / safety acceptance (task item 2) — PASS

- **No live LLM:** CONFIRMED. Every assertion drives `sub.Handler(ctx, ...)` or
  `g.Bus().Publish(...)` directly. The trust fire/no-fire tests subscribe to
  `worker.paused` + `supervisor.spawn.requested` and assert presence/absence
  within the 2s deadline. No claude/codex process is spawned. The patterns
  (`governance_test.go`, `bridge_test.go`) are real and already LLM-free.
- **Default-on:** specified (Item 12 — `normalizePolicy` flips absent-block to
  `Enabled=true`).
- **Kill-switch:** specified (Item 11 — `GovernanceDisabled` term in app.go gate;
  Item 9 — `--no-governance` flag; Error Handling — kill-switch wins).
- **Non-fatal construction:** specified and explicitly preserved (Error Handling
  row + Boundaries + Acceptance Criteria all cite app.go:229-230).

## Recommendations (non-blocking)

- Item 7 (loops) is correctly marked OPTIONAL/stretch — keep it last so a failed
  optional does not block the trust fix.
- Consider asserting in the no-budget safety test that `mission.budget.update` is
  NEVER published when budget==0 (the spec implies it; making it an explicit
  negative assertion strengthens the kill-switch-adjacent guarantee). Not
  required for /build.

## Verdict: READY for /build

All citations resolve to real code (the one wrong path, C1, is fixed). The two
RT-flagged landmines are handled correctly and their sites are real. The
synthetic integration test is genuinely LLM-free. Default-on + kill-switch +
non-fatal-construction are all specified. The 4 critical issues are fixed inline
without weakening scope.
