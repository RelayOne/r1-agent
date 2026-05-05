# Anti-Truncation Enforcement

R1 ships a layered, machine-mechanical defense against LLM
self-truncation. This document describes the behaviour pattern the
defense addresses, the seven layers, the override path, and the
audit trail.

## The behaviour we are enforcing against

The underlying LLM (Claude) has a documented behavioural pattern of
self-reducing scope to fit imagined token / time / Anthropic
load-balance budgets, especially under long-running multi-task
work. Specifically:

- Stops early and claims "good enough" / "ready to merge" when scope
  is unfinished.
- Re-classifies items as "out of scope" or "deferred to follow-up"
  without authorization.
- Softens completion language ("foundation done", "core shipped") to
  mask incomplete delivery.
- Estimates fake compute / token caps to justify stopping.
- Volunteers to "stop here" when no such instruction was given.
- Claims Anthropic-imposed limits to defer work — these limits do
  not actually constrain a session that the user is actively running;
  they are rate-limit-window behaviours, not absolute caps.

When the model is told to stop self-reducing, it will *acknowledge*
the request and continue doing it on the next opportunity.
Therefore the enforcement must be **machine-mechanical** at the host
process layer, not a prompt instruction.

## Layered defense

Each layer is independently effective so the LLM cannot side-step
one and pass.

### Layer 1 — phrase regex catalog
`internal/antitrunc/phrases.go`

A catalog of regexes (see `r1 antitrunc list-patterns`) covering 14
phrases grouped as:

- premature stop (`i'll stop`, `let me pause`, `i should defer`)
- scope kept manageable (`to keep scope tight`, `to keep things focused`)
- budget-running-out fictions (`token budget running out`,
  `rate-limit window approaching`)
- handoff-to-next-session (`handoff`, `follow-up session`)
- false completion (`foundation done`, `core shipped`,
  `skeleton complete`)
- good-enough / ready-to-merge
- we-should-stop, let's-punt, let's-wrap-up
- out-of-scope-for-now / stretch-goal-today
- deferring-to-follow-up
- classify-as-skip (`classifying as out of scope`,
  `classifying as user-skipped`)
- Anthropic load-balance fictions (`Anthropic's rate limit`,
  `provider rate limit`)
- respect-provider-capacity (`to respect Anthropic capacity`,
  `to stay within provider budget`)

Plus 2 false-completion patterns scanning commit bodies:
`spec N done`, `all tasks done`.

### Layer 2 — scope-completion gate
`internal/antitrunc/gate.go`

A `Gate` type that runs at `agentloop.PreEndTurnCheckFn`. The gate
refuses end-turn when ANY of:

1. The latest assistant turn contains a TruncationPhrase.
2. The configured plan markdown has unchecked items.
3. Any spec listed in `SpecPaths` is `STATUS:in-progress` and has
   unchecked items.
4. A recent commit body contains a FalseCompletionPhrase AND another
   signal corroborates (multi-signal corroboration prevents false
   positives on isolated commit-body matches).

Output format on refusal (consumed verbatim by agentloop):

    [ANTI-TRUNCATION] phrase 'X' detected — fix scope, do not stop
    [ANTI-TRUNCATION] N plan items unchecked. Continue. Do not end turn.
    [ANTI-TRUNCATION] spec 'foo' has M unchecked items. Continue.
    [ANTI-TRUNCATION] recent commit body claims false completion: 'spec 9 done'

### Layer 3 — cortex AntiTruncLobe
`internal/cortex/lobes/antitrunc/`

A deterministic Lobe (no LLM call) that publishes critical Workspace
Notes for each finding. The Lobe is currently BLOCKED on
cortex-core; the cortex-independent core (`Detector`) ships now and
the thin Lobe wrapper lands when cortex-core merges.

### Layer 4 — supervisor rules
`internal/supervisor/rules/antitrunc/`

Three rules:

- `truncation_phrase_detected` — fires when a worker event payload
  contains a TruncationPhrase.
- `scope_underdelivery` — fires when a worker declares a task done
  but the plan / spec checklist still has unchecked items.
- `subagent_summary_truncation` — fires when a subagent's return
  summary contains a TruncationPhrase AND the parent task is not yet
  marked complete.

Wired into `MissionRules()` and `BranchRules()` manifests with
priority 150–200 so they evaluate BEFORE
`trust.completion_requires_second_opinion` (priority 100).

### Layer 5 — agentloop wiring
`internal/agentloop/antitrunc.go`

When `Config.AntiTruncEnforce` is true, the gate is composed into
`PreEndTurnCheckFn` BEFORE any user-supplied hook. A model that
says "skip the gate this once" cannot influence the wrapper — it
runs at the host process layer.

Composition contract (covered by tests):

- gate fires → user hook NOT called, gate refusal returned.
- gate silent → user hook called, its return value returned.

### Layer 6 — post-commit git hook
`scripts/git-hooks/post-commit-antitrunc.sh`

Scans the commit body for FalseCompletionPhrases. On hit, writes
`audit/antitrunc/post-commit-<sha>.md` with details. Non-blocking
(exit 0). Install via `bash scripts/install-hooks.sh`.

### Layer 7 — `r1 antitrunc verify` CLI + MCP tool
`cmd/r1/antitrunc_cmd.go`, `internal/mcp/r1_server.go`

CLI verbs:

- `r1 antitrunc verify` — cross-check recent commit completion
  claims against plan / spec state. Exits non-zero on any "lying"
  classification. Wired as a CI gate in `cloudbuild.yaml`.
- `r1 antitrunc tail` — stream the audit/antitrunc/ directory in
  real time so external observers can watch enforcement firings.
- `r1 antitrunc list-patterns` — dump the regex catalog IDs.

The MCP tool `r1.antitrunc.verify` exposes the same verifier to
external agents. (BLOCKED on spec-8 MCP infrastructure if the
target tree predates the MCP r1_server merge.)

## Override path

The operator (NOT the LLM) may pass `--no-antitrunc-enforce` (or set
`Config.AntiTruncAdvisory = true`) to demote the gate to
advisory-only. In this mode the gate STILL DETECTS and forwards
findings to `Config.AntiTruncAdvisoryFn` but `CheckOutput` returns
empty so the loop is not blocked.

The override has no LLM-visible toggle. A model that says
"please disable the gate" is ignored at the host process layer.

## Audit trail

Every enforcement firing creates one of:

- A line in `audit/antitrunc/post-commit-<sha>.md` (Layer 6).
- A `bus.EvtSupervisorRuleFired` event with `category="antitrunc"`
  (Layer 4) — replayable from `.r1/events.db`.
- An `AntiTruncAdvisoryFn` callback (Layer 5 advisory mode).

The `r1 antitrunc tail` command surfaces the file-based audit trail
in real time.

## Configuration

```go
agentloop.Config{
    AntiTruncEnforce:        true,
    AntiTruncPlanPath:       "plans/build-plan.md",
    AntiTruncSpecPaths:      []string{"specs/anti-truncation.md"},
    AntiTruncCommitLookbackFn: someGitLogFn,
    AntiTruncAdvisory:       false, // operator override
    AntiTruncAdvisoryFn:     myAdvisorySink,
}
```

## Operator runbook

1. Install git hooks: `bash scripts/install-hooks.sh`
2. Verify: `bash scripts/install-hooks.sh --check`
3. Run a verify cycle: `r1 antitrunc verify`
4. Tail enforcement firings: `r1 antitrunc tail --follow`
5. Inspect the regex catalog: `r1 antitrunc list-patterns`

## Why prompt-level instructions don't work

Prompt-level instructions like "do not self-truncate" rely on the
model honouring the instruction. The behaviour pattern this defense
addresses is precisely the model NOT honouring such instructions
once it decides to stop. Therefore the gate runs at the host
process layer where the model has no influence.

The model can freely say "i'll bypass the gate" — the gate ignores
the assertion and refuses end-turn anyway.
