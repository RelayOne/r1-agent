# 05 — The Consensus Loop

The consensus loop is how every decision in Stoke gets made. Same loop shape regardless of what is being decided — what's in the PRD, what's in the SOW, whether a PR is ready to merge, whether a refactor is justified, whether a task is done, whether an escalation is warranted. The participants change. The artifact under review changes. The state machine and the convergence criterion do not.

The loop is not its own subsystem. It is a *pattern* that lives on the substrate that has already been specified:

- The artifact under review is a node in the ledger
- The loop's state is a field on a loop node in the ledger
- Iterations of the loop produce new draft nodes with `supersedes` edges to prior drafts
- Reviews produce agree and dissent nodes attached via edges
- State transitions are driven by supervisor rules that fire on bus events when the conditions hold
- Convergence is detected by the supervisor's `consensus.convergence.detected` rule walking the graph and checking the structural criterion

This file specifies what the loop is, what its states are, what its transitions look like, and where the heavy lifting lives in other components. It is intentionally short. If a loop spec ever grows large, that means the substrate is leaking and the loop is having to re-implement what should be in the ledger, the bus, or the supervisor.

---

## What the loop is

A loop is a finite state machine attached to an artifact. The artifact is a ledger node — a PRD draft, a SOW draft, a PR, a refactor proposal, a completion claim, a Judge invocation, an escalation request. Anything that needs consensus becomes the artifact of a loop, and the loop is the mechanism by which the artifact gets from "proposed" to either "accepted" or "escalated."

The loop itself is a ledger node. Its node type is `loop`, its content includes the artifact reference, the convened consensus partners, the iteration count, the current state, the parent loop (if any), and the child loops (if any). The loop's state field is what supervisor rules transition. The loop's other fields are what queries inspect to answer "where is this loop right now."

Loops are persistent in the ledger because the ledger is the persistent substrate. A loop that runs for an hour and a loop that runs for a week have the same on-disk representation; a crash mid-loop recovers the loop's state from the ledger; a post-mortem on a stuck loop reads the loop node and walks its history.

Loops nest. The root loop of any mission is the PRD loop, whose artifact is the PRD draft. When the PRD loop converges, it spawns the SOW loop. When the SOW loop converges, it spawns ticket loops. Each ticket loop spawns a PR review loop when its work is ready. Each PR review loop may spawn fix-cycle loops if dissents need to be addressed. The full history of a mission is queryable as a tree of loop nodes via parent/child edges.

Loop scoping matches task DAG scoping. A loop attached to a ticket-level artifact has the ticket's task DAG node ID in its scope; a loop attached to a feature-level artifact has the feature's task DAG node ID. The supervisor uses the scope to decide which rules apply (branch supervisor rules fire on branch-scoped loops, mission supervisor rules fire on mission-scoped loops) and the concern field uses the scope to project the right slice of the ledger into each consensus partner's prompt context.

---

## The loop states

There are seven states. Three of them are work-in-progress, two are iteration mechanics, two are terminal.

**`proposing`** — A stance has been asked to produce an artifact and is doing the work. No ledger writes have happened yet for this iteration; the work in progress lives in the stance's session. The loop is in `proposing` from the moment it is created until the proposing stance commits the first draft node.

**`drafted`** — The proposing stance has committed a draft node to the ledger. The draft is now visible to other components. The loop transitions to `drafted` automatically when the supervisor sees the `ledger.node.added` event for a draft attached to this loop.

**`convening`** — The supervisor's `consensus.draft.requires_review` rule has fired and is in the process of spawning the convened consensus partners as fresh-context stances. Each partner is being given the draft, the projected concern field, and the prompt to review. This state is brief — just long enough for the harness to spin up the partner stances.

**`reviewing`** — The convened partners are independently reviewing the draft. Each one will produce either an agree node or a dissent node attached to the draft. The loop stays in `reviewing` until all convened partners have committed their review nodes.

**`resolving dissents`** — At least one dissent was filed. The supervisor's `consensus.dissent.requires_address` rule fired and transitioned the loop. The proposing stance is reading the dissent(s), optionally doing additional research (which may produce its own ledger nodes), and revising. When the proposing stance commits a new draft node with a `supersedes` edge to the prior draft, the loop returns to `drafted` and the cycle iterates.

**`converged`** — All convened partners have agreed on the current draft. The supervisor's `consensus.convergence.detected` rule has fired and transitioned the loop. The artifact in its terminal form is whatever the latest draft node was. Downstream loops can spawn from this terminal state. The loop is closed.

**`escalated`** — The loop did not converge under its own mechanics. Either the iteration threshold was hit and the Judge was invoked and the Judge's verdict was something other than "keep iterating," or a dissent was deadlocked and explicitly forwarded upward, or a budget threshold was crossed. The escalation has been recorded as a ledger node with edges to the loop history. Whatever consumes the escalation (the parent loop, the user via PO) takes ownership from here. The loop is closed.

`converged` and `escalated` are both terminal. The difference is what happens next: a converged loop's downstream consumers can use the artifact directly; an escalated loop's downstream consumers have to wait for the escalation to be resolved before they can proceed.

---

## The transitions

Each transition is driven by a supervisor rule firing on a specific event or condition. This is the entire transition table:

| From | To | Trigger | Driven by |
|---|---|---|---|
| (none) | `proposing` | Loop creation | Parent loop's terminal action, or initial mission start |
| `proposing` | `drafted` | First draft node committed | Supervisor's automatic state-from-ledger reconciliation |
| `drafted` | `convening` | `consensus.draft.requires_review` rule fires | Supervisor (rule from category 2) |
| `convening` | `reviewing` | All convened partner stances have been spawned | Harness reports completion of spawning |
| `reviewing` | `resolving dissents` | First dissent node committed | Supervisor's `consensus.dissent.requires_address` rule (rule from category 2) |
| `reviewing` | `converged` | All convened partners have committed agree nodes, no dissents | Supervisor's `consensus.convergence.detected` rule (rule from category 2) |
| `resolving dissents` | `drafted` | New draft node with supersedes edge committed | Supervisor's automatic state-from-ledger reconciliation |
| `drafted` (or any iterating state) | `escalated` | Iteration threshold hit OR Judge says escalate OR budget exceeded OR explicit forwarding | Supervisor (rules from category 6 and category 5) |
| `reviewing` | `escalated` | Dissent forwarded upward by `hierarchy.escalation.forwards_upward` rule | Supervisor (rule from category 5) |

Every transition is a supervisor action. The loop itself does not transition itself — there is no daemon inside the loop watching its own state. The loop is a passive node in the ledger; the supervisor is the active enforcer that updates the loop's state field by committing new ledger nodes.

This means the entire loop's behavior is auditable: every state transition is a ledger write, every ledger write has an author (the supervisor), every supervisor write has an event causality chain back to the worker event or change-stream condition that triggered it, and the bus event log preserves the full sequence durably. A post-mortem on any loop reads the loop's state-transition history from the ledger and the corresponding event sequence from the bus log.

---

## The convergence criterion

Convergence is structural. A loop has converged when all of the following hold:

1. The current draft node exists in the ledger with all required schema fields filled per its node type
2. Every consensus partner identified at convening time has committed either an agree node or a dissent node attached to the current draft (not a prior draft — the current one)
3. All dissent nodes attached to the current draft have been resolved — meaning the dissenting partner has subsequently committed an agree node on a later draft, or the dissent has been formally forwarded upward via escalation
4. No outstanding dissents remain on the current draft

These are the four conditions the supervisor's `consensus.convergence.detected` rule checks. The rule fires immediately on any `ledger.node.added` event that could change whether the conditions hold — agree nodes, dissent nodes, or new draft nodes attached to an active loop. (These are the only commits that can affect the convergence state, so they are the only events that need to trigger the check.) When the rule fires, it walks the loop, queries the ledger for the relevant nodes, and either commits the state transition (if the criterion holds) or exits silently (if it does not).

The convergence criterion is queryable. Any caller — the supervisor, a debugging tool, the user via the dashboard — can run the same query the supervisor runs and get the same answer. There is no judgment call at the convergence point. There is no "good enough" — there is a structural condition that holds or does not hold.

Convergence detection is event-driven. There is no polling, no periodic re-evaluation, no "every N seconds the supervisor checks." A loop that meets the convergence criterion transitions on the next ledger commit that satisfies the final condition, with latency bounded only by the bus's event delivery and the rule's evaluation cost — both sub-second on a healthy system.

---

## Iteration and the Judge

A loop iterates by going through `drafted → convening → reviewing → resolving dissents → drafted` cycles. Each cycle produces a new draft node with a `supersedes` edge to the prior draft, which the supervisor's automatic reconciliation picks up to restart the cycle.

The cycle count is bounded. The supervisor's `consensus.iteration.threshold` rule fires when the count of supersedes-edge predecessors of the current draft exceeds a per-loop-type configurable threshold (defaults: 5 for PRD loops, 3 for PR review loops, 2 for refactor proposal loops). The rule spawns a Judge stance.

The Judge is a fresh-context stance created with the loop history, the original user intent, and the dissent chain as inputs. The Judge does not propose code, designs, or plans — it produces a verdict node with one of four values:

- **Keep iterating.** The loop is making progress; the Judge sees no reason to intervene. The loop returns to its current state and continues.
- **Switch approaches.** The current approach is exhausted. The loop transitions back to `proposing` with the entire prior draft chain marked as exhausted via supersedes edges. The proposing stance starts a new draft from a fundamentally different angle, with the exhausted chain available as ledger context so the same approach is not attempted again.
- **Return to PRD.** The loop has drifted from the original intent. The loop transitions to `escalated` with a `drift_detected` flag. The PO is notified and the PRD's own loop reopens with the drift evidence as input.
- **Escalate to user.** Nothing in the option space satisfies the constraints; the user needs to be involved. The loop transitions to `escalated` with a `user_required` flag. The mission supervisor's `hierarchy.user_escalation` rule fires and produces a user-facing message via the PO.

The Judge's verdict is itself a ledger node. The loop's transition is committed by the supervisor reading the verdict and applying the corresponding rule. The Judge does not transition the loop itself; the supervisor does, on the basis of the Judge's verdict.

A loop can have its Judge invoked multiple times. Each invocation is a new fresh-context Judge stance with no memory of prior invocations. The Judge reads the loop history from the ledger, which includes prior Judge invocations and verdicts, so a Judge looking at a loop that has already been Judged twice can see the prior verdicts and reason about them — but the Judge stance itself is fresh. There is no accumulated bias.

The drift detection rules in the supervisor's category 6 also trigger Judge invocations on different conditions: slow drift, milestone boundaries, budget thresholds, cross-branch divergence patterns. These produce the same kind of Judge stance with the same verdict surface, just triggered by different conditions. From the loop's perspective, a Judge invocation is a Judge invocation regardless of which rule fired it.

---

## Research is not a verdict

A Judge that needs more information to produce a verdict does not have a "defer" option. Instead, the Judge — like any other stance in Stoke — can emit a `worker.research.requested` event at any point during its evaluation. The event payload includes the specific question, the context that produced the uncertainty, and the audience the research is for.

The supervisor's research rules (category 7) handle the request:

- The requesting stance (the Judge, in this case) is paused
- One or more fresh-context researcher stances are dispatched through the harness, each with the question and the relevant ledger slice
- The researchers do their work — web searches, codebase queries, ledger walks, whatever the question requires — and produce research report nodes in the ledger
- When all dispatched researchers have either committed reports or timed out, the requesting stance is unpaused with the report(s) attached to its concern field
- The Judge resumes its evaluation with the new information and only then produces its verdict (one of the four values above)

This is the same mechanism any stance uses when it encounters uncertainty during its work. A Dev that does not know how to implement something, a Reviewer that does not know whether a proposed change is safe, a Lead Engineer that does not know which architectural approach fits a constraint — all of them can emit `worker.research.requested` events and pause until the research report comes back. Research is not a step before work; it is an action available at any point during work, available to any stance, including the Judge.

The paused-and-dispatched pattern preserves the loop's overall progress. While the Judge is paused waiting for research, the loop is in a paused-pending-research substate (still considered active for the purposes of the supervisor's other rules, but no further action will be taken until the research returns). The loop's other workers may still be progressing on independent fronts, but the specific decision the Judge was evaluating is held until the Judge has the information it needs.

---

## Loops nest

Every loop except the root of a mission has a parent loop. Every loop except a leaf has child loops. The relationship is recorded as edges in the ledger:

- A loop node has a `parent_loop` edge to its parent (or no parent edge if it is the root)
- A loop node has zero or more `child_loop` edges to its children

When a loop's terminal state is reached (`converged` or `escalated`) and the loop's resolution unblocks downstream work, the loop's terminal action spawns child loops. Examples:

- The PRD loop converging spawns the SOW loop with the converged PRD as input
- The SOW loop converging spawns one ticket loop per ticket in the decomposition
- A ticket loop's work being ready spawns its PR review loop
- A PR review loop's dissent that requires research spawns a research loop with the question as the artifact

A loop in `escalated` state does not spawn its normal child loops — instead, it spawns an escalation-handling loop whose participants and convergence criteria are different (e.g., the escalation is to the user, so the convergence criterion is "user has responded with a directive").

The parent/child structure is queryable from any direction. "What loops are descendants of this mission's root loop" returns the entire tree. "What is the parent chain of this PR review loop" returns the path from the PR loop up to the root. "What sibling loops exist for this ticket" returns the other tickets at the same level. The Judge, the SDM, and the user-facing dashboard all use these queries to answer "where in the task am I and what else is in flight."

---

## Loop creation and destruction

Loops are created by other loops (or by the mission start). A loop's parent is responsible for creating it. The creating loop produces a `loop_created` event on the bus and commits a new loop node to the ledger; downstream consumers (the supervisor's rules, the harness) react to the event by spawning the proposing stance.

Loops are not destroyed. A loop in a terminal state stays in the ledger forever, with its full history intact. "Destroying" a loop is not a meaningful operation — the substrate is append-only, so there is nothing to destroy. A loop's lifecycle is "created → iterating → terminal," where terminal is permanent.

A loop in a terminal state is no longer the subject of supervisor rules — the supervisor's rules either fire on event patterns that only apply to active loops (`drafted`, `reviewing`, `resolving dissents`) or on change-stream conditions that exclude terminal loops. A converged loop produces no further events of its own; it is read-only from the moment of convergence.

The bus event log retains all events related to a loop (including its terminal events) for the lifetime of the mission plus the configured grace period for post-mortem analysis. The ledger nodes for the loop persist forever, like everything else in the ledger.

---

## What the loop spec does not do

A few things the loop spec does not specify, with brief notes on where they live:

- **Schemas for loop node types and consensus partner stance contracts.** These are in component 6 (node types). This file says "the loop is a node, the artifact is a node, agreements and dissents are nodes attached to the draft" — the actual fields on each are the node types component's job.

- **The query templates the concern field uses to project the ledger into per-stance prompts.** These are in component 7 (the concern field). This file says "the convened partners get their concern field" — the concern field's specific query patterns are not in this file.

- **The spawning of stances.** The harness component (component 9 or 10) specifies how a stance gets created from a system prompt template + context + model selection. This file says "the supervisor's rule spawns a partner" — the actual spawning mechanism is the harness.

- **The skill manufacturer's consumption of completed loops.** Component 8 (skill manufacturer). This file says the loop terminates in `converged` or `escalated`; the skill manufacturer is the process that watches for these terminal events and runs its extraction. The trigger event is emitted by the supervisor's `skill.extraction.trigger` rule (category 7 in the supervisor spec).

- **The bus event types for loop state transitions.** The bus event taxonomy is part of the bus spec and the worker contract specs. This file refers to events like `loop.converged` and `loop.escalated` without defining them in full. The full event schemas live in the components that produce them.

The loop spec is intentionally thin. The loop is not a substrate; it is a coordination pattern. The substrate is the ledger and the bus; the enforcement is the supervisor. The loop is what those three things, together, *produce* when they are operating on a decision artifact. Specifying the loop in detail would mean re-specifying the substrate, which is the wrong shape.

---

## Validation gate

The loop has a validation gate, but most of the things being validated are validations of the components below it. The loop-specific gate is:

1. ✅ A loop node committed to the ledger has all required schema fields per the loop node type
2. ✅ A loop in `proposing` state transitions to `drafted` when the proposing stance commits the first draft node
3. ✅ A loop in `drafted` state transitions to `convening` when the `consensus.draft.requires_review` rule fires
4. ✅ A loop in `convening` state transitions to `reviewing` when all convened partner stances have been spawned by the harness
5. ✅ A loop in `reviewing` state transitions to `resolving dissents` when the first dissent node is committed
6. ✅ A loop in `reviewing` state transitions to `converged` when all convened partners have agreed and no outstanding dissents remain
7. ✅ A loop in `resolving dissents` state transitions to `drafted` when a new draft node with a supersedes edge is committed
8. ✅ A loop in any iterating state transitions to `escalated` when the iteration threshold is hit and the Judge says escalate
9. ✅ A loop in any iterating state transitions to `escalated` when the budget threshold is crossed
10. ✅ A loop in a terminal state does not respond to further supervisor rules
11. ✅ A loop's terminal action spawns the correct child loops (PRD → SOW, SOW → tickets, ticket → PR review)
12. ✅ The convergence criterion is queryable as a structural check against the ledger; running the query produces the same answer regardless of caller
13. ✅ A loop's full history (state transitions, drafts, agreements, dissents, Judge invocations) is queryable from the ledger via parent/child and supersedes edges
14. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

The loop's validation gate is short because the substrate gates (ledger, bus, supervisor) have already validated most of the underlying mechanics. The loop gate validates that the *coordination pattern* works correctly given the substrate works correctly.

---

## Forward references

This file is component 5 of the new guide. It refers to several things specified in later components:

- **Loop node schemas** (the fields on a `loop` node in the ledger) are in component 6 (node types)
- **Concern field projection** for convened consensus partners is in component 7
- **Skill extraction from completed loops** is in component 8
- **Stance spawning from supervisor hooks** is in the harness component (later)
- **Bus event taxonomies for loop state transitions** are part of the bus spec and the components that produce them

The next file to write is `06-the-node-types.md`. The node types component spells out the schemas for every node type in the ledger: loop nodes, draft nodes, agree nodes, dissent nodes, decision nodes (internal), decision nodes (repo), task DAG nodes, skill nodes, snapshot annotation nodes, escalation nodes, Judge verdict nodes. It is the schema reference that the loop spec, the consensus rules, and the concern field all refer back to.
