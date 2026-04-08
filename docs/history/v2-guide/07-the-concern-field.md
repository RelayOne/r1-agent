# 07 — The Concern Field

The concern field is what makes a stance not act like an isolated junior dev. When a stance is spawned, the harness builds its system prompt and its working context from a query against the ledger, scoped to the task DAG node the stance is operating within. The result is a structured projection of "everything this stance needs to know about its surroundings to do its job responsibly" — prior decisions, related work in flight, applicable skills, snapshot annotations, dissents that informed the current state, the original user intent. The stance reads this once at spawn time and refers to it as it works.

The concern field is a layer on top of the substrate components. It is not its own substrate. The query templates live in code; the data being queried lives in the ledger; the events that trigger query construction live on the bus; the schemas of the queried nodes are defined in component 6. The concern field is what those components, taken together, *project* into a stance's working context.

This file is short because the heavy lifting is already done. It specifies the query templates per stance role, the parameterization by task DAG scope, the two faces (proposing and reviewing), the merge behavior up the supervisor hierarchy, and the validation discipline.

---

## What the concern field is

A concern field is a structured bundle of information rendered into a stance's prompt context at spawn time. It has roughly the following shape, represented in the prompt as labeled sections:

- **Original user intent** — the verbatim text of the user's mission, plus the PRD if one has converged. Always present. Never paraphrased — the stance reads what the user actually said, not what some intermediate stance interpreted.
- **Task DAG scope** — the task node this stance is operating within, plus its parent chain up to the mission, plus its sibling tasks at the same level (so the stance knows what else is in flight)
- **Relevant prior decisions** — internal and repo decision nodes scoped to the task, with the directionality rule applied (the stance sees both internal and repo decisions, but the internal ones are clearly labeled as working notes vs the repo ones being public record)
- **Applicable skills** — skill nodes whose `applicability` criteria match the task type, the file types involved, and the role of this specific stance
- **Snapshot annotations in scope** — snapshot annotation nodes for any files or modules the task touches
- **Active loops in scope** — loop nodes whose state is non-terminal and whose task DAG scope is the current task or its descendants
- **Dissent and Judge history** — for review-shaped tasks, the dissents and Judge verdicts attached to prior drafts in the same loop (so a Reviewer reviewing iteration 3 sees what iterations 1 and 2 disagreed about and how the proposing stance addressed it)
- **SDM advisories in scope** — `sdm_advisory` nodes affecting this branch or this task
- **Research reports in scope** — research reports the proposing stance consulted, attached to the current draft via `references` edges
- **Recent ledger activity in scope** — the last N nodes committed to the same task DAG scope, as a recency feed (so the stance sees what the team has been doing very recently, even if it's not yet a formal decision)

Not every section is present in every concern field — what's relevant depends on the stance role and the task. A Dev implementing a ticket gets prior decisions, applicable skills, snapshot annotations, and the original user intent — but not the dissent history of unrelated PR loops. A Reviewer reviewing a PR gets the dissent history of the current loop (heavily) and the prior decisions and skills (lightly). The query templates per stance role determine which sections are populated and how deeply.

---

## The two faces

The concern field has two faces depending on the stance's job: **proposing** and **reviewing**.

A **proposing concern field** is for stances that are producing a new artifact — a Dev writing code, a Lead Engineer drafting a SOW, a PO drafting a PRD, a Lead Designer producing a design spec, a researcher producing a report. The proposing concern field emphasizes:

- What the stance is being asked to produce (the task acceptance criteria, the loop's expected artifact type)
- What constraints the stance must respect (snapshot annotations, applicable skills, prior decisions that bound the solution space)
- What information has already been gathered that the stance should not re-derive (research reports, prior consensus from related loops)
- What the original user intent says (so the stance is producing toward the actual goal, not a paraphrase)

A **reviewing concern field** is for stances that are evaluating an artifact someone else produced — a Reviewer reviewing a PR, the Judge evaluating a stuck loop, a CTO consulting on a snapshot modification, a QA Lead checking acceptance criteria, the mission supervisor's parent-agreement evaluation logic when evaluating a branch completion proposal. The reviewing concern field emphasizes:

- The artifact under review (the draft node being evaluated)
- The proposing stance's reasoning and the inputs the proposing stance had (so the reviewer knows what was considered)
- Prior dissents in the same loop and how they were addressed (so the reviewer knows what concerns have already been raised and resolved)
- The acceptance criteria the artifact is being measured against
- Independent verification information (the original user intent, applicable skills, snapshot annotations) — same sources the proposing stance had, so the reviewer can do an independent comparison rather than just trust the proposing stance's framing
- Whatever the reviewer's specific role demands (a CTO gets snapshot annotations heavily; a QA Lead gets acceptance criteria heavily; the Judge gets the entire loop history)

The two faces share most of their query templates. They diverge in what they emphasize and what additional sections they pull. A stance that is sometimes proposing and sometimes reviewing (e.g., a Reviewer that becomes a proposing stance for the next iteration after a dissent) gets a different concern field for each role it occupies — the same underlying query templates, parameterized differently.

---

## Query templates per stance role

Each stance role has its own concern field query template that specifies which sections to populate and with what filters. The templates live in code under `internal/concern/templates/{stance_role}.go` — one file per stance role, named after the role. Each template is a small Go struct that specifies:

- The list of sections to include in the concern field
- For each section, the ledger query to run (using the `ledger.Query` API from component 2) parameterized by task DAG scope
- For each section, how to render the query result into the stance's prompt context (formatting, labeling, ordering)
- Per-section caps to prevent unbounded prompts (e.g., "include the most recent 20 prior decisions, not all of them")

A simplified example of a template, in pseudocode:

```
template ReviewerForPR:
  sections:
    - original_user_intent: always_present
    - task_dag_scope: include_parent_chain_to_mission
    - artifact_under_review: query_draft_by_loop(current_loop_ref)
    - prior_dissents_in_loop: query_dissents_attached_to_drafts_in_loop(current_loop_ref)
    - applicable_skills: query_skills_matching_task_type(task.type, role="reviewer")
    - snapshot_annotations: query_annotations_for_files_touched_by_draft(draft.files)
    - prior_decisions_in_scope: query_decisions_scoped_to(task_dag_node, depth=3) | cap=20
    - sdm_advisories: query_advisories_for_branch(branch_task_id) | severity >= "coordinate_soon"
```

The template is a recipe; the harness reads the recipe and runs the queries against the ledger when spawning the stance. The result is rendered into the stance's system prompt before the stance's session begins.

There is one template per distinct role-and-context combination. Examples of templates that exist:

- `dev_implementing_ticket` — proposing, scoped to a ticket
- `dev_fixing_dissent` — proposing, scoped to a fix-cycle subloop, with the prior dissent prominently featured
- `reviewer_for_pr` — reviewing, scoped to a PR loop
- `reviewer_for_sow` — reviewing, scoped to a SOW loop
- `judge_for_iteration_threshold` — reviewing, with full loop history and original intent
- `judge_for_drift_check` — reviewing, with intent alignment as the primary lens
- `cto_snapshot_consultation` — reviewing, with snapshot annotations and the proposed change
- `lead_engineer_drafting_sow` — proposing, scoped to a converged PRD
- `po_drafting_prd` — proposing, scoped to a fresh mission with no prior context
- `researcher_for_uncertainty` — proposing, scoped to a research request, with the question and the requesting stance's context as primary inputs
- `qa_lead_checking_acceptance` — reviewing, with the acceptance criteria as the primary lens
- `stakeholder_for_escalation` — reviewing, scoped to an escalation, with the original user intent, the loop history, prior Stakeholder directives in the same mission, the escalation context, and the relevant snapshot annotations as primary inputs. Loaded only when the mission's operating mode is `full_auto` and the supervisor's `hierarchy.user_escalation` rule has spawned a Stakeholder. The template carries the configured Stakeholder posture (`absolute_completion_and_quality`, `balanced`, or `pragmatic`) which the harness applies to the system prompt at construction time.

Adding a new template means adding a new file under `internal/concern/templates/`. Templates are testable in isolation — given a synthetic ledger snapshot and a synthetic task DAG scope, the template's query result is deterministic and can be asserted.

---

## Skill loading is observable

Skills are a special section of the concern field. When a template's `applicable_skills` section runs its query and returns one or more skill nodes to include in the rendered output, the concern field builder does two additional things beyond simply rendering the skill content into the prompt:

**First, it commits a `skill_loaded` ledger node** (schema in component 6) for each loaded skill. The node records: which skill was loaded, which stance it was loaded into, which concern field template pulled it, which specific applicability criteria matched, the task DAG scope, the loop reference. This is the substrate record of the load.

**Second, it emits a `skill.loaded` bus event** referencing the new `skill_loaded` node. The supervisor's `skill.load.audit` rule (component 4, category 9) subscribes to this event and records the load in its audit trail.

Both the ledger write and the event emission happen before the concern field is returned to the harness — the stance never sees a concern field that includes a skill whose load has not been logged. This is the load-bearing property that makes skill governance real: skill use is not a quiet implementation detail of concern field construction, it is a first-class event that the supervisor observes and reviews.

The rendered skill content in the prompt includes an explicit framing that the stance will read at spawn time:

> The following skills have been loaded into your context. If you apply any of them in your output, you must record the application in a `skill_applied` ledger node with a substantive `how_applied` field describing how the skill shaped your work. Your immediate supervisor reviews skill applications, especially for skills below `proven` confidence. Loading a skill does not require you to apply it — read each one and decide whether it is relevant to the current situation. If you judge a skill to be irrelevant or contradicted by context, you may ignore it, but you must not silently apply a skill without recording the application.

This framing is part of the base template rendering and is non-configurable. Every stance that receives a concern field with skills in it sees this framing. It is how the stance learns that skill use is observable; it is also how the stance is given the responsibility of recording its applications.

Skills with `confidence: candidate` or `footgun_annotation` set are rendered with additional framing: candidate skills are labeled as "not yet proven — apply with caution and record the outcome carefully," and footgun-annotated skills are rendered as warnings rather than guidance ("stances have applied this pattern before and it led to bad outcomes; here's why to avoid it").

---

## Parameterization by task DAG scope

Every concern field is parameterized by a task DAG node ID (the scope) and a stance role (the template). The scope determines what slice of the ledger is queried; the template determines which sections are populated and how.

The scope is hierarchical. A ticket-scoped concern field includes the ticket itself, but the queries also walk up the parent chain (`task_dag_scope = ticket | parent | parent.parent | ... | mission`) to pull in scope-relevant context from higher levels. A decision made at the feature level is visible to a stance scoped to a ticket within that feature. A skill recorded at the mission level is visible to every stance in the mission.

The scope walking is controlled per-section in the template. Some sections (like `original_user_intent`) always walk to the mission root. Some sections (like `prior_decisions_in_scope`) walk a configurable depth (default: parent chain to feature level, but template can override). Some sections (like `prior_dissents_in_loop`) are tightly scoped to the current loop and do not walk at all.

The scope walking respects the directionality rules from component 6: a stance scoped to a ticket sees decisions from the ticket's parent chain, but does not see internal decisions from a sibling ticket's loop (because internal decisions are scoped to their loop and are not shared across sibling loops). It does see repo decisions from the sibling ticket's loop if they have been distilled and committed (because repo decisions are public to the next reader, regardless of which loop produced them).

---

## The merge pattern up the hierarchy

The concern field of a parent task includes the union of relevant slices from its child tasks. A Lead Engineer scoped to a feature-level task sees, in their concern field, a summary of what's happening across all the tickets in that feature — recent decisions, open dissents, SDM advisories, blocked work. The merge is at query time, not at storage time — there is no "merged concern field" stored anywhere. Each query the Lead Engineer's template runs walks down the task DAG into the children and pulls the relevant slices.

The merge has caps. A Lead Engineer for a feature with 50 tickets does not get 50 ticket-level dissent histories in their concern field — the per-section cap kicks in and the template renders something like "12 active dissents across 8 tickets, most recent 5 shown below" with summaries. The cap is configurable per-template per-section.

The mission supervisor's parent-agreement evaluation uses a similar merge — the mission supervisor's concern field for evaluating a branch completion proposal includes the branch's full loop history (because that's the artifact being evaluated) plus a cross-branch slice from the SDM (so the mission supervisor sees what other branches are doing) plus the original user intent (so the mission supervisor can independently verify the branch's claimed deliverable matches what was asked for).

The merge pattern is what makes higher-level stances aware of what's happening below them without forcing them to read everything below them. The query templates do the filtering and summarization at query time; the rendered concern field is bounded; the stance reads it once at spawn and works from it.

---

## Where it lives

The concern field code lives in `internal/concern/`. The package structure:

```
internal/concern/
├── doc.go                       // package documentation referencing this file as canonical
├── builder.go                   // the main API the harness calls: BuildConcernField(role, scope, ledger) → ConcernField
├── builder_test.go
├── render.go                    // rendering a ConcernField struct into prompt text
├── render_test.go
├── templates/
│   ├── dev_implementing_ticket.go
│   ├── dev_implementing_ticket_test.go
│   ├── reviewer_for_pr.go
│   ├── reviewer_for_pr_test.go
│   ├── judge_for_iteration_threshold.go
│   ├── judge_for_iteration_threshold_test.go
│   ├── ... (one file per template)
└── sections/
    ├── original_user_intent.go  // the query and rendering for the original user intent section
    ├── prior_decisions.go       // ditto for prior decisions
    ├── applicable_skills.go     // ditto for skills
    ├── ...
```

The `sections/` directory is where the per-section query and rendering logic lives. The `templates/` directory composes sections into per-role templates. The `builder.go` is the entry point the harness calls when spawning a stance: pass in the role and the scope, get back a `ConcernField` struct, render it into the stance's system prompt.

This separation lets sections be tested independently of templates, templates be tested independently of the builder, and the builder be tested with mock templates and mock sections. The dependency chain is clean: builder depends on templates, templates depend on sections, sections depend on ledger queries.

---

## What the concern field does not do

A few things the concern field explicitly does not handle:

- **Stance session state.** The concern field is constructed once at spawn and rendered into the system prompt. It is not updated mid-session. A stance's working memory during its session is the stance's responsibility, not the concern field's. If a stance needs new information mid-session (e.g., research it requested came back), the supervisor unpauses the stance with the new information attached as an additional context block — but the concern field itself is not re-rendered.

- **Real-time event subscription.** The concern field is a snapshot at construction time. It is not a live view. A stance does not "see" new ledger commits as they happen — it sees what was in the ledger at the moment its concern field was built. If something happens during the stance's session that the stance needs to know about, the supervisor pauses the stance and gives it the new information, then unpauses. The concern field is one-shot.

- **Cross-stance communication.** Stances do not talk to each other through the concern field. They emit events on the bus, which produce ledger commits, which are visible to the next stance whose concern field is built. The concern field is the projection of past ledger state into a prompt; it is not a messaging channel.

- **Per-stance memory.** Each stance is fresh. Its concern field is constructed from the ledger, not from a per-stance memory store. There is no "what this stance remembered from last time" — there is "what was in the ledger when this stance spawned." If a stance is paused and resumed, the same concern field is restored (the harness preserves it for the duration of the pause); if a stance terminates and a new stance of the same role is spawned, the new stance gets a freshly built concern field that may differ from what the prior stance had if the ledger has changed.

- **Filtering for cost.** The concern field is bounded by per-section caps, but the bounding is for prompt size and stance comprehension, not for cost optimization. Cost-aware loading (e.g., "only load skills that have been useful in similar tasks recently") is not in scope for this component. If cost becomes a problem, that's a separate optimization layer that wraps the concern field builder.

The concern field's job is "project the relevant slice of the substrate into the stance's prompt at spawn time." Everything else is somewhere else.

---

## Validation gate

1. ✅ `go vet ./...` clean, `go test ./internal/concern/...` passes with >70% coverage on the builder and >80% coverage on each template file
2. ✅ `go build ./cmd/stoke` succeeds
3. ✅ `BuildConcernField(role, scope, ledger)` returns a deterministic result for a given (role, scope, ledger snapshot) — running it twice on the same inputs produces the same output
4. ✅ Every stance role used in the team roster (component 1) has a corresponding template file in `internal/concern/templates/` (verified by grep against the roster's stance list)
5. ✅ Every template file has a unit test that constructs a synthetic ledger and a synthetic task DAG scope, runs the template, and asserts the rendered concern field contains the expected sections in the expected shape
6. ✅ The proposing and reviewing variants of templates with both faces (e.g., `dev_implementing_ticket` proposing vs `reviewer_for_pr` reviewing) produce different rendered output for the same scope (verified by a test that runs both and checks for the structural differences)
7. ✅ Per-section caps are enforced — a section configured with `cap=20` does not include more than 20 entries even when the underlying query returns more (verified by populating the synthetic ledger with 50 entries and asserting the rendered output has 20)
8. ✅ The original user intent section walks to the mission root regardless of the scope it is called from (verified by a test that calls a ticket-scoped template and asserts the user intent comes from the mission, not the ticket)
9. ✅ The directionality rule is respected: a ticket-scoped concern field for branch A does not include internal decision nodes from branch B (because internal decisions are scoped to their loop and do not cross branches)
10. ✅ The merge pattern works: a feature-scoped template includes summaries of activity from all child tickets, capped per the template's settings
11. ✅ A concern field built and rendered does not exceed a per-template token budget (verified by a test that asserts the rendered output is below the configured ceiling for each template)
12. ✅ Templates can be added by creating a new file in `internal/concern/templates/` without modifying any existing file — the builder discovers templates by their file presence (verified by adding a test template and confirming the builder can use it)
13. ✅ The concern field is one-shot: there is no API on the builder for updating an existing concern field, only for building a new one (verified by API shape — the `ConcernField` struct has no setter methods)
14. ✅ When a template's `applicable_skills` section includes a skill in its query result, the builder commits a `skill_loaded` ledger node and emits a `skill.loaded` bus event before returning the concern field to the harness (verified by integration test)
15. ✅ The rendered prompt for any stance with loaded skills includes the skill-application-observability framing as a non-configurable base-template block (verified by grep on rendered output for the specific framing text)
16. ✅ Candidate-confidence skills and footgun-annotated skills are rendered with their specific warning framings, distinct from `proven` skills (verified by rendering tests with synthetic skills of each confidence level and footgun state)
17. ✅ A stance never receives a concern field containing a skill whose `skill_loaded` node has not been committed and whose `skill.loaded` event has not been emitted (verified by a crash-injection test: if the commit fails, the builder does not return the concern field)
18. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

This file is component 7 of the new guide. It refers to several things specified in later components:

- **The harness** is the component that calls `BuildConcernField` when spawning a stance. The harness component (later) specifies how the rendered concern field becomes the stance's system prompt and how the harness manages stance lifecycle.
- **The wizard** configures per-template token budgets and per-section caps via `.stoke/config.yaml`. The wizard component specifies the configuration surface.
- **The skill manufacturer** writes skill nodes that this component reads via the `applicable_skills` section. The skill manufacturer component (component 8) specifies how skills get into the ledger in the first place.

The next file to write is `08-the-skill-manufacturer.md`. The skill manufacturer is the separate process the supervisor's `skill.extraction.trigger` rule signals — it reads completed mission decision logs, identifies patterns worth preserving, and commits skill nodes to the ledger.
