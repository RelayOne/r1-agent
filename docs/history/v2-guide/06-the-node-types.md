# 06 — Node Types

This file is the schema reference for every kind of node that lives in the ledger. Components 1–5 reference these nodes constantly without specifying their fields. This file fills that in.

The structure is reference-style rather than narrative. Each node type is its own short section with the same shape: identifier prefix, purpose, required fields, optional fields, edge constraints, validation rules, and the components that read or write it. Read this file by jumping to the type you need; don't try to read it linearly.

All node types share the substrate properties from component 2: append-only, content-addressed IDs with type prefixes, immutable once committed, queryable by ID and by edge traversal, validated at write time by both the ledger API and the git hook. Each node type has its own per-type schema version, so types can evolve independently.

There are twenty-two node types, organized into nine groups:

1. **Loop and decision artifacts** — `loop`, `draft`, `agree`, `dissent`
2. **Decision logs** — `decision_internal`, `decision_repo`
3. **Task DAG** — `task`
4. **Skills** — `skill`, `skill_loaded`, `skill_applied`, `skill_import_proposal`
5. **Snapshot** — `snapshot_annotation`
6. **Escalations and verdicts** — `escalation`, `judge_verdict`, `stakeholder_directive`
7. **Research** — `research_request`, `research_report`
8. **Supervisor and runtime tracking** — `supervisor_state_checkpoint`, `branch_completion_proposal`, `branch_completion_agreement`, `branch_completion_dissent`
9. **SDM advisories** — `sdm_advisory`

---

## Group 1: Loop and decision artifacts

### `loop` node

**ID prefix:** `loop-`

**Purpose.** The state machine for any decision in Stoke. One per decision being made — PRD loop, SOW loop, ticket loop, PR review loop, refactor proposal loop, fix-cycle subloop, escalation-handling loop. The state field on this node is what the supervisor's consensus rules transition.

**Required fields.**
- `loop_type` — one of `prd`, `sow`, `ticket`, `pr_review`, `refactor_proposal`, `fix_cycle`, `escalation`, `research`
- `state` — one of `proposing`, `drafted`, `convening`, `reviewing`, `resolving_dissents`, `converged`, `escalated`
- `artifact_ref` — the ID of the node currently being reviewed (a draft node, a research request, etc.)
- `convened_partners` — the list of stance roles that must produce agree/dissent nodes for this loop's current draft (e.g., `["reviewer", "qa_lead"]` for a PR review)
- `iteration_count` — incremented on each new draft committed via supersedes
- `proposing_stance_role` — which stance role produces drafts for this loop
- `task_dag_scope` — the task DAG node ID this loop is scoped to
- `created_at`, `created_by` — timestamp and supervisor instance that created the loop

**Optional fields.**
- `parent_loop_ref` — present on all loops except mission roots
- `judge_invocation_count` — number of times the Judge has been invoked on this loop
- `terminal_reason` — present only when state is `converged` or `escalated`; describes why the loop terminated

**Edge constraints.**
- `parent_loop` edge to the parent loop (at most one)
- `child_loop` edges to child loops (zero or more)
- `references` edge to the artifact being reviewed (exactly one, must point to a draft, research_request, escalation, or similar)

**Validation rules.**
- A loop in a terminal state cannot have its state field changed (no node ever has its state changed; "transitioning" a loop means committing a new loop node that supersedes the prior one with the new state)
- A loop's `convened_partners` cannot be empty unless `loop_type` is `research` (research loops have researchers, not consensus partners)

**Read by:** the supervisor's consensus rules, the Judge stance, the SDM, the dashboard, every fresh-context stance that needs to know what loop it is operating within.
**Written by:** the supervisor (state transitions, terminal commits) and parent loops (creating child loops).

### `draft` node

**ID prefix:** `draft-`

**Purpose.** A candidate artifact under review by a loop's convened partners. Each iteration of a loop produces a new draft node; revisions never modify the prior draft, they create a new draft node with a `supersedes` edge.

**Required fields.**
- `draft_type` — what is being drafted: `prd`, `sow`, `ticket_definition`, `pr`, `refactor_proposal`, `fix`, `judge_verdict_draft`, etc.
- `loop_ref` — the loop this draft belongs to
- `proposing_stance_id` — the stance that produced this draft
- `content` — the actual draft content (the field schema varies by `draft_type`; see "Draft type subschemas" below)
- `created_at`

**Optional fields.**
- `research_refs` — IDs of research reports the proposing stance consulted while drafting
- `skill_refs` — IDs of skills the proposing stance applied
- `snapshot_anno_refs` — IDs of snapshot annotations the proposing stance honored

**Edge constraints.**
- `supersedes` edge to a prior draft if this is a revision (at most one)
- `belongs_to` edge to the loop (exactly one)
- `references` edges to any cited research reports, skills, or snapshot annotations

**Draft type subschemas.** The `content` field's shape depends on `draft_type`. Examples:
- `prd` content: title, problem statement, audience, success criteria, out-of-scope items, constraints
- `sow` content: decomposition into tickets, dependency graph, risk register, cost estimate, milestones
- `pr` content: file diffs (referenced by git commit SHA, not stored inline), description, related ticket
- `refactor_proposal` content: target files, motivation, scope, reversibility plan
- `judge_verdict_draft` content: verdict (one of `keep_iterating`, `switch_approaches`, `return_to_prd`, `escalate_to_user`), reasoning, references to loop history slice consulted

The full per-subtype schemas are defined in the rule files that produce them; this file enumerates the shapes without exhaustively specifying every field.

**Validation rules.**
- The `loop_ref` must point to a loop in a state that accepts new drafts (`proposing` or `resolving_dissents`)
- A draft cannot be committed to a loop in a terminal state

**Read by:** consensus partners during review, the supervisor's convergence rule, the Judge.
**Written by:** the proposing stance for the loop's `proposing_stance_role`.

### `agree` node

**ID prefix:** `agree-`

**Purpose.** A consensus partner's agreement on a draft. The presence of agree nodes from all convened partners on the same draft is one of the four conditions for convergence.

**Required fields.**
- `draft_ref` — the draft being agreed to
- `agreeing_stance_id` — the stance committing the agreement
- `agreeing_stance_role` — the role of the agreeing stance (must match one of the convened partners on the loop)
- `reasoning` — why the partner agrees (free-text, but required — empty agreements are rejected)
- `created_at`

**Optional fields.**
- `caveats` — concerns the partner has but is not blocking on (an agree-with-caveats — the loop converges, but the caveats are recorded for posterity and may inform future loops)

**Edge constraints.**
- `attaches_to` edge to the draft (exactly one)

**Validation rules.**
- The `agreeing_stance_role` must be in the loop's `convened_partners` list
- Only one agree node per stance role per draft (a single partner cannot agree to the same draft twice; if they want to revise their position, they have to commit a dissent that supersedes their prior agree, which is unusual but allowed)

**Read by:** the supervisor's convergence rule, the loop's terminal state computation.
**Written by:** any consensus partner stance during review.

### `dissent` node

**ID prefix:** `dissent-`

**Purpose.** A consensus partner's disagreement with a draft. The presence of any dissent on the current draft prevents convergence and transitions the loop to `resolving_dissents`. The proposing stance must address the dissent (by revising the draft into a new version) before the loop can re-converge.

**Required fields.**
- `draft_ref` — the draft being dissented against
- `dissenting_stance_id` — the stance committing the dissent
- `dissenting_stance_role` — the role of the dissenting stance (must match one of the convened partners)
- `reasoning` — why the partner dissents (required, must be substantive — "I don't like it" is not a valid dissent)
- `requested_change` — the specific change the dissenting partner needs to see, or the alternative they are proposing
- `severity` — one of `blocking` (the dissent must be addressed before the loop can converge), `advisory` (the dissent is recorded but the loop can converge over it if other partners agree)
- `created_at`

**Optional fields.**
- `research_refs` — research reports the dissenting partner consulted while forming the dissent

**Edge constraints.**
- `attaches_to` edge to the draft (exactly one)

**Validation rules.**
- The `dissenting_stance_role` must be in the loop's `convened_partners` list
- A `blocking` dissent prevents convergence; an `advisory` dissent does not. The supervisor's convergence rule treats them differently.

**Read by:** the proposing stance (to revise the draft), the supervisor's `consensus.dissent.requires_address` rule, the Judge (if the loop is escalated).
**Written by:** any consensus partner stance during review.

---

## Group 2: Decision logs

### `decision_internal` node

**ID prefix:** `dec-i-`

**Purpose.** The team's record of how it reached agreement during a task. Task-scoped. Anti-loop context. Read by the next iteration of the loop, by the next instance of the team, by the Judge.

**Required fields.**
- `who` — the stances that participated in the decision (proposing stance, dissenting stances, ratifying stances, escalating stances), each with their role and session ID
- `what` — the specific claim or choice the entry exists to record
- `when` — timestamp, plus the git commit SHA at the moment of decision
- `why` — the reasoning. The "because" that justifies the what.
- `with_what_context` — the inputs the deciding stances had: parts of the codebase consulted, skills loaded, research reports consulted, the concern field at the moment of decision
- `affects_previous_decisions` — list of references to prior decision nodes (internal or repo) that this decision touches, supersedes, depends on, contradicts, or extends. Empty list if there are no prior decisions in scope.
- `previous_contexts_acknowledged` — for each entry in `affects_previous_decisions`, a written statement: "I read this prior decision, here is what it said, here is what its context was, here is whether the current situation is materially comparable, here is why we are nonetheless deciding the same or differently now"
- `task_dag_scope` — the task DAG node ID this decision is scoped to
- `loop_ref` — the loop this decision was made within
- `schema_version`

**Optional fields.**
- `is_summary` — true if this node is a compaction summary of older decision nodes (with `summarizes` edges to the originals)

**Edge constraints.**
- Edges of type `supersedes`, `depends_on`, `contradicts`, `extends`, `references`, `resolves` to other decision nodes
- `belongs_to` edge to the loop
- An internal decision node MAY cite repo decision nodes via edges; the directionality rule allows this direction

**Validation rules.**
- All required fields must be filled — empty `why` or empty `previous_contexts_acknowledged` is a schema violation rejected by both the ledger API and the git hook
- For every entry in `affects_previous_decisions`, there must be a corresponding entry in `previous_contexts_acknowledged`
- Every edge whose type is `supersedes`, `depends_on`, `contradicts`, or `extends` must point to a node in `affects_previous_decisions` (the edges and the field must be consistent)

**Read by:** the next iteration of the loop, the Judge, the next Stoke run on the same task class, the skill manufacturer.
**Written by:** any stance committing a decision, after consensus has been reached.

### `decision_repo` node

**ID prefix:** `dec-r-`

**Purpose.** The codebase's record of why it looks the way it does. Repo-scoped. Survives the task that produced it. Lives in the repo as part of the audit trail. Next-developer context, whether the next developer is human or a future Stoke run.

**Required fields.** Same as `decision_internal` plus:
- `provenance` — one of `stoke_authored`, `inherited_human`, `inherited_stoke`. Inherited entries may have partial schemas (e.g., human-authored ADRs imported by the wizard at initialization will have `who`, `what`, `why` but may not have the structured `previous_contexts_acknowledged`).
- `distilled_from` — for `stoke_authored` repo decisions that were distilled from internal decision nodes, the IDs of the source internal decisions. For inherited entries, this field is empty.

**Optional fields.** Same as `decision_internal`.

**Edge constraints.**
- Same edge types as internal decisions
- A repo decision node MAY NOT cite internal decision nodes via edges. The directionality rule blocks this direction. The `distills` edge type from internal to repo is the only crossing edge, and it goes the other way (an internal node points to the repo node it was distilled into).
- The git hook validates this directionality: any commit that adds an edge from a `dec-r-` node to a `dec-i-` node is rejected.

**Validation rules.**
- For `provenance: stoke_authored`, the full schema is required
- For `provenance: inherited_human` or `inherited_stoke`, partial schemas are tolerated; missing fields are read with default values supplied by the read path
- Inherited entries are read-only via the same mechanism that makes all entries read-only — no special inheritance handling, just the universal append-only enforcement

**Read by:** future Stoke runs, future human developers, the CTO during snapshot consultations, the Lead Engineer when designing a SOW, anyone walking the codebase's reasoning history.
**Written by:** the supervisor's `consensus.convergence.detected` rule when a loop terminates with an outcome that affects the codebase (PRDs converging, SOWs converging, architectural decisions reaching consensus, refactors being approved), and the wizard's import flow when initializing on a repo with pre-existing ADRs.

---

## Group 3: Task DAG

### `task` node

**ID prefix:** `task-` followed by a granularity tag: `task-mission-`, `task-feat-`, `task-mile-`, `task-branch-`, `task-tic-`, `task-sub-`

**Purpose.** A node in the task DAG. The decomposition of work the Lead Engineer produces from the SOW. Tasks have stable IDs that decision log entries, concern field projections, and supervisor scope filters reference. Tasks are append-only — a ticket cannot be edited or deleted; revisions create new task nodes that supersede the prior ones.

**Required fields.**
- `granularity` — one of `mission`, `feature`, `milestone`, `branch`, `ticket`, `sub_ticket`
- `title` — short human-readable name
- `description` — what the task is, what it produces, why it exists
- `state` — one of `proposed`, `assigned`, `in_progress`, `in_review`, `done`, `superseded`, `cancelled`
- `acceptance_criteria` — testable conditions for the task being done
- `created_at`, `created_by` — the Lead Engineer or higher stance that proposed the task

**Optional fields.**
- `assigned_to_stance_role` — which stance role owns the work (e.g., `dev_backend`, `dev_frontend`)
- `assigned_at` — present when state has reached `assigned`
- `parent_task_ref` — the higher-granularity task this one belongs to (every task except the mission root has a parent)
- `dependencies` — task IDs this task depends on (cannot start until those are done)
- `dependents` — task IDs that depend on this one (computed for query convenience but recorded in the ledger as outgoing edges, not in this field)
- `closed_at`, `closed_by` — present when state has reached `done`, `superseded`, or `cancelled`

**Edge constraints.**
- `parent_task` edge to the parent task (at most one)
- `depends_on` edges to predecessor tasks (zero or more)
- `supersedes` edge to a prior task this revises (at most one)
- A task in `superseded` state must have an edge from a successor task pointing back via `supersedes`

**Validation rules.**
- A task cannot transition states except by being superseded by a new task node with an updated state — the substrate is append-only
- A task in `done` state cannot be a target of new `depends_on` edges from new tasks (you cannot retroactively add a dependency on completed work)
- A task in `cancelled` state has its dependents released — they no longer depend on it

**Read by:** the Lead Engineer, the SDM, the supervisor (for scope filtering), the concern field projection, the dashboard.
**Written by:** the Lead Engineer when decomposing a SOW, and by the Lead Engineer or SDM when revising the decomposition mid-task.

---

## Group 4: Skills

### `skill` node

**ID prefix:** `skill-`

**Purpose.** A pattern the convergence loop has proven, or that has been shipped with Stoke, or that has been imported from an external source after consensus review. Read by stances during their work to surface known footguns, jog planning along known patterns, and bypass re-derivation of known consensus answers.

**Required fields.**
- `name` — short identifier, human-readable
- `description` — what the skill is and when it applies
- `applicability` — structured criteria for when this skill should be loaded into a stance's prompt context (file types, languages, frameworks, task types, problem shapes, stance roles)
- `content` — the actual pattern, footgun warning, or consensus shortcut. Free-text but structured by convention into "what to do," "what to avoid," "why," and "examples."
- `provenance` — one of `shipped_with_stoke` (came with the Stoke binary's embedded library), `manufactured` (extracted from a completed mission's internal decision logs), `imported_external` (imported from an external source after consensus review), `inherited_stoke` (loaded from a prior Stoke run's ledger on the same repo)
- `confidence` — one of `proven` (validated by multiple completed missions or shipped at proven level), `tentative` (validated by one completed mission or initial shipped level for cautious configs), `candidate` (extracted but not yet validated by reuse, default for newly manufactured skills)
- `category` — one of the shipped-library categories or a user-defined category (e.g., `trust`, `decision-quality`, `snapshot-defense`, `security`, `performance`, `coding-craft`, etc.)
- `created_at`, `created_by` — the skill manufacturer instance that wrote this node
- `schema_version`

**Optional fields.**
- `superseded_by` — set when a newer skill node has replaced this one (denormalized cache; the authoritative answer is via the incoming `supersedes` edge)
- `usage_count` — denormalized cache of how many times this skill has been loaded; the authoritative count is a query against `skill_loaded` nodes referencing this skill
- `footgun_annotation` — set when the supervisor's skill governance rules have marked this skill as a footgun; contains the explanation of why the pattern is to be avoided
- `import_proposal_ref` — for `imported_external` provenance, the ID of the `skill_import_proposal` node that led to this import
- `tags` — free-form tags for additional filtering

**Edge constraints.**
- `manufactured_from` edges to the source decision nodes (required when `provenance: manufactured`)
- `subsumes` edges to skills this one replaces or generalizes
- `refines` edges to skills this one extends with more specificity
- `supersedes` edge to a prior version of the same skill (at most one)
- `distilled_into` edges from source loops or decisions (the inverse of `manufactured_from`, maintained for bidirectional query convenience)

**Validation rules.**
- A skill cannot have both `subsumes` and `refines` edges to the same target
- A `manufactured` skill must have at least one `manufactured_from` edge
- An `imported_external` skill must have an `import_proposal_ref` pointing to a converged `skill_import_proposal` node
- A `shipped_with_stoke` skill's `name` must match a file under the Stoke binary's embedded `/skills/` directory (validated at import time, not at every read)

**Read by:** every stance via the concern field projection during construction, the Lead Engineer when designing a SOW, the CTO during snapshot consultations, the supervisor's skill governance rules.
**Written by:** the skill manufacturer (component 8) only. No other component creates skill nodes.

### `skill_loaded` node

**ID prefix:** `sk-load-`

**Purpose.** Records that a skill was loaded into a stance's concern field at spawn time. Every skill load is logged; this is the substrate record of that logging. The supervisor's `skill.load.audit` rule fires on the corresponding bus event and uses these nodes for audit trail queries.

**Required fields.**
- `skill_ref` — the skill that was loaded
- `loading_stance_id` — the stance whose concern field included the skill
- `loading_stance_role` — the stance role
- `concern_field_template` — the template that pulled this skill into the concern field (e.g., `dev_implementing_ticket`, `reviewer_for_pr`)
- `matching_applicability` — the specific applicability criteria that matched (which task type, which file types, which role, etc.), so post-mortems can see why the skill was deemed relevant
- `task_dag_scope` — the task DAG node the loading stance is operating within
- `loop_ref` — the loop the loading stance is operating within (if any)
- `created_at`

**Optional fields.** None.

**Edge constraints.**
- `references` edge to the skill node
- `loaded_into` edge to the stance's initial concern field (a denormalized reference; the authoritative path is through the stance's spawn event on the bus)

**Validation rules.**
- `skill_ref` must point to a non-superseded skill node (stances never load superseded skills; if a skill has been superseded, the concern field loads the superseding version)

**Read by:** the supervisor's skill governance rules, post-mortem tools, the dashboard's "skills in use" view.
**Written by:** the concern field builder in the harness when it includes a skill in a stance's rendered context.

### `skill_applied` node

**ID prefix:** `sk-apply-`

**Purpose.** Records that a stance actually used a loaded skill in producing output. Loading a skill is not the same as applying it — a stance may read a skill in its concern field and decide it doesn't apply to the current situation. Application is the stronger signal. This node captures the moment of application and is the basis for the supervisor's review when applied skills have confidence below `proven`.

**Required fields.**
- `skill_ref` — the applied skill
- `applying_stance_id` — the stance that applied the skill
- `applying_stance_role` — the stance role
- `artifact_ref` — the draft, decision, action, or other node that the application affected
- `how_applied` — free-text written by the applying stance describing how the skill shaped the output. Required to be substantive: "I followed this skill's pattern by doing X instead of Y," or "This skill warned me against Z so I chose W." Empty or placeholder `how_applied` fields are rejected by the validation layer.
- `load_ref` — the `skill_loaded` node that brought this skill into the stance's context (the full chain from concern field construction through application is preserved)
- `created_at`

**Optional fields.**
- `contradictions_found` — if the stance considered the skill but also identified ways it might not apply, the stance can record those considerations here

**Edge constraints.**
- `references` edge to the skill node
- `applied_in` edge to the artifact
- `derived_from_load` edge to the `skill_loaded` node

**Validation rules.**
- `how_applied` cannot be empty or a placeholder phrase — the validation layer checks for minimum substantive content
- `load_ref` must reference a `skill_loaded` node for the same stance and the same skill (a stance cannot apply a skill it did not load through its concern field)

**Read by:** the supervisor's `skill.application.requires_review` and `skill.application.contradicts_outcome` rules, post-mortem tools, the skill manufacturer's lifecycle workflow.
**Written by:** the applying stance during its work.

### `skill_import_proposal` node

**ID prefix:** `sk-imp-`

**Purpose.** A research stance's proposal to import a skill from an external source. Triggers a consensus loop with the CTO and Lead Engineer as required partners, with high-risk proposals automatically escalating to the user. The substrate of the trust decision that external skill imports require.

**Required fields.**
- `proposing_stance_id` — the research stance that found the candidate
- `candidate_content` — the full text of the candidate skill file as it was found (frontmatter plus body, unmodified from the source)
- `source_metadata` — structured fields: URL, author or maintaining organization, publication date, update history if available, content hash (for integrity tracking)
- `reputation_summary` — the research stance's written summary of what it found about the source's credibility: is it a well-known authority, does it have peer review, does it have user ratings or community feedback, what is the source's track record, any red flags found during the reputation check
- `security_review` — the research stance's structured security pass over the candidate content. Specifically documents findings on: prompt injection attempts found (or absence thereof), attempts to lower trust thresholds or bypass trust rules, biased instructions that would cause stances to skip safety checks, hidden instructions in comments or frontmatter, contradictions with Stoke's core operating principles, attempts to modify behavior around snapshot defense or user escalation. Every category must be explicitly addressed — "no findings" is acceptable but must be stated.
- `consistency_review` — the research stance's analysis of how the candidate interacts with skills already in the library. Specifically: does it contradict a `shipped_with_stoke` skill, does it duplicate an existing skill, does it refine or generalize an existing skill, is it covered by skills that already exist at higher confidence.
- `risk_assessment` — one of `low`, `medium`, `high`, with the research stance's reasoning. `high` automatically triggers user escalation regardless of consensus loop outcome.
- `task_context` — what the research stance was doing when it encountered the need for this skill (so reviewers can judge whether the import is motivated by real need or speculative accumulation)
- `created_at`

**Optional fields.**
- `alternative_candidates_considered` — if the research stance evaluated multiple candidates and picked this one, references to the rejected alternatives and why

**Edge constraints.**
- `proposed_by` — a stance reference (not a node edge)
- `references` edges to any existing skills the candidate interacts with (contradictions, duplications, refinements)

**Validation rules.**
- `candidate_content` must parse successfully as a skill markdown file (YAML frontmatter plus body) — malformed candidates are rejected at proposal time, before the consensus loop runs
- `security_review` must address every category explicitly — missing categories cause rejection
- `reputation_summary` cannot be empty — every proposal must have some assessment of source credibility, even if the assessment is "unknown source, low information"
- `risk_assessment: high` forces user escalation at the consensus loop's convergence point, regardless of partner agreement

**Read by:** the supervisor's `skill.import.triggers_consensus_loop` rule, the convened consensus partners (CTO, Lead Engineer, security stance if enabled), the user when escalated, the skill manufacturer on approved imports.
**Written by:** research stances that find candidate skills on the web or in other external sources during their work.

---

## Group 5: Snapshot

### `snapshot_annotation` node

**ID prefix:** `snap-anno-`

**Purpose.** The CTO's structured notes on the protected baseline (the snapshot). Captures what is intentional in the existing codebase, what is accidental, what conventions are coherent, what areas are load-bearing, what known footguns exist. Read by the CTO on every consultation as input to its "smart change vs unmotivated refactor" evaluation.

**Required fields.**
- `target` — the file, directory, module, or pattern this annotation describes (using snapshot-relative paths)
- `annotation_type` — one of `intentional_pattern`, `accidental_pattern`, `load_bearing_area`, `known_footgun`, `convention`, `out_of_scope`
- `description` — the annotation content
- `evidence` — what the CTO consulted to form this annotation (file references, git history, related decision nodes)
- `created_at`, `created_by` — the CTO consultation that produced this annotation

**Optional fields.**
- `originating_consultation_ref` — the loop ID for the CTO consultation that produced this annotation
- `superseded_by` — set when a newer annotation node has replaced this one (e.g., the CTO learned more about an area on a later consultation)

**Edge constraints.**
- `references` edges to related decision nodes
- `supersedes` edge to a prior annotation about the same target

**Validation rules.**
- `target` must be a path that existed in the snapshot at the time the annotation was created (verified against the snapshot at write time)
- An `intentional_pattern` annotation and an `accidental_pattern` annotation cannot both exist for the same target without one superseding the other (the CTO has to decide which it is, not record both)

**Read by:** the CTO on every consultation, the supervisor's `snapshot.modification.requires_cto` rule (to surface relevant annotations to the CTO), the Lead Engineer when planning work that touches snapshot code.
**Written by:** the CTO stance only.

---

## Group 6: Escalations and verdicts

### `escalation` node

**ID prefix:** `esc-`

**Purpose.** A request to forward something upward in the supervisor hierarchy or to the user. Created when a loop cannot resolve under its own mechanics, when a partner timeout cascade exhausts replacements, when the Judge says escalate, when a budget threshold is crossed, or when a worker explicitly requests escalation.

**Required fields.**
- `escalation_type` — one of `infeasible`, `blocked`, `deadlock`, `drift`, `budget`, `user_required`, `partner_exhaustion`, `cto_veto_disputed`
- `originating_loop_ref` — the loop being escalated
- `target` — one of `parent_supervisor`, `mission_supervisor`, `user_via_po`
- `context` — structured summary of the loop history, the dissents, the prior attempts, and what specifically is unresolved
- `requested_resolution` — what kind of input is being asked for (a directive, an architectural decision, a scope clarification, a yes/no on continuing)
- `created_at`, `created_by` — the stance or supervisor that created the escalation

**Optional fields.**
- `resolution_status` — one of `pending`, `resolved`, `withdrawn`, `superseded`. Note: even though this is a status field, the substrate is still append-only — "transitioning" status means committing a new escalation node with `supersedes` to the prior, with the new status set
- `resolution_node_ref` — when resolved, the ID of the node that resolved it (a user input node, a parent supervisor decision, a Judge verdict)

**Edge constraints.**
- `escalates` edge to the loop being escalated (exactly one)
- `supersedes` edge to a prior escalation if this one is a status update

**Validation rules.**
- Only the mission supervisor can create escalations with `target: user_via_po`
- An escalation in `resolved` or `withdrawn` state cannot be referenced by new edges from non-supersession edges (it is closed)

**Read by:** the receiving supervisor (parent or mission), the user via the PO when target is `user_via_po`, the dashboard.
**Written by:** any stance or supervisor that needs to escalate.

### `judge_verdict` node

**ID prefix:** `jv-`

**Purpose.** The Judge's output from an invocation. The supervisor reads the verdict and applies the corresponding loop transition.

**Required fields.**
- `invoking_rule` — the supervisor rule that triggered the Judge invocation (`consensus.iteration.threshold`, `drift.judge.scheduled`, `drift.intent_alignment_check`, `drift.budget_threshold`, etc.)
- `loop_ref` — the loop being judged
- `verdict` — one of `keep_iterating`, `switch_approaches`, `return_to_prd`, `escalate_to_user`
- `reasoning` — substantive explanation of the verdict, citing specific evidence from the loop history
- `loop_history_consulted` — references to the specific loop nodes, draft chain, dissent chain, and other context the Judge walked
- `original_intent_at_invocation` — a snapshot reference to the user's original intent as it was at the moment of invocation (the Judge always sees the original intent and is required to record what version it consulted)
- `created_at`, `created_by` — the Judge stance ID

**Optional fields.**
- `research_refs` — references to research reports the Judge consulted (the Judge may have requested research during evaluation)
- `caveats` — concerns the Judge has that are not part of the verdict but worth recording

**Edge constraints.**
- `references` edge to the loop being judged
- `references` edges to any research reports consulted

**Validation rules.**
- A verdict of `escalate_to_user` triggers the supervisor's `hierarchy.user_escalation` rule on the next event the supervisor processes
- A verdict of `return_to_prd` is only valid if the loop's task DAG scope has a PRD ancestor (you cannot return to a PRD that does not exist)

**Read by:** the supervisor's loop transition logic, the dashboard, future Judge invocations on the same loop (for prior-verdict context).
**Written by:** the Judge stance only.

### `stakeholder_directive` node

**ID prefix:** `sd-`

**Purpose.** The Stakeholder's resolution of an escalation in full-auto mode. Created when the `hierarchy.user_escalation` rule fires in full-auto mode and spawns a Stakeholder stance. The Stakeholder reads the escalation context, evaluates it (possibly requesting research first), and produces a directive that flows back into the loop the same way a user response would in interactive mode.

**Required fields.**
- `escalation_ref` — the escalation being resolved
- `stakeholder_stance_id` — the Stakeholder stance that produced the directive
- `posture_applied` — which Stakeholder posture was in effect (`absolute_completion_and_quality`, `balanced`, `pragmatic`)
- `directive_type` — one of `proceed_as_proposed`, `switch_to_alternative_approach`, `add_constraint_and_retry`, `return_to_prd_for_rescope`, `abort_mission_as_infeasible`, `dispatch_research_before_deciding`, `forward_to_user` (the safety valve when the Stakeholder determines human input is actually required)
- `directive_content` — the specific instructions the directive delivers to the unblocked work (free-text but required substantive content; the directive must be actionable enough that the paused work can resume without further clarification)
- `reasoning` — substantive explanation of why the Stakeholder produced this directive, citing specific evidence from the escalation context, the loop history, and the original user intent
- `evaluation_summary` — what the Stakeholder considered: what the escalation was, what the loop history showed, what the dissents were (if any), what the original intent says, what the relevant snapshot annotations say, what prior Stakeholder directives in this mission have been
- `prior_stakeholder_directives_considered` — for each prior Stakeholder directive in this mission, a written statement of how the current directive relates: same pattern applied, different pattern because the situation differs, builds on prior directive, etc. Same `previous_contexts_acknowledged` discipline that decision log entries use. Empty list if there are no prior Stakeholder directives in the mission.
- `original_intent_at_evaluation` — a snapshot reference to the user's original intent as it was at the moment of evaluation (the Stakeholder always sees the original intent and records what version it consulted)
- `created_at`, `created_by` — the Stakeholder stance ID

**Optional fields.**
- `research_refs` — references to research reports the Stakeholder consulted (the Stakeholder may have requested research during evaluation)
- `second_stakeholder_ref` — for high-stakes directives, the ID of a second Stakeholder stance that was convened for cross-verification and agreed with this directive
- `second_stakeholder_dissent_ref` — if a second Stakeholder was convened and disagreed, the ID of the dissent node. When this field is set, `directive_type` must be `forward_to_user` — the disagreement safety valve forces escalation back to interactive mode regardless of the initial full-auto configuration.
- `caveats` — concerns the Stakeholder has that are not part of the directive but worth recording

**Edge constraints.**
- `resolves` edge to the escalation (exactly one)
- `references` edges to any research reports consulted
- `references` edge to the second Stakeholder stance's dissent if applicable

**Validation rules.**
- `directive_content` cannot be empty — a Stakeholder directive with no actionable content is not a resolution. The validation layer rejects empty directives.
- `reasoning` cannot be trivial (single-sentence placeholder text) — the validation layer enforces a minimum length and flags directives whose reasoning looks like rubber-stamping. The bench's Stakeholder quality metrics audit this at a higher level.
- `directive_type: forward_to_user` is the only valid type when `second_stakeholder_dissent_ref` is set — the disagreement safety valve is structurally enforced
- For directives of type `switch_to_alternative_approach` or `return_to_prd_for_rescope`, the `directive_content` must specify the alternative or the rescope with enough detail that the paused work can act on it without further clarification
- `prior_stakeholder_directives_considered` must have one entry per prior Stakeholder directive in the same mission (enforced by a cross-check query at write time)

**Read by:** the supervisor's loop transition logic (which applies the directive to resume paused work), the dashboard in full-auto mode, future Stakeholder invocations in the same mission (which read prior directives as context for consistency), the bench's Stakeholder quality metrics.
**Written by:** the Stakeholder stance only.

---

## Group 7: Research

### `research_request` node

**ID prefix:** `req-`

**Purpose.** A stance's request for research. Created when any stance encounters uncertainty during its work and emits a `worker.research.requested` event. The supervisor's research rules pause the requesting stance and dispatch researchers, who eventually produce research_report nodes that flow back.

**Required fields.**
- `requesting_stance_id` — the stance that needs the research
- `requesting_stance_role` — the role of the requesting stance
- `question` — the specific question to be answered
- `context_for_question` — what the requesting stance was doing when it encountered the uncertainty, and what it has already considered
- `audience` — the requesting stance role (so the researcher knows the level of expertise of the eventual reader)
- `urgency` — one of `high` (the loop is paused waiting), `medium`, `low`
- `parallel_researchers_requested` — how many researchers should be dispatched in parallel for cross-verification (default 1, higher for high-stakes questions per wizard config)
- `created_at`

**Optional fields.**
- `loop_ref` — the loop this request is part of (most requests have one; some standalone requests might not)
- `task_dag_scope` — the task DAG node the requesting stance is scoped to

**Edge constraints.**
- `requested_by` edge to the requesting stance (recorded as a stance reference, not a node edge)
- `belongs_to` edge to the loop (when present)

**Validation rules.**
- `question` cannot be empty or trivial — the validation layer rejects placeholder questions
- `parallel_researchers_requested` must be between 1 and the wizard-configured maximum (default cap: 5)

**Read by:** the supervisor's `research.request.dispatches_researchers` rule, the dispatched researcher stances.
**Written by:** any stance that encounters uncertainty during its work.

### `research_report` node

**ID prefix:** `rep-`

**Purpose.** A researcher's completed report. Multiple report nodes attached to the same request indicate parallel cross-verification.

**Required fields.**
- `request_ref` — the research request this report answers
- `researcher_stance_id` — the stance that produced the report
- `question_being_answered` — restated from the request, to confirm the researcher understood it correctly
- `sources_cited` — structured list of sources consulted (web URLs, codebase files, ledger node IDs, prior decision references), with brief summaries of what each provided
- `conclusion` — the answer to the question
- `confidence_level` — one of `high`, `medium`, `low`, `inconclusive` — the researcher's honest assessment of how confident they are in the conclusion
- `limitations` — what the researcher could not determine, what assumptions are baked into the conclusion, what would need to be verified further
- `created_at`

**Optional fields.**
- `dissenting_evidence` — evidence the researcher found that contradicts the conclusion but was outweighed by other evidence (recorded for the requester's awareness)

**Edge constraints.**
- `answers` edge to the research request (exactly one)

**Validation rules.**
- `sources_cited` cannot be empty — every research report must cite at least one source. A report with no sources is hallucination, not research, and is rejected by the validation layer.
- `confidence_level: inconclusive` requires the `limitations` field to explain why the researcher could not reach a conclusion

**Read by:** the requesting stance after it is unpaused, the supervisor's `research.report.unblocks_requester` rule (which validates the report before unpausing), the Judge when later evaluating loops that consulted this research.
**Written by:** researcher stances only.

---

## Group 8: Supervisor and runtime tracking

### `supervisor_state_checkpoint` node

**ID prefix:** `sv-ckpt-`

**Purpose.** The mission supervisor's serialized state at a point in time. Committed by a supervisor rule on specific events (loop terminal transitions, branch completions, periodic missionary milestones — but not on a timer; the events that trigger checkpoints are themselves event-driven). A crashed supervisor recovers by reading the most recent checkpoint and replaying bus events forward from the checkpoint's cursor position.

**Required fields.**
- `supervisor_instance_id` — which supervisor wrote this checkpoint
- `supervisor_config` — `mission`, `branch`, or `sdm`
- `bus_cursor` — the bus sequence number the supervisor had processed up to
- `active_loops` — list of loop IDs the supervisor was tracking as active
- `paused_workers` — list of worker stance IDs the supervisor had paused, with the events those pauses are waiting on
- `pending_delayed_events` — list of delayed events the supervisor had scheduled but not yet seen fire
- `created_at`

**Optional fields.**
- `parent_supervisor_ref` — for branch supervisors, a reference to the mission supervisor instance

**Edge constraints.**
- `supersedes` edge to the prior checkpoint from the same supervisor instance (each supervisor maintains a chain of checkpoints; only the latest is used for recovery, but the chain is preserved)

**Validation rules.**
- `bus_cursor` must be a valid sequence number from the bus event log
- `pending_delayed_events` must reference delayed events that the bus has not yet delivered (the recovery process will re-arm them)

**Read by:** the supervisor recovery process at startup.
**Written by:** the supervisor's checkpoint rule (which fires on specific structural events).

### `branch_completion_proposal` node

**ID prefix:** `bcp-`

**Purpose.** A branch supervisor's proposal that its branch is done, awaiting parent agreement. Triggers the mission supervisor's `hierarchy.completion.requires_parent_agreement` rule.

**Required fields.**
- `branch_supervisor_id` — the proposing branch supervisor
- `branch_task_ref` — the task DAG branch node being proposed as complete
- `mission_supervisor_id` — the parent mission supervisor
- `summary_of_work` — what was completed, with references to the task nodes that were closed
- `unresolved_concerns` — anything the branch supervisor wants the mission supervisor to know about (open dissents that were resolved by escalation, deferred refactors, anything not 100% clean)
- `created_at`

**Optional fields.**
- `cross_branch_advisories_consulted` — references to SDM advisories the branch addressed before proposing completion

**Edge constraints.**
- `proposes_completion_of` edge to the branch task

**Validation rules.**
- All non-cancelled tasks within the branch must be in `done` or `superseded` state before a completion proposal can be created (the substrate validates this)

**Read by:** the mission supervisor's parent-agreement rule.
**Written by:** branch supervisors only.

### `branch_completion_agreement` node

**ID prefix:** `bca-`

**Purpose.** The mission supervisor's agreement on a branch completion proposal. Closes the branch and unblocks downstream work.

**Required fields.**
- `proposal_ref` — the proposal being agreed to
- `mission_supervisor_id` — the agreeing mission supervisor
- `agreement_reasoning` — the mission supervisor's checks that passed (sibling state, dependencies, PRD criteria, intent alignment, SDM advisories)
- `created_at`

**Edge constraints.**
- `agrees_to` edge to the proposal

**Validation rules.**
- Only the mission supervisor can write this node (verified by `mission_supervisor_id`)

**Read by:** the branch supervisor (which then closes itself), the dashboard.
**Written by:** the mission supervisor only.

### `branch_completion_dissent` node

**ID prefix:** `bcd-`

**Purpose.** The mission supervisor's disagreement with a branch completion proposal. Keeps the branch supervisor alive and gives it concrete things to address before re-proposing.

**Required fields.** Same shape as `branch_completion_agreement` but with `dissent_reasoning` and a `requested_actions` list specifying what the branch needs to do before re-proposing.

**Edge constraints.**
- `dissents_against` edge to the proposal

**Validation rules.** Same as agreement.

**Read by:** the branch supervisor (which then iterates), the dashboard.
**Written by:** the mission supervisor only.

---

## Group 9: SDM advisories

### `sdm_advisory` node

**ID prefix:** `sdm-adv-`

**Purpose.** A structured warning emitted by the SDM supervisor. Consumed by the mission supervisor's cross-team rule and by branch supervisors that need to know about cross-branch state. The SDM only emits these — it does not pause workers or transition state. The advisories are the SDM's entire output.

**Required fields.**
- `advisory_type` — one of `collision_file_modification`, `dependency_crossed`, `duplicate_work_detected`, `schedule_risk_critical_path`, `cross_branch_drift`
- `detected_condition` — structured description of what was detected
- `branches_involved` — list of branch task IDs
- `affected_workers` — list of worker stance IDs that are affected (if known)
- `suggested_coordination` — the SDM's recommendation for what should happen (e.g., "branch A and branch B need to sync on file X before either proceeds," "branch C should defer until branch D's interface is stable")
- `originating_event_ref` — the bus event that triggered the SDM rule that produced this advisory
- `created_at`

**Optional fields.**
- `severity` — one of `informational`, `coordinate_soon`, `urgent_block`. Default `coordinate_soon`. Used by consuming supervisors to prioritize.
- `resolved_by_ref` — set when a later event (a coordination consensus, a branch closure, a refactor) has resolved the condition the advisory described

**Edge constraints.**
- `references` edges to the involved branch tasks
- `references` edge to the originating event (recorded as an event sequence number, not a node ID, since the event may not be a ledger node)

**Validation rules.**
- An advisory cannot reference branch tasks that do not exist
- The SDM is the only supervisor that can write this node type

**Read by:** the mission supervisor's `cross_team.modification.requires_cto` rule, branch supervisors that subscribe to SDM advisories for their own branch's coordination, the dashboard.
**Written by:** the SDM supervisor only.

---

## Validation gate

The node types component has its own validation gate, focused on the correspondence between the schemas defined in this file and the Go structs in the ledger package that the ledger validates against. The check is "for every node type defined here, there is a Go struct in the ledger package whose fields match the schema, and the ledger's validation logic enforces the schema at write time."

The gate is:

1. ✅ For every node type defined in this file (`loop`, `draft`, `agree`, `dissent`, `decision_internal`, `decision_repo`, `task`, `skill`, `skill_loaded`, `skill_applied`, `skill_import_proposal`, `snapshot_annotation`, `escalation`, `judge_verdict`, `stakeholder_directive`, `research_request`, `research_report`, `supervisor_state_checkpoint`, `branch_completion_proposal`, `branch_completion_agreement`, `branch_completion_dissent`, `sdm_advisory`), there is a corresponding Go struct in `internal/ledger/nodes/` named after the type (e.g., `nodes.DecisionInternal`, `nodes.SnapshotAnnotation`, `nodes.SkillApplied`)
2. ✅ Every required field in this file's schemas is present on the corresponding Go struct, with the same name (snake_case in this file, the conventional Go field name in the struct), and the struct field is tagged as required for the validation layer
3. ✅ Every optional field in this file's schemas is present on the corresponding Go struct as a nullable or zero-valued field, tagged as optional
4. ✅ Every node type's Go struct has a `NodeType()` method returning the canonical type prefix (e.g., `dec-i`, `task-tic`, `snap-anno`) that the ledger uses for ID minting and directory routing
5. ✅ Every node type's Go struct has a `Validate()` method that returns an error if any required field is missing, any field has an invalid value (e.g., a state field set to a value not in the allowed enum), or any cross-field invariant is violated (e.g., a `decision_internal` node where `affects_previous_decisions` has entries but `previous_contexts_acknowledged` is missing entries for some of them)
6. ✅ The ledger's `AddNode` API calls `Validate()` on every node before persisting, and rejects nodes whose `Validate()` returns an error (verified by ledger gate item from component 2)
7. ✅ The git hook validates the same schemas independently of the API at the storage layer (verified by hook gate items from component 2)
8. ✅ The directionality rule for decision logs is enforced: a Go test commits a `decision_repo` node, then attempts to add an edge from it to a `decision_internal` node, and verifies the edge is rejected
9. ✅ A Go test for each node type creates a valid instance, persists it via the ledger API, queries it back, and verifies the round-trip preserves all fields
10. ✅ A Go test for each node type creates an invalid instance (each required field missing in turn), attempts to persist it, and verifies the rejection
11. ✅ A Go test for the inheritance schema tolerance: a `decision_repo` node with `provenance: inherited_human` and partial fields can be persisted (verifying that human-imported ADRs work even when they don't have the full Stoke schema), and the read path returns sensible defaults for missing fields
12. ✅ A Go test for the schema_version field: every node type's struct includes a `SchemaVersion` field, every persisted node has it set, and the read path can handle at least two different schema versions per type (one for current, one for a synthetic "v0" representing legacy / inherited shape)
13. ✅ A grep against `internal/ledger/nodes/` confirms there are no node type definitions there that are not also defined in this file, and a grep against this file confirms there are no node types defined here that are not also implemented in `internal/ledger/nodes/` (the schemas and the structs must be in 1:1 correspondence — neither side has secret types)
14. ✅ The package documentation in `internal/ledger/nodes/doc.go` references this file as the canonical schema source
15. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

The 1:1 correspondence rule is the load-bearing one. It is the property that prevents this file from drifting out of sync with the codebase. If a developer adds a new node type to the Go package, item 13 fails until they also add it here. If they document a new node type here, item 13 fails until they implement the struct. The gate forces the two to evolve together.

---

## Forward references

This file is component 6 of the new guide. It is referenced by every component below it that reads or writes ledger nodes. The components that depend on this file most heavily are:

- **Component 7 (concern field)** — uses these schemas as the targets of its query templates. The concern field's projections pull specific fields from these node types into stance prompt context.
- **Component 8 (skill manufacturer)** — reads `decision_internal` nodes from completed loops and writes `skill` nodes. The schemas of both are defined here.
- **The wizard** (later) — uses the `decision_repo` schema with `provenance: inherited_human` when importing pre-existing ADRs at initialization.
- **The harness** (later) — creates worker stances that emit events referencing these node types via their `worker.*` events.

The next file to write is `07-the-concern-field.md`. The concern field is now a thin file: it specifies the query templates that build per-stance prompt context from the ledger, scoped by task DAG node IDs, projecting the relevant slices of the node types defined here. It is a layer on top of components 2–6, not a substrate of its own.
