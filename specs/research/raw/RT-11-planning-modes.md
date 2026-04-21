# RT-11: Planning / Execution Mode Split + Ask/Notify Interaction

Date: 2026-04-20
Scope: `stoke plan --sow`, `stoke chat`, Ask/Notify operator interface, Intent Gate, per-file repair cap.
Primary sources: CL4R1T4S leak archive, awesome-system-prompts/EliFuzz leak archive (2025-08-09), lmstudio dirty-data, x1xhlol/system-prompts-and-models-of-ai-tools, Devin docs, Cursor forum.

---

## 1. Devin's mode machine

Devin 2.0 uses a **3-mode state machine** driven by a `suggest_plan` transition and a `block_on_user_response` enum. Verbatim extractions from the leaked system prompt (CL4R1T4S/Devin + EliFuzz 2025-08-09 archive + lmstudio dirty-data/devin):

### Modes

> "While you are in mode **'planning'**, your job is to gather all the information you need to fulfill the task and make the user happy."

> "While you are in mode **'standard'**, the user will show you information about the current and possible next steps of the plan."

> "While in **'edit'** mode, you must execute all the file modifications that you listed in your plan. Execute all edits at once using your editor commands."

Planning permits browser + LSP + file read. Edit mode is a batch commit of the plan. Standard is the normal execution loop.

### suggest_plan

> "Once you have a plan that you are confident in, call the **suggest_plan** command. At this point, you should know all the locations you will have to edit."

This is the planning → standard transition. The plan carries concrete file citations; Devin's UI shows them to the operator for inspection before execution. Devin docs ("Interactive Planning") confirm the UX:

> "By default, Devin will wait thirty seconds for feedback from you before automatically proceeding with its plan."

There is an operator-side "Wait for my approval" toggle to convert the 30s soft-pause into an indefinite block.

### block_on_user_response (BLOCK / DONE / NONE)

This prop is attached to every user-visible message. Verbatim semantics:

- **BLOCK**: "completely blocked by critical questions or issues that ONLY the user can answer." Examples from the prompt: "I need your database password...and cannot find it" or "The codebase is completely broken and I cannot determine how to fix the build errors."
- **DONE**: "Be careful since the session will be terminated and your computer will be turned off after you output DONE." Terminal state.
- **NONE**: "Use NONE or omit the prop to keep going." Default for collaboration, design discussion, clarifying scope questions.

Rule: "The message you send to the user must always be consistent with the value you provide for block_on_user_response, and you should never block on the user if your message does not clearly convey that you are expected a response and will not proceed without it."

### `<think>` forced reasoning

Mandatory `<think>` invocation is required before:
- transitioning planning → standard,
- using non-standard git operations (branch selection, PR creation strategy),
- telling the operator the task is complete,
- opening images/screenshots or after browser steps.

Constraint: "you are only allowed to output at most one think command per response." Hidden from operator: "The user will not see any of your thoughts here, so you can think freely."

---

## 2. Manus's notify vs ask

Manus ships **two distinct tools** (from the Manus tool manifest, jlia0 gist + x1xhlol archive):

### message_notify_user (non-blocking)

> "Send a message to user without requiring a response. Use for acknowledging receipt of messages, providing progress updates, reporting task completion, or explaining changes in approach."

Params: `text` (required), `attachments` (optional file paths/URLs).

### message_ask_user (blocking)

> "Ask user a question and wait for response. Use for requesting clarification, asking for confirmation, or gathering additional information."

Params: `text` (required), `attachments` (optional), `suggest_user_takeover` ∈ {"none","browser"} (default "none").

### Rules (from Message_rules)

> "Message tools are divided into notify (non-blocking, no reply needed from users) and ask (blocking, reply required)."

> "Actively use notify for progress updates, but reserve ask for only essential needs to minimize user disruption and avoid blocking progress."

Manus additionally requires: on task completion, summarize and report current results before entering idle. This is the hand-off signal.

---

## 3. Factory DROID — Phase 0 Intent Gate

From the leaked Droid CLI system prompt (Pliny/elder_plinius leak, lmstudio dirty-data/factory-droid). Phase 0 runs **before every message**:

> "**Simple Intent Gate (run on EVERY message):** If you will make ANY file changes (edit/create/delete) or open a PR, you are in IMPLEMENTATION mode. Otherwise, you are in DIAGNOSTIC mode. If unsure, ask one concise clarifying question and remain in diagnostic mode until clarified."

> "Re-evaluate intent on EVERY new user message."

### IMPLEMENTATION mode mandates
- git fetch + pull + dependency sync (frozen/locked install) BEFORE any change
- feature branch + descriptive commits
- lint, type-check, test, build must pass before PR
- terminates in validated PR

### DIAGNOSTIC mode mandates
- may open/inspect any source file immediately
- MUST NOT install/update deps unless user explicitly approves
- no branch creation, no PR, no file writes
- output is analysis/report only

The single-clarifying-question rule is the key differentiator: if intent is ambiguous, Droid does NOT guess an implementation — it asks one question and remains read-only.

---

## 4. Cursor 2.0 — per-file repair cap + ambiguity policy

From CL4R1T4S/CURSOR/Cursor_Prompt.md and Agent Prompt 2.0 (x1xhlol archive):

### Per-file linter loop cap (verbatim)

> "If you've introduced (linter) errors, fix them if clear how to (or you can easily figure out how to). Do not make uneducated guesses. And **DO NOT loop more than 3 times on fixing linter errors on the same file. On the third time, you should stop and ask the user what to do next.**"

Cap is **per-file**, not per-session. At cap, Cursor escalates to user, not to a different fix strategy.

### When to pause (verbatim)

> "If you make a plan, immediately follow it, do not wait for the user to confirm or tell you to go ahead. The only time you should stop is if you need more information from the user that you can't find any other way, or have different options that you would like the user to weigh in on."

> "Bias towards not asking the user for help if you can find the answer yourself."

> "Only terminate your turn when you are sure that the problem is solved."

Cursor's default is **proceed, don't pause**. This contrasts with Devin (which has explicit planning-mode suspension) and Droid (which defaults to diagnostic/read-only when ambiguous).

---

## 5. `stoke plan` design (synthesis)

### Command signature

```
stoke plan --sow ./spec.md [--out plan.json] [--yes] [--estimate-only]
stoke execute --plan plan.json [--resume]
stoke ship --sow ./spec.md              # plan + auto-approve + execute (CI/CD shape)
```

Separate `plan` and `execute` commands mirror Devin's planning → standard mode boundary. `stoke ship` is the non-interactive convenience for CI. `--yes` on `plan` auto-approves, matching Devin's 30s soft-timer but deterministic.

### plan.json artifact shape

```json
{
  "plan_id": "pln_<sha256prefix>",
  "sow_path": "./spec.md",
  "sow_hash": "<sha256>",
  "created_at": "2026-04-20T...",
  "stoke_version": "...",
  "dag": {
    "nodes": [
      {"id":"T1","kind":"execute","title":"...","file_scope":["a.go"],"deps":[],"grpw":0.8,"est_cost_usd":0.42,"est_tokens":18000,"est_duration_s":90,"stance":"dev","intent":"IMPLEMENT"}
    ],
    "edges": [{"from":"T1","to":"T2","kind":"data"}]
  },
  "briefings": {"T1":{"system_prompt_hash":"...","context_bundle_ref":"ctx_..."}},
  "preflight": {
    "protected_files_clean": true,
    "baseline_captured": true,
    "baseline_commit": "abc123",
    "snapshot_ref": "snap_...",
    "repo_clean": true,
    "skills_detected": ["go","react"]
  },
  "cost_estimate": {"total_usd": 2.10, "p95_usd": 4.00, "token_budget": 220000},
  "approval": null
}
```

On approval, stoke appends an `approval` block signed with operator identity + timestamp and replays through `bus/` (event-sourced) so subsequent `stoke execute --plan` is resumable.

### Pre-flight split (plan vs ship)

**Run in both:** protected-files clean check, baseline capture, repo-clean check, skill detection, snapshot (`snapshot.Take`), budget estimate against `CostTracker.OverBudget`.

**`stoke plan` only:** interactive confirmation prompt, DAG rendering, briefing preview, per-task cost display.

**`stoke ship` only:** synthesize an auto-approval event and write it to the bus immediately so `execute` does not pause. This is the Devin "auto-proceed after 30s" pattern but deterministic.

### Approval event

Emit `bus.Event{Kind: "plan.approved", Actor: "operator:<user>", PlanID: ..., SowHash: ..., Ts: ...}`. `stoke execute --plan` asserts presence of `plan.approved` with matching `plan_id` + `sow_hash` before dispatching any worker. `--resume` replays the bus and picks up from the first unsatisfied DAG node.

---

## 6. Ask/Notify `Operator` interface

Mirror Manus's two-function split. Keep it small:

```go
package operator

type Level int
const (
    Info Level = iota
    Progress
    Warning
)

type Operator interface {
    // Non-blocking: progress, status, completion. Never halts.
    Notify(level Level, format string, args ...any)

    // Blocking: must receive a response.
    Ask(prompt string, options []string) (choice string, err error)
    Confirm(prompt string) (bool, error)

    // Structured: escalate when soft-pass or cap hit, allow operator override.
    Escalate(ctx EscalationContext) (Decision, error)
}

type EscalationContext struct {
    Reason   string   // "repair-cap-hit" | "ac-soft-pass" | "intent-ambiguous"
    TaskID   string
    Evidence []string
    Options  []string // e.g. ["retry", "skip", "manual-fix", "abort"]
}

type Decision struct {
    Choice string
    Note   string
}
```

### Implementations

1. **terminal** (`operator/term`): prompts on stdin/tty with `huh` or raw readline. `Notify` writes to stderr with a timestamped prefix. `Ask`/`Confirm` block the calling goroutine.
2. **ndjson** (`operator/ndjson`): emits `{"kind":"operator.notify"|"operator.ask", ...}` on stdout; blocks `Ask`/`Confirm` on a reply channel fed by `{"kind":"operator.reply", "ask_id":...}` on stdin. This is the CloudSwarm shape — same wire protocol as existing `stream/` NDJSON events.

Both satisfy `Operator`. Injected into mission runner via `app.Orchestrator.Operator`. Unit tests swap in a fake.

### Integration with descent soft-pass

The descent engine (commit 8611d48, `STOKE_DESCENT=1`) currently auto-soft-passes after 2× `ac_bug` verdicts (commit dccf6dd). Rewire:

- `auto` mode (default CI, `stoke ship`): current behavior — soft-pass automatically, Notify.
- `interactive` mode (`stoke chat`, `stoke plan --interactive`): soft-pass triggers `Operator.Escalate`. Options: `["accept-softpass", "retry-harder", "abort"]`. Operator choice writes to bus.
- `strict` mode: soft-pass Asks; never auto-grants.

Config: `descent.soft_pass_policy ∈ {auto, interactive, strict}`.

---

## 7. Stoke Intent Gate

Mirror Droid Phase 0 exactly, run before every worker dispatch (not every LLM turn — per-dispatch is the stoke unit of work).

### Placement
`scheduler/` dispatches tasks. Add an intent-gate hook in `scheduler.Dispatch` before `harness.Spawn`. Reuse existing `intent/` package (currently does intent classification and verbalization).

### Classifier
Two-stage:
1. **Deterministic**: action-verb scan on the task title + SOW excerpt. Verbs `{add, create, implement, fix, refactor, delete, rename, migrate, port}` → IMPLEMENT. Verbs `{explain, analyze, audit, investigate, review, diagnose, why}` → DIAGNOSE. Ambiguous → stage 2.
2. **LLM judgment**: use `model.TaskTypeClassification` with a tight prompt: "Does this task require file edits? IMPLEMENT/DIAGNOSE/AMBIGUOUS." AMBIGUOUS → `Operator.Ask`.

### Tool authorization (extend `harness/tools`)
- **IMPLEMENT**: full tool set — edit, shell, worktree, git, PR. Standard stance.
- **DIAGNOSE**: read-only bundle — `read_file`, `grep`, `glob`, `shell(readonly)`, `bash(readonly commands only)`. No `edit`, no `write`, no `git commit`, no `pnpm install`. Block at harness tool-auth layer, not in prompt.
- Output of DIAGNOSE is a markdown report written to `reports/` (not the source tree), and the ledger node kind is `diagnostic_report`, not `code_change`.

### Re-evaluation
Re-run the gate when an upstream task's output mutates the current task's SOW excerpt. The bus emits `task.sow.updated`; scheduler re-classifies before dispatch.

---

## 8. Per-file repair cap

Adopt Cursor's rule verbatim: **3 fix attempts per file per session, then escalate**. Stoke has richer state than Cursor so the cap lives in `taskstate`.

### Counter location
`taskstate.TaskState` gains:

```go
type TaskState struct {
    // ... existing fields
    RepairCounts map[string]int // file path → consecutive lint-fix attempts
}
```

Increment when `verify/` reports lint/type-check failure localized to a file AND the next worker dispatch targets the same file with intent `repair-lint`. Reset when a fix produces a clean build for that file.

### At cap (attempt == 3)
Do NOT auto-retry. Sequence:
1. `verify/` emits `repair.cap.hit{file, attempts:3, last_errors:[...]}`
2. `failure.Classify` assigns class `RepairCapExceeded` (new entry in `failure/` 10-class taxonomy).
3. `scheduler` consults `descent.soft_pass_policy`:
   - `auto` + CI: mark task `failed`, fall through to next strategy (different stance, e.g. escalate to Codex via `model.CrossModelReviewer`, or open a TODO comment + skip).
   - `interactive`: `Operator.Escalate` with `Reason:"repair-cap-hit"`, `Options:["reassign-codex","manual-fix","skip-file","abort"]`.
   - `strict`: always `Operator.Ask`.
4. Whatever the choice, write it to the bus as `repair.cap.resolution{decision:..., actor:...}` for audit.

### Why not session-global
Per-file matches Cursor's wording and matters because a single task may touch many files; a global counter would be too coarse. Pairs with `atomicfs/` multi-file transactions: the cap is tested per path before commit.

### Config
```yaml
repair:
  per_file_cap: 3
  on_cap: interactive   # auto | interactive | strict
  fallback_strategy: reassign-codex
```

---

## Open questions

1. Should `stoke plan` support `--dry-run-execute` (run scheduler up to worker-spawn without actual LLM calls)? Useful for CI plan validation.
2. Devin's 30s auto-proceed: adopt or drop? Recommend drop — deterministic `--yes` is cleaner for CI; interactive users should explicitly approve.
3. `<think>` block equivalent: stoke's `intent/verbalize` gate already forces reasoning. Confirm it fires at same transitions (mode change, completion claim).
4. Should intent gate AMBIGUOUS block (Droid style) or proceed with DIAGNOSE (safer default)? Recommend proceed-as-DIAGNOSE — maximizes autonomy, read-only is safe.
5. Should per-file repair cap include formatter auto-fixes (`autofix/`)? Recommend no — count only LLM-driven repair attempts; formatter is deterministic.

---

## Sources

- [CL4R1T4S repository (elder-plinius)](https://github.com/elder-plinius/CL4R1T4S)
- [Devin system prompt leak — EliFuzz archive 2025-08-09](https://github.com/EliFuzz/awesome-system-prompts/blob/main/leaks/devin/archived/2025-08-09_prompt_system.md)
- [awesome-system-prompts Devin summary](https://elifuzz.github.io/awesome-system-prompts/devin)
- [lmstudio dirty-data/devin](https://lmstudio.ai/dirty-data/devin)
- [lmstudio dirty-data/factory-droid](https://lmstudio.ai/dirty-data/factory-droid)
- [Pliny Droid leak (X/Twitter)](https://x.com/elder_plinius/status/1972429577608986908)
- [Droid CLI system prompt gist (AshikNesin)](https://gist.github.com/AshikNesin/8c5b16f4f50734d1413bce4002223e22)
- [x1xhlol system-prompts-and-models-of-ai-tools (Cursor 2.0, Manus)](https://github.com/x1xhlol/system-prompts-and-models-of-ai-tools)
- [Cursor Agent Prompt 2.0 verbatim](https://raw.githubusercontent.com/x1xhlol/system-prompts-and-models-of-ai-tools/main/Cursor%20Prompts/Agent%20Prompt%202.0.txt)
- [Manus tools and prompts gist (jlia0)](https://gist.github.com/jlia0/db0a9695b3ca7609c9b1a08dcbf872c9)
- [Devin docs — Interactive Planning](https://docs.devin.ai/work-with-devin/interactive-planning)
- [Cursor forum — linter loop bug reports](https://forum.cursor.com/t/unrestricted-loop-of-linter-errors/36436)
