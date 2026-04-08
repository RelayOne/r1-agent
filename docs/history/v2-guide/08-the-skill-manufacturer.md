# 08 — The Skill Manufacturer

The skill manufacturer is the process that handles the full lifecycle of skills in Stoke. It is not the supervisor and it is not the harness — it is its own long-running process that runs alongside them, subscribed to specific bus events, with specific authority to write skill-related ledger nodes and no other authority.

Skills are serious. A skill loaded into a stance's concern field shapes what the stance does; a pattern of skill misapplication shapes what the whole team does; a skill imported from the web without scrutiny is a prompt injection vector into every future session. The skill manufacturer's job is to treat skills with the gravity they deserve: pre-built skills are shipped and audited, manufactured skills come from proven consensus, imported skills go through an explicit security-reviewed consensus loop, and every skill use is logged and reviewable.

This file specifies the manufacturer's responsibilities across four workflows (shipped library import at initialization, manufacturing from completed missions, external skill import, skill lifecycle management), the file format for skill markdown files, the catalog of skills that ships with Stoke, and the validation gate.

---

## What the skill manufacturer is

The skill manufacturer is a long-running Go process, launched by Stoke's runtime alongside the mission supervisor. It has its own package at `internal/skillmfg/` and its own bus subscription. It holds no rules of enforcement — it is a consumer of supervisor-emitted events, not an enforcer of rules. The supervisor triggers the manufacturer via specific events; the manufacturer does its work; the manufacturer emits its own events and commits ledger nodes.

The manufacturer has four distinct workflows, each triggered by different events:

1. **Shipped library import.** Triggered once at initialization by a `wizard.init.complete` event. Reads the skill markdown files bundled with Stoke's binary distribution, validates them, and writes them to the ledger as `skill` nodes with `provenance: shipped_with_stoke`.

2. **Manufacturing from completed missions.** Triggered by `skill.extraction.requested` events from the supervisor's `skill.extraction.trigger` rule (component 4, category 8). Reads the decision_internal nodes from the completed mission, identifies patterns worth preserving, and writes new `skill` nodes with `provenance: manufactured`.

3. **External skill import.** Triggered by `skill.import.approved` events from the supervisor — these are emitted after a skill import consensus loop has converged. The manufacturer reads the approved import proposal, validates the candidate skill file one more time against its own security checks, and writes it to the ledger as a `skill` node with `provenance: imported_external`.

4. **Skill lifecycle management.** Triggered by `skill.review.completed` events from the supervisor's skill governance rules (component 4, new category). The manufacturer handles confidence promotions and demotions, skill supersession, and applicability refinement based on how skills have performed in practice.

The manufacturer has no other duties. It does not enforce anything. It does not spawn stances (except for one case in workflow 2, where it spawns extraction stances to do the actual pattern identification from completed missions — more on that below). It does not modify existing skill nodes (the substrate is append-only; modification is always a new node that supersedes the prior). It does not decide which skills to load into concern fields (that is the harness's job via the concern field templates).

---

## Workflow 1: Shipped library import at initialization

When the wizard completes Stoke's initialization on a repo, it emits a `wizard.init.complete` event on the bus. The manufacturer subscribes to this event and runs the shipped library import:

1. Reads the skill markdown files from the Stoke binary's embedded resources (shipped at the directory `/skills/` in the Stoke source tree, embedded into the binary at build time via Go's `embed` package)
2. Parses each file: YAML frontmatter for structured fields, markdown body for content
3. For each parsed file, constructs a `skill` ledger node with `provenance: shipped_with_stoke`, the fields from the frontmatter, and the body as the `content` field
4. Validates each skill node against the schema in component 6 before persisting
5. Commits the skills to the ledger in a single batch
6. Emits a `skill.library.imported` event with the count of imported skills

The user can inspect the shipped library via the wizard before initialization completes — the wizard presents the catalog with brief descriptions and gives the user the option to:

- Accept all shipped skills with their default confidence levels (the common case)
- Downgrade specific skills to a lower confidence (e.g., "I want to treat this as `tentative` until I see it apply to my codebase successfully")
- Disable specific skills entirely (they won't be imported; their files are embedded in the binary but the ledger doesn't get nodes for them)
- Accept all with a global confidence downgrade (e.g., "import everything as `tentative` for my first mission, promote after successful use")

The shipped library is the floor of the skill set. The user can be more cautious than the floor but not less — there is no flag to "import shipped skills as silently authoritative" that bypasses the normal confidence and audit mechanisms. Even shipped skills are loaded, applied, and reviewed by the supervisor like any other skill.

---

## Workflow 2: Manufacturing from completed missions

When a mission converges or escalates with user-directed change, the supervisor's `skill.extraction.trigger` rule fires and emits a `skill.extraction.requested` event with the completed mission's scope. The manufacturer subscribes to this event and runs the extraction workflow:

1. Reads all `decision_internal` nodes from the completed mission
2. Reads the loop history: what iterations happened, what dissents were raised, what the Judge verdicts said, what was escalated and how it was resolved
3. Spawns one or more extraction stances through the harness — these are fresh-context stances with a dedicated extraction template, given the decision log slice and the loop history as their concern field
4. Each extraction stance identifies candidate patterns: recurring decision shapes (same kind of decision being made repeatedly with the same reasoning), footguns the team caught (dissents that consistently warned against something), consensus shortcuts that worked (agreements that converged quickly and held through the mission), failure modes that turned into escalations (loops that got stuck in recognizable shapes)
5. Candidate patterns are committed as `skill` nodes with `provenance: manufactured`, `confidence: candidate` (not `proven` — first-time extractions are always candidate until they prove themselves in future missions), and `manufactured_from` edges to the source decision nodes
6. The manufacturer emits `skill.manufactured` events for each new skill

Manufactured skills are not immediately available to future concern fields at full weight. The concern field templates query for skills by applicability and confidence, and `candidate` skills are either excluded entirely (strict mode) or included with a lower rendering priority (permissive mode) depending on the wizard config. A candidate skill earns promotion to `tentative` after it has been loaded and applied without contradiction in one mission, and to `proven` after it has been loaded and applied successfully in multiple missions (the threshold is configurable, default 3).

Promotion and demotion are driven by the supervisor's skill governance rules (not by the manufacturer itself). The manufacturer writes new nodes that supersede the prior with updated confidence; the supervisor is what decides when to trigger a supersession based on the skill's track record.

---

## Workflow 3: External skill import

This is the most careful of the four workflows because importing a skill is a trust decision. A skill is a piece of text that will shape every future stance that loads it. A malicious skill is a prompt injection vector. An incorrect skill leads every stance that loads it astray. Imports must be deliberate, reviewed, and auditable.

The workflow begins with a research stance encountering uncertainty during its work and searching for relevant information. The research stance's toolset includes web search with a filter for skill file sources (well-known pattern libraries, framework best-practice guides, vetted security posture documents, etc.). If the research returns a candidate skill file that the research stance believes is relevant, the stance does NOT import it directly. It produces a `skill_import_proposal` ledger node (new node type, specified in component 6 retrofit) with the following required fields:

- **Candidate skill content** — the full text of the skill file as it was found
- **Source metadata** — URL, author or maintaining organization, publication date, update history if available
- **Reputation summary** — what the research stance found about the source's credibility: is it a well-known authority (e.g., OWASP for security, the Go team for Go idioms), does it have peer review, does it have user ratings or community feedback, what is the source's track record
- **Security review** — the research stance's structured pass over the candidate content. Specifically looking for: prompt injection attempts (text that tries to override Stoke's instructions), attempts to lower trust thresholds or bypass the trust rules, biased instructions that would cause stances to skip safety checks, hidden instructions in comments or frontmatter, contradictions with Stoke's core operating principles, attempts to modify behavior around snapshot defense or user escalation
- **Consistency review** — how the candidate interacts with skills already in the library. Does it contradict a `shipped_with_stoke` skill? Does it duplicate an existing skill? Does it refine or generalize an existing skill? Is it covered by skills that already exist at higher confidence?
- **Risk assessment** — the research stance's overall risk score (`low`, `medium`, `high`), with reasoning. High-risk automatically triggers user escalation regardless of the consensus loop outcome.

The proposal triggers a consensus loop with specific convened partners:

- The research stance that found the candidate (as proposing stance)
- The CTO (required — skill imports affect behavior across all future work, which is a CTO-scope concern)
- The Lead Engineer (required — skill imports affect engineering practice)
- A security-focused stance if the wizard config has one enabled (recommended)

The loop runs through normal consensus mechanics. Partners review the proposal, raise dissents if anything is suspicious or uncertain, propose refinements, and either converge on import (with agreement from all required partners) or escalate. The loop has stricter convergence requirements than normal: any blocking dissent from the CTO, Lead Engineer, or security stance prevents convergence. Advisory dissents are recorded but do not block.

If the loop converges on import, the supervisor emits a `skill.import.approved` event. The manufacturer receives this event and executes the import: it re-validates the candidate against its own security checks (independent of the research stance's review, to catch anything the research stance missed), writes the candidate as a `skill` ledger node with `provenance: imported_external`, and attaches edges to the import proposal (so the full audit trail is preserved).

If the risk assessment is `high` OR the loop determines that user judgment is needed, the loop transitions to `escalated` with a `user_required` flag. The mission supervisor's `hierarchy.user_escalation` rule fires and surfaces the proposal to the user via the PO with the full security and consistency reviews attached. The user sees the candidate skill content, the sources, the reviews, and the risks, and decides whether to approve or reject. The user's response becomes a ledger node that resolves the escalation and, if approved, triggers the same `skill.import.approved` event the consensus loop would have produced.

**Imports that get rejected are still recorded.** A rejected import proposal and its loop history stay in the ledger permanently. This creates a negative corpus the manufacturer and future research stances can consult: "we considered importing this and decided not to; here's why." A future research stance that encounters the same candidate in different context can read the prior rejection and decide whether the new context changes the calculus.

---

## Workflow 4: Skill lifecycle management

After a skill is in the ledger (regardless of how it got there), its lifecycle continues. Skills get used, skills perform well or badly, skills become obsolete, skills contradict each other as the codebase evolves. The supervisor's skill governance rules (component 4 retrofit, new category) fire on events related to skill use and produce `skill.review.completed` events when they have evaluated a skill's performance. The manufacturer subscribes to these events and handles the resulting lifecycle actions.

**Confidence promotion.** When the supervisor decides a skill has earned a higher confidence based on its track record, the manufacturer writes a new skill node that supersedes the prior with the updated confidence. The old node remains in the ledger (append-only); the new node has a `supersedes` edge to the old; future concern field queries find the new node because queries walk to the latest non-superseded version.

**Confidence demotion.** Same mechanism in reverse. When the supervisor decides a skill has been misapplied enough times that it should be treated more cautiously, the manufacturer writes a new superseding node with lower confidence. Demoted skills are still queryable and loadable, but their lower confidence changes how the concern field renders them (more prominent disclaimers, lower priority in the prompt, flagged for immediate supervisor review on any application).

**Applicability refinement.** When the supervisor identifies that a skill was being loaded into stances where it wasn't actually helpful, the manufacturer writes a superseding node with narrower applicability criteria. This prevents the skill from being loaded in irrelevant contexts without removing it from the library.

**Marking as footgun.** When a skill has been demonstrably wrong in multiple missions, the supervisor can request that the manufacturer mark it as a footgun. The manufacturer writes a new node with `confidence: candidate` (demoted to the lowest level) and a special annotation indicating the skill should be surfaced to stances as a warning rather than as guidance: "stances have applied this pattern in the past and it has led to bad outcomes — here's why you should avoid it." Footgun skills are still useful because they capture institutional learning about what not to do.

**Supersession by contradicting evidence.** When a new skill is manufactured that directly contradicts an existing skill (e.g., a new pattern the team has proven that replaces an older pattern), the manufacturer writes the new skill with a `supersedes` edge to the old. The old remains in the ledger for audit but is no longer loaded into concern fields. This is the mechanism by which the library evolves — old skills don't die, they get superseded by better ones, and the audit trail shows the progression.

---

## The file format for skill markdown files

Skill files are markdown with YAML frontmatter. The file extension is `.skill.md`. Example layout:

```markdown
---
name: never-trust-done-claim
category: trust
confidence: proven
applicability:
  task_types: [all]
  stance_roles: [all]
  triggers:
    - "worker emits declaration.done"
    - "worker claims completion"
tags: [trust, verification, completion, fresh-context]
provenance: shipped_with_stoke
schema_version: 1
---

# Never trust a model's "I'm done" claim

## What to do

When any stance declares work complete, a fresh-context second-opinion reviewer
must verify the claim before it is accepted. The declaring stance is paused
pending the review. The reviewer gets the artifact, the original user intent,
the task context, and no prior knowledge of the declaring stance's reasoning.

## What to avoid

Do not accept completion claims at face value. Do not assume that because a
stance has worked hard on a problem, it has correctly identified that the
problem is solved. Do not skip the second-opinion step even when the work
looks obviously done — "obviously done" is the modal framing for premature
completion declarations.

## Why

Self-assessment is the weakest signal in any agentic system. Models
systematically underestimate what they've missed. Fresh-context review catches
the gap between "the stance believes it is done" and "the stance is actually
done per the spec." Multiple research studies document completion-claim failure
rates above 30% in single-model workflows.

## Examples

- Dev declares a ticket done; Reviewer checks and finds unhandled edge case
- Reviewer approves a PR; second-pass Reviewer finds a missing test
- Researcher declares a question answered; verification finds the answer was
  based on stale information

## Related skills
- fresh-context-independence
- self-assessment-unreliability
- reflexion-regression-risk
```

The YAML frontmatter is the structured part — all fields that map to the `skill` node schema in component 6. The markdown body is the `content` field. Fields in the frontmatter are validated at import time; unrecognized fields are rejected (to prevent smuggling data into skills via hidden fields).

The markdown body has a loose convention of sections (What to do / What to avoid / Why / Examples / Related skills) but the convention is not enforced — some skills are short enough to be a paragraph, some are long enough to warrant additional sections. The body is what stances actually read when the skill is loaded into their concern field.

---

## The shipped skill library catalog

The shipped library ships with Stoke's binary as embedded resources. It contains roughly 70 skills across 13 categories, all at `provenance: shipped_with_stoke` and `confidence: proven` by default. The categories and their skill counts:

**Trust and verification (8 skills).** Never trust a model's done claim; never trust a model's fix claim; never trust a model's problem claim; self-assessment is the weakest signal; worker uncertainty is a research trigger not a verdict; single-model verification has blind spots; long-running agents drift from intent; loops that cycle more than N times need outside view.

**Decision quality (7 skills).** Every decision must acknowledge prior decisions; decisions without cited inputs are unauditable; repo-level decisions must be self-contained; decisions under uncertainty are incomplete until research resolves them; convergence is structural not vibes; dissents must be addressed substantively; caveats vs blocking dissents.

**Snapshot defense (6 skills).** Pre-existing code is not Stoke's to silently modify; auto-formatters on snapshot are modification; refactors of snapshot require motivation; smart changes approved / unmotivated rewrites pushed back on; CTO posture is "show me the case"; snapshot annotations accumulate across sessions.

**Cross-team and collision (6 skills).** Two branches modifying same file is the common collision; cross-branch dependencies affect completion ordering; duplicate work produces hellish merges; critical-path branches falling behind are mission-level risks; interface changes must be visible to sharing branches; SDM outputs are advisories not enforcement.

**Research and uncertainty (6 skills).** Research is always right when uncertain; research reports without sources are hallucination; parallel researchers for high-stakes questions; research has timeouts; requesting stance pauses while research runs; research limitations must be recorded honestly.

**Hierarchy and authority (6 skills).** Branch supervisors cannot self-declare done; mission supervisors cannot self-declare done; escalations propagate one level at a time; user is the only closer of terminal decisions; subordinates pause without parent; trust rules apply recursively at every level.

**Cost and budget (5 skills).** Cost overruns are structural concerns; budget thresholds trigger Judge before user; hard stops protect user from runaway costs; cost is multi-dimensional; necessary overruns are still recorded as overruns.

**Ledger and audit (6 skills).** Append-only is non-negotiable; git hook is failsafe behind API; index corruption recoverable / canonical corruption not; schema evolution via versioning not migration; inherited human records are read-only and supersedable; decision logs are relational graph not bullet list.

**Event-driven discipline (6 skills).** Nothing polls; subscribers observe and hooks act; hook authority is privileged; replay reads without side effects; causality references are the audit trail; bus is per-mission with cross-mission via ledger.

**Stance and team (6 skills).** Fresh sessions are independent reviewers; persistent stances get compacted against internal decision log; round-2 reviewers read round-1's internal decision log; Lead Designer required on user-facing work; CTO veto on snapshot / vote on Stoke-written; persistent stances are persistent in role not session.

**Security (8 skills).** Validate all inputs at trust boundaries; never log secrets or include them in prompts; authentication vs authorization are different concerns; supply chain dependencies are attack surface; common CVE classes (injection, XSS, SSRF, path traversal, deserialization); least-privilege by default; defense in depth — no single layer is load-bearing; security decisions need CTO review even for Stoke-written code.

**Performance (5 skills).** Measure before optimizing; N+1 queries are the modal backend performance footgun; premature optimization is a real cost; caches introduce consistency problems; performance regressions are as serious as correctness regressions.

**Coding craft (5 skills).** Changing a function signature requires updating all callers in the same change; adding a field to a struct requires checking serialization and comparison; don't catch exceptions you can't handle meaningfully; comments explain why not what; defensive copies of mutable inputs.

**Stakeholder patterns (manufactured-only, ships empty).** This category exists in the catalog but is not populated in the shipped library. It is reserved for skills manufactured from prior Stakeholder directives in full-auto missions — patterns like "when escalations cite performance concerns, the usual right answer is X," "when escalations cite dependency conflicts, the usual right answer is Y." The category is loaded by the Stakeholder's concern field template when full-auto mode is active. Until manufacturing has produced entries (which requires full-auto missions to have run and the supervisor's `skill.extraction.trigger` rule to have fired on their `loop.escalated` events), the category is empty and the Stakeholder operates without category-specific guidance, relying on the other skill categories the same way other stances do. The category is mentioned here so the catalog is complete; the manufacturing workflow that fills it is workflow 2.

The file organization under `/skills/` in the Stoke source tree:

```
/skills/
├── trust/
│   ├── never-trust-done-claim.skill.md
│   ├── never-trust-fix-claim.skill.md
│   ├── never-trust-problem-claim.skill.md
│   ├── ...
├── decision-quality/
│   ├── acknowledge-prior-decisions.skill.md
│   ├── ...
├── snapshot-defense/
├── cross-team/
├── research/
├── hierarchy/
├── cost/
├── ledger/
├── event-driven/
├── stance-and-team/
├── security/
├── performance/
├── coding-craft/
└── stakeholder-patterns/  # ships empty, populated by manufacturing workflow 2
```

Each file is independent. The manufacturer imports them in a single batch at initialization, with ordering by category. Adding a new shipped skill means adding a new `.skill.md` file to the appropriate category directory and rebuilding the binary; the build process embeds the file into the binary via Go's `embed` package.

---

## Skill use is logged and reviewed

The manufacturer is responsible for writing the skill nodes. The logging and review of skill use happens elsewhere: the harness writes `skill_loaded` nodes when it builds a concern field that includes skills (component 7 retrofit); stances write `skill_applied` nodes when they reference a loaded skill in their output; the supervisor's skill governance rules (component 4 retrofit) fire on these events and perform reviews.

The manufacturer's only role in logging is receiving `skill.review.completed` events from the supervisor and applying the resulting lifecycle actions (confidence promotion, demotion, refinement, footgun marking, supersession). The manufacturer does not generate `skill_loaded` or `skill_applied` events itself — those come from the harness and the stances respectively.

This separation of concerns is important: the manufacturer owns the skill *substance* (writing the skill nodes, handling imports and extractions), and the supervisor owns the skill *governance* (auditing use, enforcing review on low-confidence applications, deciding when to promote or demote). Both are event-driven. Both write to the ledger. Neither has authority over the other's domain.

---

## Package structure

```
internal/skillmfg/
├── doc.go                       // references this file as canonical
├── manager.go                   // the manufacturer's main loop: subscribe to bus, dispatch to workflows
├── manager_test.go
├── workflows/
│   ├── library_import.go        // workflow 1: shipped library import
│   ├── library_import_test.go
│   ├── extraction.go            // workflow 2: manufacturing from completed missions
│   ├── extraction_test.go
│   ├── external_import.go       // workflow 3: external skill import
│   ├── external_import_test.go
│   ├── lifecycle.go             // workflow 4: lifecycle management
│   └── lifecycle_test.go
├── parser/
│   ├── parser.go                // markdown + YAML frontmatter parser
│   ├── parser_test.go
│   ├── validator.go             // skill content validation (security checks, schema, applicability)
│   └── validator_test.go
├── shipped/
│   ├── embed.go                 // go:embed directive for /skills/ directory
│   └── shipped_test.go          // tests that every shipped skill file parses and validates
└── extraction_template.go       // the concern field template for extraction stances (workflow 2)
```

The `shipped/embed.go` file uses Go's `//go:embed skills/**/*.skill.md` directive to bundle the shipped library into the binary. The tests in `shipped/shipped_test.go` run at build time to validate every shipped file — a broken shipped skill prevents the binary from building, which is the right enforcement level for the library that the manufacturer treats as the floor.

---

## What the skill manufacturer does not do

- **Enforce anything.** The manufacturer is a consumer of supervisor-emitted events. The supervisor enforces skill governance rules; the manufacturer handles the substance of skill files.
- **Decide which skills to load.** The concern field builder does that via its query templates (component 7). The manufacturer writes skill nodes; the concern field decides which to read.
- **Spawn stances.** Except in workflow 2 (extraction), where the manufacturer spawns dedicated extraction stances through the harness. All other spawning is the harness's job triggered by the supervisor.
- **Modify existing skill nodes.** The substrate is append-only. Lifecycle changes always write new nodes that supersede the prior.
- **Import skills without consensus.** External imports go through the mandatory consensus loop. Shipped and manufactured skills have their own provenance paths but even they are subject to confidence levels and supervisor review on application.
- **Reach into stance sessions.** The manufacturer does not see what stances do with loaded skills; it sees only the `skill.review.completed` events the supervisor emits. The separation preserves the audit chain.

---

## Validation gate

1. ✅ `go vet ./...` clean, `go test ./internal/skillmfg/...` passes with >70% coverage on the manager and >80% coverage on each workflow file
2. ✅ `go build ./cmd/stoke` succeeds — and the build embeds every `.skill.md` file under `/skills/` via go:embed
3. ✅ Every shipped `.skill.md` file parses successfully (YAML frontmatter + markdown body) and validates against the skill node schema from component 6 — a broken shipped file prevents the binary from building
4. ✅ Every shipped skill has `provenance: shipped_with_stoke`, `confidence: proven` (or a documented lower default), and all required frontmatter fields
5. ✅ The shipped library contains at least one skill in every declared category (trust, decision-quality, snapshot-defense, cross-team, research, hierarchy, cost, ledger, event-driven, stance-and-team, security, performance, coding-craft)
6. ✅ Workflow 1 (library import) imports all shipped skills when the manufacturer receives a `wizard.init.complete` event, with the wizard's per-skill confidence adjustments applied
7. ✅ Workflow 2 (extraction) spawns extraction stances via the harness on `skill.extraction.requested` events and commits `candidate` skills with `manufactured_from` edges to source decisions
8. ✅ Workflow 3 (external import) writes skill nodes only when triggered by `skill.import.approved` events — the manufacturer cannot import externally without the supervisor's prior consensus-loop-driven approval
9. ✅ Workflow 3 re-validates candidate skills against its own security checks at import time, independent of the research stance's review
10. ✅ Workflow 4 writes superseding skill nodes on `skill.review.completed` events with promotion, demotion, refinement, or footgun marking
11. ✅ Rejected import proposals and their loop history are preserved in the ledger permanently (verified by a test that creates a rejected proposal and confirms it is still queryable)
12. ✅ A malicious candidate skill (one containing prompt injection attempts, trust rule bypass language, or hidden instructions in frontmatter comments) is rejected by the security review in workflow 3 (verified by a test with a synthetic malicious candidate)
13. ✅ A skill file with unrecognized frontmatter fields is rejected by the parser (verified by a test with a candidate containing an extra field)
14. ✅ The manufacturer has no authority to register hooks on the bus (verified by attempting to register a hook from the manufacturer and asserting the bus rejects it — only the supervisor has hook authority)
15. ✅ The manufacturer does not modify any existing skill nodes — every lifecycle action is a new node that supersedes the prior (verified by inspecting the manufacturer's code for any direct update calls on existing ledger nodes)
16. ✅ The full lifecycle of a shipped skill can be traced via the ledger: imported → loaded into concern fields → applied by stances → reviewed by supervisor → promoted or demoted via new superseding node, with the full chain queryable
17. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

- **Supervisor skill governance rules** are a retrofit to component 4 — new rule category for skill load auditing, skill application review, skill application contradicts outcome, and skill import consensus loop trigger.
- **New node types** are a retrofit to component 6 — `skill_loaded`, `skill_applied`, `skill_import_proposal`, plus new provenance values on the existing `skill` type (`shipped_with_stoke`, `manufactured`, `imported_external`).
- **Concern field integration** is a retrofit to component 7 — the concern field builder writes `skill_loaded` nodes and emits `skill.loaded` events when it includes a skill in a stance's context.
- **The wizard** presents the shipped library to the user at initialization with per-skill inspection and confidence adjustment.
- **The harness** spawns extraction stances (workflow 2) and researcher stances that can find candidate skills (workflow 3 initiator).
- **The research stance's toolset** includes web search with filters for vetted skill file sources.

The next work is the retrofits to components 4, 6, and 7. Component 8 is the new component with the most content; the retrofits are focused edits to existing components to bring them into alignment with the skill governance model.
