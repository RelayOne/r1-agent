# 13 — Implementation Order and Validation Gates

This file is the execution plan. It sequences the build of Stoke from empty repo to shippable v1, threading together the per-component validation gates from components 2–12 into a single plan. It also documents the user-facing command surface so the CLI shape is fixed before implementation starts.

The sequencing principle is that no component can be built before the components it structurally depends on have passed their validation gates. The ledger comes before the bus (no — they are peers and can be built in parallel). The supervisor comes after both of them (because the supervisor uses both the ledger API and the bus API). The consensus loop comes after the supervisor (because the loop is driven by supervisor rules). And so on.

The result is a dependency-respecting order with a small number of phases. Within each phase, components can often be built in parallel if multiple engineers are working. Across phases, the phase boundaries are hard gates — the next phase cannot begin until the current phase's components have all passed their validation gates.

---

## The command surface

Stoke exposes three primary user-facing commands plus supporting commands for configuration and diagnostics. The primary commands are the entry points for all missions; every mission Stoke runs is initiated by one of these three.

**`stoke scope "goal description"`** — scopes a requirement into a PRD and a SOW without implementing anything. Creates a mission whose terminal state is "SOW converged and delivered to user." The mission runs through the PRD loop and the SOW loop, invoking the team's planning stances (PO, Lead Engineer, Lead Designer when user-facing concerns exist, CTO for any snapshot analysis, VP Eng for architecture review), and terminates with the SOW ready for user review. No code gets written during a scope mission. The output is a set of committed ledger nodes (the PRD, the SOW, the task DAG decomposition) and a user-facing summary produced by the PO.

**`stoke build`** — executes a previously-scoped mission. Takes a SOW that was produced by a prior `stoke scope` invocation (or by a human who wrote one directly) and runs it through to implementation. The mission starts at the "SOW accepted" state and terminates at "all tickets done, branches completed, user has accepted the terminal deliverable." This command is where Devs write code, Reviewers review PRs, QA runs tests, and the SDM coordinates across branches. Most of Stoke's runtime complexity is exercised by `stoke build`.

**`stoke review-and-fix`** — a compound command that chains three phases: audit the entire current repo for issues, scope all the fixes and hardening that the audit surfaces, then build the scoped changes. The command takes no "goal description" — its goal is implicit: find everything that could be improved and improve it. The audit phase produces a set of findings (bugs, security issues, unreached code, missing tests, outdated dependencies, inconsistent patterns, stale documentation, footguns). The scope phase turns the findings into a PRD and a SOW the way `stoke scope` does. The build phase executes the SOW the way `stoke build` does. Each phase is a distinct mission internally, with its own ledger scope and its own supervisor, but they are presented to the user as a single command invocation.

These three commands plus `stoke init` (initialization of a repo with the wizard), `stoke config` (configuration changes), and `stoke bench` (running the bench) are the full CLI surface. The full list:

```
stoke init                          # first-time setup for a repo
stoke init --global                 # first-time setup for global user preferences
stoke scope "goal"                  # produce a PRD+SOW for the goal
stoke build                         # execute a scoped SOW
stoke review-and-fix                # audit, scope, build in sequence
stoke config show                   # display current config
stoke config set <field> <value>    # update a config field
stoke config edit                   # open config in an editor
stoke config preset <name>          # apply a named preset
stoke bench run                     # run the bench
stoke bench compare <a> <b>         # compare two bench runs
stoke bench regression              # check for regressions against baseline
stoke status                        # show current mission state if one is running
stoke abort                         # cancel the current mission safely
stoke resume                        # resume a paused mission after a config change
```

The primary commands accept `--mode {interactive,full_auto}` to select the operating mode for a single invocation (overriding the config default), and `--budget <amount>` to set a per-invocation cost ceiling. The config overrides at the invocation level do not persist; they apply to the one mission being started.

**What a command does at invocation time.** The CLI dispatcher is a thin layer. For `stoke scope "goal"`:
1. Validates the repo has been initialized (`.stoke/` exists, config is present, snapshot is taken)
2. Creates a mission node in the ledger with the goal as the initial PRD intent
3. Resolves the operating mode from the invocation flag, per-repo config, global config, and defaults
4. Spawns a mission supervisor configured for the mode
5. The mission supervisor creates the root PRD loop, which begins by spawning the PO stance to draft the PRD
6. The CLI waits for the mission to reach terminal state (converged, escalated, aborted, or timed out) and displays a summary to the user

The CLI itself does not contain mission logic. All of the work happens in the supervisor, the harness, and the stances. The CLI is just the user's door.

---

## Phase 0: Scaffolding and substrate

**Goal.** Establish the repo structure, the build system, the CI wiring, and the most primitive building blocks that every other component needs.

**Components built in this phase.**

- Repo skeleton (`cmd/stoke/`, `internal/`, `internal/bench/golden/`, etc.)
- Go module setup with dependencies locked
- CI pipeline that runs `go vet`, `go test`, `go build`
- Pre-commit hook infrastructure (the hook itself won't be installed until the ledger is built in Phase 1, but the hook scripts live here)
- Configuration schema validation (a small package that parses `.stoke/config.yaml` and validates it against a schema — this is needed by everything and is standalone)
- Logging infrastructure with structured logging that feeds into the bus when the bus is built (in Phase 1) but works standalone until then
- An error types package so every component can return errors with consistent structure
- A small utility package for content-addressed IDs (used by the ledger for node IDs, but factored out so the ID shape is consistent across the codebase)

**Validation gate for Phase 0.** `go vet` clean, `go test ./...` passes, `go build ./cmd/stoke` produces a binary, CI runs these on every commit. No component-specific gates yet — Phase 0 is about the scaffolding being correct.

**Dependencies.** None. This is the foundation.

---

## Phase 1: The substrate components

**Goal.** Build the two foundational substrates (ledger and bus) that every other component depends on. These are peers — neither depends on the other, and they can be built in parallel.

**Components built in this phase.**

- **Component 2: The Ledger.** Package `internal/ledger`. API surface (`AddNode`, `AddEdge`, `Query`, `Resolve`, `Walk`, `Batch`). Content-addressed IDs. Append-only enforcement at the API layer. Git hook installation scripts and enforcement logic. SQLite index at `.stoke/ledger/.index.db`. Schema versioning. Inheritance mechanism for prior Stoke runs and human-authored ADRs.

- **Component 3: The Bus.** Package `internal/bus`. API surface (`Publish`, `Subscribe`, `RegisterHook`, `Replay`, `Cursor`, `PublishDelayed`, `CancelDelayed`). Durable write-ahead log at `.stoke/bus/{mission-id}/events.log`. Hook authority model. Delayed event mechanism. Event ordering and causality. Per-mission isolation.

- **Component 6: The Node Types** (partial). The Go structs in `internal/ledger/nodes/` that correspond to every node type defined in component 6 of the guide. Each struct with its `NodeType()`, `Validate()`, and `SchemaVersion` method. This is built in lockstep with the ledger because the ledger's validation logic calls into these structs.

**Validation gates for Phase 1.**

1. ✅ Component 2's validation gate (17 items from the ledger spec)
2. ✅ Component 3's validation gate (21 items from the bus spec)
3. ✅ Component 6's validation gate (15 items from the node types spec)
4. ✅ Integration test: a synthetic mission that writes nodes via the ledger API and emits events via the bus API produces a consistent audit trail queryable from both substrates
5. ✅ Integration test: a crash during a ledger write or a bus publish is recoverable — the ledger's git-backed store and the bus's WAL both recover cleanly from partial writes
6. ✅ Integration test: the ledger's SQLite index can be deleted and rebuilt deterministically from the git history, and the bus's cursor can be recovered from the WAL

**Dependencies.** Phase 0 complete. Components 2, 3, and 6 have no dependencies on each other and can be developed in parallel by separate engineers (or separate Stoke instances).

---

## Phase 2: The supervisor

**Goal.** Build the rules engine that turns the substrate into a system with behavior. Without the supervisor, the ledger and bus are just storage and messaging; with the supervisor, they become Stoke.

**Components built in this phase.**

- **Component 4: The Supervisor.** Package `internal/supervisor`. Core loop in `core.go`. Manifest files in `internal/supervisor/manifests/` for the three configurations (mission, branch, SDM). Rule files in `internal/supervisor/rules/{category}/{rule-name}.go`, one file per rule, with unit tests alongside. The full rule taxonomy from component 4:

  - Category 1 (Trust): `completion_requires_second_opinion`, `fix_requires_second_opinion`, `problem_requires_second_opinion`
  - Category 2 (Consensus): `draft_requires_review`, `dissent_requires_address`, `convergence_detected`, `iteration_threshold`, `partner_timeout`
  - Category 3 (Snapshot): `modification_requires_cto`, `formatter_requires_consent`
  - Category 4 (Cross-team): `modification_requires_cto`
  - Category 5 (Hierarchy): `completion_requires_parent_agreement`, `escalation_forwards_upward`, `user_escalation` (with interactive and full-auto variants, including the Stakeholder spawn path for full-auto)
  - Category 6 (Drift): `judge_scheduled`, `intent_alignment_check`, `budget_threshold`
  - Category 7 (Research): `request_dispatches_researchers`, `report_unblocks_requester`, `timeout`
  - Category 8 (Skill extraction trigger): `extraction_trigger`
  - Category 9 (SDM detection): `collision_file_modification`, `dependency_crossed`, `duplicate_work_detected`, `schedule_risk_critical_path`, `drift_cross_branch`

- Supervisor state checkpoint mechanism (writes `supervisor_state_checkpoint` nodes on structural events)
- Supervisor crash recovery (reads latest checkpoint, replays bus events forward)
- Hook registration authority (only the supervisor can register hooks, verified structurally by the bus)

**Validation gate for Phase 2.** Component 4's validation gate (26 items from the supervisor spec), plus:

1. ✅ A synthetic mission run with a scripted event sequence produces the expected cascade of rule firings
2. ✅ The mission supervisor, a branch supervisor, and the SDM supervisor can all run concurrently on the same mission without conflicting
3. ✅ Crash recovery works for all three supervisor configurations
4. ✅ The full-auto variant of `hierarchy.user_escalation` correctly spawns a Stakeholder stance when the mission's operating mode is full-auto (stubbed Stakeholder for this phase — the actual stance logic is in Phase 4)

**Dependencies.** Phase 1 complete (ledger, bus, node types). The supervisor reads and writes the ledger, subscribes to and publishes on the bus, and reads node type schemas.

---

## Phase 3: The consensus loop and concern field

**Goal.** Establish the coordination pattern (the consensus loop) and the per-stance context projection (the concern field) that together make stances operate as a team rather than as isolated workers.

**Components built in this phase.**

- **Component 5: The Consensus Loop.** This is not a standalone package — it's a coordination pattern implemented as supervisor rules (already in Phase 2) plus a small state-tracking helper in `internal/ledger/loops/` that makes it easy to query "what is the current state of this loop" without walking the full supersede chain manually. The query helper is a thin layer over the ledger API.

- **Component 7: The Concern Field.** Package `internal/concernfield`. Query templates that project the ledger into per-stance prompt context, parameterized by task DAG scope and stance role. The templates are stored as Go code, not as config, because the shape of each template is role-specific and benefits from type-checking.

**Validation gates for Phase 3.**

1. ✅ Component 5's validation gate (14 items from the consensus loop spec)
2. ✅ Component 7's validation gate (whatever the concern field file specifies)
3. ✅ Integration test: a synthetic PRD loop runs through all states (proposing → drafted → convening → reviewing → resolving dissents → drafted → converged) driven by supervisor rules, producing the correct ledger nodes at each transition
4. ✅ Integration test: a concern field query for a specific stance role and task DAG scope returns a context payload that matches the expected shape

**Dependencies.** Phase 2 complete. The consensus loop is driven by supervisor rules; the concern field is a layer on top of the ledger.

---

## Phase 4: The harness and the stances

**Goal.** Build the runtime layer that actually creates worker stances when the supervisor spawns them, and establish the system prompt templates and session shapes for every stance role in the team roster.

**Components built in this phase.**

- **Component 11: The Harness.** Package `internal/harness`. Stance creation logic. Model selection from wizard config. System prompt construction by combining the stance role's template with the concern field projection. Session initialization through the model provider APIs. Pause and resume mechanics. Tool authorization. Worker event emission.

- **Stance templates for every role in component 1.** One template file per role under `internal/harness/stances/{role}.go` containing the system prompt template, the default model preference, the consensus posture, and the session shape directives. The eleven stances: PO, Lead Engineer, Lead Designer, VP Eng, CTO, SDM, QA Lead, Dev, Reviewer, Judge, Stakeholder.

- **Tool authorization matrix.** Which stance roles can use which tools. Researchers can use web search. Devs can read and write code. The CTO can only read snapshot code. The Judge cannot write code at all. This matrix is a config file validated by the harness at stance creation time.

**Validation gate for Phase 4.** Component 11's validation gate (16+ items from the harness spec), plus:

1. ✅ Each stance role can be spawned, receives its correct system prompt and tool authorizations, and emits events correctly
2. ✅ The Stakeholder stance specifically: spawning it produces a fresh-context session with the `absolute_completion_and_quality` posture applied by default, and the anti-rubber-stamp language in the system prompt is present and testable via a synthetic escalation
3. ✅ Pause and resume mechanics work across all stance roles
4. ✅ A paused stance cannot be silently unpaused by anything other than the supervisor's hook action

**Dependencies.** Phase 3 complete. The harness depends on the concern field for prompt construction and on the supervisor for spawn requests. The stance templates depend on all of components 1–10 conceptually but only on the harness interface structurally.

---

## Phase 5: The snapshot and the wizard

**Goal.** Build the user-facing configuration surface and the snapshot mechanism that defends the user's pre-existing code.

**Components built in this phase.**

- **Component 9: The Snapshot Mechanism.** Package `internal/snapshot`. Snapshot capture at initialization (git commit SHA + list of files + directory structure). The "in the snapshot" query used by the supervisor's snapshot rules. Snapshot annotation CRUD through the ledger. Cold-start handling for brand-new repos. Explicit snapshot updates when the user promotes Stoke-written code to snapshot status via the wizard.

- **Component 10: The Wizard.** Package `internal/wizard` with CLI entry points in `cmd/stoke/` for `init`, `config show/set/edit/preset`, and the global preferences commands. Initialization flow. Configuration file schema. Global user preferences with per-repo override chain. Operating mode configuration (interactive vs full-auto). Supervisor rule strength presets. Decision log import from human-authored ADRs.

**Validation gates for Phase 5.**

1. ✅ Component 9's validation gate (whatever the snapshot spec has)
2. ✅ Component 10's validation gate (whatever the wizard spec has)
3. ✅ Integration test: `stoke init` on a fresh repo produces a valid `.stoke/` directory with config, snapshot, and empty ledger
4. ✅ Integration test: operating mode is correctly read from config and passed to the mission supervisor at mission start
5. ✅ Integration test: `stoke config set operating_mode full_auto` is respected by the next mission invocation

**Dependencies.** Phase 4 complete. The snapshot uses the ledger. The wizard writes the config file that the supervisor and harness read at startup.

---

## Phase 6: The skill manufacturer

**Goal.** Build the separate process that handles skill lifecycle management, including shipped skill import, manufacturing from completed missions, and external skill import through the security-reviewed consensus loop.

**Components built in this phase.**

- **Component 8: The Skill Manufacturer.** Package `internal/skillmfr`. The four workflows from the component 8 spec: shipped library import at initialization, manufacturing from completed missions, external skill import, and skill lifecycle management. The file format for skill markdown files. The shipped skill library catalog. Skill use logging and review.

**Validation gate for Phase 6.** Component 8's validation gate.

**Dependencies.** Phase 5 complete. The skill manufacturer writes skill nodes to the ledger, reads completed mission decision logs, and is triggered by the supervisor's `skill.extraction.trigger` rule.

---

## Phase 7: The bench and the golden set

**Goal.** Build the self-evaluation infrastructure and seed the golden mission set. This phase is distinct from the earlier phases because the bench depends on everything else being functional — you cannot run a mission through the bench until there is a working mission runtime.

**Components built in this phase.**

- **Component 12: The Bench.** Package `internal/bench`. Runner, metrics, baselines, reporter. CLI commands under `stoke bench`. Golden mission set seeded with at least one mission per category (greenfield, brownfield refactor, bug fix, multi-branch coordination, impossible/ambiguous, long-horizon, known footgun).

- **CI integration** for regression detection on commits to main.

**Validation gate for Phase 7.** Component 12's validation gate (16 items from the bench spec), plus:

1. ✅ Running the bench against a known-good build produces a baseline report
2. ✅ The golden set has at least one mission per category
3. ✅ The bench's regression detection can be triggered with a synthetic regression and correctly flags it
4. ✅ The bench's Stakeholder quality metric correctly measures directive quality on full-auto missions

**Dependencies.** Phases 1–6 complete. The bench exercises everything.

---

## Phase 8: End-to-end and shippable v1

**Goal.** Tie everything together, exercise all the commands against all the golden mission categories, fix everything the bench reveals, and cut the v1 release.

**Work in this phase.**

- Run the full golden set in both interactive and full-auto modes. Document any failures and address them.
- Verify all three primary commands (`stoke scope`, `stoke build`, `stoke review-and-fix`) work end-to-end on representative missions.
- Verify the command surface is complete and the CLI is usable (help text, error messages, progress indicators).
- Documentation for users: getting started, command reference, configuration reference, what to do when things go wrong.
- Release engineering: binary builds for the supported platforms, installation instructions, signing.

**Validation gates for Phase 8 and v1 release.**

1. ✅ Every component validation gate from phases 1–7 still passes (no regressions)
2. ✅ Every golden mission runs to completion (converges, escalates appropriately, or times out with a clear reason) in both interactive and full-auto modes
3. ✅ `stoke scope` successfully produces a PRD+SOW for each golden mission category
4. ✅ `stoke build` successfully executes a scoped SOW end-to-end for the simpler mission categories
5. ✅ `stoke review-and-fix` successfully audits, scopes, and builds fixes on a representative repo with known issues
6. ✅ The bench reports no unexplained regressions against the v0 baseline
7. ✅ The Stakeholder quality metric in full-auto mode is above the configured floor
8. ✅ Documentation covers all user-facing behaviors
9. ✅ The release build is reproducible and signed

---

## Cross-phase validation principles

A few principles that apply across all phases, not specific to any one:

**No phase can begin until the prior phase's gates pass.** This is the hard sequencing rule. If Phase 2's validation gate has an open item, Phase 3 does not start. This prevents the common failure mode where later components are built on top of earlier components that have latent bugs, and the bugs surface only after significant work has been layered on top of the broken foundation.

**Gates are not negotiable for convenience.** If a validation gate item is failing and the team's instinct is to mark it as "good enough for now" or "we'll fix it later," the right move is to either fix it now or remove the gate item from the spec (with documented reasoning). A gate item that exists but is not enforced is worse than no gate at all, because it creates the illusion of validation without the substance.

**Every phase has a smoke test that exercises the components together, not just individually.** Unit tests per component are necessary but not sufficient. Each phase includes integration tests that verify the new components work with the prior phases' components in a representative scenario. These integration tests are part of the phase's validation gate.

**Regression detection runs continuously from Phase 7 onward.** Once the bench exists, every commit must pass the subset-bench in CI. The full bench runs nightly. Regressions that appear in the nightly bench but not the commit bench are still regressions — the subset is for speed, not for permission to regress.

**The bench's Stakeholder quality metric is part of the v1 gate.** Full-auto mode is not considered shippable if the Stakeholder is producing rubber-stamp directives or inconsistent decisions. The bench's Stakeholder metric has to clear a floor before v1 ships, and if it does not, the fix is to improve the Stakeholder's system prompt, tune the rule strength, or modify the posture presets — not to ship a broken full-auto mode.

---

## Effort estimation

Rough effort estimates per phase, understanding that LLM-assisted development (with Stoke eating its own dog food once Phase 5 lands) changes the shape of these numbers significantly:

- **Phase 0:** 1–2 days (scaffolding, CI, standalone utilities)
- **Phase 1:** 2–3 weeks (ledger + bus + node types, in parallel)
- **Phase 2:** 2–3 weeks (supervisor with all 26 rules, tested)
- **Phase 3:** 1 week (consensus loop state tracking + concern field templates)
- **Phase 4:** 2 weeks (harness + 11 stance templates + tool authorization)
- **Phase 5:** 1–2 weeks (snapshot + wizard + CLI plumbing)
- **Phase 6:** 1 week (skill manufacturer with four workflows)
- **Phase 7:** 2 weeks (bench + golden set seed + CI integration)
- **Phase 8:** 2–4 weeks (end-to-end validation, bug fixing, documentation, release)

**Total: 12–17 weeks** for a focused engineer using Stoke to build Stoke from Phase 5 onward. This estimate assumes the specs in components 1–12 are stable (which they are after the review rounds that produced the current guide). It does not assume a large team; a single disciplined engineer can execute this plan because most of the components are small and the phase gates prevent drift.

---

## What the implementation order does not do

- **Does not specify which engineer does what.** The phases are dependency-ordered, but within a phase multiple components can be built in parallel if multiple engineers are available. The sequencing document is agnostic about team composition.

- **Does not specify which language features to use.** Every component is in Go (the decision from Phase 0), but idiomatic Go patterns, error handling styles, testing frameworks, and the like are delegated to the component specs and to the team's collective style conventions.

- **Does not freeze the specs.** If a phase's implementation reveals that a component spec is wrong or incomplete, the spec gets updated through the same consensus loop mechanism the rest of Stoke uses. The implementation order does not make the specs immutable; it sequences the building of what the specs currently say.

- **Does not account for unknowns.** Model API changes, new research that invalidates assumptions, bugs in upstream dependencies, hardware availability — all of these are real and all of them can blow up the schedule. The effort estimates above assume nominal conditions; real execution will need slack.

- **Does not replace the per-component validation gates.** The phase gates are aggregations of the component gates; they do not introduce new validation concerns. If a component's gate is satisfied, that component is done; the phase gate is a rollup of the component gates plus integration tests.

---

## The all-in-one validation gate summary

For reference, the component-by-component validation gate counts:

| Component | Gate items | File |
|---|---|---|
| 2: Ledger | 17 | `02-the-ledger.md` |
| 3: Bus | 21 | `03-the-bus.md` |
| 4: Supervisor | 26 | `04-the-supervisor.md` |
| 5: Consensus loop | 14 | `05-the-consensus-loop.md` |
| 6: Node types | 15 | `06-the-node-types.md` |
| 7: Concern field | 18 | `07-the-concern-field.md` |
| 8: Skill manufacturer | 17 | `08-the-skill-manufacturer.md` |
| 9: Snapshot mechanism | 19 | `09-the-snapshot-mechanism.md` |
| 10: Wizard | 30 | `10-the-wizard.md` |
| 11: Harness | 18 | `11-the-harness.md` |
| 12: Bench | 16 | `12-the-bench.md` |

Total: 211 individual validation gate items across the eleven gated components (component 1 is the team roster and has no gate; component 13 is this implementation order document and is itself a gate aggregator), plus integration tests per phase, plus the Phase 8 end-to-end validation. The number is intentionally large because each item is meant to catch a specific kind of bug or drift. A build that passes all of them is not guaranteed to be correct, but it is guaranteed to not have the specific failures each item tests for — and the failures each item tests for are the failures we most care about.

---

## This is the last file in the guide

Component 13 closes the new guide. The guide covers:

- **Component 1: Team** — who the stances are and what their roles are, now including the Stakeholder for full-auto mode
- **Component 2: Ledger** — the append-only graph substrate for persistent reasoning
- **Component 3: Bus** — the event-driven runtime substrate with delayed events and hook authority
- **Component 4: Supervisor** — the rules engine that enforces Stoke's behavior through three configurations
- **Component 5: Consensus loop** — the coordination pattern for how decisions get made
- **Component 6: Node types** — the schema reference for everything in the ledger
- **Component 7: Concern field** — how the ledger projects into per-stance prompt context
- **Component 8: Skill manufacturer** — the separate process for skill lifecycle management
- **Component 9: Snapshot mechanism** — how the user's pre-existing code is defended
- **Component 10: Wizard** — the user-facing configuration surface including operating mode
- **Component 11: Harness** — the runtime layer that creates worker stances
- **Component 12: Bench** — the self-evaluation infrastructure
- **Component 13: Implementation order** — the execution plan and the command surface

Thirteen components, approximately four thousand lines, specifying a system that starts with an empty repo and an empty ledger and ends with a high-trust, high-completeness, high-quality engineering team built from model sessions operating as a team of stances. The guide is the spec; the implementation plan above sequences the build; the bench measures whether it works.

The next move is to start Phase 0.
