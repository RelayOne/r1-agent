# 11 — The Harness

The harness is the runtime layer that actually creates worker stances when the supervisor's hooks call into it. When the trust rule fires and says "spawn a fresh-context Reviewer with this artifact and this concern field," the harness is what does the spawning. It handles model selection, system prompt construction from concern field templates, context loading, session initialization, pause/resume mechanics, tool authorization, and stance lifecycle. It is the one place in Stoke that actually talks to LLM APIs.

The harness is a relatively thin component because the substrate components above it have absorbed most of the complexity. The concern field (component 7) gives the harness the rendered prompt context. The node types (component 6) define what the harness reads and writes. The supervisor (component 4) tells the harness what to do and when. The ledger (component 2) and the bus (component 3) are how the harness communicates with everything else. The harness's job is the actual mechanics of "turn a spawn request into a running stance producing events."

This file specifies the harness's responsibilities, its API surface (including the headless-library surface that lets other harnesses invoke it), the stance lifecycle, the tool authorization model, and the validation gate.

---

## What the harness is

The harness is a Go package at `internal/harness/` that ships as part of Stoke's runtime. It runs in-process with the supervisor in standard single-process deployments, but it is also fully invocable as a library — other harnesses, integrating systems, testing frameworks, and nested orchestrators can import the harness package and call its public API directly, without going through a CLI layer. The library surface is a first-class concern, not an afterthought.

The harness has four distinct responsibilities:

1. **Stance creation.** Translate a supervisor spawn request into a running stance: select a model, build the system prompt, load the initial context, initialize the session, connect the stance to its tools, subscribe it to its relevant bus events, and begin execution.

2. **Stance lifecycle management.** Track every active stance's state (running, paused, waiting on research, waiting on consensus partners, terminated). Handle pause requests from the supervisor. Handle resume requests. Handle termination (normal or crash). Restore state on harness restart via the ledger and bus replay.

3. **Tool authorization.** Mediate what tools each stance has access to based on its role, its task DAG scope, and the wizard's configuration. A Dev has access to file-read, file-write (within the branch), code-run, and web-search. A Reviewer has access to file-read and web-search but not file-write. A Researcher has access to web-search, web-fetch, file-read, and the skill-import-proposal mechanism but not code-run. A Judge has access to file-read and ledger-query but not much else. The harness is the place that enforces these authorizations at tool-call time.

4. **Model API calls.** The actual network calls to LLM providers. The harness holds the API credentials, implements retry and rate-limiting, tracks token and dollar costs for budget events, and streams responses back through the stance session abstraction.

The harness does not hold rules. It does not decide what stances should exist — the supervisor does. It does not decide what's in a stance's prompt — the concern field does. It does not decide what skills a stance loads — the concern field builder does. The harness executes the decisions other components make.

---

## The library API

The harness exposes a public Go API that other code can call directly. This is the headless interface. A simplified view:

```go
package harness

// New creates a new harness instance bound to a specific mission.
// The harness depends on the ledger, the bus, and the concern field
// builder, which are passed in as interfaces so the harness can be
// wired up by the runtime, by tests, or by another harness that wants
// to nest stoke inside itself.
func New(config Config, ledger LedgerAPI, bus BusAPI, concern ConcernFieldBuilder) (*Harness, error)

// SpawnStance creates and begins execution of a worker stance.
// The spawn request comes from the supervisor (or from another harness
// when nested). The returned StanceHandle can be used to pause, resume,
// or inspect the stance.
//
// This is the method supervisor hooks call to create workers. It is
// also the method another harness would call to nest a stance inside
// itself — the API is the same regardless of caller.
func (h *Harness) SpawnStance(ctx context.Context, req SpawnRequest) (StanceHandle, error)

// PauseStance pauses a running stance at its next safe checkpoint.
// The stance's session state is preserved. Used by the supervisor's
// trust rules, snapshot rules, and research rules to hold work in flight
// while fresh-context reviewers or researchers run.
func (h *Harness) PauseStance(ctx context.Context, handle StanceHandle, reason PauseReason) error

// ResumeStance resumes a previously paused stance, optionally with
// additional context attached. The additional context is rendered
// as a new context block alongside the original concern field, not
// folded into it — the concern field is immutable after spawn.
func (h *Harness) ResumeStance(ctx context.Context, handle StanceHandle, additional AdditionalContext) error

// TerminateStance ends a stance permanently. Used on mission close,
// supervisor shutdown, or explicit termination requests. The stance
// emits a final termination event; its session state is committed
// to the ledger as a supervisor_state_checkpoint snapshot for audit.
func (h *Harness) TerminateStance(ctx context.Context, handle StanceHandle) error

// InspectStance returns the current state of a stance (running,
// paused, waiting, terminated) plus recent events. Used by the
// dashboard, debugging tools, and nested harnesses that want to
// observe a child stance's progress.
func (h *Harness) InspectStance(ctx context.Context, handle StanceHandle) (StanceState, error)

// ListStances returns all stance handles currently tracked by this
// harness. Used for recovery on restart and for debugging.
func (h *Harness) ListStances(ctx context.Context) ([]StanceHandle, error)

// Recover rebuilds the harness's in-memory state from the ledger
// and the bus event log after a crash. Used at startup to resume
// stances that were active when the harness went down.
func (h *Harness) Recover(ctx context.Context) error
```

All of these methods are callable by any Go code that imports the harness package and has an authenticated `*Harness` instance. The `New` constructor is the gatekeeper: you have to provide a valid config and working references to the ledger, bus, and concern field builder. Once you have a harness, you can spawn stances, inspect them, pause them, and so on, without going through any CLI layer.

**This enables several use cases beyond the single-process default:**

- **Nested harnesses.** A parent harness can create a child harness and spawn stances inside it. The child harness has its own ledger and bus scope (typically a sub-mission within the parent's mission). This is how complex missions can sandbox portions of their work into isolated sub-environments without losing the full audit trail.

- **Testing.** Tests can create a harness with mock ledger, mock bus, and mock concern field builder, then exercise the stance lifecycle without making real LLM API calls. The headless library surface makes every public method directly exercisable.

- **Integration with other systems.** An external orchestrator that wants to use Stoke's trust rules and consensus loop machinery inside its own workflow can import the harness package, construct a harness, and spawn stances from its own code. The external orchestrator sees Stoke as a library of primitives, not as a CLI tool.

- **CI and batch modes.** A CI job that runs Stoke on a repo can invoke the harness directly instead of shelling out to `stoke run`. Faster startup, cleaner error handling, structured results.

- **Alternate frontends.** A GUI, a web dashboard, or an IDE extension can sit in front of the harness and call it programmatically, with the CLI being just one of several frontends.

The CLI (`stoke run`, `stoke status`, etc.) is itself a consumer of the harness library — the CLI is not the only interface to the harness, it is one interface among several. Keeping the library surface first-class means all frontends are on equal footing.

---

## The spawn request

A `SpawnRequest` is the structured input to `SpawnStance`. It carries everything the harness needs to construct and start a stance:

```go
type SpawnRequest struct {
    // The role of the stance being spawned. Maps to a concern field
    // template name and a set of default tool authorizations.
    Role string  // e.g., "reviewer_for_pr", "judge_for_iteration_threshold"

    // The task DAG node this stance is scoped to. Determines which
    // slice of the ledger the concern field queries will walk.
    TaskDAGScope NodeID

    // The loop this stance is operating within, if any. Drafts,
    // agrees, dissents, and verdicts are attached to this loop.
    LoopRef NodeID  // may be empty for standalone stances

    // The supervisor instance that requested the spawn. Used for
    // authority checks and for causality tracking.
    SpawningSupervisor SupervisorID

    // The causality chain: which bus event triggered this spawn.
    // The harness records this on the stance's spawn event so the
    // audit trail can be walked backward from any stance action to
    // the original trigger.
    CausalityRef SeqNum

    // Model selection override. If unset, the harness picks a model
    // based on the role and the wizard config. If set, the harness
    // uses the specified model (subject to the trust rules about
    // cross-family verification for second-opinion stances).
    ModelOverride string  // optional

    // Initial tool authorizations. If unset, the harness applies the
    // defaults for the role. If set, the harness uses the specified
    // authorizations (subject to validation — authorizations cannot
    // exceed what the role is allowed to have).
    ToolAuthOverride []ToolName  // optional

    // Additional context to attach alongside the concern field at
    // spawn time. Used when the supervisor is restarting a stance
    // with new information (e.g., a research report that arrived
    // for a previously-paused stance).
    AdditionalContext []ContextBlock  // optional

    // Budget constraints for this stance. Inherits from the mission's
    // budget by default; can be constrained further for specific
    // stances (e.g., a Judge has a tighter per-invocation budget than
    // an unlimited Dev).
    Budget BudgetConstraints  // optional
}
```

The harness validates every field: the role must be a known template, the task DAG scope must exist in the ledger, the loop reference must point to a valid loop in a non-terminal state, the model override must be an available model, the tool authorizations must be within the role's allowed set. Invalid spawn requests are rejected with a clear error before any model API call is made.

---

## Stance creation flow

When a valid `SpawnRequest` arrives at the harness, the flow is:

1. **Resolve the concern field template.** Look up the template file in `internal/concern/templates/` matching the `Role`. If no template exists for the role, reject the spawn (this is the enforcement of component 7's validation gate item 4).

2. **Build the concern field.** Call the concern field builder with the role, the task DAG scope, and the ledger. Receive back a rendered prompt context. This step also commits `skill_loaded` nodes and emits `skill.loaded` events for any skills included in the concern field (component 7 retrofit).

3. **Attach additional context.** If the spawn request carries additional context (e.g., a research report for a resuming stance), render it as a separate block appended after the concern field. The concern field is immutable; additional context is a sibling, not a replacement.

4. **Construct the system prompt.** Combine the role-specific system prompt template, the concern field, the additional context, and a boilerplate block that tells the stance about its available tools and how to emit events. The final system prompt is what the model actually sees as its instructions.

   For Stakeholder spawns specifically (role `stakeholder_for_escalation`, only used in full-auto mode), the harness reads the configured `stakeholder_posture` from the merged config (per-mission override > per-repo > global > default) and applies the matching posture block to the system prompt template. The three postures (`absolute_completion_and_quality` default, `balanced`, `pragmatic`) each have their own posture block embedded in the Stakeholder template. The selected block establishes the Stakeholder's tradeoff stance for that specific spawn — the same Stakeholder template can produce different effective behaviors depending on the active posture, without changing the template itself. The posture is recorded on the resulting `stakeholder_directive` ledger node so the audit trail captures which posture was applied to each directive.

5. **Select the model.** If the spawn request specifies a model override, use it (after validating it). Otherwise, pick a model based on the role and the wizard config. For second-opinion stances (spawned by trust rules), apply the "different family if available" rule: prefer a model from a different provider or family than the stance whose work is being second-opinion-reviewed.

6. **Authorize tools.** Build the tool set for the stance based on its role and any overrides. The authorization is enforced at tool-call time, not at spawn time — the stance sees the tools in its system prompt but the harness verifies each tool call against the authorization set.

7. **Initialize the session.** Open a new session with the LLM provider, set the system prompt, and begin execution. The session is identified by a stance ID assigned at this step, which is the handle returned to the caller.

8. **Subscribe to bus events.** The stance needs to know when it has been paused, resumed, or terminated. The harness subscribes the stance's handler to the relevant bus events scoped to the stance's ID. When a pause event arrives, the harness signals the stance at its next safe checkpoint.

9. **Emit the spawn event.** The harness publishes a `stance.spawned` event on the bus with the stance's ID, role, task DAG scope, loop reference, causality ref, and model selection. This is the event the supervisor tracks for stance accounting and the event post-mortem tools use to trace activity.

10. **Return the handle.** The caller (the supervisor, or another harness) receives a `StanceHandle` that can be used for subsequent pause, resume, inspect, or terminate calls.

The full flow takes ~1-2 seconds in practice, dominated by the model provider's session initialization. The harness does not block other stance operations during spawn — multiple spawns can proceed in parallel.

---

## The stance session abstraction

Inside the harness, each stance is represented by a `StanceSession` struct that holds:

- The stance ID
- The current state (`running`, `paused`, `waiting_on_research`, `waiting_on_consensus`, `terminated`)
- The model provider connection (an abstraction over Anthropic, OpenAI, or other providers)
- The current system prompt and context blocks
- The authorized tool set
- A pointer to the stance's event subscription on the bus
- Token usage and cost tracking since spawn
- The spawn request that created it (for recovery after crash)

The stance session is the harness's internal state. It is not in the ledger (runtime state, not persistent reasoning) and not in the bus (too large and too specific to be a bus event). The harness holds it in memory during the stance's lifetime and discards it at termination.

**On harness crash and restart**, the `Recover` method rebuilds the in-memory stance sessions from the ledger's `stance.spawned` events and the bus's event log. For each active stance at the time of the crash, the harness:

1. Reads the `stance.spawned` event to reconstruct the spawn request
2. Reads subsequent events for the stance to determine its state at crash time (was it running, paused, waiting on research)
3. Re-opens the model provider session (or starts a new one if the provider does not support session resumption)
4. Rebuilds the system prompt from the concern field at recovery time (which may differ slightly from the original if the ledger has changed — this is acceptable because the concern field is a snapshot at build time, and recovery is a legitimate rebuild)
5. Resumes the stance from whatever state it was in before the crash

The recovery is best-effort. A stance whose model provider cannot be resumed is terminated cleanly and re-spawned by the supervisor if the work needs to continue. The supervisor sees the termination and makes the call about whether to re-spawn or to escalate.

---

## Pause and resume mechanics

Pausing is how the supervisor enforces trust rules, snapshot consultations, and research dispatches. When a pause is requested, the harness:

1. Sends a pause signal to the stance session
2. The stance session waits for its next safe checkpoint (between model turns, between tool calls, or at a point where the session state can be cleanly held)
3. When the checkpoint is reached, the session stops consuming new tokens and emits a `stance.paused` event
4. The harness updates the stance's state to `paused`
5. The pause reason is recorded on the event (which trust rule fired, which consultation is pending, which research is being awaited)

Resuming follows the reverse flow:

1. The supervisor (or another caller) invokes `ResumeStance` with any additional context that should be attached
2. The harness adds the additional context to the stance's context blocks
3. The harness sends a resume signal to the stance session
4. The session wakes up, reads the additional context, and continues execution
5. A `stance.resumed` event is emitted with the resume reason

**Pause is not termination.** A paused stance retains its session state, its model provider connection, its authorized tool set, and its position in its work. It is waiting, not gone. A stance that has been paused for a long time (longer than the paused-stance timeout, configurable via the wizard) is escalated to the supervisor, which decides whether to extend the pause, terminate and re-spawn, or escalate upward.

**Pause is a request, not a guarantee of immediate stop.** The stance's next safe checkpoint may be several seconds away if the stance is in the middle of a long model turn. The harness waits for the checkpoint rather than ripping the stance mid-thought, because ripping would corrupt the stance's state and make resumption unreliable.

---

## Tool authorization

Each stance has an authorized tool set computed at spawn time. The set is the intersection of: the role's default tool set, any override in the spawn request, and the wizard config's per-role tool limits. The intersection produces the effective authorized set.

When the stance attempts to call a tool during its session, the harness intercepts the call, checks it against the authorized set, and either permits or rejects it:

- **Permitted calls** proceed to the actual tool implementation and the result is returned to the stance
- **Rejected calls** return an error to the stance indicating the tool is not authorized, without the tool being executed

The stance can see the error and either try a different tool or give up on the approach. The rejection is recorded on the bus as a `stance.tool_rejected` event so the supervisor can see patterns of rejected calls (which might indicate a stance that is trying to exceed its authorization, or a misconfigured authorization set that is preventing legitimate work).

**The tool authorization is enforced by the harness, not by the model provider.** Even if the model's native tool support would let the stance call any tool, the harness's interception layer is what decides whether the call runs. This is the property that makes authorization reliable: an authorized tool set is a structural constraint, not a hope that the model will respect the system prompt.

Specific tools and their default authorizations per role:

- **`file_read`** — all roles except Judge (Judge has ledger-query instead, which subsumes file-read for its purposes)
- **`file_write`** — Dev (scoped to branch), Lead Engineer (scoped to mission), PO (scoped to PRD artifacts only)
- **`code_run`** — Dev, Reviewer (for test execution), QA Lead
- **`web_search`** — All research-capable roles: Researcher, Lead Engineer, CTO, QA Lead, PO
- **`web_fetch`** — Same as web_search
- **`ledger_query`** — All roles (everyone can read the ledger via the stance's query tool)
- **`ledger_write`** — Constrained by role: each role can only write the node types it is authorized to write per the "Written by" field in component 6's schemas
- **`skill_import_propose`** — Researcher only (researchers are the role that can emit skill import proposals based on their web searches)
- **`bus_publish`** — All roles, for emitting events specific to their work (e.g., a Dev emitting `worker.action.started`)
- **`bus_subscribe`** — All roles, for receiving pause/resume signals and other stance-specific events
- **`research_request`** — All roles (any stance can emit `worker.research.requested`)

The full tool catalog is maintained in `internal/harness/tools/` with one file per tool, each defining its authorization rules. Adding a new tool means adding a new file and updating the per-role default sets in the config schema.

---

## Nested harnesses

A harness can create and manage another harness inside itself. This is the mechanism by which a mission can sandbox a portion of its work into an isolated sub-environment — typically a "spike" or "experiment" where the team wants to explore an approach without committing to it, with the ability to throw away the whole sub-environment if the approach does not pan out.

The parent harness creates a child harness by:

1. Creating a new ledger scope (a sub-ledger that shares the parent's canonical git store but queries within a scoped prefix)
2. Creating a new bus scope (a sub-bus that delivers events only within the child's scope, with structural events propagated back to the parent)
3. Calling `harness.New(childConfig, childLedger, childBus, childConcern)` to construct the child
4. Spawning stances within the child harness as needed for the sub-work

The child harness is fully functional. It has its own supervisor (which the parent starts when the child is created), its own rule set, its own stance sessions. The parent's supervisor sees structural events from the child's bus (via the upward propagation filter from component 3) and can evaluate whether the child's work is progressing or needs intervention.

When the child's work is done (converged or escalated), the parent decides what to do with it:

- **Integrate.** The child's ledger nodes that are worth keeping are copied into the parent's scope via the parent's consensus loop. The rest stays in the child's sub-scope as historical record.
- **Discard.** The child's scope is abandoned. The sub-ledger and sub-bus event log are preserved for audit, but no nodes are promoted into the parent's scope.
- **Iterate.** The child is torn down and a new child is spawned with a different approach.

The nested harness pattern is the same mechanism for all sub-work, not a special case for experiments. The team can use it for any work that benefits from isolation. The overhead is real (spawning a child harness is heavier than spawning a stance within the current harness) so the team should only use it when isolation is genuinely needed — the consensus loop asks "does this work warrant its own scope" as part of the decision to nest.

---

## Package structure

```
internal/harness/
├── doc.go
├── harness.go               // the main Harness struct and public API
├── harness_test.go
├── spawn.go                 // stance creation flow
├── spawn_test.go
├── lifecycle.go             // pause, resume, terminate, recover
├── lifecycle_test.go
├── session.go               // StanceSession struct and internal state management
├── session_test.go
├── models/
│   ├── provider.go          // the ModelProvider interface
│   ├── anthropic.go         // Anthropic implementation
│   ├── openai.go            // OpenAI implementation
│   ├── mock.go              // mock implementation for tests
│   └── *_test.go
├── tools/
│   ├── tool.go              // the Tool interface and authorization model
│   ├── file_read.go
│   ├── file_write.go
│   ├── code_run.go
│   ├── web_search.go
│   ├── web_fetch.go
│   ├── ledger_query.go
│   ├── ledger_write.go
│   ├── skill_import_propose.go
│   ├── bus_publish.go
│   ├── bus_subscribe.go
│   ├── research_request.go
│   └── *_test.go
├── prompts/
│   ├── builder.go           // system prompt construction
│   ├── templates.go         // role-specific system prompt templates
│   └── *_test.go
└── nested/
    ├── nested.go            // nested harness creation and teardown
    └── nested_test.go
```

The harness is a larger package than most — it has to cover model providers, tool implementations, prompt construction, session management, lifecycle, and the nested-harness pattern. But each sub-package is focused and independently testable.

---

## What the harness does not do

- **Hold rules.** The supervisor holds rules. The harness executes spawn requests from the supervisor; it does not decide when to spawn.
- **Decide what's in the concern field.** The concern field builder does. The harness calls the builder and uses its output.
- **Select which skills to load.** The concern field templates decide. The harness renders whatever the concern field contains.
- **Review stance output.** Reviewers review stance output, the supervisor's trust rules enforce review, and the harness just spawns the reviewer when told to.
- **Modify the ledger except for stance-specific nodes.** The harness writes `stance.spawned` events (as bus events, not ledger nodes) and commits `skill_loaded` nodes via the concern field builder. It does not write decision nodes, draft nodes, or any substantive reasoning content.
- **Run on its own schedule.** The harness is reactive. It does work when called. It does not poll, does not tick, does not run background loops — every harness action is triggered by a supervisor hook, a bus event, or an external library call.

---

## Validation gate

1. ✅ `go vet ./...` clean, `go test ./internal/harness/...` passes with >70% coverage on the core and >80% on the tool implementations
2. ✅ `go build ./cmd/r1` succeeds
3. ✅ The harness's public API (`New`, `SpawnStance`, `PauseStance`, `ResumeStance`, `TerminateStance`, `InspectStance`, `ListStances`, `Recover`) is callable from external Go code (verified by a test that imports `internal/harness` from a test package and calls each method)
4. ✅ A valid spawn request produces a running stance with the expected role, scope, model, and tool set
5. ✅ An invalid spawn request (unknown role, nonexistent task scope, unauthorized tool override) is rejected before any model API call is made
6. ✅ Pause requests are honored at the stance's next safe checkpoint, not immediately mid-turn
7. ✅ Resume requests attach additional context as a separate block, not folded into the original concern field
8. ✅ Tool authorization is enforced at tool-call time: an unauthorized tool call is rejected and a `stance.tool_rejected` event is emitted
9. ✅ The tool authorization for a role matches the "Written by" fields in component 6 for the node types that role is allowed to write
10. ✅ The harness's `Recover` method rebuilds active stance state from the ledger and bus after a simulated crash
11. ✅ A stance whose model provider cannot be resumed after crash is terminated cleanly (not left in a corrupt state)
12. ✅ Second-opinion stances spawned by trust rules prefer a different model family than the original stance when possible (verified by a test that spawns a Reviewer second-opinion and confirms the family differs from the declaring stance's family)
13. ✅ Nested harnesses can be created with their own scoped ledger and bus, and structural events from the child propagate to the parent via the bus's propagation rules
14. ✅ A child harness can be torn down without corrupting the parent's state (verified by a test that creates a child, runs work in it, discards it, and confirms the parent is unaffected)
15. ✅ The harness does not poll — no `time.Tick`, `time.NewTicker`, or sleep-and-loop patterns in the harness codebase (verified by grep on `internal/harness/`)
16. ✅ Token and dollar cost tracking is accurate per stance and is emitted as `mission.budget.update` events (verified by a test that runs a stance with mock model provider and compares tracked costs to the mock's reported costs)
17. ✅ The library API can be called from a test harness (mock ledger, mock bus, mock concern field builder) without any real LLM API calls occurring
18. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

- **The bench** is component 12. The bench invokes the harness's library API directly to run scenarios against mock or real models, using the headless interface.
- **Implementation order and validation gates** is component 13. The harness is one of the later components in the implementation order because it depends on the substrate being in place.

The next file to write is `12-the-bench.md`. The bench is how we measure whether Stoke works — how we run scenarios, collect metrics, compare configurations, and produce evidence that the trust rules and consensus loops actually improve outcomes.
