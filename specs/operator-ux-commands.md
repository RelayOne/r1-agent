<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: operator-ux-memory Parts B + G (shipped), memory-full-stack (for meta-reasoner's memory writes), executor-foundation, event-log-proper -->
<!-- BUILD_ORDER: 21 -->

# Operator UX — Commands, Panes, Meta-Reasoner, Progress

## Overview

This spec lands the still-unshipped operator surfaces from
`specs/operator-ux-memory.md` — namely Parts **A**, **C**, **E**, and **F**.
Part B (HITL Operator interface, `internal/hitl/`) and Part G (cost events via
`stoke.cost` emissions) already shipped in tier1; Part D (the full persistent
memory stack — embeddings, consolidation, `stoke memory` CLI) is scoped in a
separate companion spec, `specs/memory-full-stack.md`, and this document does
**not** re-scope it. This spec consumes Part B's Operator interface, Part G's
cost snapshots, and the memory-full-stack API for the meta-reasoner's rule
writes.

Together these four parts turn Stoke from "a single CLI that prints and
exits" into a real operator-facing agent platform: Part A gives operators a
structured planning command (`stoke plan`) distinct from `stoke ship`; Part C
upgrades the TUI's execute-phase panes from the current flat linear progress
renderer to a nested per-session / per-task / per-AC tree with keyboard
navigation; Part E adds a live inter-session meta-reasoner that runs
**between** sessions to consolidate failure lessons into the next session's
worker prompts; Part F writes a human-readable `progress.md` to the repo
root at session/task/AC boundaries for `tail -f` and README-adjacent
visibility. An optional Intent Gate (closely related to Part A — it is the
DIAGNOSE/IMPLEMENT classifier that separates "plan + diagnose" from
"execute") is included as a checklist item because, although its code lives
in `internal/router/`, its user-facing surface is operator-UX.

## Stack & Versions

- Go 1.22+
- Bubble Tea (`github.com/charmbracelet/bubbletea`) — existing dependency in
  `internal/tui/`
- `teatest` (`github.com/charmbracelet/x/exp/teatest`) — snapshot/capture
  tests for Bubble Tea
- `encoding/json` stdlib (2-space indent) for `plan.json`
- `crypto/sha256` for SOW hashing
- `github.com/oklog/ulid/v2` for plan_id / event_id — already in go.mod
- `internal/atomicfs/` — for atomic tmp+rename `progress.md` writes
- `internal/costtrack/` — read-only consumer (Part G shipped)
- `internal/hitl/` — read-only consumer (Part B shipped)
- `internal/bus/` — subscriber + publisher
- `internal/plan/meta_reasoner.go` — extended, not forked (per existing
  operator-ux-memory §E.1 decision)

## Existing Patterns to Follow

- Plan/SOW shapes: `internal/plan/sow.go` (`Session`, `AcceptanceCriterion`),
  `internal/plan/plan.go` (`Task`).
- Event subtypes + emitter: `internal/tui/progress.go` already defines the
  canonical `hub.EventType` constants (`stoke.plan.ready`,
  `stoke.session.start`, `stoke.task.start`, `stoke.ac.result`, etc.). Reuse
  them verbatim — do NOT invent parallel names.
- Bus pub/sub: `internal/bus/bus.go` + `internal/bus/wal.go` — durable
  WAL-backed, pattern-matched subscribers; the meta-reasoner subscribes
  here.
- Operator interface: `internal/hitl/hitl.go` (shipped tier1) — re-used by
  Part A's `--approve` path for interactive approval.
- Cost snapshots: `internal/costtrack/` — `Global.Snapshot()` provides the
  burn data the Part C cost pane renders (read-only; no changes).
- Meta-reasoner: `internal/plan/meta_reasoner.go` owns end-of-SOW
  consolidation today; Part E adds a `RunLiveMetaReasoner` entry point to
  the same package (do NOT create `internal/metareason/`).
- TUI progress renderer: `internal/tui/progress.go:1-835`. Nested panes
  extend this file's state model rather than replacing it.
- Command scaffolding: `cmd/stoke/run_cmd.go` is the canonical pattern for
  a new top-level subcommand (flags, NDJSON emitter, HITL reader wiring).

---

# PART A — `stoke plan` command

## A.1 Command surface

```
stoke plan   --sow PATH [--repo PATH] [--out PATH] [--approve] [--json]
stoke execute --plan PATH [--resume]
stoke ship   --sow PATH                  # existing, unchanged
```

- `stoke plan` runs planning + pre-flight + cost estimate to completion,
  writes `plan.json`, prints a human-readable summary (or a
  machine-parseable envelope when `--json` is set), and **exits without
  executing**.
- `stoke execute --plan plan.json` loads the approved plan, re-hashes the
  SOW file, asserts a `plan.approved` row exists in the event log for that
  plan_id, and dispatches sessions respecting the plan's DAG edges.
- `stoke ship` is unchanged — remains the CI/CD shortcut (plan →
  auto-approve → execute inline, single process).
- `--approve` inlines the approval step: prints the plan summary, prompts
  via `hitl.Ask` (TTY) or `hitl.Confirm` (non-TTY), and on affirmative
  writes `plan.approved` to bus + event log before exiting. Without
  `--approve`, the operator runs `stoke plan` → inspects plan.json →
  separately runs `stoke execute --plan` (which re-checks approval state).
- `--json` writes a single JSON envelope to stdout: `{plan_path, plan_id,
  sow_hash, approved: bool, cost_estimate, dag_summary}` — for CI tooling
  that chains planning into its own gate.

Default `--out` is `./.stoke/plan.json` — this is intentional: the `.stoke/`
directory is the Stoke-private workspace (already used for runs/, memory/).
The plan artifact is NOT in the repo root because it contains per-run
briefings (hashes, cost estimates) that shouldn't pollute `git status`.

## A.2 plan.json shape (unchanged from operator-ux-memory §A.2)

The plan.json artifact shape is defined verbatim in
`specs/operator-ux-memory.md §A.2` and is NOT re-scoped here. Part A MUST
produce a file that parses as that schema. In summary:

- Top-level fields: `plan_id`, `sow_path`, `sow_hash`, `created_at`,
  `stoke_version`, `dag`, `briefings`, `preflight`, `cost_estimate`,
  `risks`, `approval`.
- `dag.nodes[]` carry `id`, `kind`, `title`, `file_scope`, `deps`, `grpw`,
  `stance`, `intent`, `est_cost_usd`, `est_tokens`, `est_duration_s`.
- `approval` is `null` until `--approve` populates
  `{actor, ts, mode, event_id}`.

## A.3 `plan.approved` bus event

- Kind: `stoke.plan.ready` emitted on plan.json write (already defined as
  `EventStokePlanReady` in `internal/tui/progress.go`).
- A second event `bus.Event{Kind: "plan.approved", Data: {plan_id,
  sow_hash, approval_mode, actor}}` emitted on approval. Persisted to the
  event log via `internal/eventlog/` (from executor-foundation).
- `stoke execute --plan` asserts at least one `plan.approved` row whose
  `plan_id` matches the plan.json AND whose `sow_hash` equals
  `SHA256(<current SOW file>)`. Mismatch → refuse (exit 1) with message
  "SOW changed since plan — re-run `stoke plan`".

## A.4 Resumability

`stoke execute --plan plan.json --resume` replays the event log up to the
last `task.completed` or `session.completed`, then dispatches the next
unsatisfied DAG node. Pre-flight is skipped if `baseline_commit` in the
plan still matches current `HEAD`. The pattern is additive over the event
log shipped in `executor-foundation` / `event-log-proper`.

---

# PART C — Execute-phase TUI panes

## C.1 Motivation

`internal/tui/progress.go` today renders a flat linear progress log — one
line per event, collapsed by session header, no navigation. Operators
running long missions want to drill into a specific session to see its
tasks, into a task to see its ACs, and into an AC to see its descent tier
history. Manus's event-stream UX and Devin's execute-mode reference (per
RT-11) are the inspiration.

## C.2 Files

- Extend `internal/tui/progress.go` — add a pane tree model alongside the
  existing linear `state` map. Do NOT delete the linear model: it remains
  the fallback (§C.5) and the data source for the panes.
- New file: `internal/tui/panes.go`. Hosts three Bubble Tea sub-models:
  `SessionPane` (list of sessions), `TaskPane` (list of tasks inside a
  session), `ACPane` (list of ACs + descent tier history for a task).
- New file: `internal/tui/panes_test.go`. Snapshot tests via `teatest`.

## C.3 Pane model

```go
// internal/tui/panes.go

type Pane int
const (
    SessionsPane Pane = iota
    TasksPane
    ACsPane
    CostPane       // reserved — Part G's cost widget, wiring only (Part G shipped)
)

type PaneModel struct {
    focus      Pane
    sessions   SessionList      // nested lists, each carrying a cursor
    tasks      TaskList
    acs        ACList
    selected   struct{ session, task, ac string }
    width      int
    height     int
    tty        bool             // false => fall through to flat renderer
}

func NewPaneModel(r *ProgressRenderer) *PaneModel
func (m *PaneModel) Init() tea.Cmd
func (m *PaneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (m *PaneModel) View() string
```

State flows **from** the existing `ProgressRenderer` into the pane model
via a shared mutex-protected snapshot — the pane model does not subscribe
to the bus directly; it reads the renderer's `state` map on tick. This
preserves the single bus subscription and keeps failures in pane rendering
from affecting event ingestion.

## C.4 Navigation keymap

| Key            | Action |
|----------------|--------|
| `Tab` / `Shift-Tab` | Cycle focus forward / backward across panes (Sessions → Tasks → ACs → Cost → Sessions) |
| `j` / `↓`      | Move cursor down in focused pane |
| `k` / `↑`      | Move cursor up in focused pane |
| `Enter`        | Drill in: SessionsPane → TasksPane (for selected session); TasksPane → ACsPane; ACsPane → expanded descent-tier view inline |
| `Esc` / `h` / `←` | Drill out |
| `q`            | Quit TUI (same as current) |
| `r`            | Force re-render from current snapshot (for debugging) |

Focus is indicated by a coloured border on the focused pane; cursor within
a pane by a `▶` glyph.

## C.5 Fallback when not a TTY

`term.IsTerminal(int(os.Stdout.Fd())) == false` → pane model's `Init()`
returns `tea.Quit` immediately, and the existing flat-log renderer
(current `progress.go` behavior) takes over. `stoke ship --output
stream-json` is unaffected: streamjson emission is orthogonal to the pane
model and is never triggered by pane code.

## C.6 Rollout gate

Gated by `STOKE_TUI_PANES=1` env var for two weeks of real-world use
before defaulting on. Unset → current flat renderer. The Bubble Tea main
loop inspects the env var once at `tea.NewProgram` time.

---

# PART E — Live inter-session meta-reasoner

## E.1 Package placement

**Extend `internal/plan/meta_reasoner.go`** — do NOT create
`internal/metareason/`. The existing package already owns end-of-SOW
meta-reasoning, shares prompt builders, event-log parsing, and cost
accounting. Creating a parallel package duplicates wiring.

Export one new function from the existing package:

```go
// RunLiveMetaReasoner consolidates just-completed session events into
// prevention rules for subsequent sessions. Skipped when the session had
// zero failures (cheap trick, RT-08 §9), when STOKE_META_LIVE is unset,
// or when costtrack is over budget.
func RunLiveMetaReasoner(ctx context.Context, sessionID string) ([]Rule, error)
```

## E.2 Trigger

Subscribe to bus `stoke.session.end` (the existing event; reuse the
constant `EventStokeSessionEnd` from `internal/tui/progress.go`):

```go
bus.SubscribeOn("stoke.session.end", func(ev bus.Event) {
    if os.Getenv("STOKE_META_LIVE") != "1" { return }
    sid := ev.Data["session_id"].(string)

    state := taskstate.Load(sid)
    if state.FailureCount == 0 {
        log.Printf("[meta-reasoner] skipping live pass for %s (0 failures)", sid)
        return  // ~60% cost savings on clean runs
    }
    if costtrack.Global.OverBudget() {
        log.Printf("[meta-reasoner] budget exhausted, skipping live pass")
        return
    }

    rules, err := plan.RunLiveMetaReasoner(ctx, sid)
    if err != nil { return }

    for _, r := range rules {
        memoryRouter.Put(ctx, memory.Item{
            Tier:       memory.Tier("semantic"),
            Scope:      memory.ScopeRepo,
            Category:   "meta_rule",
            Content:    r.Rule,
            Metadata:   map[string]any{"tags": []string{"meta-rule", "auto"}, "source_session": sid},
            Importance: 6.0,
            Confidence: 0.6,
        })
    }
})
```

The `memoryRouter.Put` API is owned by `specs/memory-full-stack.md` — this
spec depends on it landing. Until that spec ships, Part E is guarded by
`STOKE_META_LIVE=0` default and the call becomes a no-op at build time if
the memory API symbol is absent (compile-time dep).

## E.3 Sequence

```
stoke.session.end (S1) fires
    │
    ├── STOKE_META_LIVE=1? state.FailureCount > 0? !OverBudget?  (all three → proceed)
    │
    ├── collect inputs:
    │     - S1 bus events filtered to failures, resolutions, descent tier transitions
    │     - S1 verify results (AC pass / fail / soft-pass)
    │     - S1 failure fingerprints via failure.Classify
    │
    ├── build prompt: ~8k tokens in, ~1k tokens out, Sonnet 4.6
    │     "Produce at most 5 JSON prevention rules for S2+."
    │
    ├── LLM call (~$0.04 — RT-08 §9)
    │
    ├── parse rules [{rule: str, rationale: str, applies_to_files: [glob]}]
    │
    ├── write to memoryRouter (tier=semantic, scope=repo, category=meta_rule,
    │                          confidence=0.6, importance=6)
    │
    └── S2 worker dispatch: injection point 2 (from memory-full-stack spec)
          auto-picks up the new rules via CoreAndQuery →
          `## Relevant learnings` block, capped 400 tokens
```

Cost: ~$0.04 per session transition. A 10-session SOW adds ~$0.40.

## E.4 Budget guardrails

- Hard stop via `costtrack.Global.OverBudget()`.
- Cap absolute meta-rule count per repo scope to 50; FIFO-evict by
  `last_used` when over.
- Cap injected meta-rules per worker prompt to 400 tokens (drop
  lowest-confidence first) — enforced by memory-full-stack's retrieval
  layer.

## E.5 Rollout gate

Gated by `STOKE_META_LIVE=1`. Default off because cost is nonzero. The
cheap-trick skip (zero failures → no-op) means steady-state cost on a
"clean" mission is exactly zero even when the flag is on.

---

# PART F — `progress.md` writer

## F.1 Placement

New file: `internal/plan/progress_renderer.go`.

Output path: `<repo>/progress.md` (repo root — NOT `.stoke/progress.md`).
This is the deliberate departure from operator-ux-memory's original
§F.1 path. Rationale: `progress.md` is intended for human consumption
(operator `tail -f`, GitHub PR preview rendering, README-adjacent
discoverability). Placing it in the repo root is analogous to
`README.md` / `CHANGELOG.md` and makes it discoverable without pathology.
`.stoke/runs/<run-id>/progress.md` remains available as a secondary write
target for historical archival (keyed by run_id), but the live pointer
lives at the root.

Opt-out: `STOKE_NO_PROGRESS_MD=1` disables writing. Recommended when
`progress.md` would conflict with an existing project file (rare; the
renderer never overwrites a file it did not itself create — see §F.4).

## F.2 Hook

Subscribes to the existing bus events (constants already in
`internal/tui/progress.go`):

```go
scheduler.OnProgress(func(ev ProgressEvent) {
    if os.Getenv("STOKE_NO_PROGRESS_MD") == "1" { return }
    repoRoot := gitRoot(ev.RunCtx)
    path := filepath.Join(repoRoot, "progress.md")
    if err := renderer.RenderToFile(path, ev.Plan, ev.State); err != nil {
        log.Printf("[progress.md] render failed: %v (non-fatal)", err)
    }
})
```

Fired on: `stoke.session.start`, `stoke.session.end`, `stoke.task.start`,
`stoke.task.end`, `stoke.ac.result`, `stoke.descent.tier`, `operator.ask`,
`session.failed`.

## F.3 Format

```markdown
# <SOW Title>

**Started:** 2026-04-20 15:32 UTC   **Cost:** $1.24 / $4.00

## Session S1: Descent hardening foundation

### T1 Add FileRepairCounts to DescentConfig
- [x] AC1 `go build ./...` passes
- [x] AC2 `go vet ./...` passes

### T2 Enforce cap in T4 before RepairFunc
- [x] AC1 `go build ./...` passes
- [~] AC2 unit test `TestFileCap` passes (in descent, attempt 2/3)

### T3 Emit descent.file_cap_exceeded bus event
- [ ] AC1 event visible on bus
- [ ] AC2 streamjson mirror present

## Session S2: Truthfulness contract

### T1 TRUTHFULNESS_CONTRACT prompt block
- [ ] (pending)

---
**Legend:** `[x]` done   `[~]` in-flight   `[ ]` pending   `[!]` failed   `[?]` soft-pass
```

Structural rules (deterministic, so golden-file tests are stable):

- **H1** = SOW title (plus optional parenthetical plan_id).
- **H2** = `Session <ID>: <title>` — one per session.
- **H3** = `<TaskID> <title>` — one per task inside its session's H2.
- Checkbox line per AC: `- [<state>] <ACID> <title>` with state glyph per
  legend.
- Sessions sorted by DAG topological order (deterministic).
- Within a session, tasks sorted by their DAG topological order.
- Within a task, ACs sorted by AC.ID lexical order.

## F.4 Atomic write + diff hygiene

- Write via `internal/atomicfs/` — write to
  `progress.md.tmp.<pid>.<rand>`, fsync, `os.Rename` into place. Never
  leaves a half-written file.
- **Never overwrite a pre-existing non-Stoke `progress.md`.** On first
  write in a repo, check that the file either does not exist OR contains
  the magic comment `<!-- stoke:progress.md -->` as its first line. If a
  non-Stoke file exists, log once + skip all future writes in that run.
- Emit magic comment as first line on create: `<!-- stoke:progress.md
  run=<run-id> plan=<plan-id> -->`.
- Strip trailing whitespace from every line before render.
- Collapse `\n{3,}` → `\n\n` so diffs stay minimal on event churn.

## F.5 Rollout

Ships **unflagged** — additive, opt-out via `STOKE_NO_PROGRESS_MD=1`.
Magic-comment guard (§F.4) makes it safe to add to any repo.

---

# Intent Gate (optional — Part A consumer)

Included here because its operator-facing effect is the planning/execute
split. Spec ownership could debatably live in `executor-foundation`; this
spec owns the checklist items.

## IG.1 Placement

New file: `internal/router/intent_gate.go`. Extends the router already
used by `cmd/stoke/run_cmd.go`.

## IG.2 Classifier — two stages

**Stage 1 — deterministic action-verb scan** (zero-cost):

- IMPLEMENT verbs: `{add, create, implement, fix, refactor, delete,
  rename, migrate, port, write, build, apply, edit, patch, update}`
- DIAGNOSE verbs: `{explain, analyze, audit, investigate, review,
  diagnose, why, inspect, summarize, describe, compare, evaluate}`
- Match lowercased task title + first 500 chars of SOW excerpt.
- Tie / zero-match / both-present → Stage 2.

**Stage 2 — Haiku LLM fallback** (~$0.001/call). Result cached by
`task.ID`.

- `IMPLEMENT` → full tool set.
- `DIAGNOSE` → read-only masked tools at the harness/tools auth layer.
- `AMBIGUOUS` → proceed as DIAGNOSE (safer default, per RT-11 open
  question 4). Emit `intent.ambiguous` bus event; if `--interactive`,
  Operator.Ask for reclassification.

## IG.3 Tool authorization

Extend `internal/harness/tools/` authorization model with a `ToolMask`
parameter carrying Intent. Blocked in DIAGNOSE: `edit`, `write`,
`multi_edit`, `git_commit`, `pnpm install`, `pr_create`, any `rm`/`mv`,
any redirect `>` / `>>`. Readonly bash allowlist hard-coded in
`internal/harness/tools/readonly.go`: `ls, cat, head, tail, grep, rg,
find, file, stat, wc, git status, git log, git diff, git show, go list,
go vet, go doc`.

DIAGNOSE dispatch writes output to `reports/<task-id>.md` with ledger
node kind `diagnostic_report` (not `code_change`).

## IG.4 Re-evaluation

Bus subscriber on `task.sow.updated` re-classifies and emits
`task.intent.changed{task_id, from, to}`; scheduler re-queues.

---

# Implementation Checklist

Ordered; each item: file path, function, what it does, existing file to
mirror, test to add.

### Part A — `stoke plan` command

1. [ ] **`cmd/stoke/plan_cmd.go`** — new file. `func RunPlan(ctx,
   args) error` — flags `--sow`, `--repo`, `--out`, `--approve`,
   `--json`. Mirror flag-wiring pattern from `cmd/stoke/run_cmd.go:1-80`.
   Add `plan_cmd_test.go` verifying flag parsing.
2. [ ] **`cmd/stoke/plan_cmd.go`** — `func buildPlanArtifact(ctx,
   sowPath, repoPath) (*PlanArtifact, error)`. Loads SOW via existing
   `plan.LoadSOW`, runs existing plan builder + `PreflightACCommands`.
   Test: `TestBuildPlanArtifact_FromFixture` loads a fixture SOW and
   asserts DAG non-empty + cost_estimate present.
3. [ ] **`internal/plan/artifact.go`** — new file. `type PlanArtifact
   struct` with fields matching operator-ux-memory §A.2 verbatim.
   `func (p *PlanArtifact) MarshalIndent() ([]byte, error)` — `json.Indent`
   2-space. Test: `TestPlanArtifact_RoundTrip` marshals + unmarshals.
4. [ ] **`internal/plan/artifact.go`** — `func HashSOW(path string)
   (string, error)` — `sha256` of file bytes, hex-encoded with `sha256:`
   prefix. Test: `TestHashSOW_Stable` for a known fixture.
5. [ ] **`internal/plan/artifact.go`** — `func NewPlanID(sowHash string,
   ts time.Time) string` — returns `pln_<first 8 hex of sha256(sowHash +
   ts)>`. Test: `TestNewPlanID_Deterministic`.
6. [ ] **`cmd/stoke/plan_cmd.go`** — `func writePlanAtomic(path string,
   artifact *PlanArtifact) error` — uses `internal/atomicfs/`. Test:
   mock fs failure → returns error without leaving tmp files.
7. [ ] **`cmd/stoke/plan_cmd.go`** — `func emitPlanReady(bus,
   streamjson, artifact)` — emits `stoke.plan.ready` via both the bus
   and the streamjson emitter (reusing `EventStokePlanReady` constant
   from `internal/tui/progress.go`). Test:
   `TestEmitPlanReady_BothChannels` via fake emitter.
8. [ ] **`cmd/stoke/plan_cmd.go`** — `func confirmInteractive(ctx,
   hitlReader, artifact) (approved bool, err error)` — uses
   `internal/hitl/hitl.go` Ask/Confirm. Falls through to stdin prompt
   when not a TTY. Test:
   `TestConfirmInteractive_TTY_and_NonTTY` with fake hitl.
9. [ ] **`cmd/stoke/plan_cmd.go`** — `func attachApproval(artifact,
   actor, mode) *ApprovalBlock` + rewrite plan.json. Test: approval
   round-trip via fixture.
10. [ ] **`cmd/stoke/plan_cmd.go`** — `func emitPlanApproved(bus,
    eventlog, plan_id, sow_hash, approval)` — writes `plan.approved`
    event. Test:
    `TestEmitPlanApproved_PersistsToEventLog`.
11. [ ] **`cmd/stoke/execute_cmd.go`** — extend existing execute path
    (reuses `run_cmd.go` scaffold). On `--plan PATH`, call
    `verifyApproved(plan_id, currentSowHash)` which queries event log
    for a matching `plan.approved` row and asserts hashes. Test:
    `TestExecuteVerifyApproved_MatchAndMismatch`.
12. [ ] **`cmd/stoke/execute_cmd.go`** — `--resume` path: replay event
    log up to last `task.completed` / `session.completed`, dispatch
    next DAG node. Test:
    `TestExecuteResume_SkipsCompleted`.
13. [ ] **`cmd/stoke/plan_cmd.go`** — `func renderSummary(w io.Writer,
    artifact *PlanArtifact)` — human-readable plan summary (sessions,
    task counts, cost). Test: golden-file output comparison.
14. [ ] **`cmd/stoke/plan_cmd.go`** — `--json` envelope writer: single
    line to stdout `{plan_path, plan_id, sow_hash, approved,
    cost_estimate, dag_summary}`. Test:
    `TestPlanCmd_JSONEnvelope`.
15. [ ] **`cmd/stoke/main.go`** — wire `plan` subcommand into the top
    dispatcher (same place `run` is registered).

### Part C — Execute-phase TUI panes

16. [ ] **`internal/tui/panes.go`** — new file. `type PaneModel struct`
    with `focus Pane`, `sessions SessionList`, etc. per §C.3. Test:
    `TestPaneModel_InitNotTTY_Quits` — ensures non-TTY fast-quit.
17. [ ] **`internal/tui/panes.go`** — `type SessionList struct` — wraps
    `internal/tui/progress.go`'s session snapshot with a cursor +
    selected-id. Test: `TestSessionList_CursorBounds`.
18. [ ] **`internal/tui/panes.go`** — `type TaskList struct` — tasks
    for the currently-selected session. Test:
    `TestTaskList_FollowsSessionSelection`.
19. [ ] **`internal/tui/panes.go`** — `type ACList struct` — ACs for
    current task + inline descent-tier history expander. Test:
    `TestACList_DescentTierExpand`.
20. [ ] **`internal/tui/panes.go`** — `func (m *PaneModel) Update(msg
    tea.Msg) (tea.Model, tea.Cmd)` — keymap per §C.4. Test:
    `TestPaneModel_TabCyclesFocus`, `TestPaneModel_EnterDrillsIn`,
    `TestPaneModel_EscDrillsOut`.
21. [ ] **`internal/tui/panes.go`** — `func (m *PaneModel) View()
    string` — composes pane borders; focused pane has coloured border.
    Test: `TestPaneModel_View_FocusedBorder` via `teatest.FinalOutput`.
22. [ ] **`internal/tui/progress.go`** — add `func (r
    *ProgressRenderer) Snapshot() Snapshot` — mutex-protected deep-copy
    of state for pane model consumption. Test:
    `TestProgressRenderer_Snapshot_Concurrent` with race detector.
23. [ ] **`internal/tui/panes.go`** — `func (m *PaneModel) tick() tea.Cmd` —
    1Hz tick calling `Snapshot()`, feeds new state into pane lists.
    Test: `TestPaneModel_TickUpdatesFromSnapshot`.
24. [ ] **`internal/tui/panes.go`** — `func EnvTUIPanesEnabled() bool` —
    reads `STOKE_TUI_PANES` once + memoizes. Test:
    `TestEnvTUIPanesEnabled`.
25. [ ] **`internal/tui/panes.go`** — `func NonTTYFallback(r
    *ProgressRenderer) tea.Cmd` — when not a TTY, returns `tea.Quit`
    and hands control to existing flat renderer. Test:
    `TestNonTTYFallback_InvokesFlatRenderer`.
26. [ ] **`internal/tui/panes_test.go`** — `teatest` snapshot test:
    drive a sequence of events (2 sessions, 5 tasks, 3 ACs each) and
    assert final screenshot matches golden.
27. [ ] **`cmd/stoke/main.go`** — on TUI startup, check
    `EnvTUIPanesEnabled()` → instantiate `PaneModel` or fall through
    to existing `ProgressRenderer`-only path.
28. [ ] **`internal/tui/panes.go`** — `func (m *PaneModel) handleCostPane(snap
    costtrack.Snapshot) string` — reserved placeholder; Part G's cost
    widget already renders the data, this just embeds it. Test:
    `TestCostPane_RendersSnapshot`.
29. [ ] **`internal/tui/panes.go`** — `q` / Ctrl-C handling preserves
    current exit behavior. Test: `TestPaneModel_QuitKey`.
30. [ ] **`internal/tui/panes.go`** — `func (m *PaneModel) Resize(w, h
    int)` — handles terminal resize. Test:
    `TestPaneModel_Resize_ReflowsLists`.

### Part E — Live inter-session meta-reasoner

31. [ ] **`internal/plan/meta_reasoner.go`** — add `func
    RunLiveMetaReasoner(ctx context.Context, sessionID string) ([]Rule,
    error)`. Builds prompt from bus events + verify results + failure
    fingerprints for that session. Test:
    `TestRunLiveMetaReasoner_HappyPath` with canned LLM.
32. [ ] **`internal/plan/meta_reasoner.go`** — `func
    collectLiveInputs(bus, sessionID string) LiveInputs` — filters bus
    events to failures, resolutions, descent tier transitions. Test:
    `TestCollectLiveInputs_FiltersCorrectly`.
33. [ ] **`internal/plan/meta_reasoner.go`** — `func
    buildLivePrompt(inputs LiveInputs, context []memory.Item) string` —
    ~8k token prompt, explicit "at most 5 rules" instruction.
    Test: `TestBuildLivePrompt_TokenBudget`.
34. [ ] **`internal/plan/meta_reasoner.go`** — `func parseLiveRules(out
    string) ([]Rule, error)` — strict JSON array parse with schema
    validation. Test: `TestParseLiveRules_MalformedJSON_ReturnsError`.
35. [ ] **`app/` (new subscriber)** — subscribe to `stoke.session.end`.
    Check env + failure count + budget. Test:
    `TestMetaLiveSubscriber_GuardsRespected`.
36. [ ] **`app/`** — on rule set, call `memoryRouter.Put` per rule
    (tier=semantic, scope=repo, category=meta_rule, importance=6,
    confidence=0.6, tags=[meta-rule,auto,source_session=<id>]). Test:
    `TestMetaLiveSubscriber_WritesToMemory` (fake memory router).
37. [ ] **`internal/plan/meta_reasoner.go`** — `func
    capMetaRules(router memory.Router, scope memory.Scope, maxCount
    int)` — FIFO-evict by `last_used` when over 50. Test:
    `TestCapMetaRules_EvictsOldest`.
38. [ ] **`internal/plan/meta_reasoner.go`** — `func
    EnvMetaLiveEnabled() bool`. Test: trivial env-var read.
39. [ ] **`internal/plan/meta_reasoner_test.go`** — end-to-end test:
    two-session fixture → S1 fails twice → meta-reasoner runs → S2
    worker prompt contains the generated rule. Requires fake
    memoryRouter + fake LLM.

### Part F — `progress.md` writer

40. [ ] **`internal/plan/progress_renderer.go`** — new file. `type
    ProgressRenderer struct` with `plan *Plan`, `state *State`. Test:
    `TestProgressRenderer_Construct`.
41. [ ] **`internal/plan/progress_renderer.go`** — `func (r
    *ProgressRenderer) RenderToFile(path string) error` — calls
    `Render()` + `atomicfs.WriteFile`. Test:
    `TestRenderToFile_AtomicWrite` (kills mid-write, asserts no
    partial file left).
42. [ ] **`internal/plan/progress_renderer.go`** — `func (r
    *ProgressRenderer) Render() string` — H1/H2/H3/checkbox per §F.3.
    Deterministic topological sort. Test:
    `TestRender_GoldenFixture` against
    `testdata/progress.md.golden`.
43. [ ] **`internal/plan/progress_renderer.go`** — `func iconFor(status
    string) string` — returns `[x] [~] [ ] [!] [?] [b]`. Test:
    `TestIconFor_AllStates`.
44. [ ] **`internal/plan/progress_renderer.go`** — `func
    stripTrailing(s string) string` — strips trailing whitespace per
    line. Test: `TestStripTrailing`.
45. [ ] **`internal/plan/progress_renderer.go`** — `func
    collapseBlanks(s string) string` — `\n{3,}` → `\n\n`. Test:
    `TestCollapseBlanks`.
46. [ ] **`internal/plan/progress_renderer.go`** — `func
    magicComment(runID, planID string) string` — returns `<!--
    stoke:progress.md run=... plan=... -->`. Test:
    `TestMagicComment_Format`.
47. [ ] **`internal/plan/progress_renderer.go`** — `func
    safeToWrite(path string) (bool, error)` — file DNE OR first line
    is magic comment. Test:
    `TestSafeToWrite_RespectsPreExistingFile`.
48. [ ] **`internal/plan/progress_renderer.go`** — `func
    EnvProgressDisabled() bool` — reads `STOKE_NO_PROGRESS_MD`. Test:
    trivial.
49. [ ] **`app/` subscriber wiring** — subscribe to
    `stoke.session.start/end`, `stoke.task.start/end`,
    `stoke.ac.result`, `stoke.descent.tier`, `operator.ask`,
    `session.failed`. On each, call `RenderToFile`. Test:
    `TestProgressRendererSubscriber_AllEvents`.
50. [ ] **`app/`** — compute repo root via `gitRoot(ctx)`; fall back to
    cwd when not in a git repo. Test: `TestGitRoot_FallsBackToCwd`.
51. [ ] **`internal/plan/progress_renderer_test.go`** — two-session,
    five-task fixture. Drive all boundary events. Assert final
    rendered file matches
    `testdata/progress_twoSession.golden`.
52. [ ] **`internal/plan/progress_renderer.go`** — error handling is
    **always non-fatal**: log + continue. Test:
    `TestRenderToFile_DiskFull_LogsNonFatal`.

### Intent Gate

53. [ ] **`internal/router/intent_gate.go`** — new file. `func
    ClassifyIntent(task Task, sowExcerpt string) (Intent, error)` —
    Stage 1 verb scan, Stage 2 Haiku fallback via
    `model.Resolve("intent-gate")`. Cache by `task.ID`. Test:
    `TestClassifyIntent_Verbs` + `TestClassifyIntent_HaikuFallback`.
54. [ ] **`internal/router/intent_gate.go`** — verb sets as package
    constants. Test: `TestImplementVerbs_DiagnoseVerbs`.
55. [ ] **`internal/harness/tools/readonly.go`** — new file with
    readonly bash allowlist per §IG.3. Test:
    `TestReadonlyAllowlist_AllowsExpected_BlocksWrites`.
56. [ ] **`internal/harness/tools/`** — extend auth model with
    `ToolMask{Mode Intent}`. Test:
    `TestToolMask_DIAGNOSE_BlocksWrites`.
57. [ ] **`internal/scheduler/scheduler.go`** — hook
    `router.ClassifyIntent` into `Dispatch` before `harness.Spawn`.
    Test: `TestDispatch_CallsIntentGate`.
58. [ ] **`internal/scheduler/scheduler.go`** — DIAGNOSE dispatches
    write output to `reports/<task-id>.md`, ledger kind
    `diagnostic_report`. Test:
    `TestDispatchDiagnose_WritesReportOnly`.
59. [ ] **`internal/router/intent_gate.go`** — subscribe bus
    `task.sow.updated` → re-classify + `scheduler.Requeue`. Emit
    `task.intent.changed`. Test:
    `TestTaskSowUpdated_TriggersReclassify`.
60. [ ] **`cmd/stoke/plan_cmd.go`** — on each DAG node build,
    populate `node.intent` from `ClassifyIntent`. Test:
    `TestBuildPlan_PopulatesIntent`.

### Cross-cutting

61. [ ] **Integration test**: `stoke plan --sow /tmp/fixture.md
    --approve` → assert `plan.json` written + `plan.ready` emitted +
    `plan.approved` persisted. Part A end-to-end.
62. [ ] **Integration test**: `STOKE_TUI_PANES=1 stoke execute --plan
    /tmp/plan.json` → via `teatest`, drive tab/enter/esc sequences +
    snapshot the final frame.
63. [ ] **Integration test**: full mission with 2 sessions, S1 fails
    2×. Assert S2 worker prompt contains the generated meta-rule.
    Requires `STOKE_META_LIVE=1` + fake LLM. Part E end-to-end.
64. [ ] **Integration test**: same mission, assert `progress.md`
    exists at repo root, matches golden, contains magic comment on
    line 1. Part F end-to-end.
65. [ ] **`go build ./cmd/stoke && go test ./... && go vet ./...`** —
    all green before marking spec done.

---

# Acceptance Criteria

- WHEN `stoke plan --sow /tmp/fixture.md --out /tmp/plan.json` runs THE
  SYSTEM SHALL write a parseable `plan.json` with a non-empty `dag.nodes`
  and a non-null `cost_estimate`, emit `stoke.plan.ready` on both the bus
  and the streamjson channel, and exit 0.
- WHEN `stoke plan --approve` runs AND the operator confirms THE SYSTEM
  SHALL populate `plan.json`'s `approval` block with `{actor, ts, mode,
  event_id}` AND persist a `plan.approved` row in the event log with
  matching `plan_id` and `sow_hash`.
- WHEN the SOW file changes between `stoke plan` and `stoke execute
  --plan` THE SYSTEM SHALL refuse execution with exit 1 and message "SOW
  changed since plan — re-run `stoke plan`".
- WHEN `STOKE_TUI_PANES=1` is set AND stdout is a TTY THE SYSTEM SHALL
  render the nested pane model; Tab cycles focus; j/k navigates;
  Enter/Esc drill in/out. Otherwise the existing flat renderer remains
  in effect.
- WHEN a session ends with zero failures AND `STOKE_META_LIVE=1` THE
  SYSTEM SHALL NOT invoke the meta-reasoner LLM (zero cost on clean
  runs).
- WHEN a session ends with ≥1 failure AND `STOKE_META_LIVE=1` AND
  !OverBudget THE SYSTEM SHALL invoke the meta-reasoner, parse its
  rules, and write each as a `memory.Item{tier=semantic, scope=repo,
  category=meta_rule}` via `memoryRouter.Put`.
- WHEN a mission runs THE SYSTEM SHALL write `<repo>/progress.md`
  atomically on every session/task/AC boundary event, with H1/H2/H3
  structure and checkbox lines exactly per §F.3, topologically sorted.
- WHEN `<repo>/progress.md` exists AND its first line is NOT the magic
  comment THE SYSTEM SHALL skip all writes in that run and log once.
- WHEN `STOKE_NO_PROGRESS_MD=1` is set THE SYSTEM SHALL not write
  `progress.md`.
- WHEN the intent gate classifies a task as DIAGNOSE THE SYSTEM SHALL
  reject any `edit` / `write` / `git_commit` tool call at the harness
  auth layer and return a structured error.

### Bash AC commands

```bash
./stoke plan --help | grep -q 'sow'
./stoke plan --sow /tmp/fixture.md --out /tmp/plan.json && jq -e '.dag.nodes | length > 0' /tmp/plan.json
./stoke plan --sow /tmp/fixture.md --out /tmp/plan.json --json | jq -e '.plan_id'
./stoke execute --plan /tmp/plan.json --help | grep -q 'resume'
STOKE_TUI_PANES=1 ./stoke execute --plan /tmp/plan.json &   # teatest asserts frame
STOKE_META_LIVE=1 go test ./internal/plan/... -run TestRunLiveMetaReasoner
go test ./internal/plan/... -run TestProgressRenderer
go test ./internal/tui/... -run TestPaneModel
go test ./internal/router/... -run TestClassifyIntent
go test ./internal/harness/tools/... -run TestToolMask_DIAGNOSE
go build ./cmd/stoke
go vet ./...
```

---

# Testing

## Part A unit tests

- `TestPlanArtifact_RoundTrip` — marshal + unmarshal preserves every
  field.
- `TestHashSOW_Stable` — deterministic over identical bytes.
- `TestNewPlanID_Deterministic` — same inputs → same ID.
- `TestBuildPlanArtifact_FromFixture` — real SOW fixture →
  non-empty DAG.
- `TestEmitPlanReady_BothChannels` — fake bus + fake emitter.
- `TestConfirmInteractive_TTY_and_NonTTY` — fake hitl reader.
- `TestEmitPlanApproved_PersistsToEventLog` — fake event log.
- `TestExecuteVerifyApproved_MatchAndMismatch` — happy + SOW-changed.
- `TestExecuteResume_SkipsCompleted` — fake event log with mid-run
  completed rows.
- `TestPlanCmd_JSONEnvelope` — `--json` output shape.

## Part C TUI tests

- `TestPaneModel_InitNotTTY_Quits`.
- `TestSessionList_CursorBounds`, `TestTaskList_FollowsSessionSelection`,
  `TestACList_DescentTierExpand`.
- `TestPaneModel_TabCyclesFocus`, `TestPaneModel_EnterDrillsIn`,
  `TestPaneModel_EscDrillsOut`, `TestPaneModel_QuitKey`,
  `TestPaneModel_Resize_ReflowsLists`.
- `TestPaneModel_View_FocusedBorder` via `teatest.FinalOutput`.
- `TestProgressRenderer_Snapshot_Concurrent` — run with `-race`.
- `TestPaneModel_FullSnapshot` — teatest drives events, asserts
  `testdata/panes_final.golden`.

## Part E meta-reasoner tests

- `TestRunLiveMetaReasoner_HappyPath`.
- `TestCollectLiveInputs_FiltersCorrectly`.
- `TestBuildLivePrompt_TokenBudget`.
- `TestParseLiveRules_MalformedJSON_ReturnsError`.
- `TestMetaLiveSubscriber_GuardsRespected` — env off, failure==0,
  overbudget → no-op.
- `TestMetaLiveSubscriber_WritesToMemory` — fake memory router.
- `TestCapMetaRules_EvictsOldest`.
- Integration: `TestMetaReasoner_EndToEnd_TwoSessions` — S2 prompt
  contains the S1-derived rule.

## Part F progress.md tests

- `TestRenderToFile_AtomicWrite`.
- `TestRender_GoldenFixture` — against
  `testdata/progress.md.golden`.
- `TestIconFor_AllStates`, `TestStripTrailing`,
  `TestCollapseBlanks`, `TestMagicComment_Format`.
- `TestSafeToWrite_RespectsPreExistingFile`.
- `TestProgressRendererSubscriber_AllEvents`.
- `TestRenderToFile_DiskFull_LogsNonFatal`.
- Integration: `TestProgressRenderer_TwoSessionFixture` against
  `testdata/progress_twoSession.golden`.

## Intent Gate tests

- `TestClassifyIntent_Verbs`, `TestClassifyIntent_HaikuFallback`.
- `TestReadonlyAllowlist_AllowsExpected_BlocksWrites`.
- `TestToolMask_DIAGNOSE_BlocksWrites`.
- `TestDispatch_CallsIntentGate`,
  `TestDispatchDiagnose_WritesReportOnly`.
- `TestTaskSowUpdated_TriggersReclassify`.
- `TestBuildPlan_PopulatesIntent`.

---

# Rollout

| Part | Flag | Default | Graduation |
|------|------|---------|------------|
| Part A (`stoke plan`) | — | on | ships unflagged — additive |
| Part C (TUI panes) | `STOKE_TUI_PANES=1` | off | default-on after 2 weeks of real-world use confirms navigation UX |
| Part E (live meta-reasoner) | `STOKE_META_LIVE=1` | off | default stays off indefinitely (cost concerns, RT-08 §9) |
| Part F (`progress.md`) | opt-out via `STOKE_NO_PROGRESS_MD=1` | on | ships unflagged — magic-comment guard makes it safe |
| Intent Gate | — | on | ships unflagged; DIAGNOSE mode is safer default |

Part A + Part F are additive and ship immediately. Part C is gated two
weeks for UX tuning. Part E stays env-gated because its steady-state
cost is nonzero even with the zero-failure skip (it burns once per
failing session). Intent Gate ships on because DIAGNOSE (safer default)
preserves autonomy while preventing accidental writes on ambiguous
tasks.

---

# Boundaries — What NOT To Do

- Do NOT re-scope Part D (memory stack) — that is owned by
  `specs/memory-full-stack.md`.
- Do NOT re-design the HITL Operator interface — Part B shipped in
  tier1 (`internal/hitl/hitl.go`). This spec consumes it.
- Do NOT re-scope cost tracking — Part G shipped. Part C's Cost Pane
  renders an existing `costtrack.Global.Snapshot()`; no new data
  sources.
- Do NOT create `internal/metareason/` — extend
  `internal/plan/meta_reasoner.go` (preserves the decision from
  operator-ux-memory §E.1).
- Do NOT delete the flat `ProgressRenderer` — Part C's pane model is
  a consumer of its snapshot and the flat renderer remains the non-TTY
  fallback.
- Do NOT overwrite a pre-existing non-Stoke `progress.md` — the
  magic-comment guard is load-bearing for adoption safety.
- Do NOT break `stoke ship` — existing end-to-end path unchanged.
- Do NOT commit `.stoke/plan.json` or `<repo>/progress.md` to git in
  Stoke's own repo (add a README note; do NOT auto-edit user repos'
  `.gitignore`).
