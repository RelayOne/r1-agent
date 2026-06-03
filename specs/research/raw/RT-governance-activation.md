# RT: Governance Activation (audit finding B1) — Integration Map

Status: research notes for a spec author. READ-ONLY investigation. All paths absolute-relative to
`/home/eric/repos/r1-agent`. Every claim is anchored to `file:line` against the working tree as of
branch `fix/desktop-tauri-types-and-vite-override` (first slice = commit c5e5e787).

## Context: what already exists (the landed slice)

`internal/governance/governance.go` defines `Governor`:

- `New(ctx, stateDir, missionID string, budgetUSD float64) (*Governor, error)` — governance.go:67.
  Opens `bus.New(<stateDir>/governance/bus)` (gov.go:72), `ledger.New(<stateDir>/governance/ledger)`
  (gov.go:77), constructs `supervisor.New(Config{Type: TypeMission, Scope:{MissionID}}, bus, ledger)`
  (gov.go:83-87), `sup.RegisterRules(manifests.MissionRules()...)` (gov.go:88), `sup.Start(ctx)` (gov.go:90).
- `HubSubscriber() hub.Subscriber` — gov.go:114. ID `governance.bridge.<missionID>`, `Events:["*"]`,
  `Mode: hub.ModeObserve`, `Handler: g.handle`.
- `handle(ctx, ev)` — gov.go:125. Switch on `ev.Type`: ONLY three cases wired today:
  - `hub.EventModelPostCall` → `onCost` (gov.go:144) → accumulates `ev.Model.CostUSD`, publishes
    bus event `Type:"mission.budget.update"` with payload `{"spent_usd","budget_usd"}` (gov.go:154-170).
  - `hub.EventTaskStarted` → `onTask(...,"started")` (gov.go:132).
  - `hub.EventTaskCompleted` → `onTask(...,"completed")` (gov.go:135).
  - `onTask` (gov.go:182) writes a ledger node `Type:"task"`, `SchemaVersion:1`, content
    `{title,description(=ev.Phase),state}` via `g.ledger.AddNode` (gov.go:195).
- `Close()` — gov.go:206. `closeOnce`-guarded: stops supervisor, closes ledger, closes bus.

Wiring into the live runtime — `internal/app/app.go`:
- `RunConfig.GovernanceEnabled bool` — app.go:122; `RunConfig.GovernanceBudgetUSD float64` — app.go:127.
- `Orchestrator.governor *governance.Governor` — app.go:134.
- Construction gate — app.go:218: `if cfg.EventBus != nil && (cfg.GovernanceEnabled || policy.Governance.Enabled)`.
  stateDir = `r1dir.JoinFor(cfg.RepoRoot)` (app.go:219); missionID = WorktreeName→TaskType→"mission"
  fallback (app.go:220-226); budget = `cfg.GovernanceBudgetUSD` (app.go:227). Failure is non-fatal
  (app.go:229-230). On success: `cfg.EventBus.Register(g.HubSubscriber())` + `orch.governor = g` (app.go:232-233).
- Close on run end — app.go:247-252 (`governor.Close()`).

Tests today — `internal/governance/governance_test.go`: `TestGovernorBudgetRuleFires` (budget rule at 90%),
`TestGovernorWritesLedgerTaskNode`, `TestGovernorCloseIsClean`, `TestGovernorBudgetWarningBelowSpawn` (60%).
`newTestGovernor` (gov_test.go:17) + `collect` bus-subscription helper (gov_test.go:29).

**The gap (B1):** governance maps only cost+task. The mission/workflow runtime emits ~12 more lifecycle
events that produce NO bus/ledger output, and the deterministic supervisor rules that depend on ledger
nodes (esp. the trust second-opinion gate) can never be satisfied because nothing writes the nodes they
query. Governance is also default-OFF with no kill-switch flag.

---

## 1. FULL hub → governance event mapping (what the live mission actually emits)

`hub.Event` payload structs (internal/hub/events.go): `Event` (events.go:247) carries `MissionID,TaskID,
WorktreeID,AgentID,Phase` plus optional `*ModelEvent`(295), `*GitEvent`(309), `*CostEvent`(319),
`*TestEvent`(355), `*LifecycleEvent`(347), `*SecurityEvent`(368). Note: `ModelEvent` has
`Provider,Model,InputTokens,OutputTokens,CachedTokens,CostUSD,Duration,StopReason,ToolCalls`;
`GitEvent` has `Operation,Branch,CommitHash,FilesChanged,Conflicts,Message`; `TestEvent` has
`Phase,Passed,Failed,Skipped,Coverage,Duration,FailedTests,NewTests,RemovedTests`;
`LifecycleEvent` has `Entity,State,Duration,Attempt`.

### Live emission sites (the per-task workflow engine — internal/workflow/workflow.go)

This is the engine that runs real tasks (the `sow`/`build` paths). `emitEvent` (workflow.go:2219,
sync, returns Decision) and `emitEventAsync` (workflow.go:2230, fire-and-forget). All are nil-safe via
`e.EventBus`.

| Lifecycle point | hub.EventType | Emit site | Payload available | Mapped today? |
|---|---|---|---|---|
| Task failed (deferred) | `EventTaskFailed` | workflow.go:243 | `TaskID`, `Lifecycle{task,failed}` | NO |
| Plan start | `EventMissionPlanStart` | workflow.go:489 | `TaskID,Phase=plan`, `Lifecycle{task,plan_start}` | NO |
| Plan done | `EventMissionPlanDone` | workflow.go:510 | `Model{Provider,InputTokens,OutputTokens,CostUSD}` | NO |
| Budget exceeded (pre-attempt gate) | `EventCostBudgetExceeded` | workflow.go:554 | `Cost{TotalSpent,BudgetLimit,PercentUsed=100,Threshold=exceeded}` | NO |
| Execute start (per attempt) | `EventMissionExecuteStart` | workflow.go:568 | `Phase=execute`, `Lifecycle{task,execute_start,Attempt}` | NO |
| Execute model post-call (cost) | `EventModelPostCall` | workflow.go:723 | `Model{Provider,InputTokens,OutputTokens,CachedTokens,CostUSD,Duration}` | YES (cost only) |
| Verify build/test/lint result | `EventVerifyBuildResult` | workflow.go:767 | `Phase=verify`, `Test{Phase=o.Name}` — **emitted once per outcome (build/test/lint), all under the SAME type `EventVerifyBuildResult`**; the per-outcome name is in `Test.Phase` | NO |
| Security scan result | `EventSecurityScanResult` | workflow.go:920 | `Security{Category=scan,Severity,Details}` | NO |
| Convergence start | `EventVerifyConvergenceStart` | workflow.go:975 | `Phase=convergence` | NO |
| Convergence result (pass) | `EventVerifyConvergenceResult` | workflow.go:1105 | `Phase=convergence` (no body) | NO |
| Pre-commit | `EventGitPreCommit` | workflow.go:1147 | `Git{Operation=commit,Branch,FilesChanged,Message}` | NO |
| Pre-merge | `EventGitPreMerge` | workflow.go:1177 | `Git{Operation=merge,Branch,Message}` | NO |
| Post-merge | `EventGitPostMerge` | workflow.go:1211 | `Git{Operation=merge,Branch,FilesChanged}` | NO |
| Task completed | `EventTaskCompleted` | workflow.go:1221 | `Lifecycle{task,completed,Attempt}`, `Model{CostUSD=result.TotalCostUSD}` | YES (task node) |

### Cross-model review verdict — **NOT EMITTED ANYWHERE** (the critical hole for the trust rule)

`runCrossModelReview` (workflow.go:1297) computes `verdict, _ := parseReviewVerdict(...)` (workflow.go:1422)
and sets `evidence.ReviewPass = verdict.Pass` (workflow.go:1474). On approval it advances state
`taskstate.Reviewed` (workflow.go:1547) and returns the validated file list. **It emits NO hub event** —
`EventVerifyCrossModelReview` (events.go:93) has zero producers anywhere in the tree (verified:
`grep EventVerifyCrossModelReview` outside events.go/tests = none). Therefore today the governance bridge
cannot observe a review verdict, so it cannot write a `review.agree`/`review.dissent` ledger node, so the
trust rule (§4) can never be satisfied. **This is the single most important missing emission.**

### Live emission sites (the mission convergence runner — internal/mission/handlers.go)

`emitMissionEvent` (handlers.go) is the mission-loop emitter; `deps.EventBus` is the same `*hub.Bus`.
- `EventMissionResearchStart` — handlers.go:309 (`TaskID=m.ID,Phase=research`).
- `EventMissionPlanStart` — handlers.go:567 (`Phase=plan`).
- `EventMissionExecuteStart` — handlers.go:673 (`Phase=execute`).
- `EventMissionValidateStart` — handlers.go:1101 (`Phase=validate`).
- `EventMissionConsensusStart` — handlers.go:1592 (`Phase=consensus`).
- generic `bus.Emit(ctx, ev)` — handlers.go:1812.
Note: `EventMissionConverged` / `EventMissionFailed` (events.go:22-23) exist but were NOT found emitted
in handlers.go; spec author should grep once more before assuming a converged emitter exists.

### Recommended per-event mapping (hub event → bus event Type + ledger node)

For each lifecycle event the Governor's `handle` switch (gov.go:129) should add a case. Two output channels
exist: (a) publish a v2 `bus.Event` (drives supervisor rules), (b) `ledger.AddNode` (populates the graph).
`AddNode` accepts ANY `Type` string and only requires `Type`, `Content`, `SchemaVersion>=1`
(ledger.go:242-251) — it does NOT call `nodes.*.Validate()`. So the Governor may write raw nodes with
arbitrary type strings; the `internal/ledger/nodes` structs are convenience/registry helpers, not enforced.

| hub event | → bus.Event Type | → ledger node (Type string) |
|---|---|---|
| `EventMissionPlanStart`/`Done` | (optional) `plan.started`/`plan.done` | `task` node granularity=mission OR a dedicated plan record; or a `draft` (loop_type=sow) |
| `EventModelPostCall` | `mission.budget.update` (exists) | (none / cost_record) |
| `EventVerifyBuildResult` (build/test/lint) | (optional) `verify.<name>.result` | `verification_evidence` node (one per check) |
| `EventVerifyCrossModelReview` **(must be newly emitted)** | `worker.declaration.done` (to TRIGGER trust rule) AND a `review.agree`/`review.dissent` ledger node (to SATISFY it) | `review.agree` if `verdict.Pass`, else `review.dissent` |
| `EventVerifyConvergenceResult` | (optional) drive loop `converged` transition | `loop` transition (§3) |
| `EventGitPreMerge`/`PostMerge` | (optional) `merge.started`/`merge.done` | a merge/decision node (§2) |
| `EventTaskStarted`/`Completed` | (optional) `worker.action.*` | `task` node (exists) |
| `EventCostBudgetExceeded` | `mission.budget.update` with spent==budget | (none) |

---

## 2. LEDGER NODE TYPES available (internal/ledger/nodes/)

`AddNode` writes by raw `Type` string. The registered structs (each `init()` calls
`Register(<type>, ...)`) and their `NodeType()` literal:

- **task** — nodes/task.go:10. `NodeType()="task"` (task.go:40). Fields: `Granularity` (must be one of
  mission/feature/milestone/branch/ticket/sub_ticket, task.go:30), `Title`, `Description`, `State`
  (proposed/assigned/in_progress/in_review/done/superseded/cancelled, task.go:35), `AcceptanceCriteria []string`
  (required-non-empty), `CreatedAt`, `CreatedBy`, optional `AssignedToStanceRole,ParentTaskRef,Dependencies`.
  NOTE: the landed Governor writes a `task` node with a SIMPLER ad-hoc shape `{title,description,state}`
  (gov.go:174) that does NOT satisfy `nodes.Task.Validate()` — acceptable because AddNode skips Validate.
- **verification_evidence** — nodes/verification.go:23. `NodeType()="verification_evidence"` (verification.go:77).
  Fields: `SubjectRef,SubjectKind,ProducerModel,VerifierModel` (producer≠verifier enforced by Validate,
  verification.go:100), `Verdict` ∈ {agree,disagree,partial,insufficient} (verification.go:80), `EvidenceRefs []string`,
  `Notes`, `CrossFamily bool`, `When`. **This is the node a build/test/lint verify result should write.**
- **agree** — nodes/agree_dissent.go:10. `NodeType()="agree"` (agree_dissent.go:23). Fields: `DraftRef,
  AgreeingStanceID,AgreeingStanceRole,Reasoning,CreatedAt`, optional `Caveats`.
- **dissent** — nodes/agree_dissent.go:51. `NodeType()="dissent"` (agree_dissent.go:70). Fields: `DraftRef,
  DissentingStanceID,DissentingStanceRole,Reasoning,RequestedChange,Severity`∈{blocking,advisory},`CreatedAt`.
- **draft** — nodes/draft.go:11. `NodeType()="draft"`. Fields: `DraftType`∈{prd,sow,ticket_definition,pr,
  refactor_proposal,fix,judge_verdict_draft}, `LoopRef,ProposingStanceID,Content (raw),CreatedAt`, optional `Supersedes`.
- **loop** — nodes/loop.go:10. `NodeType()="loop"`. Fields: `State,LoopType,ArtifactRef,ConvenedPartners,
  IterationCount,ProposingStanceRole,TaskDAGScope,CreatedAt,CreatedBy`, optional `ParentLoopRef,TerminalReason`.
- Others present: `decision.go`, `artifact.go`/`artifact_annotation.go`, `escalation.go`, `hitl.go`,
  `research.go`, `skill.go`, `snapshot.go`, `supervisor.go`, `memory.go`, `beacon.go`, `trust.go`,
  `system_prompt_fingerprint.go`, `execution_env.go`, `admin_viewed.go`.

### ⚠️ NODE-TYPE NAMING SPLIT (load-bearing bug the spec MUST resolve)

There are TWO different type strings for "a reviewer agreed", and they do not match:

1. The registered node type is **`"agree"`** (agree_dissent.go:23) / **`"dissent"`** (:70). The loops
   tracker counts these exact strings: `case "agree"` / `case "dissent"` (loops.go:251-253).
2. The trust rules and partner-timeout rule query the literal **`"review.agree"`** /
   **`"review.dissent"`**:
   - `trust/completion_requires_second_opinion.go:69`: `l.Query(..., QueryFilter{Type: "review.agree"})`.
   - `trust/fix_requires_second_opinion.go:53`: same `"review.agree"`.
   - `consensus/partner_timeout.go:72-73`: `n.Type == "review.agree"`, `n.Type == "review.dissent"`.

Nothing in the tree writes a node of type `"review.agree"` (grep confirms only the queries reference it).
So **the trust gate is currently un-satisfiable by construction.** The spec must pick ONE of:
- (A) Have the governance bridge write `review.agree`/`review.dissent` nodes (the literal strings the rules
  query) from the cross-model review verdict. Lowest-risk: matches the rule queries verbatim, no rule edits.
- (B) Change the trust+partner_timeout rules to query `"agree"`/`"dissent"` and write `nodes.Agree`/`Dissent`.
  Higher-risk: must verify no other consumer depends on `review.agree`, and `nodes.Agree.Validate()` requires
  `DraftRef` (a real draft node) which the per-task workflow has no concept of.
Recommendation: (A) — write a minimal `review.agree` node `{task_id}` (matching `agreeContent`, §4) so the
existing rule query at trust.go:69 succeeds with zero rule changes.

---

## 3. ledger/loops — loop state machine API (internal/ledger/loops/loops.go)

`Tracker` over a `*ledger.Ledger`: `NewTracker(l)` (loops.go:104). Seven states (loops.go:25-43):
`StateProposing`→`StateDrafted`→`StateConvening`→`StateReviewing`→`StateResolvingDissents`→terminal
`StateConverged`/`StateEscalated` (terminalStates: loops.go:46).

API:
- `TransitionState(ctx, loopID string, newState LoopState, reason string) error` — loops.go:344. Resolves
  current loop node, mutates `loopContent.State`+`Reason`, writes a NEW `loop` node, links it with a
  `EdgeSupersedes` edge to the prior (loops.go:363-384). **There is NO `CreateLoop`/`NewLoop` in this
  package** — `TransitionState` requires a pre-existing loop node to resolve (loops.go:345). To bootstrap a
  loop the bridge must `ledger.AddNode` a `loop` node directly (Type `"loop"`, content per the loops.go
  internal `loopContent` shape: `{state,loop_type,artifact_id,convened_partners,reason}` — loops.go:73-80;
  note this is a DIFFERENT, looser shape than `nodes.Loop`).
- Query helpers: `Get` (:119), `CurrentDraft` (:163), `IterationCount` (:188), `IsConverged` (:204 — all
  convened partners have agree nodes AND zero dissents on current draft), `countStances` (:236 — counts
  `"agree"`/`"dissent"` reachable backward over `EdgeReferences`), `ActiveLoops` (:305), `Children`/`ParentChain`.

### Recommended lifecycle → transition mapping (if the spec wires loops)
- plan start → AddNode loop `state=proposing`.
- plan done / draft produced → `TransitionState(loopID, StateDrafted)`.
- review convened → `StateConvening` → `StateReviewing`.
- cross-model review verdict: pass → `StateConverged`; dissent → `StateResolvingDissents`.
- convergence result (workflow.go:1105) → `StateConverged`.
- retry budget exhausted / escalate (workflow.go:1268) → `StateEscalated`.
Wiring loops is OPTIONAL for the minimal B1 fix — the trust rule only needs review.agree nodes (§4), not
a loop. Treat loop wiring as a stretch checklist item.

---

## 4. TRUST RULE — completion_requires_second_opinion.go (FULLY DOCUMENTED)

File: `internal/supervisor/rules/trust/completion_requires_second_opinion.go`.

- `Name()` = `"trust.completion_requires_second_opinion"` (:27).
- `Pattern()` = `bus.Pattern{TypePrefix: string(bus.EvtWorkerDeclarationDone)}` (:31) — i.e. it fires on a
  bus event whose Type is **`"worker.declaration.done"`** (`bus.EvtWorkerDeclarationDone`, bus.go:37).
  **The Governor does NOT currently publish this event** → the trust rule never even triggers today.
- `Priority()` = 100 (:34).
- Expected trigger payload (`declarationPayload`, :41): JSON `{"worker_id","task_id","artifact_id"}`.

**Evaluate (does the rule FIRE?)** — :52:
1. Unmarshal declaration payload (:53). taskID = `dp.TaskID`, falling back to `evt.Scope.TaskID` (:58-61).
   If both empty → returns `true` (FIRE — require review to be safe, :63).
2. `nodes, _ := l.Query(ctx, QueryFilter{Type: "review.agree"})` (:69). **Literal query type = `"review.agree"`.**
3. For each agree node: skip if `n.CreatedBy == evt.EmitterID` (:72, same worker doesn't count); unmarshal
   `agreeContent{task_id}` (:48-50, :75); if `ac.TaskID == taskID` → returns `false` (do NOT fire — second
   opinion exists, :80).
4. Otherwise returns `true` (FIRE — no independent agree found, :84).

**Action (what firing does)** — :87: publishes `bus.EvtWorkerPaused` ("worker.paused") with
`{worker_id,reason:"awaiting_review"}` (validated by `WorkerPausedSchema`, :103/:140), then publishes
`"supervisor.spawn.requested"` with `{role:"Reviewer",artifact_id,task_id,worker_id}` (:117-130).

**Therefore, to make the trust rule behave correctly the bridge must, at task completion:**
- (a) **NOT fire when a real cross-model review approved:** BEFORE publishing the declaration event, write a
  ledger node of literal Type **`"review.agree"`** with `CreatedBy` ≠ the declaration's `EmitterID`, content
  `{"task_id": <taskID>}` (matching `agreeContent`). Source = `verdict.Pass == true` in
  `runCrossModelReview` (workflow.go:1474). The CreatedBy must be a distinct reviewer identity
  (e.g. `"governance.reviewer"` or the reviewer model name from `evidence.ReviewEngine`, workflow.go:1393),
  NOT the same emitter id the declaration uses, or the `n.CreatedBy == evt.EmitterID` skip (:72) drops it.
- (b) **Fire when missing:** if no cross-model review ran (policy `CrossModelReview` off, workflow.go:959→965
  sets `evidence.ReviewOutput="cross-model review disabled by policy"`) or the verdict was a dissent,
  write NO `review.agree` node, then publish the `worker.declaration.done` bus event → Evaluate finds no
  matching agree → fires → pauses worker + spawns Reviewer.

Where the review verdict lives to drive this: `runCrossModelReview` workflow.go:1422 (`verdict`),
:1474 (`verdict.Pass`). A `review.agree`/`review.dissent` ledger write must be inserted there (today there
is none). The triggering `worker.declaration.done` bus publish must be emitted at task completion
(workflow.go:1221 `EventTaskCompleted` is the natural hub hook to translate).

Sibling rules with the SAME pattern (will also need the same node, or will mis-fire):
`trust/fix_requires_second_opinion.go` (queries `review.agree`, :53), `trust/problem_requires_second_opinion.go`,
and `antitrunc/scope_underdelivery.go` (Pattern also `EvtWorkerDeclarationDone`, scope_underdelivery.go:42)
— so publishing `worker.declaration.done` will also activate the anti-truncation scope check. The spec must
account for the declaration payload carrying enough for scope_underdelivery too.

---

## 5. CLI FLAG — adding `--governance` (model on the `--specexec` pattern)

`--specexec` pattern (the template to copy):
- `RunConfig.SpecExec bool` field — cmd/r1/main.go:116.
- Flag def: `specExec := fs.Bool("specexec", false, "...")` — main.go:1185 (build cmd), main.go:1689 (sow cmd).
- Wired into RunConfig: `SpecExec: *specExec` — main.go:1465 (build), main.go:3415 (sow).

### RunConfig construction sites that must set the new fields
1. **main.go:412** — `appCfg := app.RunConfig{...}` (the `sow_native`/per-task dispatch path). Add
   `GovernanceEnabled: <resolved>` and `GovernanceBudgetUSD: <budget>`. `cfg` here is the parent
   `runBuildConfig`-style struct; the budget already in scope is `*costBudget` (see below) carried as
   `cfg.CostBudgetUSD` (main.go:3087).
2. **buildRunConfig (main.go:6563)** — the shared builder used by other paths. RunConfig literal at
   main.go:6564; opts struct `buildRunConfigOpts` (main.go:6542) currently carries Boulder/CostTracker/
   TestGraph/RepoMap/EventBus and is applied at main.go:6589-6595. Add `GovernanceEnabled`/`GovernanceBudgetUSD`
   to `buildRunConfigOpts` and set `cfg.GovernanceEnabled = opts.GovernanceEnabled` there.
3. Other RunConfig literals to keep consistent: main.go:1104 (`run` cmd), main.go:3942 (`plan` cmd) — these
   may legitimately leave governance at the default.

### Budget source (GovernanceBudgetUSD)
- The `sow` cost budget flag is `costBudget := fs.Float64("cost-budget", 0, "...")` — main.go:1709.
- It flows into the run config as `CostBudgetUSD: *costBudget` — main.go:3087.
- The live `costtrack.Tracker` is created with budget 0 today (main.go:327, :1074, :1380 all
  `costtrack.NewTracker(0, ...)`), with budget enforcement via `OverBudget()`/`BudgetRemaining()`
  (tracker.go:267/:275). For governance, source `GovernanceBudgetUSD` directly from `*costBudget`
  (sow) — that is the only user-facing $ budget in scope. When unset (0), the budget rule no-ops
  (gov.go:60-63 comment; drift.BudgetThreshold treats non-positive budget as no-op).

### Adding the flag + kill-switch
- `--governance` (bool, default reflects the new default-on decision in §6).
- `--no-governance` kill-switch: add a second `fs.Bool("no-governance", false, ...)`; resolve
  `enabled := !(*noGovernance) && (default || *governance || policy.Governance.Enabled)`. Because the
  app.go gate (app.go:218) already ORs `cfg.GovernanceEnabled || policy.Governance.Enabled`, the cleanest
  kill-switch is: compute the final bool in `cmd/r1` and pass it as `GovernanceEnabled`, while ALSO honoring
  `--no-governance` by forcing `GovernanceEnabled=false` AND requiring the app.go gate to respect an explicit
  off (see §6 — today app.go ORs policy, so a flag-level false can't override a policy-level true; the spec
  must decide whether `--no-governance` overrides policy. Recommended: make app.go gate
  `(cfg.GovernanceEnabled || policy.Governance.Enabled) && !cfg.GovernanceDisabled`).

---

## 6. DEFAULT-ON plumbing + kill-switch

Current default: `policy.Governance.Enabled` defaults to the **zero value (false)** — `GovernanceConfig`
(policy.go:263) is `{Enabled bool}`; `DefaultGovernanceConfig()` (policy.go:269) returns `{Enabled:false}`
but is NOT called on the parse path. `parseGovernanceBlock` (governance_config.go:25) returns the zero
config when the `governance:` block is absent (policy.go:472-477). So an absent block == disabled today.

### Problem: distinguishing "absent" from "explicitly false"
Flipping the default to true naively (`DefaultGovernanceConfig`→`{Enabled:true}`) does NOT help because the
parse path uses the zero value, not the default constructor. To make default-ON with a kill-switch you need
the same trick the verification block uses: `verificationExplicit bool` (policy.go:236) distinguishes
"all gates intentionally disabled" from "section omitted". Mirror it:
- Add `governanceExplicit bool` to `Policy` (alongside policy.go:236).
- In `parseGovernanceBlock`, set it true when a `governance:` mapping node is present.
- In `normalizePolicy` (policy.go:509): `if !p.governanceExplicit { p.Governance.Enabled = true }`.
- The user kill-switch is then `governance:\n  enabled: false` in `stoke.yaml` (explicit → honored), plus the
  `--no-governance` CLI flag (§5).

### app.go gate change for a hard kill-switch
The gate at app.go:218 ORs flag||policy. For `--no-governance` to override a default-on policy, add a
`RunConfig.GovernanceDisabled bool` and change the gate to:
`if cfg.EventBus != nil && !cfg.GovernanceDisabled && (cfg.GovernanceEnabled || policy.Governance.Enabled)`.

### Tests asserting default-OFF that must be updated
- `internal/config` policy tests: any test asserting `policy.Governance.Enabled == false` for an absent
  block (grep `Governance` in internal/config/*_test.go). Spec author MUST grep and enumerate; at minimum a
  new test that an ABSENT block yields Enabled==true and an EXPLICIT `enabled:false` yields false.
- `internal/governance/governance_test.go` stays valid (it constructs Governor directly, not via policy).
- `internal/app` tests (if any) that assert `governor == nil` by default would flip; grep `governor` in
  internal/app/*_test.go.

---

## 7. TEST PATTERN — synthetic-event integration test (no live LLM)

Two proven patterns to combine:

**(a) Governor hub-handler driving (governance_test.go):**
- `newTestGovernor(t, budgetUSD)` (gov_test.go:17) → `New(ctx, t.TempDir(), "mission-test", budget)`.
- `collect(t, g.Bus(), prefix)` (gov_test.go:29) → subscribes via `g.Bus().Subscribe(bus.Pattern{TypePrefix},
  fn)` and returns a snapshot accessor + cancel.
- Drive a synthetic hub event straight through the handler: `sub := g.HubSubscriber();
  sub.Handler(ctx, &hub.Event{Type: ..., Model/TaskID/...})` (gov_test.go:64, :103). No engine, no LLM.
- Poll with a ~2s deadline because bus delivery + rule eval are async (gov_test.go:70-82).
- Assert ledger via `g.Ledger().Query(ctx, ledger.QueryFilter{Type: "task"})` (gov_test.go:109).

**(b) Bridge synthetic-event assertions (bridge_test.go):**
- `setup(t)` (bridge_test.go:19) opens a real `bus.New(tmp/wal)` + `ledger.New(tmp/ledger)` with cleanups.
- `collectEvents(t, b, prefix)` (bridge_test.go:39) — identical subscription helper.
- Pattern: call the bridge method → poll for the bus event type → unmarshal payload → then
  `l.Query(ctx, QueryFilter{Type:"verification"|"cost_record"|"audit_report"})` and assert node count
  (bridge_test.go:96-103, :186-193). This is the template for "assert a ledger node was written per event".

**The B1 integration test the spec should specify (combining both):**
1. Build a Governor (pattern a) with budget set.
2. For EACH lifecycle event in §1, call `sub.Handler(ctx, &hub.Event{Type: <event>, ...})` with a synthetic
   payload.
3. Assert (pattern b) a ledger node of the expected Type exists (`task`, `verification_evidence`,
   `review.agree`, etc.) — node-count + key fields.
4. **Trust-rule fire/no-fire test (the headline assertion):**
   - No-fire: write a `review.agree` node `{task_id:"T1"}` with `CreatedBy:"reviewer"`, then publish a
     synthetic `bus.Event{Type: bus.EvtWorkerDeclarationDone, EmitterID:"worker", Scope:{TaskID:"T1"},
     Payload: {"task_id":"T1","worker_id":"worker"}}` on `g.Bus()`. Subscribe to `"worker.paused"` and
     `"supervisor.spawn.requested"`; assert NEITHER is published within the deadline.
   - Fire: same declaration but with NO `review.agree` node present → assert `worker.paused` AND
     `supervisor.spawn.requested` ARE published.
   This directly exercises trust.go:52-133 with zero LLM. It also pins the §2 naming-split fix: if the
   bridge writes `"agree"` instead of `"review.agree"`, the no-fire case will (incorrectly) still fire,
   catching the bug.

---

## Spec recommendations (concrete checklist items)

1. **[review-emit]** In `internal/workflow/workflow.go` `runCrossModelReview` (workflow.go:1422-1480), emit a
   hub `EventVerifyCrossModelReview` carrying the verdict (Pass + Severity + reviewer engine
   `evidence.ReviewEngine`). Currently NO producer of this event exists — this is the root unblocker.
   Include `TaskID=name`, `Model{Provider: verifyRunnerName}`, and a `Custom["review_pass"]` bool (or a new
   typed field). Verify with a unit test on the emit.
2. **[gov-map-review]** In `internal/governance/governance.go` `handle` (gov.go:129) add a case for the new
   review event: on pass, `ledger.AddNode(Node{Type:"review.agree", SchemaVersion:1, CreatedBy:"governance.reviewer",
   MissionID, Content: {"task_id": ev.TaskID}})`; on dissent write `"review.dissent"`. CreatedBy MUST differ
   from the declaration EmitterID (trust.go:72).
3. **[gov-emit-declaration]** In `handle`, translate `EventTaskCompleted` (and/or a new declaration hook) into
   a v2 `bus.Publish(Event{Type: bus.EvtWorkerDeclarationDone, EmitterID:"governance.worker", Scope:{MissionID,
   TaskID}, Payload: {"task_id","worker_id","artifact_id"}})` — AFTER the review.agree node is written, so the
   trust rule (trust.go:52) sees the node and does NOT fire on approved work.
4. **[gov-map-verify]** Map `EventVerifyBuildResult` (workflow.go:767, one per build/test/lint via `Test.Phase`)
   → `verification_evidence` ledger nodes (nodes/verification.go) with SubjectKind="task", distinct
   Producer/Verifier model strings (Validate requires producer≠verifier, verification.go:100).
5. **[gov-map-lifecycle]** Add `handle` cases for plan start/done (workflow.go:489/510), execute start
   (workflow.go:568), convergence start/result (workflow.go:975/1105), pre/post merge (workflow.go:1147/1177/1211),
   budget-exceeded (workflow.go:554), each writing the node/bus output from the §1 table.
6. **[naming-fix]** Resolve the `"review.agree"` vs `"agree"` split (§2): adopt option (A) — write
   `"review.agree"`/`"review.dissent"` literal types (matches trust.go:69, fix_requires_second_opinion.go:53,
   partner_timeout.go:72) — so NO supervisor rule edits are needed. Document why `nodes.Agree` (type "agree")
   is NOT used here.
7. **[cli-flag]** Add `--governance`/`--no-governance` bool flags on the `build` (main.go:1165) and `sow`
   (main.go:1633) flag sets, modeled on `--specexec` (main.go:1185/1689). Resolve to a single bool.
8. **[runconfig]** Set `GovernanceEnabled` + `GovernanceBudgetUSD` at the RunConfig sites main.go:412 and
   buildRunConfig main.go:6564 (extend `buildRunConfigOpts` main.go:6542). Source `GovernanceBudgetUSD` from
   `*costBudget`/`CostBudgetUSD` (main.go:1709/3087).
9. **[kill-switch + app gate]** Add `RunConfig.GovernanceDisabled bool`; change the gate at app.go:218 to
   `... && !cfg.GovernanceDisabled && (cfg.GovernanceEnabled || policy.Governance.Enabled)`.
10. **[default-on]** Add `governanceExplicit bool` to `Policy` (mirror `verificationExplicit`, policy.go:236);
    set it in `parseGovernanceBlock` (governance_config.go:25); in `normalizePolicy` (policy.go:509) default
    `Governance.Enabled=true` when not explicit. Update `DefaultGovernanceConfig()` (policy.go:269) doc.
11. **[tests-default]** Add/adjust config tests: absent block ⇒ Enabled==true; explicit `enabled:false` ⇒
    false; grep internal/config + internal/app tests for existing default-off assertions and update.
12. **[integration-test]** Add `internal/governance/*_test.go` synthetic-event test per §7: drive every
    lifecycle event through `HubSubscriber().Handler`, assert ledger nodes by Type, and assert the trust rule
    fires (no review.agree) / does-not-fire (review.agree present) by subscribing to `worker.paused` +
    `supervisor.spawn.requested`. No live LLM.
13. **[loops — stretch]** Optionally drive `loops.Tracker.TransitionState` (loops.go:344) from plan→review→
    converge events; bootstrap loops via a raw `loop` AddNode (no CreateLoop exists). Mark optional.
