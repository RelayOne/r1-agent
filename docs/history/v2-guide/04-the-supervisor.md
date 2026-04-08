# 04 — The Supervisor

The supervisor is the process that holds Stoke's rules and enforces them. Workers do not know the rules exist. Workers emit events as they work; the supervisor watches the events; when a rule says "now an X must happen," the supervisor fires a hook on the bus that injects the forced action. From a worker's perspective, the supervisor is invisible — workers only experience the consequences of supervisor actions, never the supervisor itself.

This is the load-bearing structural property of Stoke: **workers cannot bypass the rules because they cannot see the rules.** The only way for a worker to interact with the rest of Stoke is to emit events on the bus, and the bus's hook authority is privileged so that only the supervisor (and components the supervisor explicitly authorizes) can register hooks. A worker that tries to declare itself done has its declaration intercepted by a supervisor hook that pauses it and spawns a fresh-context Reviewer. A worker that tries to modify snapshot code has its action intercepted by a supervisor hook that pauses it and convenes a CTO consultation. The interception is invisible to the worker — the worker just sees that its declaration was followed by a request to address feedback, or that its action was followed by a redirect.

The supervisor is the second-most-load-bearing component in Stoke after the ledger. It is where the team's behavior is actually defined. The team roster says who exists; the ledger says what they remember; the bus says what they emit; the supervisor says what they are required to do.

This file specifies what the supervisor is, what its rules are, how the rules are organized, and how the three supervisor configurations (mission, branch, SDM) relate to each other.

---

## What the supervisor is

The supervisor is a long-running process, written in Go, that ships as part of Stoke's runtime. It is not a stance. It does not have a session or a system prompt. It does not call models. Its behavior is entirely deterministic given (rules + events + ledger state), which makes it testable in isolation: given a synthetic event stream and a ledger snapshot, the supervisor's actions are a pure function and can be asserted with normal Go tests.

The supervisor's core is a small piece of code — a few hundred lines — that does the following loop:

1. Read the next event from the bus subscription
2. Match the event against the registered rules in priority order
3. For each matching rule, evaluate the rule's condition (which may involve querying the ledger)
4. For each rule whose condition is met, fire the rule's hook action through the bus's hook API
5. Advance the cursor and repeat

The hook actions themselves go through the bus's privileged hook authority — they are not direct calls into other Stoke components. The supervisor never reaches into a worker's session, never touches the ledger except through the normal `AddNode`/`AddEdge` API, never bypasses any other component's validation. Everything the supervisor does is observable on the bus and recorded in the ledger.

**The supervisor does not contain rule logic in its core.** The core just reads events, matches rules, fires hooks. The rule logic — which event types to match, what conditions to evaluate, what hook actions to take — lives in named rule files under `internal/supervisor/rules/`. The core is an engine; the rules are the program the engine runs.

---

## The three supervisor configurations

There is one supervisor codebase, parameterized by which rules it loads at startup. Three configurations are defined, each loading a different rule set:

**Mission supervisor.** One per user-invoked mission. Topmost authority. Loads the trust rules, consensus rules, snapshot rules, cross-team rules, hierarchy rules (the parent-side ones), and drift detection rules. The mission supervisor is the only thing that can escalate to the user. It is the only thing that can agree to a branch supervisor's completion proposal. It is what the user is actually talking to (through the PO) when the user invokes Stoke.

**Branch supervisor.** One per active branch in a multi-branch mission. Subordinate to the mission supervisor. Loads the trust rules, consensus rules, snapshot rules, the hierarchy rules (the subordinate-side ones for forwarding completion proposals upward and forwarding escalations upward), and the iteration-threshold drift rule. Does not load the user-escalation rule (only the mission supervisor escalates to the user) or the parent-agreement rule (only the mission supervisor evaluates branch completion proposals).

**SDM supervisor.** One per multi-branch mission. A peer of the mission supervisor, not a layer between mission and branches. Loads only the SDM detection rules. Does not load any enforcement rules. Its job is to watch the cross-branch event stream, detect collisions, dependencies, drift, and duplicate work, and emit advisory events that other supervisors consume. The SDM has no direct authority over workers, branches, or the mission — it produces structured warnings, and the mission supervisor and branch supervisors are the consumers of those warnings.

All three configurations run the same supervisor core. The difference between them is:

- **Which rules are loaded** at startup (per-configuration manifest file)
- **What scope the bus subscription is filtered to** (mission scope for the mission supervisor, branch scope for branch supervisors, multi-branch event types for the SDM)
- **What hook authorities the configuration has** (mission and branch supervisors can pause workers and spawn stances; the SDM can only emit advisory events)

---

## The supervisor hierarchy

The supervisor hierarchy is the chain of authority: mission supervisor at the top, branch supervisors below it as subordinates, the SDM supervisor as a peer with detection-only scope.

**Mission supervisor responsibilities relative to branches.**

- Spawns branch supervisors when the SOW decomposes the mission into multiple parallel branches
- Spawns the SDM supervisor when the mission has more than one active branch
- Receives `branch.completion.proposed` events from branch supervisors and evaluates them through the parent-agreement rule
- Receives `escalation.forwarded` events from branch supervisors when an in-branch issue cannot be resolved at the branch level
- Stops branch supervisors when their branch is closed (either by agreement or by mission cancellation)
- Stops the SDM supervisor when the mission collapses to a single active branch or completes

**Branch supervisor responsibilities relative to the mission.**

- Reports completion proposals upward via `branch.completion.proposed` events
- Reports escalations upward via `escalation.forwarded` events when the branch supervisor cannot resolve an issue with its own rules
- Pauses its own work in flight if the mission supervisor goes down (the parent has to be available to agree to anything; without the parent, subordinates cannot make terminal decisions)

**SDM supervisor responsibilities.**

- Subscribes to cross-branch event types from all active branches
- Maintains cross-branch state in the ledger as task-DAG annotation nodes (specifics in component 5)
- Emits advisory events when collisions, dependencies, or duplicate work are detected
- Does not pause workers, spawn stances, transition state, or escalate
- Continues running independently of the mission supervisor's enforcement actions; if the mission supervisor goes down, the SDM continues to detect and emit advisories, but no other supervisor is acting on them until the mission supervisor recovers

**Crash recovery rules.**

- If a branch supervisor crashes, the mission supervisor restarts it from the ledger state plus the bus event log
- If the mission supervisor crashes, branch supervisors continue running but pause all parent-agreement-required actions until the mission supervisor recovers
- If the SDM supervisor crashes, the mission supervisor restarts it from the ledger state; SDM detections are not re-emitted on restart (the missed events are visible in the bus event log via replay if needed for post-mortem)
- The mission supervisor's own state is in the ledger (the rule that fires on a `mission.checkpoint` event commits the mission supervisor's current state as a ledger node), so a mission supervisor crash recovers from the most recent checkpoint

---

## The rule taxonomy

There are eight enforcement categories with twenty-four rules total, plus one detection category with five rules loaded only on the SDM, plus one trigger rule for the skill manufacturer. Thirty named rules total, distributed across the three supervisor configurations.

Three additions from the original taxonomy: the consensus partner timeout rule (in category 2) handles unresponsive consensus partners through a delayed event scheduled at spawn time. The research category (category 7) handles uncertainty by pausing the requesting stance and dispatching researcher stances, with the report flowing back to the requester before the requester resumes. The skill governance category (category 9) handles audit and review of skill use, including the consensus-driven workflow for importing external skills. All additions are event-driven — there are no polling loops anywhere in the supervisor.

Each rule has the same fields: a name, an event pattern it fires on, a condition the condition evaluator checks, a hook action it takes when the condition is met, a rationale (the prose justification for why the rule exists), and a configurability surface (the knobs the wizard exposes for the user to adjust). The full schemas are in component 5; this file lists the rules with brief descriptions.

### Category 1: Trust rules

These are the load-bearing trust rules. They cannot be disabled. The wizard can configure their *strength* (whether second-opinion Reviewers must use a different model family when available, how many fix cycles can run before the dispute escalates, what the timeout for second-opinion completion is) but not their existence. Loaded on the mission supervisor and on branch supervisors.

**`trust.completion.requires_second_opinion`** — fires on `worker.declaration.done`. Condition: the declaring worker has not already had a fresh-context Reviewer agree to the declaration. Action: pause the declaring worker; spawn a fresh-context Reviewer with the artifact, the ticket, the SOW, the PRD, the original user intent, and the relevant slice of the ledger projected as the Reviewer's concern field. Unpause the worker only after the Reviewer commits an agree node, or after a dissent has been resolved through another iteration of the consensus loop. Rationale: never trust a worker's "I'm done" claim without external verification. The worker's self-assessment is the weakest signal in the system.

**`trust.fix.requires_second_opinion`** — fires on `worker.fix.completed`. Condition: the fix is in response to a prior dissent or a prior failed verification. Action: pause the fixing worker; spawn a fresh-context Reviewer to answer "did this fix actually address the failure or did the worker paper over it." Same unpause rule as the completion check. Rationale: never trust a worker's "I fixed it" claim. Reflexion-style fix loops have documented regression rates when self-evaluated.

**`trust.problem.requires_second_opinion`** — fires on `worker.escalation.requested` when the escalation type is `task.infeasible` or `task.blocked`. Condition: the escalating worker has not already had a fresh-context Reviewer agree that the escalation is justified. Action: pause the escalating worker; spawn a fresh-context Reviewer to answer "is this actually infeasible or did the worker give up too early." If the Reviewer confirms, the escalation propagates upward. If the Reviewer rejects, the worker is sent back to keep working with the Reviewer's reasoning attached. Rationale: never trust a worker's "this is impossible" claim. Premature termination is a documented frequent failure mode in autonomous coding agents.

### Category 2: Consensus rules

These enforce the consensus loop's structural convergence criterion. The loop is a state field on a ledger node; these rules drive state transitions on the loop node. Loaded on the mission supervisor and on branch supervisors.

**`consensus.draft.requires_review`** — fires on `ledger.node.added` when the new node is a draft (PRD draft, SOW draft, PR, refactor proposal, or any node with a `draft` status). Condition: the loop the draft belongs to is in a state that requires review (most loop states do). Action: spawn the convened consensus partners as fresh-context stances per the team roster's consensus posture rules for the draft's node type. Each partner gets the draft, their projected concern field, and the prompt to review. Rationale: a draft is not a decision until the convened partners have reviewed it.

**`consensus.dissent.requires_address`** — fires on `ledger.node.added` when the new node is a dissent attached to a draft. Action: transition the loop to `resolving dissents` state via a new ledger node; notify the proposing worker that there is a dissent to address. The proposing worker reads the dissent, optionally does additional research (which produces ledger nodes if anything substantive comes back), revises the draft, and commits a new draft node with a `supersedes` edge to the prior draft. The loop iterates. Rationale: dissents are not optional. Every dissent has to be addressed before the loop can converge.

**`consensus.convergence.detected`** — fires on `ledger.node.added` events when the new node is an agree node, dissent node, or draft node attached to an active loop. (These are the only events that can change whether a loop has converged, so they are the only events that need to trigger the check.) Walks the current draft and its review nodes, checks the convergence criterion (all consensus partners have agree nodes, no outstanding dissents, the draft's required schema fields are filled). If met, transitions the loop to `converged` via a new ledger node. If not met, takes no action. Rationale: convergence is a structural property of the ledger, not a judgment call. The supervisor reacts immediately to any commit that could change the convergence state and checks the structural condition. There is no polling.

**`consensus.iteration.threshold`** — fires on `ledger.node.added` when the new node is a draft and the count of supersedes-edge predecessors of the loop's current draft exceeds the per-loop-type threshold. Defaults: 5 for PRD loops, 3 for PR review loops, 2 for refactor proposal loops. Action: spawn a Judge stance with the loop history, the original user intent, and the dissent chain. The Judge produces a verdict node: keep iterating, return to PRD, switch approaches, or escalate to user. Rationale: a loop that has cycled too many times is a stuck loop. Worker self-assessment cannot detect stuck-ness reliably; an outside view is required.

**`consensus.partner.timeout`** — fires on a delayed event scheduled at consensus partner spawn time. When the supervisor's `consensus.draft.requires_review` rule spawns a partner, it schedules a delayed event with a per-loop-type partner-response timeout (defaults: 5 minutes for PR review partners, 10 minutes for SOW review partners, 30 minutes for Judge invocations because the Judge may be doing extensive analysis or waiting on research). The delayed event is cancelled if the partner commits an agree, dissent, or research-request node before the timer fires. If the timer fires (the partner has gone unresponsive), the rule marks the partner as timed-out via a ledger node attached to the loop, and spawns a replacement consensus partner with the same role and inputs in a fresh session. If the replacement also times out, the rule escalates to the next-up consensus partner per the team roster's escalation paths. If the loop loses too many partners in succession, the rule transitions the loop to `escalated` and the Judge is invoked. Rationale: consensus cannot wait forever on an unresponsive partner. Time-bounded responsiveness is enforced through delayed events scheduled at spawn time, not through polling.

### Category 3: Snapshot rules

These fire on proposed changes to snapshot code (code that was in the repo when Stoke was initialized). The CTO has veto authority on snapshot code, and these rules enforce that authority structurally. Loaded on the mission supervisor and on branch supervisors.

**`snapshot.modification.requires_cto`** — fires on `worker.action.proposed` when the action type is `file.modify`, `file.delete`, or `file.rename` and the target file is in the snapshot. Condition: the worker has not already obtained a CTO approval for this specific change. Action: pause the worker; spawn a fresh-context CTO stance with the snapshot context, the proposed change, the worker's reasoning, the original user intent, and the relevant slice of the ledger. The CTO produces an approve, deny, or escalate-to-user decision node. Unpause the worker on approve, send the worker back with the deny reasoning attached on deny, or hold the worker pending user response on escalate. Rationale: the user's pre-existing code is not Stoke's to silently modify. The CTO's posture is "show me the case" — smart changes are approved freely, unmotivated ones are pushed back on.

**`snapshot.formatter.requires_consent`** — fires on `worker.action.proposed` when the action involves running an auto-formatter on a file in the snapshot. Condition: the wizard config does not have `formatter_on_snapshot: true`. Action: same pause-spawn-CTO pattern as the modification rule. Rationale: auto-formatters can silently rewrite huge swaths of the snapshot. Reformatting code Stoke didn't write is a snapshot modification and triggers the CTO unless the user has explicitly opted in via the wizard.

### Category 4: Cross-team rules

These fire on changes to Stoke-written code that affect another team's work. The CTO is consulted in consensus mode (vote, not veto) for these. Loaded on the mission supervisor primarily, with the trigger condition relying on SDM advisory state.

**`cross_team.modification.requires_cto`** — fires on `worker.action.proposed` against a file or module that the SDM has flagged as cross-branch (via a previously emitted `sdm.collision.detected` advisory) or that another active stance has open in-flight work on (queryable from the bus's active worker set). Action: pause the worker; convene a consensus loop including the CTO, the affected branch's Lead Engineer, and the proposing worker. The CTO is brought in last per the team roster's "CTO is the final reviewer in any consensus that touches the codebase-wide view" rule. The loop resolves through normal consensus mechanics — no veto, just consensus iteration until convergence or escalation. Rationale: a stance modifying work that another team has open is creating a collision. The CTO's role here is consensus, not enforcement, because the work is Stoke-internal — but the cross-team awareness has to be present in the consensus loop or collisions ship silently.

### Category 5: Hierarchy rules

These enforce the supervisor hierarchy itself: subordinates report completions upward, escalations propagate upward, only the mission supervisor escalates to the user. Loaded selectively per supervisor configuration.

**`hierarchy.completion.requires_parent_agreement`** — loaded on the mission supervisor. Fires on `supervisor.branch.completion.proposed` from a branch supervisor. Condition: always evaluate. Action: evaluate the proposal against mission-level state (sibling branches in flight, cross-branch dependencies, PRD acceptance criteria, PO confirmation of intent alignment, SDM advisories about unresolved collisions). If all checks pass, commit a `branch.completion.agreed` ledger node and signal the branch supervisor to close. If any fail, commit a `branch.completion.dissented` node with reasoning, and the branch supervisor stays alive to address the dissent. Rationale: branch supervisors cannot self-declare done. Same trust rule applied at the supervisor level as at the worker level.

**`hierarchy.escalation.forwards_upward`** — loaded on branch supervisors. Fires on `worker.escalation.requested` when the escalation cannot be resolved at the branch level (the branch supervisor's other rules have fired and the issue is not resolved). Action: emit a `supervisor.escalation.forwarded` event to the mission supervisor with the escalation context attached. The branch supervisor pauses the affected workers pending the parent's response. Rationale: branch supervisors handle what they can; what they cannot, they forward.

**`hierarchy.user_escalation`** — loaded only on the mission supervisor. Fires on `supervisor.escalation.forwarded` events when the mission supervisor has determined that the escalation needs the user's input (the mission supervisor's other rules have fired and the issue is not resolved at the mission level either). The rule has two variants, selected by the wizard's `operating_mode` configuration:

*Interactive mode variant* (default): Produce a user-facing message via the PO with the loop history and escalation context attached. Pause the relevant work pending the user's response. The user's response becomes a ledger node that resolves the escalation when it arrives.

*Full-auto mode variant*: Spawn a fresh-context Stakeholder stance (team roster role 11) with the escalation node, the original user intent, the loop history, the relevant ledger slice as concern field, and any prior Stakeholder directive nodes in the same mission. The Stakeholder reads all of this, optionally requests research through the normal research rules, and produces a directive node that resolves the escalation. For high-stakes directives, the Stakeholder may convene a second-opinion Stakeholder via the consensus partner mechanism; if the two Stakeholders disagree, the escalation falls back to the interactive-mode variant and the user is contacted despite full-auto mode being on. The directive node is the resolution; the paused work is unpaused with the directive in its concern field.

Rationale: in interactive mode, the user is the only thing that can close certain decisions. In full-auto mode, the Stakeholder stance replaces the user in that role with a high-authority reasoning session whose posture is "absolute completion and quality" — the Stakeholder evaluates the escalation and produces a thoughtful directive rather than rubber-stamping. Full-auto mode is opt-in at mission start time and can be toggled between missions via the wizard.

### Category 6: Drift detection rules

These fire on patterns rather than single events. They catch loops that are stuck, drifting, or running over budget. Loaded on the mission supervisor; the iteration-threshold rule is also loaded on branch supervisors.

**`drift.judge.scheduled`** — fires on delayed events scheduled by the supervisor when a new draft is committed. At draft commit time, the supervisor schedules a delayed event with a per-loop-type wall-clock duration (the slow-drift threshold). If the draft is superseded before the timer fires, the cancellation handle is used to cancel the scheduled event. If the timer fires (meaning the draft has gone stale without being superseded), the rule fires and spawns a Judge stance with the loop history and the original user intent. Also fires on `worker.dissent.raised` events when the new dissent semantically resembles a prior dissent in the same loop (the rule walks the loop's dissent history at fire time and matches by content similarity). Rationale: not all stuck-ness looks like rapid iteration. Slow drift and pattern repetition need their own detection. Both detection paths are event-driven — slow drift through delayed events, repeated dissents through the dissent commit event.

**`drift.intent_alignment_check`** — fires on `task.milestone.reached` events. Action: spawn a fresh-context Judge stance with the original user intent, the current state, and the loop history. The Judge's specific question is "are we still building what the user asked for, or have we drifted." This is narrower than the general "are we stuck" check from the other Judge invocation rules. Rationale: drift through inaction is a documented failure mode for long-running agents — the team can be working productively on something that has drifted from what the user wanted. Milestone boundaries are the natural points to check.

**`drift.budget_threshold`** — fires on `mission.budget.update` events when the cost crosses a configured threshold. Cost is measured per the wizard's configuration (tokens, dollars, wall time, iteration count, or any combination). Defaults: warning at 50% of budget, check-in with the Judge at 80%, escalate to user via PO at 100%, hard stop at 120%. Action: depending on threshold, emit a warning event, spawn a Judge for evaluation, or escalate to the user. Rationale: 92% of agentic AI deployments experience cost overruns; the supervisor is the place to catch them structurally rather than relying on workers to be cost-aware.

### Category 7: Research rules

These handle research requests from any stance. Research is always the right move when a stance is uncertain — uncertainty is not a verdict, it is a signal that the work cannot proceed responsibly without more information. The supervisor's research rules ensure the requesting stance is paused, dedicated researcher stances are dispatched, the report flows back to the requester, and the requester resumes with the new information in its concern field.

Loaded on the mission supervisor and on branch supervisors. Any stance can request research — Devs, Reviewers, Lead Engineers, the Judge, even the CTO during a snapshot consultation. The mechanism is the same regardless of who requests it.

**`research.request.dispatches_researchers`** — fires on `worker.research.requested` events. The event payload includes the question, the context that produced the uncertainty, and the requesting stance ID. Action: pause the requesting stance (the work that triggered the uncertainty cannot continue while waiting for the answer); spawn one or more fresh-context researcher stances through the harness, each with the question, the relevant ledger slice projected as concern field, and authorization to use the web search tool, the codebase query tools, and any other research tools the wizard config has enabled. The number of researchers depends on the question type and the wizard config — high-stakes questions get multiple researchers in parallel for cross-verification, routine questions get one. Rationale: research is the uncertainty resolution mechanism. Uncertain stances do not produce uncertain output — they pause and ask for help.

**`research.report.unblocks_requester`** — fires on `worker.research.completed` events from a researcher stance. The event payload references the original research request and includes the report node ID in the ledger. Action: read the report node, verify it has all required fields (the question being answered, the sources cited, the conclusion, the confidence level, and the limitations), and if valid, unpause the requesting stance with the report attached to its concern field. The requesting stance resumes its work with the new information. If multiple researchers were dispatched in parallel, the rule waits until all of them have committed reports (or until the partner timeout fires for unresponsive researchers) before unpausing the requester. Rationale: the requester needs the full picture from research before it can proceed; partial returns from one of several parallel researchers are not enough.

**`research.timeout`** — fires on a delayed event scheduled at researcher spawn time. Default duration: 10 minutes for routine research, longer for deep-research questions per the wizard config. If a researcher is unresponsive past the timeout, the rule marks it as timed-out and either spawns a replacement (if the question is still answerable) or signals to the requester that the research could not be completed (which the requester then handles by either escalating to its supervisor or proceeding with limited information and noting the limitation in its output). Rationale: research cannot wait forever. Time-bounded responsiveness applies to researchers the same way it applies to consensus partners.

### Category 8: Skill extraction trigger

One rule, in its own category because it triggers a separate process rather than firing an in-supervisor enforcement action.

**`skill.extraction.trigger`** — fires on `loop.converged` events for completed missions, and on `loop.escalated` events when the escalation outcome was "user said try a different approach" or "user said abandon this." Action: emit a `skill.extraction.requested` event onto the bus with the completed mission's scope. No further action — the skill manufacturer (a separate process, component 8) subscribes to that event and runs the extraction independently. Rationale: post-task learning is its own concern and runs in its own process. The supervisor's involvement is just emitting the trigger.

### Category 9: Skill governance rules

These handle the audit and review of skill use across the team. Skills shape what stances do, so skill use is observable and reviewable — not a casual implementation detail. Every skill load is logged; every skill application is logged; low-confidence applications are reviewed; patterns of misapplication trigger demotion; patterns of success trigger promotion; external skill imports require consensus. Loaded on the mission supervisor and on branch supervisors (the immediate supervisor reviews skill use for its scope).

**`skill.load.audit`** — fires on `skill.loaded` events emitted by the harness when a concern field includes a skill. Condition: always evaluate (every load is logged). Action: does not pause workers; records the load in the supervisor's audit trail so post-mortems can see what skills informed which decisions. The `skill_loaded` ledger node is already committed by the harness before this event fires; the supervisor's action is acknowledgment and indexing, not gating. Rationale: every skill that shapes a stance's behavior should be traceable back to the load event. This is non-blocking audit, not enforcement.

**`skill.application.requires_review`** — fires on `skill.applied` events when the applied skill has confidence below `proven` (i.e., `tentative` or `candidate`). Condition: applied skill's current confidence is not `proven`. Action: does not pause the applying stance (applications have already happened by the time the event fires — blocking would rewind work). Queues the application for the immediate supervisor's review in the next review pass, which spawns a review stance with the applied skill, the stance's output that used it, and the outcome of the work. The review stance produces a `skill.review.completed` event with a verdict: promote confidence, hold confidence, demote confidence, or mark as footgun. The manufacturer consumes the verdict and writes the appropriate superseding skill node. Rationale: skills that are not yet proven earn their confidence through observable use. The supervisor is the structural mechanism that converts observations into confidence updates.

**`skill.application.contradicts_outcome`** — fires on `loop.escalated` or `judge_verdict` events when the loop's terminal state is negative (escalated, switch_approaches, return_to_prd) and at least one `skill_applied` node exists attached to any draft in the loop's history. Condition: the escalation or negative verdict happened on a loop where a skill was applied. Action: queues an urgent skill review — the review stance evaluates whether the applied skill contributed to the negative outcome and recommends a lifecycle action. The manufacturer consumes the verdict. Rationale: skills that correlate with failures need re-evaluation. One negative outcome does not condemn a skill, but a pattern of negative correlations should drive demotion or footgun marking.

**`skill.import.triggers_consensus_loop`** — fires on `ledger.node.added` events when the new node is a `skill_import_proposal`. Condition: always evaluate. Action: spawn a consensus loop with the proposal as its artifact. The loop's convened partners are the research stance that found the candidate (proposing), the CTO (required), the Lead Engineer (required), and a security-focused stance if the wizard config enables one. The loop runs through normal consensus mechanics. If the proposal's `risk_assessment` field is `high`, the loop is automatically routed to user escalation at convergence time regardless of partner agreement (user judgment is required for high-risk imports). If the loop converges on import, the supervisor emits a `skill.import.approved` event that the manufacturer consumes to write the skill node. If the loop escalates without approval, the rejection is recorded permanently in the ledger as part of the negative corpus. Rationale: importing a skill from an external source is a trust decision with the same gravity as a major refactor. The substrate enforces that gravity by requiring explicit consensus with CTO and Lead Engineer participation, by making high-risk imports mandatory user escalations, and by preserving rejected proposals for future research to consult.

### Category 10: SDM detection rules

Loaded only on the SDM supervisor. All detection-only — they emit advisory events but do not pause workers, spawn stances, or transition state. The advisories are consumed by other supervisors that load the cross-team rules and react accordingly.

**`sdm.collision.file_modification`** — fires on `worker.action.proposed` events from any branch when the target file has been modified (or has open in-flight modification) in another active branch within the last N events. Action: emit a `sdm.collision.detected` advisory event with the file path, the colliding branches, and the affected workers. Rationale: two branches modifying the same file is the most common form of cross-branch collision and the easiest to detect.

**`sdm.dependency.crossed`** — fires on `ledger.node.added` events when the new node is a task DAG node whose dependencies span branches. Action: emit a `sdm.dependency.crossed` advisory with the dependency graph slice. Rationale: a ticket in branch A depending on a ticket in branch B is a cross-branch dependency that affects the ordering of the mission supervisor's parent-agreement evaluation.

**`sdm.duplicate_work.detected`** — fires on `worker.action.proposed` events from any branch and on `ledger.node.added` events for new task DAG nodes. At fire time, the rule queries active work across other branches (file paths being touched, function names being created, acceptance criteria of in-flight tickets, concern field projections) and looks for overlap with the triggering action or new task. If overlap is found above a threshold, emits a `sdm.duplicate_work.detected` advisory with the suspected duplicates and the branches involved. Rationale: independent duplicate work is the failure mode that produces hellish merges later. Catching it early lets the affected leads coordinate. Detection runs on the events that actually introduce new work, not on a timer.

**`sdm.schedule_risk.critical_path`** — fires on `task.timing.update` events when one branch's progress is blocking other branches more than its priority would suggest. Action: emit a `sdm.schedule_risk.detected` advisory. Rationale: a critical-path branch falling behind is a mission-level risk that the mission supervisor needs to know about to reorder priorities.

**`sdm.drift.cross_branch`** — fires on `ledger.node.added` events for interface, schema, or contract nodes (any node that defines a shared boundary between branches), and on `worker.action.proposed` events for files that are part of a known cross-branch shared boundary. At fire time, the rule walks the recent history of the affected boundary across all branches and looks for divergence (e.g., two branches implementing different parts of the same feature whose APIs are starting to look incompatible). If divergence is detected, emits a `sdm.cross_branch_drift.detected` advisory. Rationale: cross-branch coherence is the SDM's primary responsibility. Drift detection runs on the events that actually change shared boundaries, not on a timer.

---

## Rule file structure

Each rule lives in its own file under `internal/supervisor/rules/{category}/{rule-name}.go`. Example layout:

```
internal/supervisor/
├── core.go                    // the supervisor's core loop
├── core_test.go
├── manifests/
│   ├── mission.go             // which rules the mission supervisor loads
│   ├── branch.go              // which rules branch supervisors load
│   └── sdm.go                 // which rules the SDM supervisor loads
└── rules/
    ├── trust/
    │   ├── completion_requires_second_opinion.go
    │   ├── completion_requires_second_opinion_test.go
    │   ├── fix_requires_second_opinion.go
    │   ├── fix_requires_second_opinion_test.go
    │   ├── problem_requires_second_opinion.go
    │   └── problem_requires_second_opinion_test.go
    ├── consensus/
    │   ├── draft_requires_review.go
    │   ├── ...
    ├── snapshot/
    ├── cross_team/
    ├── hierarchy/
    ├── drift/
    ├── skill/
    └── sdm/
```

Each rule file defines a single rule struct conforming to the `Rule` interface:

```go
package trust

type Rule interface {
    Name() string                                  // unique rule ID
    Pattern() bus.Pattern                          // event pattern to fire on
    Priority() int                                 // hook priority
    Evaluate(ctx context.Context, evt bus.Event, ledger ledger.API) (bool, error)  // condition check
    Action(ctx context.Context, evt bus.Event, hooks bus.HookAPI) error            // hook action
    Configurable() ConfigSchema                    // wizard surface
    Rationale() string                             // why this rule exists
}
```

The supervisor's core loop iterates registered rules in priority order, calls `Evaluate` on each, and calls `Action` on each whose `Evaluate` returned true. The core loop does not contain any rule-specific logic; it just walks the registered set.

Each rule file has a unit test alongside it that constructs synthetic events, runs the rule against a mock bus and a mock ledger, and asserts that the right hook action is taken (or not taken) in the right conditions. The test discipline is "every rule has tests for every branch of its evaluate function and every action it can take."

The manifest files (`manifests/mission.go`, `manifests/branch.go`, `manifests/sdm.go`) are simple lists of rules to load with their priorities. Adding a rule to a configuration is editing one manifest file. Removing a rule is the same. The manifests are how the three configurations diverge despite running the same supervisor core.

---

## The wizard surface for rule configuration

During Stoke's initialization on a repo (the wizard's first run), the wizard surfaces the rule list to the user with brief descriptions and asks for confirmation or adjustment of the configurable strength of each one. The user sees:

- Each rule's name and rationale (so they know what it does and why)
- The default configuration values (so they have a baseline)
- The configurable knobs (so they know what they can change)
- A note on whether the rule is non-disable-able (the trust rules) or fully optional

The user can:

- Adjust strength on configurable rules (e.g., raise the iteration threshold for PRD loops from 5 to 7, set the budget warning to 30% instead of 50%)
- Disable optional rules entirely (e.g., turn off the formatter consent rule if they want Stoke to run formatters freely)
- Confirm that the trust rules are at default strength (the trust rules cannot be disabled but their strength is configurable)

The wizard records the user's choices in `.stoke/config.yaml` under a `supervisor.rules` section. The supervisor reads this on startup and applies the user's adjustments to the loaded rules. Configuration changes mid-mission are possible but require restarting the supervisor (the wizard surfaces this as a separate command, not a normal mission action).

---

## Validation gate

Before any other component depends on the supervisor, the supervisor has to pass its own validation gate. The gate is:

1. ✅ `go vet ./...` clean, `go test ./internal/supervisor/...` passes with >70% coverage on the core and >90% coverage on each rule file
2. ✅ `go build ./cmd/stoke` succeeds
3. ✅ The supervisor core loop reads events from the bus, matches against rules, and fires hooks deterministically (verified by an integration test that runs a synthetic event sequence and asserts the expected hook fires)
4. ✅ Every rule in the rule taxonomy has a unit test in its file
5. ✅ The mission supervisor manifest loads the correct rules; the branch supervisor manifest loads the correct rules; the SDM supervisor manifest loads the correct rules
6. ✅ The trust rules cannot be disabled via configuration (verified by a test that attempts to disable them and asserts the configuration is rejected)
7. ✅ The wizard configuration is correctly applied to rule strength (verified by an integration test that loads a custom config and asserts the rules behave per the config)
8. ✅ A worker that emits a `done` declaration is paused; a fresh-context Reviewer is spawned; the worker is unpaused when the Reviewer commits an agree node
9. ✅ A worker that proposes modifying a snapshot file is paused; a fresh-context CTO is spawned; the worker is unpaused on approve, redirected on deny, or held on escalate
10. ✅ A loop that exceeds the iteration threshold spawns a Judge; the Judge's verdict transitions the loop accordingly
11. ✅ A branch supervisor that proposes completion is held until the mission supervisor agrees
12. ✅ The SDM supervisor emits advisory events for collisions, dependencies, and duplicates without taking direct enforcement action
13. ✅ The supervisor recovers from a crash by reading the bus event log from the last cursor position and rebuilding its in-memory state
14. ✅ Branch supervisors pause their parent-agreement-required actions when the mission supervisor is unavailable
15. ✅ The skill extraction trigger rule emits the trigger event but does not spawn the skill manufacturer itself
16. ✅ A consensus partner that does not respond within the per-loop-type timeout is replaced by a fresh-context partner via the `consensus.partner.timeout` rule
17. ✅ A worker that emits a `worker.research.requested` event is paused; researcher stances are spawned; the worker is unpaused only after the research report node is committed and validated
18. ✅ Multiple researchers spawned in parallel for cross-verification all return reports before the requesting stance is unpaused (or unresponsive researchers time out via `research.timeout`)
19. ✅ A `skill.loaded` event triggers the `skill.load.audit` rule, which records the load without blocking the stance
20. ✅ A `skill.applied` event for a `tentative` or `candidate` skill triggers the `skill.application.requires_review` rule, which queues a review without blocking the stance
21. ✅ A `loop.escalated` event on a loop with applied skills triggers the `skill.application.contradicts_outcome` rule, which spawns an urgent skill review
22. ✅ A `skill_import_proposal` ledger node triggers the `skill.import.triggers_consensus_loop` rule, which spawns a consensus loop with CTO and Lead Engineer as required partners
23. ✅ A high-risk skill import proposal is automatically routed to user escalation even if the consensus loop would otherwise have converged on import
24. ✅ The supervisor codebase contains no polling loops, ticker-based periodic checks, or sleep-and-loop patterns (verified by grep on `internal/supervisor/` for `time.Tick`, `time.NewTicker`, `for { ... time.Sleep ... }`, and similar patterns — every time-based behavior must use the bus's `PublishDelayed`)
25. ✅ Every supervisor rule fires on a specific bus event pattern or a delayed event; no rule has a periodic re-evaluation trigger (verified by inspecting each rule file's `Pattern()` method)
26. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## What the supervisor does not do

A few things the supervisor explicitly does not handle:

- **Worker reasoning.** The supervisor enforces rules about worker behavior; it does not produce reasoning. When a Reviewer or Judge or CTO is spawned, the supervisor creates the stance and gives it the inputs, but the actual reasoning happens in the spawned stance. The supervisor never thinks about code or design; it thinks about whether the rules say a particular action is required right now.

- **Skill manufacturing.** The supervisor triggers it via the `skill.extraction.trigger` rule, but the actual extraction is done by the skill manufacturer (component 7). The supervisor's responsibility ends when the trigger event is emitted.

- **Persistent reasoning storage.** Decisions, plans, and skills go to the ledger. The supervisor's *actions* are recorded in the ledger (via the rules that commit ledger nodes for state transitions), but the reasoning content is produced by stances and committed by stances, not by the supervisor.

- **User interface.** The supervisor escalates to the user via the PO stance, not directly. The PO is the only place where Stoke meets the user; the supervisor's `hierarchy.user_escalation` rule produces a message *for* the PO to deliver, not a direct user-facing output.

- **Bus implementation.** The supervisor uses the bus through its public API; it does not reach into the bus's internals. If the bus's implementation changes, the supervisor is unaffected as long as the bus's API contract is honored.

The supervisor's job is rules-and-enforcement. Everything else is somewhere else.

---

## Forward references

This file is component 4 of the new guide. It refers to several things specified in later components:

- **Rule schemas in detail** are in component 5 (node types). This file describes the rule taxonomy; the node types component spells out the schema for the ledger nodes that the rules read and write.
- **The skill manufacturer** is component 7. This file describes the trigger rule; the skill manufacturer component describes what happens after the trigger event is emitted.
- **The harness** is component 9 or 10. This file describes the supervisor calling into the harness to spawn worker stances; the harness component specifies how stance creation actually works.
- **The wizard** is later. This file describes the wizard surfacing rules to the user; the wizard component specifies the initialization flow.
- **The PO stance's user-facing message format** is part of the team roster's stance contracts and the wizard's user interaction model. This file describes the supervisor delegating user-facing escalation to the PO; the actual format of those messages is the PO's responsibility.

The next file to write is `05-the-consensus-loop.md`. The consensus loop is now a much smaller spec because the heavy lifting is in the ledger (substrate), the bus (runtime), and the supervisor (enforcement). The loop spec describes the state machine that lives on a loop ledger node, the rules that drive its transitions, and the convergence criterion that the supervisor checks. Most of the loop spec is forward references back to components 2, 3, and 4 — which is the right shape, because the loop is a coordination pattern, not a substrate.
