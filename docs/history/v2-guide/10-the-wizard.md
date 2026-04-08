# 10 — The Wizard

The wizard is where the user meets Stoke's configuration surface. Every knob we have referenced in prior components — supervisor rule strength, concern field caps, skill confidence adjustments, snapshot initialization and updates, decision log import, bus filter configuration, per-loop-type iteration thresholds, budget thresholds, cost measurement, file-type-specific CTO treatment — is exposed through the wizard. It is the single place where the user declares how Stoke should behave on their repo.

The wizard is not a dashboard and not a runtime. It is a command-line tool that runs out-of-mission (during initialization) and occasionally in-between missions (for explicit configuration changes). Once a mission is running, the wizard is no longer the user's interface — the PO stance is. The wizard handles setup; the PO handles in-flight interaction.

This file specifies what the wizard does at initialization, what ongoing commands it supports, what the configuration surface looks like, how configuration changes propagate through the system, and the validation gate.

---

## What the wizard is

The wizard is a Go CLI program that ships as part of the Stoke binary, invoked via `stoke init`, `stoke config`, `stoke snapshot`, and similar subcommands. It is the user-facing configuration layer. It has no runtime role: it does not participate in missions, it does not subscribe to the bus, it does not spawn worker stances, it does not apply rules. It reads and writes configuration files, reads and writes specific ledger nodes (inherited decision imports, initial snapshot annotations), and orchestrates the initialization flow.

The wizard's responsibilities fall into four categories:

1. **First-time initialization** on a repo with no existing `.stoke/` directory
2. **Ongoing configuration** changes to Stoke's behavior after initial setup
3. **Snapshot management** commands (update, revert, cold-start annotation pass)
4. **Inspection** commands that let the user see current configuration, current skills, current annotations, and other state

Everything the wizard does is user-directed. It does not run on its own schedule; it runs when the user invokes it.

---

## The initialization flow

When the user first runs `stoke init` in a repo, the wizard executes a structured initialization flow. Each step is its own screen in the CLI with clear explanations and safe defaults. The user can skip most steps and accept defaults for a minimal setup, or step through each for fine-grained control.

**Step 1: Detect the repo and confirm.** The wizard reads the git state, confirms the current branch, and asks the user to confirm that they want to initialize Stoke on this repo at this commit. The confirmation is the moment the snapshot commit SHA gets locked in.

**Step 2: Create the `.stoke/` directory structure.** The wizard creates `.stoke/` with subdirectories for `ledger/`, `snapshot/`, `bus/`, and `config.yaml` at the root. These are all committed as part of the initialization's own git commit. The ledger's git hook is installed as part of this step.

**Step 3: Take the snapshot.** The wizard walks the repo's git-tracked state, computes the manifest, writes it to `.stoke/snapshot/manifest.json`, and commits. The user sees a summary: "Snapshot taken at commit abc123, 1,247 files tracked, 89 directories, 4 ignore patterns." This is the baseline the CTO will defend going forward.

**Step 4: Decide on the cold-start annotation pass.** The wizard asks whether the user wants the CTO to walk the snapshot and write initial annotations before any mission starts. The wizard estimates the token cost based on repo size (file count × average tokens per CTO consultation) and presents the estimate. Default: **skip** (annotations accumulate during actual missions instead). The user can choose to run the pass, skip it, or run it only on specific directories.

**Step 5: Import shipped skill library.** The wizard presents the shipped library catalog from component 8 — roughly 70 skills across 13 categories. For each category, the user sees the count and a brief description. The user can:

- Accept all shipped skills at default confidence (the common case — single confirmation)
- Drill into any category to see individual skills with descriptions
- Adjust confidence levels per skill (downgrade only, never upgrade from defaults)
- Disable specific skills or entire categories
- Request a global downgrade (e.g., "import everything as tentative")

The default is accept all at proven confidence, because the shipped library is curated. The user's ability to be more cautious is preserved but the default is the floor.

**Step 6: Import pre-existing decision records.** The wizard scans the repo for files that look like decision records. The scan has two passes and produces a confidence score for each candidate:

1. **Pattern pass.** Files under `docs/adr/`, `docs/architecture/`, `docs/decisions/`, and files matching filename patterns like `*.adr.md`, `ADR-*.md`, `DECISION-*.md`, and numbered title patterns like `001-*.md`. This is the fast first cut.

2. **Content pass.** Every markdown file in the repo (excluding `node_modules`, `vendor`, and other configured exclusions) is scanned for ADR-like structural signatures: the classic Nygard template (`## Status`, `## Context`, `## Decision`, `## Consequences` as section headers), the MADR template markers, numbered ADR-style titles at the top of the file (`# 0001. Title` or `# ADR 0001: Title`), explicit "Architectural Decision Record" phrases, and presence of status values like "Accepted", "Proposed", "Superseded", or "Deprecated" near the top. The content pass catches ADRs in non-standard locations and with non-standard filenames, which is common in repos that have evolved documentation structure over time.

Each candidate gets a confidence score:
- **High** — matched both the pattern pass and the content pass (filename looks like an ADR and content looks like one)
- **Medium** — matched only the content pass (content looks like an ADR even though filename does not)
- **Low** — matched only the pattern pass (filename looks like an ADR but content does not clearly confirm)

For each detected file, the wizard asks the user whether to import it as an inherited `decision_repo` node with `provenance: inherited_human`. The user can:

- Accept all high-confidence candidates with one command, then review medium and low individually
- Review each candidate individually regardless of confidence
- Skip all detected files
- Add additional paths the wizard did not auto-detect
- Specify a parent/child relationship between imported decisions if the user's existing structure has one

Imported human decisions have partial schemas (they will not have the structured `previous_contexts_acknowledged` field that Stoke-authored decisions have), and Stoke's read path tolerates missing fields on inherited entries.

**Step 7: Configure supervisor rule strength.** The wizard presents the supervisor's rule catalog with brief descriptions and current defaults. The user can:

- Accept all defaults (the common case)
- Adjust configurable knobs on specific rules (iteration thresholds, budget percentages, consensus partner timeouts, research timeouts, etc.)
- Disable optional rules entirely (rules in the "configurable" category, but NOT the trust rules, which cannot be disabled — the wizard displays them as locked)
- Configure rule strength presets ("minimal" / "balanced" / "strict") for users who want a one-click tuning

The trust rules are always shown in the list with a locked badge and a note explaining why they cannot be disabled. Attempting to disable them produces a clear message: "This is a trust rule and cannot be disabled. You may configure its strength (which model to use for second-opinion, how long to wait for response) but the rule always fires."

**Step 8: Configure budget and cost tracking.** The wizard asks the user to specify the cost dimensions they want Stoke to track and the thresholds they want to enforce. Defaults: track tokens, dollars, wall time, and iteration count; warn at 50% of budget, Judge check-in at 80%, user escalation at 100%, hard stop at 120%. The user can adjust each threshold, disable specific dimensions, or set "no budget" (not recommended but allowed).

**Step 9: Configure bus propagation filters.** The wizard presents the default propagation rules between branch supervisors and the mission supervisor with a brief explanation of the tradeoff. Default: filtered upward propagation (structural events forwarded, operational events filtered out). The user can select "verbose" (everything propagates, higher mission supervisor load) or "minimal" (only completion and escalation events propagate, lowest mission supervisor load).

**Step 10: Configure file-type CTO treatment.** The wizard lists file types and directories detected in the repo and asks the user to set CTO consultation policy. Policies are expressed two ways, and both are supported simultaneously:

- **Glob patterns** for file-type-based policy: `"*.go": "strict"`, `"*.md": "loose"`, `"*.lock": "excluded"`, `"*.generated.*": "excluded"`. Applied to any file in the repo whose path matches the glob.
- **Directory patterns** for location-based policy: `"internal/": "strict"`, `"cmd/": "strict"`, `"docs/": "loose"`, `"vendor/": "excluded"`, `"generated/": "excluded"`. Applied to any file whose path is under the directory.

Options per policy: `strict` (normal CTO consultation on all modifications), `loose` (CTO consultation only on significant modifications, based on line count thresholds), `excluded` (no CTO consultation for this file type or directory, Stoke can modify freely).

**Precedence rules when both apply.** When a file matches both a glob pattern and a directory pattern, the more specific wins. Specificity is computed as:

1. Directory patterns beat glob patterns when the directory pattern is deeper (more path segments) than the file is deep from the glob's implicit root
2. Longer glob patterns beat shorter glob patterns (`"*.generated.go"` beats `"*.go"`)
3. Deeper directory patterns beat shallower directory patterns (`"internal/auth/"` beats `"internal/"`)
4. If two patterns have equal specificity, the stricter policy wins (`strict` beats `loose` beats `excluded`) — the default is to be cautious when in doubt

Defaults: strict for common source code file globs (`*.go`, `*.py`, `*.ts`, `*.rs`, etc.), loose for documentation globs (`*.md`, `*.rst`, `*.txt`), excluded for lock files and generated file globs (`*.lock`, `*-lock.json`, `*.generated.*`, `*.pb.go`). Directory defaults: excluded for `vendor/`, `node_modules/`, `target/`, `build/`, `dist/`; no default directory policies beyond that (the user specifies location-based policies for their specific repo structure).

**Step 11: Summary and commit.** The wizard presents a summary of all choices made and asks for final confirmation. On confirmation, it writes the full `.stoke/config.yaml`, commits all initialization artifacts, and emits a `wizard.init.complete` event (which the skill manufacturer consumes to run the shipped library import, and which any other component can react to).

The full flow takes 5–15 minutes of the user's attention. A user who just wants defaults can complete it in under a minute by accepting each step. A user who wants fine-grained control can spend longer.

---

## The configuration file

The wizard's output is `.stoke/config.yaml`, which is git-tracked. All ongoing configuration changes are edits to this file (via wizard commands, not via direct editing — direct editing is possible but the wizard's commands are the supported path). The file's structure mirrors the component organization:

```yaml
stoke_version: "0.1.0"
initialized_at: "2026-04-06T14:23:00Z"
initialized_commit: "abc123..."

ledger:
  git_hook_enabled: true

bus:
  propagation_filter: "filtered"  # "verbose" | "filtered" | "minimal"
  ephemeral_events_allowed: true

supervisor:
  rules:
    trust:
      completion_second_opinion:
        enabled: true  # locked - cannot be set to false
        second_opinion_model_preference: "different_family_if_available"
        timeout_seconds: 300
      # ... other trust rules
    consensus:
      iteration_thresholds:
        prd_loop: 5
        pr_review_loop: 3
        refactor_proposal_loop: 2
      partner_timeout_seconds:
        pr_review_partner: 300
        sow_review_partner: 600
        judge_invocation: 1800
    # ... other categories
  budget:
    dimensions: ["tokens", "dollars", "wall_time", "iterations"]
    thresholds:
      warning: 0.50
      judge_checkin: 0.80
      user_escalation: 1.00
      hard_stop: 1.20

concern_field:
  per_section_caps:
    prior_decisions: 20
    applicable_skills: 15
    snapshot_annotations: 25
  per_template_token_budget: 8000

skills:
  library_confidence_override: null  # null = use shipped defaults
  disabled_skills: []
  disabled_categories: []

snapshot:
  cold_start_pass_completed: false
  file_type_policy:
    "*.go": "strict"
    "*.md": "loose"
    "*.lock": "excluded"
    "*.generated.*": "excluded"

wizard:
  last_updated_at: "2026-04-06T14:23:00Z"
  last_updated_by: "init"
```

Every field has a default; the wizard writes all fields at initialization with the user's choices (or defaults where the user skipped). Ongoing commands update specific fields and write the file back, recording the `last_updated_at` and `last_updated_by` for audit.

---

## Global user preferences

Stoke supports global user preferences that apply across all repos the user works on, with per-repo configuration overriding global preferences on a field-by-field basis. The precedence chain is:

1. **Built-in defaults** (shipped with the Stoke binary) — the floor
2. **Global user preferences** (`~/.config/stoke/config.yaml` on Linux/macOS, `%APPDATA%\stoke\config.yaml` on Windows) — the user's cross-repo tuning
3. **Per-repo configuration** (`.stoke/config.yaml` in the repo) — the repo-specific override
4. **Runtime overrides** — command-line flags on specific `stoke` invocations (e.g., `--budget-limit`) that take effect for that one invocation only

When a field is read at startup or during a config lookup, the system resolves it by checking per-repo first, then global, then defaults, and returning the first non-null value. Fields that are not set in per-repo fall through to global; fields not set in global fall through to defaults.

**What lives in global preferences.** Not every config field is suitable for global scope. The fields that make sense globally are the ones that represent user tuning independent of a specific repo's shape:

- Preferred model families for specific stance roles (e.g., "when spawning a Judge, prefer a model from a different family than whatever the proposing stance used")
- Default budget thresholds in dollar terms (e.g., "warn me at $10, hard-stop at $50, regardless of which repo I'm in")
- UI preferences (colors, verbosity, whether to show the full event stream or a summarized view)
- Default supervisor rule strength preset (`minimal` / `balanced` / `strict`) — the user's baseline posture
- Default file-type policies for common globs (e.g., "I always want `*.generated.*` excluded, in every repo")
- Researcher parallelism defaults (e.g., "spawn 3 researchers in parallel for high-stakes questions by default")
- Consensus partner timeout defaults

**What does NOT live in global preferences.** Repo-specific state is always per-repo, never global:

- The snapshot (defined per repo)
- The ledger (defined per repo)
- Inherited human decision records (imported per repo)
- Initialized commit SHA (the moment Stoke started on this specific repo)
- Cold-start annotation pass status (per-repo investment)
- Directory-based file-type policies (depend on the repo's structure)

The config schema validator enforces this distinction: attempting to set a repo-only field in the global preferences file produces a rejection with a clear message ("this field is per-repo and cannot be set globally").

**Commands for global preferences.** All `stoke config` commands accept a `--global` flag that targets the global preferences file instead of the per-repo config. Examples:

- `stoke config show --global` — displays the current global preferences
- `stoke config set --global supervisor.budget.thresholds.hard_stop 1.20` — updates a global preference
- `stoke config edit --global` — opens an editor on the global config
- `stoke config preset --global balanced` — applies a preset to global preferences

When run without `--global`, all commands target the per-repo config as before. The behavior difference between `--global` and the default is the file being written and the schema validation (global preferences are validated against a subset of the full schema).

**Global install and first run.** On first run of Stoke (before any repo initialization), the user can create a global preferences file via `stoke config init --global`. This is a simplified version of the initialization flow that asks only about the globally-applicable fields, not about snapshot or repo-specific concerns. The global init is optional — if the user never runs it, Stoke uses built-in defaults everywhere until the first per-repo `stoke init` runs.

**Config changes during a mission** follow the same propagation rules regardless of scope. A global preference change during an active mission emits a `wizard.config.changed` event with a `scope: global` tag. The supervisor sees the change, reads the merged configuration (per-repo overrides still apply), and acts accordingly. The audit trail distinguishes global vs per-repo changes so post-mortems can see which layer was modified.

---

## Operating mode

Stoke has two operating modes, selected via the `operating_mode` configuration field. The field is settable in both global preferences and per-repo config, with the normal precedence chain applying. Runtime overrides via the `--mode` flag on `stoke scope`, `stoke build`, or `stoke review-and-fix` take the highest precedence for a single invocation.

**`operating_mode: interactive`** (default). The user is the top of the escalation chain. When the mission supervisor's `hierarchy.user_escalation` rule fires, the escalation is delivered through the PO stance as a user-facing message, and the relevant work is paused pending the user's response. The user reads the loop history, the dissents, the Judge's verdict if present, and the requested resolution, and then replies. The reply becomes a ledger node that resolves the escalation, and the paused work resumes with the user's input in its concern field. This is the standard collaborative mode: Stoke handles the work it can handle and asks the user for help when it hits genuine decision points.

**`operating_mode: full_auto`** (opt-in). The Stakeholder stance (team roster role 11) replaces the user as the top of the escalation chain. When the `hierarchy.user_escalation` rule fires, instead of producing a PO message, the supervisor spawns a fresh-context Stakeholder stance with the escalation context and the `stakeholder_posture` configuration applied. The Stakeholder reads the escalation, evaluates it, may request research, may convene a second Stakeholder for cross-verification on high-stakes directives, and produces a directive node that resolves the escalation. The directive flows back into the loop the same way a user response would.

**The default posture for full-auto mode is `absolute_completion_and_quality`.** This is the built-in setting that, when applied to the Stakeholder's system prompt, establishes: "find the smartest, most complete, engineering-standards-compliant way to solve this problem, use research when uncertain, do not take shortcuts on quality or correctness for the sake of speed or cost, and do not rubber-stamp — genuinely evaluate whether the escalation is hard or symptomatic of something else." The wizard exposes the posture as a configurable field so advanced users can tune the Stakeholder's tradeoffs:

- `stakeholder_posture: absolute_completion_and_quality` (default) — no compromises on engineering standards for speed or cost. Research is used freely. The Stakeholder is biased toward doing the work thoroughly even if it costs more or takes longer.
- `stakeholder_posture: balanced` — the Stakeholder weighs cost, speed, and quality approximately equally. Appropriate for missions where "good enough now" is a legitimate answer.
- `stakeholder_posture: pragmatic` — the Stakeholder will accept minor quality tradeoffs to hit cost or time constraints. Appropriate for rapid prototyping or throwaway work.

The posture configuration is read by the harness at Stakeholder stance creation time and applied to the system prompt template. The Stakeholder's behavior on a given escalation is a function of (escalation context + mission intent + ledger slice + posture setting + prior Stakeholder directives in this mission). Changing the posture mid-mission takes effect on the next Stakeholder spawn, not retroactively on prior directives.

**Safety valve: Stakeholder disagreement.** If the Stakeholder convenes a second Stakeholder for high-stakes cross-verification and the two disagree, the escalation falls back to the interactive-mode path and the user is contacted regardless of the operating mode setting. Genuine Stakeholder-level disagreement is a signal that the decision requires actual human judgment. The user's response resolves the escalation, and full-auto mode resumes on the next escalation.

**Safety valve: self-forwarding.** If a Stakeholder's evaluation concludes "this genuinely requires the human's input" (e.g., a business decision outside engineering scope, a security boundary the user must authorize, a scope change that affects what the user actually wants built), the Stakeholder can produce a directive that forwards the escalation to the user via PO anyway. Full-auto mode defers to the Stakeholder's judgment about when human input is actually necessary, rather than forcing the Stakeholder to invent a directive on questions it should not be answering.

**The bench (component 12) monitors Stakeholder quality.** The full-auto mode is only as good as the Stakeholder's actual judgment. The bench includes metrics for Stakeholder directive quality: the fraction of Stakeholder directives that are non-trivial (something other than "proceed as proposed"), the correlation between Stakeholder decisions and downstream mission success, the rate at which Stakeholder decisions get revised by later Stakeholder invocations. If these metrics drift toward rubber-stamping or toward high-variance decision quality, the Stakeholder's system prompt, rule strength, or posture defaults need tuning. Full-auto mode is not fire-and-forget — it is a configuration that has to be validated against the bench before being trusted on real missions, and the bench's output is the evidence of trust.

**Switching modes.** Interactive and full-auto are set per-mission at invocation time. A mission invoked with `stoke scope --mode full_auto "goal"` runs in full-auto. A mission invoked without the flag uses whatever the config says is the default. The supervisor reads the mode at mission start and does not change it mid-mission (to prevent confusion about which escalations went where). If the user wants to change modes partway through, they abort the current mission and re-invoke with the new mode, with the existing ledger and snapshot carrying forward.

---

## Ongoing commands

After initialization, the wizard supports these commands for ongoing configuration:

**`stoke config show [section]`** — displays the current configuration, optionally scoped to a specific section (`supervisor`, `skills`, `snapshot`, etc.). Output is human-readable with current values and notes indicating which values differ from defaults.

**`stoke config set <key> <value>`** — updates a specific configuration value. The key is a dotted path into the YAML (e.g., `supervisor.rules.consensus.iteration_thresholds.prd_loop`). The wizard validates the value against the field's schema, rejects invalid values, and notes whether the change requires a supervisor restart. This form is scriptable — it is the preferred way to automate configuration changes in CI or in onboarding scripts.

**`stoke config edit [section]`** — opens the user's `$EDITOR` on the current configuration, optionally scoped to a specific section. Without a section argument, the full `.stoke/config.yaml` is opened; with a section argument (e.g., `stoke config edit supervisor`), only that section is extracted, the user edits it, and the wizard merges the edited section back into the full config. This form is for interactive human tuning — adjusting many related values at once is faster in an editor than running `config set` repeatedly. On save, the wizard validates the edited content against the config schema; invalid edits are rejected with a clear error message and the user is returned to the editor to fix them (or to abort the edit, which discards changes).

**`stoke config preset <name>`** — applies a named configuration preset (`minimal`, `balanced`, `strict`). Presets bundle multiple related adjustments into one command for users who want to shift Stoke's overall posture without tuning each rule individually.

**`stoke snapshot update [--all | --files ...]`** — updates the snapshot manifest per component 9's spec.

**`stoke snapshot revert`** — reverts the snapshot to the prior committed manifest per component 9's spec.

**`stoke snapshot annotate [--pass]`** — runs the cold-start annotation pass (if `--pass` is given) or adds a user-authored annotation for a specific file/directory (if used without `--pass`).

**`stoke skills list [category]`** — lists skills in the library, optionally filtered by category. Shows each skill's confidence, provenance, and usage count.

**`stoke skills inspect <skill-name>`** — displays the full content of a skill file, including its applicability, content, and history (loads, applications, reviews, supersedings).

**`stoke skills adjust <skill-name> --confidence <level>`** — changes a skill's confidence level. The wizard writes a new skill node superseding the prior with the updated confidence. Only downgrades are allowed through this command; upgrades must come from supervisor review (via the `skill.application.requires_review` rule's verdict pipeline).

**`stoke skills disable <skill-name>`** — disables a skill from being loaded into concern fields. The skill node remains in the ledger (append-only) but the concern field builder filters it out. Can be re-enabled with `stoke skills enable`.

**`stoke decisions import <path>`** — imports a human-authored decision record as an inherited `decision_repo` node. Used post-initialization when the user has new ADRs to add to the ledger's context.

**`stoke status`** — displays a summary of Stoke's current state: active missions, supervisor instances, recent events, recent decisions, open escalations, pending user input requests. This is the out-of-mission health check; the in-mission equivalent is the PO's updates.

Every command that modifies state (anything other than `show`, `list`, `inspect`, `status`) writes to `.stoke/config.yaml` or to the ledger, commits the change with a descriptive message, and emits a bus event so any running supervisor can react. Commands that require supervisor restart are clearly labeled and prompt the user before proceeding.

---

## Configuration change propagation

Some configuration changes can be applied while a supervisor is running (hot-reloadable); others require a supervisor restart to take effect. The wizard is explicit about which is which and handles the propagation correctly.

**Hot-reloadable changes:**

- Per-section concern field caps (affect only the next concern field built)
- Skill confidence adjustments (affect only the next concern field query)
- Skill enable/disable (same)
- Snapshot manifest updates (affect only the next CTO consultation)
- File-type CTO treatment (affect only the next modification proposal)
- Budget threshold warnings (affect only the next budget update event)
- Decision record imports (affect only the next ledger read)

These propagate through the normal bus event stream: the wizard commits the config change, emits a `wizard.config.changed` event with the affected section, and the components that care react at the next opportunity.

**Restart-required changes:**

- Supervisor rule enabling/disabling (rules are loaded at supervisor startup and the rule set does not change at runtime)
- Bus propagation filter (the filter is applied at publication, and changing it mid-mission would require re-publishing events under new routing)
- Budget dimensions (adding or removing a tracked dimension requires the budget tracker to restart)
- Consensus partner timeout defaults (timeouts are scheduled as delayed events at spawn time; changing the default does not affect already-scheduled events)

The wizard detects restart-required changes and presents the user with a clear choice: either apply the change and restart the supervisor now (which pauses active missions and resumes them with the new config after restart), or defer the change until the next mission (which keeps the current config file but applies the change at next mission start). The default is defer unless the user specifies otherwise.

---

## The wizard as a trust boundary

The wizard is where the user's trust posture gets recorded. Every choice the user makes during initialization and ongoing configuration is a declaration about how much autonomy Stoke has on this repo. This makes the wizard a meaningful trust boundary, and the configuration file a meaningful artifact.

Some properties that follow from this:

**The configuration file is auditable.** Every change to `.stoke/config.yaml` is a git commit with a descriptive message, so a user can walk the config history with `git log` and see exactly what was changed, when, and by what command. The full history is preserved indefinitely.

**Certain configurations require explicit confirmation.** Downgrading trust rule strength, disabling snapshot protection for a file type, allowing ephemeral events, or setting "no budget" all produce explicit warnings before they take effect. The wizard does not let the user accidentally weaken Stoke's defensive posture.

**The configuration cannot be manipulated by Stoke itself.** Stoke's runtime components (supervisor, harness, skill manufacturer, etc.) can *read* `.stoke/config.yaml` but cannot *write* to it. The only thing that writes to the config file is the wizard, and the wizard runs only when the user invokes it. This means there is no path by which Stoke can relax its own constraints during a mission — not via a worker that tries to edit the config, not via a hook that tries to change a rule, not via any other mechanism. The user's trust boundary declarations are immutable from Stoke's perspective during a mission.

**Config changes during a mission are recorded as supervisor events.** If the user runs `stoke config set` while a mission is active, the wizard's config change is emitted as a bus event that the active supervisor sees. The supervisor commits a decision node recording "the user changed X to Y during this mission" so the mission's audit trail captures the change. Config changes are never silent.

---

## Package structure

```
internal/wizard/
├── doc.go
├── cli.go                   // the main CLI entry point and subcommand dispatch
├── cli_test.go
├── init/
│   ├── init.go              // the full initialization flow
│   ├── init_test.go
│   ├── steps.go             // individual step implementations
│   └── steps_test.go
├── commands/
│   ├── config.go            // stoke config show/set/preset
│   ├── snapshot.go          // stoke snapshot update/revert/annotate
│   ├── skills.go            // stoke skills list/inspect/adjust/disable/enable
│   ├── decisions.go         // stoke decisions import
│   ├── status.go            // stoke status
│   └── *_test.go
├── config/
│   ├── schema.go            // config file schema definitions (the Go structs)
│   ├── validator.go         // validates config values against schemas
│   ├── loader.go            // reads .stoke/config.yaml
│   ├── writer.go            // writes .stoke/config.yaml with commit
│   └── *_test.go
└── tui/                     // terminal UI helpers for the initialization wizard
    ├── prompts.go
    ├── progress.go
    └── *_test.go
```

The wizard is a self-contained component. It depends on the ledger (to write inherited decision nodes), the snapshot mechanism (to take and update snapshots), and the skill manufacturer (which consumes `wizard.init.complete` events to run the shipped library import). It does not depend on the supervisor, the bus, or the harness at initialization time — those components have not yet been started when the wizard is running its init flow.

---

## What the wizard does not do

- **Run missions.** The wizard is a setup and configuration tool. Mission invocation is a separate command (`stoke run`, `stoke mission`, or similar) that the harness handles.
- **Talk to the user during missions.** That is the PO's job. The wizard is out-of-mission; the PO is in-mission.
- **Modify the ledger except through specific imports.** The wizard writes `snapshot_annotation` nodes during the cold-start pass and `decision_repo` nodes during ADR import, but it does not write any other ledger nodes. The rest of the ledger is populated by the supervisor, harness, and stances during actual missions.
- **Override trust rules.** The wizard can configure trust rule *strength* but cannot disable them. Attempts to disable via direct config edits are caught by the config validator.
- **Apply configuration changes without user confirmation.** Every config change goes through an explicit prompt unless the user specified `--yes` at the command line.
- **Store state between invocations.** The wizard is stateless. Each invocation reads the current `.stoke/config.yaml`, does its work, and writes back. There is no wizard daemon, no wizard session, no wizard memory.

---

## Validation gate

1. ✅ `go vet ./...` clean, `go test ./internal/wizard/...` passes with >70% coverage on the CLI and >80% on the init flow and config components
2. ✅ `go build ./cmd/stoke` succeeds and the `stoke` binary supports `init`, `config`, `snapshot`, `skills`, `decisions`, and `status` subcommands
3. ✅ `stoke init` on a repo with no existing `.stoke/` directory executes all eleven initialization steps and produces a valid `.stoke/config.yaml` plus the initial git commit
4. ✅ `stoke init` on a repo with an existing `.stoke/` directory either refuses to proceed or offers a migration path (configurable via a `--force` flag that the user must explicitly pass)
5. ✅ The initialization flow can be completed with all defaults in under ten keystrokes (measured by a scripted test using the `--yes` flag to accept each prompt)
6. ✅ `stoke config show` displays the current configuration accurately
7. ✅ `stoke config set` validates values against the config schema and rejects invalid values (verified by a test that attempts to set an invalid value and confirms rejection)
8. ✅ `stoke config set` for a restart-required field prompts the user to either restart the supervisor or defer, and does not silently apply the change
9. ✅ Trust rules cannot be disabled via `stoke config set` (verified by a test that attempts to set `supervisor.rules.trust.completion_second_opinion.enabled = false` and confirms rejection)
10. ✅ `stoke snapshot update` re-computes the manifest per component 9's spec and writes the updated manifest
11. ✅ `stoke snapshot revert` restores the prior manifest via git revert
12. ✅ `stoke skills list` enumerates skills in the ledger with their current confidence and provenance
13. ✅ `stoke skills adjust --confidence` only permits downgrades; upgrades are rejected with a message explaining they must come from supervisor review
14. ✅ `stoke decisions import` writes a `decision_repo` node with `provenance: inherited_human` and tolerates partial schemas on imported content
15. ✅ `stoke status` displays a non-error summary even when no mission is active
16. ✅ The wizard's config file writes are always git commits with descriptive messages (verified by inspecting the commit message of a test config change)
17. ✅ Stoke's runtime components cannot write to `.stoke/config.yaml` — the file has no runtime writers (verified by grep across `internal/` for writes to the config path; only `internal/wizard/config/writer.go` may write)
18. ✅ Configuration changes during an active mission emit `wizard.config.changed` events that the supervisor sees and commits as decision nodes in the mission's audit trail
19. ✅ The shipped library import triggered by `wizard.init.complete` is handled by the skill manufacturer, not the wizard itself — the wizard emits the event and moves on
20. ✅ `stoke config edit` opens the user's `$EDITOR` on the current configuration and validates the edited content before saving; invalid edits are rejected with a clear error and the user is returned to the editor or allowed to abort
21. ✅ ADR detection runs both the pattern pass and the content pass, produces confidence scores (high/medium/low), and presents candidates grouped by confidence
22. ✅ Content-based ADR detection correctly identifies files using the Nygard template, the MADR template, numbered ADR titles, and explicit ADR phrases (verified by a test with synthetic ADR files in each format)
23. ✅ File-type policy supports both glob patterns and directory patterns simultaneously, with correct precedence resolution when both match
24. ✅ When equal-specificity patterns conflict, the stricter policy wins (`strict` > `loose` > `excluded`)
25. ✅ Global user preferences in `~/.config/stoke/config.yaml` (or the platform equivalent) are read at startup and merged with per-repo config with per-repo taking precedence
26. ✅ Attempting to set a repo-only field in the global preferences file is rejected by the config validator with a clear error message
27. ✅ `stoke config show --global` displays only global preferences; `stoke config show` displays the merged effective configuration with notes indicating which values came from which layer (default / global / per-repo)
28. ✅ `stoke config init --global` creates the global preferences file with a simplified initialization flow that excludes repo-specific concerns
29. ✅ Config changes at the global scope emit `wizard.config.changed` events with a `scope: global` tag, distinguishable in the audit trail from per-repo changes
30. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

This file is component 10 of the new guide. It refers to things specified in later components:

- **The harness** is component 11. The harness is what starts missions and manages worker stance lifecycle; the wizard hands off to the harness at mission start via the `stoke run` command.
- **The bench** is component 12. Some bench commands are exposed through the wizard (e.g., `stoke bench run <scenario>`) as convenience wrappers, but the bench itself is its own component.

The next file to write is `11-the-harness.md`. The harness is the runtime layer that creates worker stances when the supervisor's hooks call into it — it handles model selection, system prompt construction, context loading, session initialization, and stance lifecycle. It is a relatively thin component because the substrate components above it have absorbed most of the complexity.
