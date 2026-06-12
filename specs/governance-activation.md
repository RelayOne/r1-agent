<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-06-03 -->
<!-- CREATED: 2026-06-03 -->
<!-- DEPENDS_ON: (extends commit c5e5e787 governance first slice) -->
<!-- BUILD_ORDER: 2 -->
<!-- REVIEW: Codex (GPT) cross-model PASS after fix loop — found 2 HIGH (async trust-rule race G15, budget<=0 contract G16), both fixed + re-reviewed clean. Full gate: go test ./... = 0 failures, governance default-on. -->
<!-- ITEM 7 (ledger/loops 7-state) DEFERRED — spec marked it optional/stretch; trust fix does not depend on it. -->
<!-- HOLISTIC: production-readiness/collision/playwright N/A — internal Go governance wiring. -->

# Governance Activation — Wire the V2 Governance Layer Into the Live Mission Path (Default-On + Kill-Switch)

## Overview

Commit `c5e5e787` landed the first governance slice: `internal/governance/governance.go` defines a `Governor` that owns a durable v2 bus (`internal/bus`), a v2 ledger (`internal/ledger`), and a mission `supervisor` running `manifests.MissionRules()`, and exposes a `hub.Subscriber` (`HubSubscriber()`, governance.go:114) that observes v1 hub events and translates them into v2 bus events / ledger nodes. Today it maps **only** cost (`hub.EventModelPostCall` → `mission.budget.update`) and task lifecycle (`EventTaskStarted`/`EventTaskCompleted` → a `"task"` ledger node). It is wired into the runtime in `internal/app/app.go` (construction gate app.go:218, close app.go:251) but is **default-OFF** and has **no kill-switch flag**.

This spec finishes the wiring. It (1) maps **every** mission lifecycle event the live `internal/workflow/workflow.go` and `internal/mission/handlers.go` engines emit (plan start/done, execute start, verify build/test/lint results, convergence start/result, pre-commit/pre-merge/post-merge, budget-exceeded) to the correct `bus.Event` Type and the correct `internal/ledger/nodes` node type; (2) fixes **the critical hole**: the cross-model review verdict is emitted nowhere, and the trust rule queries the literal ledger Type `"review.agree"` while the registered node type is `"agree"` — so `trust.completion_requires_second_opinion` is un-satisfiable by construction. We emit a new `EventVerifyCrossModelReview` hub event from `runCrossModelReview` and have the Governor write a `"review.agree"` / `"review.dissent"` ledger node (literal strings the rules query) plus publish `worker.declaration.done` to trigger the rule; (3) optionally drives the `internal/ledger/loops` 7-state machine; (4) adds `--governance` / `--no-governance` CLI flags (mirroring `--specexec`) and wires `RunConfig.GovernanceEnabled` + `RunConfig.GovernanceBudgetUSD` at both RunConfig sites; (5) flips the default to **ON** with a kill-switch, mirroring the existing `verificationExplicit` "absent vs explicit-false" trick; (6) proves all of it with a **synthetic-event integration test** that drives lifecycle events through the real wired `HubSubscriber().Handler` and asserts ledger nodes + rule firing — **no live claude/codex run required**.

Who: the mission/build/sow runtime operator. Acceptance is proven entirely by Go unit + synthetic integration tests under `internal/governance/`, `internal/workflow/`, and `internal/config/`.

## Stack & Versions

- Go 1.23+ (existing module `github.com/RelayOne/r1`).
- stdlib only: `context`, `encoding/json`, `sync`, `time`, `path/filepath`, `testing`.
- Existing internal packages (no new deps): `internal/bus`, `internal/ledger`, `internal/ledger/nodes`, `internal/ledger/loops`, `internal/hub`, `internal/supervisor`, `internal/supervisor/manifests`, `internal/supervisor/rules/trust`, `internal/config`, `internal/app`, `internal/workflow`.
- `gopkg.in/yaml.v3` (already in go.mod) for the config default-on change.
- No third-party additions. `go mod tidy` must report no changes.

## Existing Patterns to Follow

- Governor + handler: `internal/governance/governance.go` — `handle` switch (governance.go:129), `onCost` (governance.go:144), `onTask` (governance.go:182), ledger write via `g.ledger.AddNode` (governance.go:195).
- Synthetic-event test driving: `internal/governance/governance_test.go` — `newTestGovernor(t, budgetUSD)` (governance_test.go:17), `collect(t, g.Bus(), prefix)` (governance_test.go:29), drive `sub := g.HubSubscriber(); sub.Handler(ctx, &hub.Event{...})` (governance_test.go:64,:103), async poll with ~2s deadline (governance_test.go:70-82), ledger assert via `g.Ledger().Query(ctx, ledger.QueryFilter{Type:"task"})` (governance_test.go:109).
- Bridge synthetic-event ledger-node assertions: `internal/bridge/bridge_test.go` — `setup(t)` (bridge_test.go:19) opens real `bus.New`+`ledger.New`, `collectEvents(t,b,prefix)` (bridge_test.go:39), pattern "call method → poll bus type → unmarshal payload → `l.Query(QueryFilter{Type:...})` → assert node count" (bridge_test.go:96-103,:186-193).
- CLI flag: `--specexec` — `RunConfig.SpecExec bool` (main.go:116), `fs.Bool("specexec", ...)` (main.go:1185 build, main.go:1689 sow), wired `SpecExec: *specExec` (main.go:1465 build, main.go:3415 sow).
- Config "absent vs explicit-false" distinction: `verificationExplicit bool` (`internal/config/policy.go:236`), set in JSON probe path (policy.go:421) AND in the YAML line-scanner path (policy.go:688), consumed in `normalizePolicy` (policy.go:536). NOTE: governance is parsed differently from verification — see Item 12 for the two distinct parse paths.
- Hub event emission in workflow: `emitEvent` / `emitEventAsync` (workflow.go:2219/:2230), nil-safe via `e.EventBus`; existing emit at workflow.go:1221 (`EventTaskCompleted`) is the template for a new emit.

## Library Preferences

- JSON: stdlib `encoding/json` (match `onCost`/`onTask` `json.Marshal`).
- Ledger writes: `ledger.AddNode(ctx, ledger.Node{Type, SchemaVersion:1, CreatedBy, MissionID, Content})` — `AddNode` accepts ANY `Type` string, requires only `Type`+`Content`+`SchemaVersion>=1` (ledger.go:243-251) and does NOT call `nodes.*.Validate()`. We therefore write raw nodes with the literal Type strings the rules query.
- Bus publishes: `g.bus.Publish(bus.Event{Type, EmitterID, Scope, Payload})` (match `onCost` governance.go:165).
- YAML: `gopkg.in/yaml.v3` (same as `internal/config`).

## Data / Event Mapping (authoritative)

Each row is one `handle` case (governance.go:129) the Governor must add. "→ bus.Event Type" drives the supervisor rules; "→ ledger node (literal Type)" populates the graph. Node-type names are the **literal `Type` strings written via `AddNode`** — NOT struct names — because `AddNode` skips `Validate()`.

| Source hub event | Emit site (file:line) | → bus.Event Type | → ledger node (literal Type) | Notes |
|---|---|---|---|---|
| `EventMissionPlanStart` | workflow.go:490; handlers.go:567 | `plan.started` | `task` (granularity=mission) | `Content:{title:ev.TaskID,description:"plan_start",state:"started"}` |
| `EventMissionPlanDone` | workflow.go:511 | `plan.done` | `task` (state `planned`) | `Model{Provider,InputTokens,OutputTokens,CostUSD}` present |
| `EventModelPostCall` | workflow.go:724 (cost) | `mission.budget.update` *(exists)* | (none) | already wired in `onCost` (governance.go:144); keep |
| `EventCostBudgetExceeded` | workflow.go:555 | `mission.budget.update` (spent==budget, PercentUsed=100) | (none) | force a hard-stop budget update |
| `EventMissionExecuteStart` | workflow.go:569; handlers.go:673 | `execute.started` | `task` (state `executing`) | carries `Lifecycle{Attempt}` |
| `EventVerifyBuildResult` | workflow.go:768 | `verify.result` | `verification_evidence` (**one per check**) | emitted once per outcome (build/test/lint), per-outcome name in `ev.Test.Phase`; write one node per call |
| `EventVerifyCrossModelReview` **(NEW emit)** | runCrossModelReview workflow.go:1474 (after verdict.Pass set, before the dissent early-return at workflow.go:1475) | `worker.declaration.done` (to TRIGGER trust rule) | `review.agree` if pass, else `review.dissent` (to SATISFY it) | **the critical fix — see Item 2** |
| `EventVerifyConvergenceStart` | workflow.go:976 | `convergence.started` | `loop` transition `convening`→`reviewing` (Item 7, optional) | |
| `EventVerifyConvergenceResult` | workflow.go:1106 | `convergence.done` | `loop` transition `converged` (Item 7, optional) | no body |
| `EventGitPreCommit` | workflow.go:1148 | `commit.started` | (none / `decision`) | `Git{Operation=commit,Branch,FilesChanged}` |
| `EventGitPreMerge` | workflow.go:1178 | `merge.started` | `decision` node (merge intent) | `Git{Operation=merge,Branch}` |
| `EventGitPostMerge` | workflow.go:1212 | `merge.done` | `decision` node (merge complete) | `Git{Operation=merge,FilesChanged}` |
| `EventTaskStarted` | workflow.go | (none) | `task` *(exists)* | already wired |
| `EventTaskCompleted` | workflow.go:1221 | `worker.declaration.done` (Item 2b) | `task` *(exists)* | translate into declaration AFTER review.agree write |
| `EventTaskFailed` | workflow.go:244 | `task.failed` | `task` (state `failed`) | `Lifecycle{task,failed}` |

### Ledger node types referenced (from `internal/ledger/nodes/`)
- `task` — nodes/task.go:10, `NodeType()="task"` (task.go:40). Governor writes the existing ad-hoc `{title,description,state}` shape (governance.go:174 `taskNodeContent`) — acceptable because `AddNode` skips Validate.
- `verification_evidence` — struct `VerificationEvidence` at nodes/verification.go:23, `NodeType()="verification_evidence"` (verification.go:77). Fields `SubjectRef,SubjectKind,ProducerModel,VerifierModel,Verdict∈{agree,disagree,partial,insufficient},EvidenceRefs,Notes,CrossFamily,When`. We write a raw node of this Type for each build/test/lint outcome.
- `review.agree` / `review.dissent` — **literal strings**, NOT a registered struct. The registered struct is `"agree"`/`"dissent"` (agree_dissent.go:23/:70) but the trust + partner-timeout rules query `"review.agree"`/`"review.dissent"`. We write the literal queried strings. See Item 2 / Item 6.
- `decision` — nodes/decision.go (merge intent/complete records).
- `loop` — nodes/loop.go:10, `NodeType()="loop"` (Item 7).

## Existing Patterns to Follow — call sites being touched

- `internal/workflow/workflow.go`: `runCrossModelReview` (workflow.go:1297), verdict computed at workflow.go:1422 (`verdict, parseErr := parseReviewVerdict(...)`), `verdict.Pass` consumed at workflow.go:1474, dissent early-return at workflow.go:1475-1480, reviewer engine string at `evidence.ReviewEngine = verifyRunnerName` (workflow.go:1393).
- `internal/governance/governance.go`: `handle` (governance.go:129), `onCost` (governance.go:144), `onTask` (governance.go:182), `AddNode` template (governance.go:195).
- `internal/hub/events.go`: `EventVerifyCrossModelReview = "verify.cross_model_review"` (events.go:93) — **zero producers today** (verified by grep: only the const definition references it). `ModelEvent` (events.go:295), `GitEvent` (events.go:309), `LifecycleEvent` (events.go:347-352, fields `Entity,State,Duration,Attempt`), `TestEvent` (events.go:355-365, fields `Phase,Passed,Failed,...`).
- `internal/bus/bus.go`: `EvtWorkerDeclarationDone = "worker.declaration.done"` (bus.go:37), `EvtWorkerPaused = "worker.paused"` (bus.go:40).
- `internal/supervisor/rules/trust/completion_requires_second_opinion.go`: `Name()` (:26), Pattern = `EvtWorkerDeclarationDone` (:31), `agreeContent{task_id}` (:48-49), `l.Query(QueryFilter{Type:"review.agree"})` (:69), skip `n.CreatedBy == evt.EmitterID` (:72), fires → `worker.paused` (publish :108) + `supervisor.spawn.requested` (publish :124).
- `internal/config/policy.go`: `Policy.Governance GovernanceConfig` (policy.go:232), `verificationExplicit bool` (policy.go:236), `GovernanceConfig{Enabled bool}` (policy.go:263-265), `DefaultGovernanceConfig()` → `{Enabled:false}` (policy.go:269-271), `normalizePolicy` (policy.go:509), governance YAML parse via `parseGovernanceBlock(raw)` at policy.go:473 (NOT the line-scanner). `internal/config/governance_config.go`: `parseGovernanceBlock` (governance_config.go:25) returns zero `GovernanceConfig` when block absent.
- `internal/app/app.go`: gate `if cfg.EventBus != nil && (cfg.GovernanceEnabled || policy.Governance.Enabled)` (app.go:218), `RunConfig.GovernanceEnabled` (app.go:122), `RunConfig.GovernanceBudgetUSD` (app.go:127), `governance.New` (app.go:228), non-fatal warn (app.go:229-230), `orch.governor = g` (app.go:233), close at app.go:251.
- `cmd/r1/main.go`: `RunConfig{...}` at main.go:412 (per-task dispatch), `buildRunConfig` literal at main.go:6564, `buildRunConfigOpts` at main.go:6542, `costBudget := fs.Float64("cost-budget", 0, ...)` (main.go:1709), `CostBudgetUSD: *costBudget` (main.go:3087).

## Error Handling

| Failure | Strategy | Result |
|---|---|---|
| Governor construction error (`governance.New`) | **Non-fatal** — log, leave `orch.governor == nil`, run proceeds ungoverned | existing app.go:229-230 behavior; MUST be preserved |
| `ledger.AddNode` error inside a `handle` case | swallow (`_, _ =`), match `onTask` (governance.go:195) — observe-mode never blocks v1 | run continues |
| `bus.Publish` error inside a `handle` case | swallow (`_ =`), match `onCost` (governance.go:165) | run continues |
| `json.Marshal` error on payload/content | early-return the case, match `onCost`/`onTask` | event dropped, run continues |
| New `EventVerifyCrossModelReview` emit when `e.EventBus == nil` | nil-safe via `emitEventAsync` (workflow.go:2230) | no-op |
| `--governance` and `--no-governance` both set | `--no-governance` wins (kill-switch is authoritative) | governance disabled |
| Budget 0 (unset) | budget rule no-ops (drift treats non-positive budget as no-op; governance.go:60-63 doc) | no budget events |

## Boundaries — What NOT To Do

- Do **NOT** make governance block, veto, or mutate v1 execution. `HubSubscriber()` stays `Mode: hub.ModeObserve` (governance.go:118); every `handle` case is fire-and-forget and `handle` always returns nil (governance.go:137).
- Do **NOT** let default-on break existing runs. Construction failure stays non-fatal (app.go:229-230). With budget 0 the budget rule must no-op. The synthetic integration test must prove a no-budget Governor processes every lifecycle event without error.
- Do **NOT** edit the supervisor trust/partner-timeout rules. Adopt option (A): write the literal `"review.agree"`/`"review.dissent"` strings the rules already query — zero rule changes. Document why `nodes.Agree` (Type `"agree"`, which requires a real `DraftRef`) is NOT used.
- Do **NOT** call `nodes.*.Validate()` from the Governor. `AddNode` intentionally skips it; raw typed writes are the contract (ledger.go:243-251).
- Do **NOT** change `app.go`'s OR-of-flag||policy semantics except to add the explicit-off override. The kill-switch is added as a new `&& !cfg.GovernanceDisabled` term — existing default-on policy still wins absent the kill-switch.
- Do **NOT** require a live claude/codex run for acceptance. Every assertion is a synthetic event driven through `HubSubscriber().Handler` or a direct `bus.Publish`.
- Do **NOT** make the new `EventVerifyCrossModelReview` emit fatal — if `parseReviewVerdict` errored upstream, the error paths return before workflow.go:1474, so no emit is reached; do not add a panic path.
- Do **NOT** write the cross-model review node with `CreatedBy == evt.EmitterID` of the declaration — the trust rule's skip (trust.go:72) would drop it and the no-fire case would (incorrectly) still fire. Use a distinct reviewer identity (`"governance.reviewer"`).
- Do **NOT** touch RunConfig literals at main.go:1104 (`run`) or main.go:3942 (`plan`) — they may legitimately leave governance at default.

## Testing

### `internal/workflow/workflow_review_emit_test.go` (Item 1)
- [ ] Emit happy (pass): construct an `Engine` with a recording `hub.Bus`, invoke the review-emit path with `verdict.Pass=true` → exactly one `EventVerifyCrossModelReview` event observed, carrying `TaskID`, reviewer engine string, and a pass=true signal.
- [ ] Emit dissent: `verdict.Pass=false` → one `EventVerifyCrossModelReview` with pass=false. (This pins that the emit precedes the dissent early-return at workflow.go:1475.)
- [ ] Nil bus: `e.EventBus == nil` → no panic, no emit.

### `internal/config/governance_default_test.go` (Item 6)
- [ ] Absent block: parse a policy YAML with no `governance:` section → `policy.Governance.Enabled == true`.
- [ ] Explicit false: `governance:\n  enabled: false` → `policy.Governance.Enabled == false`.
- [ ] Explicit true: `governance:\n  enabled: true` → `true`.
- [ ] `governanceExplicit` is set only when the block is present (mirror verification probe).

### `internal/governance/activation_test.go` (Item 8 — the headline synthetic integration test)
- [ ] Per-event ledger nodes: drive each lifecycle hub event through `sub.Handler(ctx, &hub.Event{Type:...})` and assert a ledger node of the expected literal Type exists (`task`, `verification_evidence`, `review.agree`, `decision`) via `g.Ledger().Query(ctx, ledger.QueryFilter{Type:...})` — node-count ≥ 1 + key fields. Use the async-poll pattern (governance_test.go:70-82).
- [ ] Budget escalation: `$0.90` post-call against a `$1` budget → `supervisor.spawn.requested` published (extends `TestGovernorBudgetRuleFires`).
- [ ] **Trust no-fire**: write a `review.agree` node `{task_id:"T1"}` with `CreatedBy:"governance.reviewer"`, then `g.Bus().Publish(bus.Event{Type:bus.EvtWorkerDeclarationDone, EmitterID:"governance.worker", Scope:{TaskID:"T1"}, Payload:{"task_id":"T1","worker_id":"governance.worker"}})`; subscribe `worker.paused` + `supervisor.spawn.requested`; assert NEITHER published within the ~2s deadline.
- [ ] **Trust fire**: same declaration with NO `review.agree` node present → assert `worker.paused` AND `supervisor.spawn.requested` ARE published. (This case also pins the naming-split fix: writing `"agree"` instead of `"review.agree"` would make the no-fire case wrongly fire.)
- [ ] End-to-end via review event: drive `sub.Handler(ctx, &hub.Event{Type:hub.EventVerifyCrossModelReview, TaskID:"T2", ...pass=true})` then `EventTaskCompleted` for `T2`; assert a `review.agree` node for `T2` exists AND the subsequent `worker.declaration.done` does NOT fire `worker.paused`.
- [ ] No-budget safety: build Governor with budget 0, drive every lifecycle event → no error, no budget events, all expected nodes written.

### `internal/governance/loops_test.go` (Item 7, optional/stretch)
- [ ] Bootstrap loop via raw `AddNode` Type `"loop"` `state:proposing`, then drive plan-done → `TransitionState(StateDrafted)`, convergence-result → `TransitionState(StateConverged)`; assert terminal node state.

### VERIFY commands (per area)
```bash
go test ./internal/workflow/... -run TestReviewEmit && go vet ./internal/workflow
go test ./internal/config/...   -run TestGovernanceDefault && go vet ./internal/config
go test ./internal/governance/... && go vet ./internal/governance
go test ./internal/app/...      && go vet ./internal/app
go build ./cmd/r1 && go test ./... && go vet ./...
```

## Acceptance Criteria

- WHEN `runCrossModelReview` computes a verdict THE SYSTEM SHALL emit exactly one `hub.EventVerifyCrossModelReview` carrying `TaskID`, the reviewer engine string (`evidence.ReviewEngine`), and a pass/dissent signal — for BOTH pass and dissent verdicts (the emit precedes the dissent early-return at workflow.go:1475) — verified by `internal/workflow/workflow_review_emit_test.go`.
- WHEN the Governor handles an `EventVerifyCrossModelReview` with pass=true THE SYSTEM SHALL write a ledger node of literal Type `"review.agree"` with `CreatedBy="governance.reviewer"` and `Content={"task_id":<TaskID>}` — verified by ledger Query in `activation_test.go`.
- WHEN an independent `review.agree` node exists for a task AND a `worker.declaration.done` event is published for that task THE SYSTEM SHALL NOT publish `worker.paused` or `supervisor.spawn.requested` within 2s — verified by the trust no-fire test.
- WHEN no `review.agree` node exists for a task AND a `worker.declaration.done` event is published THE SYSTEM SHALL publish both `worker.paused` and `supervisor.spawn.requested` — verified by the trust fire test.
- WHEN the Governor handles `EventVerifyBuildResult` THE SYSTEM SHALL write one `verification_evidence` ledger node per build/test/lint outcome (one per `ev.Test.Phase`) — verified by node-count assertion.
- WHEN the Governor handles plan/execute/convergence/merge/budget-exceeded events THE SYSTEM SHALL write the ledger node and/or publish the bus event per the Data/Event Mapping table — verified per-event in `activation_test.go`.
- WHEN a `stoke.yaml` has no `governance:` block THE SYSTEM SHALL default `policy.Governance.Enabled = true` — verified by `governance_default_test.go`.
- WHEN a `stoke.yaml` sets `governance:\n  enabled: false` THE SYSTEM SHALL honor it (`Enabled == false`) — verified by `governance_default_test.go`.
- WHEN `--no-governance` is passed THE SYSTEM SHALL set `RunConfig.GovernanceDisabled=true` and the app gate SHALL NOT construct a Governor even if policy default-on — verified by an app-gate unit test.
- WHEN `--governance` is passed on `build`/`sow` THE SYSTEM SHALL set `RunConfig.GovernanceEnabled=true` and source `GovernanceBudgetUSD` from `*costBudget`/`CostBudgetUSD`.
- WHEN governance is default-on and `GovernanceBudgetUSD == 0` THE SYSTEM SHALL process every lifecycle event without error and emit no budget events — verified by the no-budget safety test.
- WHEN `governance.New` returns an error THE SYSTEM SHALL leave `orch.governor == nil` and continue the run ungoverned (non-fatal) — preserved from app.go:229-230.
- `go build ./cmd/r1 && go test ./... && go vet ./...` all exit 0.

## Implementation Checklist

1. [ ] **[review-emit]** In `internal/workflow/workflow.go` `runCrossModelReview` (workflow.go:1297), emit a hub `EventVerifyCrossModelReview` (`hub.EventType` const at events.go:93 — currently zero producers, verified) on the line **immediately after `evidence.ReviewPass = verdict.Pass` (workflow.go:1474) and BEFORE the `if !verdict.Pass { ... return nil, err }` early-return block (workflow.go:1475-1480)**. The emit MUST precede that early-return — otherwise a dissent verdict (`verdict.Pass == false`) returns at workflow.go:1479 and the dissent event is never emitted, breaking the "emit dissent" acceptance. Use the nil-safe `emitEventAsync` (workflow.go:2230) pattern (template: the `EventTaskCompleted` emit at workflow.go:1221). Payload: `TaskID = name`, `Phase = "review"`, `Model{Provider: evidence.ReviewEngine}` (engine string set at workflow.go:1393), and the pass/dissent signal — carry it via a `*hub.LifecycleEvent{Entity:"review", State: ternary(verdict.Pass,"agree","dissent")}` (LifecycleEvent shape events.go:347-352, fields `Entity`,`State`) so the Governor can read `ev.Lifecycle.State` without a new field. If `parseReviewVerdict` errored upstream (`parseErr != nil` at workflow.go:1422, the error paths set `evidence.ReviewPass=false` and return before workflow.go:1474), no emit is reached — acceptable, do not add a panic path. Files: `internal/workflow/workflow.go`. Test: `internal/workflow/workflow_review_emit_test.go` (emit pass / emit dissent / nil-bus no-panic). VERIFY: `go test ./internal/workflow/... -run TestReviewEmit && go vet ./internal/workflow`.

2. [ ] **[gov-map-review + THE CRITICAL FIX]** In `internal/governance/governance.go` `handle` (governance.go:129), add `case hub.EventVerifyCrossModelReview:` calling a new `onReview(ctx, ev)`. `onReview` reads `ev.Lifecycle.State` (set in Item 1): if `"agree"` write a ledger node `ledger.Node{Type:"review.agree", SchemaVersion:1, CreatedBy:"governance.reviewer", MissionID:g.missionID, Content: json({"task_id": ev.TaskID})}`; if `"dissent"` write `Type:"review.dissent"`. **`CreatedBy` MUST be `"governance.reviewer"` (distinct from the declaration EmitterID)** or the trust rule's `n.CreatedBy == evt.EmitterID` skip (trust.go:72) drops it. The `Content` shape `{"task_id":...}` must match the trust rule's `agreeContent{task_id}` (trust.go:48-49,:75). Write the **literal `"review.agree"`/`"review.dissent"` strings** the trust rule queries (trust.go:69) — NOT `nodes.Agree`/`"agree"`. Template: `onTask` `AddNode` (governance.go:195). Files: `internal/governance/governance.go`. Test: covered by `activation_test.go` (Item 8). VERIFY: `go test ./internal/governance/... -run TestActivationReview && go vet ./internal/governance`.

3. [ ] **[gov-emit-declaration]** In `internal/governance/governance.go`, extend the existing `EventTaskCompleted` handling (governance.go:134) so that AFTER `onTask(ctx, ev, "completed")` it publishes a v2 `bus.Event{Type: bus.EvtWorkerDeclarationDone (bus.go:37), EmitterID:"governance.worker", Scope: bus.Scope{MissionID:g.missionID, TaskID: ev.TaskID}, Payload: json({"task_id": ev.TaskID, "worker_id":"governance.worker", "artifact_id": ev.TaskID})}`. The `review.agree` node (Item 2) is written when the review event arrives — which precedes task-completion in the workflow — so the trust rule (trust.go:52) sees the node and does NOT fire on approved work. `EmitterID="governance.worker"` MUST differ from the review node's `CreatedBy="governance.reviewer"` (trust.go:72). Template: `onCost` Publish (governance.go:165). Note this declaration also activates `internal/supervisor/rules/antitrunc/scope_underdelivery.go` (same Pattern, scope_underdelivery.go:42) — payload carries `task_id`+`worker_id`, sufficient. Files: `internal/governance/governance.go`. Test: `activation_test.go` trust fire/no-fire (Item 8). VERIFY: `go test ./internal/governance/... && go vet ./internal/governance`.

4. [ ] **[gov-map-verify]** In `handle`, add `case hub.EventVerifyBuildResult:` calling `onVerify(ctx, ev)`. The workflow emits this **once per outcome** (build/test/lint, all under the same hub Type, the name in `ev.Test.Phase`, workflow.go:768) — so write ONE `verification_evidence` ledger node per call: `ledger.Node{Type:"verification_evidence", SchemaVersion:1, CreatedBy:"governance.bridge", MissionID:g.missionID, Content: json({"subject_ref": ev.TaskID, "subject_kind":"task", "producer_model":"executor", "verifier_model":"governance.verify", "verdict": ternary(ev.Test.Passed>0 && ev.Test.Failed==0, "agree","disagree"), "notes": ev.Test.Phase})}`. ProducerModel≠VerifierModel matters only if the `VerificationEvidence.Validate()` method were called (the producer==verifier check at verification.go:100) — it is NOT (AddNode skips it) but we keep them distinct for forward-compat. Files: `internal/governance/governance.go`. Test: `activation_test.go` asserts a `verification_evidence` node per build/test/lint drive. VERIFY: `go test ./internal/governance/... -run TestActivationVerify && go vet ./internal/governance`.

5. [ ] **[gov-map-lifecycle]** In `handle`, add cases for the remaining lifecycle events per the Data/Event Mapping table, each writing the node/bus output: `EventMissionPlanStart` (workflow.go:490) → `task` node state `started` + publish `plan.started`; `EventMissionPlanDone` (workflow.go:511) → `task` node state `planned` + publish `plan.done`; `EventMissionExecuteStart` (workflow.go:569) → `task` node state `executing` + publish `execute.started`; `EventCostBudgetExceeded` (workflow.go:555) → publish `mission.budget.update` with `spent_usd==budget_usd` (reuse `onCost`'s payload shape forced to 100%); `EventGitPreMerge` (workflow.go:1178) → `decision` node + publish `merge.started`; `EventGitPostMerge` (workflow.go:1212) → `decision` node + publish `merge.done`; `EventGitPreCommit` (workflow.go:1148) → publish `commit.started`; `EventTaskFailed` (workflow.go:244) → `task` node state `failed`. Each case is fire-and-forget; swallow marshal/AddNode/Publish errors. Files: `internal/governance/governance.go`. Test: `activation_test.go` drives each and asserts the node/bus output. VERIFY: `go test ./internal/governance/... && go vet ./internal/governance`.

6. [ ] **[naming-fix — documented]** In `internal/governance/governance.go`, add a package/func doc comment on `onReview` explaining the resolved naming split: the registered node struct is Type `"agree"`/`"dissent"` (agree_dissent.go:23/:70) and the loops tracker counts those (loops.go:251-253), BUT the trust rules + `partner_timeout` query the literal `"review.agree"`/`"review.dissent"` (trust/completion_requires_second_opinion.go:69, trust/fix_requires_second_opinion.go:53, consensus/partner_timeout.go:72-73). We adopt **option (A)** — write the literal queried strings — so NO supervisor rule edits are required, and `nodes.Agree` (which requires a real `DraftRef` the per-task workflow has no concept of) is intentionally NOT used. Files: `internal/governance/governance.go` (comment only). Test: the trust no-fire test in `activation_test.go` is the regression guard (writing `"agree"` would make it wrongly fire). VERIFY: `go vet ./internal/governance`.

7. [ ] **[loops — optional/stretch]** In `internal/governance/governance.go`, optionally bootstrap and drive a `loops.Tracker` (`loops.NewTracker(g.ledger)`, loops.go:104). There is NO `CreateLoop`/`NewLoop` — bootstrap by `ledger.AddNode` a raw `Type:"loop"` node with the looser `loopContent` shape `{state:"proposing", loop_type, artifact_id, convened_partners, reason}` (loops.go:73, distinct from `nodes.Loop`). Then on `EventMissionPlanDone` → `TransitionState(loopID, loops.StateDrafted, ...)` (loops.go:344); on `EventVerifyConvergenceStart` → `StateConvening`→`StateReviewing`; on `EventVerifyConvergenceResult` (pass) → `StateConverged`; on escalate/retry-exhaust → `StateEscalated` (terminal states loops.go:46). Mark this case explicitly OPTIONAL — the trust fix does NOT depend on it. Files: `internal/governance/governance.go`. Test: `internal/governance/loops_test.go` (bootstrap → transitions → terminal state). VERIFY: `go test ./internal/governance/... -run TestLoops && go vet ./internal/governance`.

8. [ ] **[integration-test]** Create `internal/governance/activation_test.go` combining `governance_test.go` and `bridge_test.go` patterns. Reuse `newTestGovernor(t, budget)` (governance_test.go:17) and `collect(t, g.Bus(), prefix)` (governance_test.go:29). Write: (a) a per-event loop driving each lifecycle `hub.Event` through `sub.Handler(ctx, ...)` and asserting `g.Ledger().Query(ctx, ledger.QueryFilter{Type:<literal>})` node-count ≥ 1 + key fields, with the ~2s async poll (governance_test.go:70-82); (b) budget escalation at 90%; (c) **trust no-fire** — write a `review.agree` node `{task_id:"T1"}` `CreatedBy:"governance.reviewer"`, publish `bus.EvtWorkerDeclarationDone` `EmitterID:"governance.worker"` `Scope{TaskID:"T1"}`, subscribe `worker.paused`+`supervisor.spawn.requested`, assert NEITHER fires; (d) **trust fire** — same declaration, no node, assert BOTH fire; (e) end-to-end via `EventVerifyCrossModelReview`(pass) then `EventTaskCompleted` → `review.agree` for that task exists AND declaration does NOT fire; (f) no-budget safety — budget 0, drive all events, no error, no budget events. No live LLM anywhere. Files: `internal/governance/activation_test.go`. VERIFY: `go test ./internal/governance/... && go vet ./internal/governance`.

9. [ ] **[cli-flag]** In `cmd/r1/main.go`, add `--governance` and `--no-governance` bool flags on BOTH the `build` flag set (alongside `specExec := fs.Bool("specexec",...)` main.go:1185) and the `sow` flag set (alongside main.go:1689), modeled on `--specexec`. `governance := fs.Bool("governance", false, "Force-enable the V2 governance layer (default on via policy)")` and `noGovernance := fs.Bool("no-governance", false, "Kill-switch: disable the V2 governance layer for this run")`. Resolve a single bool + a disabled bool: `govEnabled := *governance` (the policy default-on still applies in app.go); `govDisabled := *noGovernance`. Files: `cmd/r1/main.go`. Test: a small `cmd/r1` flag-parse test asserting both flags parse; functional coverage via Item 11 app-gate test. VERIFY: `go build ./cmd/r1`.

10. [ ] **[runconfig]** Set `GovernanceEnabled`, `GovernanceBudgetUSD`, and the new `GovernanceDisabled` (Item 11) at BOTH RunConfig sites. (a) The per-task dispatch literal `appCfg := app.RunConfig{...}` (main.go:412): add `GovernanceEnabled: govEnabled`, `GovernanceDisabled: govDisabled`, `GovernanceBudgetUSD: cfg.CostBudgetUSD` (the budget carried at main.go:3087 from `*costBudget` main.go:1709). (b) `buildRunConfig` literal (main.go:6564): extend `buildRunConfigOpts` (main.go:6542) with `GovernanceEnabled bool`, `GovernanceDisabled bool`, `GovernanceBudgetUSD float64`, and set `cfg.GovernanceEnabled = opts.GovernanceEnabled` etc. where the other opts are applied. Leave main.go:1104 (`run`) and main.go:3942 (`plan`) at default. Files: `cmd/r1/main.go`. Test: build + Item 11 gate test. VERIFY: `go build ./cmd/r1 && go test ./cmd/r1/...`.

11. [ ] **[kill-switch + app gate]** In `internal/app/app.go`, add `RunConfig.GovernanceDisabled bool` (next to `GovernanceEnabled` app.go:122). Change the construction gate (app.go:218) from `if cfg.EventBus != nil && (cfg.GovernanceEnabled || policy.Governance.Enabled)` to `if cfg.EventBus != nil && !cfg.GovernanceDisabled && (cfg.GovernanceEnabled || policy.Governance.Enabled)`. This makes `--no-governance` override a default-on policy. Keep construction failure non-fatal (app.go:229-230) and Close-on-end (app.go:251). Files: `internal/app/app.go`. Test: `internal/app/governance_gate_test.go` — (i) `GovernanceDisabled=true` + policy Enabled=true + non-nil EventBus → `orch.governor == nil`; (ii) `GovernanceEnabled=false` + policy Enabled=true → governor constructed; (iii) `GovernanceDisabled=true` + GovernanceEnabled=true → nil. VERIFY: `go test ./internal/app/... -run TestGovernanceGate && go vet ./internal/app`.

12. [ ] **[default-on]** In `internal/config/policy.go`, add `governanceExplicit bool` field to `Policy` (mirror `verificationExplicit` policy.go:236). Governance is parsed by TWO distinct paths (verify each):
    - **JSON path** (policy.go:411-422): the `if strings.HasPrefix(trimmed, "{")` branch. Extend the local `probe` struct (currently `{Verification *json.RawMessage}`, policy.go:417-418) with a `Governance *json.RawMessage \`json:"governance"\`` field, then set `p.governanceExplicit = probe.Governance != nil` next to policy.go:421.
    - **YAML path** (policy.go:424-478): governance is NOT handled by the line-scanner (the line-scanner SKIPS the `governance:` section, policy.go:591-594); it is parsed by `parseGovernanceBlock(raw)` at policy.go:473. To detect presence, have `parseGovernanceBlock` (governance_config.go:25) ALSO return a `present bool` (true when the top-level `governance:` mapping node exists in the parsed YAML), and set `p.governanceExplicit = present` at policy.go:473-477 before `normalizePolicy`. Do NOT change `parseGovernanceBlock`'s returned `GovernanceConfig` value — it still returns the zero config (`{Enabled:false}`) when absent; only ADD the presence boolean.

    In `normalizePolicy` (policy.go:509) add: `if !p.governanceExplicit { p.Governance.Enabled = true }` (place it near the `verificationExplicit` consumption at policy.go:536). Update the `DefaultGovernanceConfig()` doc comment (policy.go:267-271) to note the parse-path default is now ON via `normalizePolicy`, while the bare constructor still returns the zero `{Enabled:false}` for callers that want the explicit off. Files: `internal/config/policy.go`, `internal/config/governance_config.go` (add the `present bool` return). Test: `internal/config/governance_default_test.go` (Item 6 Testing block) — MUST cover BOTH the YAML path (the common `stoke.yaml` case) and at least one JSON-policy case. VERIFY: `go test ./internal/config/... -run TestGovernanceDefault && go vet ./internal/config`.

13. [ ] **[tests-default]** Grep `internal/config/*_test.go` and `internal/app/*_test.go` for existing assertions of governance default-off (verified at spec time: `Governance` appears in NO `internal/config/*_test.go` and `governor`/`Governance` in NO `internal/app/*_test.go` — so there are no existing default-off tests to flip). Regardless, ADD the new `internal/config/governance_default_test.go` asserting: absent block ⇒ `Enabled==true`; explicit `enabled:false` ⇒ `false`; explicit `enabled:true` ⇒ `true`; `governanceExplicit` set only when present. Confirm `internal/governance/governance_test.go` stays valid (it constructs the Governor directly, not via policy). Files: `internal/config/governance_default_test.go`. VERIFY: `go test ./internal/config/... && go test ./internal/governance/...`.

14. [ ] **[full-gate]** Run the CI gate and confirm green: `go build ./cmd/r1 && go test ./... && go vet ./...`. Confirm `go mod tidy` reports no changes (no new deps). Files: none (verification). VERIFY: `go build ./cmd/r1 && go test ./... && go vet ./...`.
