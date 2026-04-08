# 02 — Implementation Order and Dependencies

## The dependency graph

```
                            ┌─────────────────────┐
                            │  Phase 1: Skills    │
                            │  (skill registry +  │
                            │   skillselect)      │
                            └──────────┬──────────┘
                                       │
                       ┌───────────────┼───────────────┐
                       │               │               │
                       ▼               ▼               ▼
            ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐
            │  Phase 2:    │  │  Phase 3:    │  │  Phase 8:            │
            │  Wizard      │  │  Hub event   │  │  Skill library       │
            │              │  │  bus         │  │  extraction          │
            └──────┬───────┘  └──────┬───────┘  │  (parallel, indep.)  │
                   │                 │          └──────────────────────┘
                   │                 │
                   │                 ▼
                   │       ┌──────────────────────┐
                   │       │  Phase 4: Harness    │
                   │       │  independence        │
                   │       │  (tools, agentloop,  │
                   │       │   native runner)     │
                   │       └──────────┬───────────┘
                   │                  │
                   └──────────┬───────┘
                              ▼
                   ┌─────────────────────┐
                   │  Phase 5: Wisdom    │
                   │  SQLite + cleanup + │
                   │  package audit      │
                   └─────────────────────┘
```

## Sequencing rules

1. **Phase 1 must complete first.** Skills are referenced by every other phase. The skill registry rewrite + the new `skillselect` package are foundational.
2. **Phase 8 (skill library extraction) runs in parallel with everything.** It produces content; it doesn't touch Go code. Run it on a separate Claude Code session.
3. **Phase 2 (wizard) and Phase 3 (hub) are independent of each other** — they can run in parallel after Phase 1 finishes.
4. **Phase 4 (harness independence) requires Phase 3 (hub).** The native runner uses hub events for tool execution. It also benefits from Phase 1 because the agentloop's prompt construction uses the skill registry.
5. **Phase 5 is cleanup.** Run last.

## Recommended single-agent sequence (no parallelism)

If Eric runs this with one Claude Code session, do them in order:

1. Phase 1: Skills (1–2 days)
2. Phase 8: Skill library extraction (1–2 days, can happen anytime but most useful here)
3. Phase 2: Wizard (1 day)
4. Phase 3: Hub (2–3 days, this is the biggest)
5. Phase 4: Harness independence (3–5 days, this is the most complex)
6. Phase 5: Cleanup + package audit (1 day)

**Total estimated effort:** 9–14 days of focused work.

## Recommended multi-agent sequence (3 parallel sessions)

| Time | Agent A | Agent B | Agent C |
|---|---|---|---|
| Day 1–2 | Phase 1 | Phase 8 | (waiting) |
| Day 3 | Phase 2 | Phase 8 | Phase 3 |
| Day 4 | Phase 3 (continued) | Phase 8 (finishing) | Phase 3 (continued) |
| Day 5–7 | Phase 4 | Phase 4 | Phase 4 |
| Day 8 | Phase 5 | (done) | (done) |

**Total wall-clock:** ~8 days.

## Dependencies between Stoke packages and new packages

This section maps what existing packages need to be modified vs. what's new.

### New packages to create

| Package | Purpose | Phase |
|---|---|---|
| `internal/skillselect` | Repo tech stack detection + skill selection | 1 |
| `internal/wizard` | Configuration wizard | 2 |
| `internal/hub` | Unified event bus | 3 |
| `internal/tools` | Tool definitions and executor for native harness | 4 |
| `internal/agentloop` | Agentic conversation loop using Anthropic Messages API | 4 |

### Existing packages to modify

| Package | What changes | Phase | Risk |
|---|---|---|---|
| `internal/skill` | Rewrite parser to support YAML frontmatter; add `InjectPromptBudgeted`, `UpdateFromResearch`, `References` field | 1 | Low — backward compat |
| `internal/app/app.go` | Wire `skill.Registry` into `OrchestratorConfig`; add `SkillSelector` | 1 | Low |
| `internal/workflow/workflow.go` | Inject skills via `InjectPromptBudgeted` at lines ~1474, ~1552 | 1 | Medium — touches the prompt construction hot path |
| `internal/orchestrate/orchestrator.go` | Pass `skill.Registry` to workflow Engine on construction | 1 | Low |
| `internal/prompts/mission.go` | Accept optional `skillContent string` parameter in BuildMission*Prompt funcs | 1 | Low |
| `internal/config/config.go` | Add `skills:` section to policy YAML parsing | 1 | Low |
| `internal/wisdom/store.go` | Replace in-memory implementation with SQLite (interface stays same) | 5 | Low — interface preserved |
| `internal/hooks/hooks.go` | Wrap existing bash hook installation as a hub Subscriber adapter (don't delete) | 3 | Medium — must not break Claude Code mode |
| `internal/lifecycle/hooks.go` | Adapt as a thin wrapper over hub (don't delete) | 3 | Low — currently dead code |
| `internal/engine/types.go` | Add `NativeRunner` field to `Registry` struct | 4 | Low |
| `internal/engine/native_runner.go` | NEW — implements CommandRunner using agentloop | 4 | Medium |
| `internal/provider/anthropic.go` | Add tool_use support to existing `Chat`/`ChatStream` | 4 | Medium |
| `internal/costtrack/` | Subscribe to hub events for `model.post_call` to track per-call costs | 3 | Low |
| `internal/scan/` | Subscribe to hub events for `tool.post_use` to scan file writes | 3 | Low |
| `cmd/stoke/main.go` | Add `wizard` command, `audit` command for package audit | 2, 5 | Low |

### Files to create at repo root

| File | Purpose | Phase |
|---|---|---|
| `STOKE-IMPL-NOTES.md` | Running log of decisions, blockers, questions for Eric | continuous |
| `PACKAGE-AUDIT.md` | Tag every package as CORE/HELPFUL/DEPRECATED | 5 |
| `docs/architecture/skill-pipeline.md` | Explain how skills flow through the prompt | 1 |
| `docs/architecture/hub.md` | Explain the event bus | 3 |
| `docs/architecture/agentloop.md` | Explain the native agentic loop | 4 |
| `docs/architecture/wizard.md` | Explain the wizard | 2 |

## Validation gates between phases

Each phase has a validation gate that MUST pass before moving to the next phase. These are documented in detail in `09-validation-gates.md`. The summary:

| After phase | Must pass |
|---|---|
| 1 | Skills are visible in plan/execute prompts when running an existing test mission. `go test ./...` passes. Skills can be added via `stoke skill add`. |
| 2 | `stoke wizard --auto` on a sample repo produces a complete `.stoke/config.yaml` and copies relevant skill files. `stoke wizard` (interactive) works end-to-end. |
| 3 | Existing bash hooks still fire correctly via the hub adapter. New in-process hooks fire on PreToolUse. Audit log records every event. `go test ./internal/hub/...` passes with >70% coverage. |
| 4 | Native runner can execute a simple task (e.g., "create a hello.go file that prints hello world") end-to-end without invoking Claude Code CLI. Cost tracking matches actual API spend. Tool execution is sandboxed. |
| 5 | `stoke audit` produces `PACKAGE-AUDIT.md` with all 103 packages tagged. Wisdom learnings persist across `stoke` invocations. All other tests still pass. |

## How to track progress

Create `STOKE-IMPL-NOTES.md` at the repo root on day 1. After every meaningful change, append an entry:

```markdown
## 2026-04-XX — Phase 1, skill registry rewrite

- Added YAML frontmatter parsing to `parseSkill()`
- Added `internal/skillselect` package with `DetectProfile`
- Wired `SkillRegistry` into `OrchestratorConfig`
- Open question: should skills be hot-reloaded on file change? Filewatcher exists but not used. **Default: no, requires explicit `stoke skill reload`.**
- Tests passing: `go test ./internal/skill/... ./internal/skillselect/... ./internal/app/...`
- Build clean: `go build ./cmd/stoke`
```

## Now go read the phase you're starting with.

If you're doing Phase 1 first (the recommended starting point), go to `03-phase1-skills.md`.

If you're working in parallel and starting on the skill library, go to `08-skill-library-extraction.md`.
