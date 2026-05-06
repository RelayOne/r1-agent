<!-- STATUS: ready -->
<!-- CREATED: 2026-05-05 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 34 -->

# Skill-aware compactor + scope manager (production callers for skilltracker)

## 1. Overview

PR #166 shipped `internal/skilltracker.Tracker` with three unload entry points: `Drop`, `CloseScope`, `EvictByCompactor`. PR #167 wired the load side: every successful `SkillInjector.handle` calls `Tracker.NoteLoadInfo`. What's still missing is the **production callers** that drop skills back out of the tracker:

- A skill-aware compactor that detects which skills get evicted under budget pressure and calls `Tracker.EvictByCompactor`
- A scope manager that calls `Tracker.CloseScope` when a task DAG closes (workflow phase exit, mission convergence, stance terminate)

Without these callers the tracker grows unbounded — every load notes; nothing ever drops. The v2 dashboard's side panel renders skill-loaded events but never the matching skill-unloaded counterparts.

This spec ships the two callers as the smallest possible additions on top of the existing `internal/microcompact` and `internal/workflow` surfaces.

## 2. Stack & Versions

- Existing `internal/skilltracker.Tracker` (PR #166)
- Existing `internal/hub/builtin.SkillInjector.Tracker` field (PR #167)
- New surface: a thin `concern.SkillCompactor` + `workflow.SkillScopeCloser` that wrap Tracker calls into the hot paths

## 3. Architecture

```
                            ┌──────────────────────┐
                            │ skilltracker.Tracker │
                            │  Note* (PR #167)     │
                            │  EvictByCompactor    │
                            │  CloseScope          │
                            └──────┬───────────────┘
                                   │
            ┌──────────────────────┼─────────────────────┐
            │                                            │
            ▼                                            ▼
  internal/concern/                          internal/workflow/
  skill_compactor.go                         skill_scope_closer.go
    NEW: tracks per-(stance,                   NEW: hooks the workflow
    skill) token cost; on                      phase-exit + mission-
    budget overflow, decides                   convergence callbacks
    evictions and calls                        and emits CloseScope
    Tracker.EvictByCompactor                   for every (stance, scope)
                                               that the just-closed task
                                               owned.
```

## 4. Compactor design

A skill-aware compactor is a layer ABOVE `internal/microcompact`. microcompact works on label-keyed text Sections — it doesn't know that `Section{Label:"skills"}` came from N distinct loaded skills. The new code:

1. **At load time** the compactor records `(stance, skill) → tokens` from `Tracker.Snapshot()` so it knows the per-skill cost.
2. **On budget pressure** (when `microcompact.Compact()` is about to drop or summarise the skills section), the compactor decides which skills to drop. Default policy: LRU by `LoadInfo.LoadedAt` (oldest loaded first); budget-tight = drop more.
3. The decided list is passed to `Tracker.EvictByCompactor` which emits the `SkillUnloaded` ledger nodes with `Reason="compactor_evicted"` + `BudgetTokensFreed` per skill.

## 5. Scope-closer design

The workflow phase machine already emits transitions via `internal/workflow`. The new code:

1. Subscribes to phase-exit events.
2. For each phase that owned skill loads (`TaskDAGScope` matched the closing scope), calls `Tracker.CloseScope(ctx, stanceID, taskScope)`.
3. The tracker's `CloseScope` internally drops every skill in that scope and emits `SkillUnloaded` with `Reason="scope_exit"`.

Idempotency: the same scope can be closed twice (manually + by phase-exit auto-fire). `Tracker.CloseScope` already handles this — second call returns `(0, nil)`.

## 6. Boundaries

- **No new ledger node types.** Reuses `nodes.SkillUnloaded` already shipped.
- **No microcompact rewrite.** This spec layers on top; microcompact stays section-level.
- **No new bus event types.** The audit trail goes through ledger nodes only.
- **Default policy: LRU.** The eviction policy can be made pluggable in a future spec; this one ships LRU.
- **No retroactive emission.** Tracker entries that pre-date this spec's deployment don't get synthetic CloseScope events on first boot.

## 7. Implementation checklist (8 items — self-contained)

### concern.SkillCompactor

- [ ] T1 — Write `internal/concern/skill_compactor.go` with `type SkillCompactor struct { Tracker *skilltracker.Tracker; budget int }`. Constructor `NewSkillCompactor(t, budget)`. Method `EvictForBudget(ctx, stanceID, currentTokens) (evicted []skilltracker.EvictionRequest, err)`: reads `Tracker.Snapshot()[stanceID]`, sorts by `LoadInfo.LoadedAt` ascending (oldest first), drops oldest until `currentTokens - SUM(dropped.Tokens) ≤ budget`, calls `Tracker.EvictByCompactor(ctx, stanceID, dropped)`, returns the list. If `Tracker == nil`, return nil + nil (tracker-disabled mode). Includes 6 unit tests in `skill_compactor_test.go` covering: empty tracker, single-skill drop, no-drop-needed, exhausted-eviction (still over budget), zero-budget, nil-tracker.
- [ ] T2 — Wire `SkillCompactor` into the existing `internal/microcompact` budget path. Find the call site where the skills section is being shrunk (grep `Section{Label:"skills"}` or equivalent — the section the SkillInjector populates). Add a hook: when shrinking the skills section, look up the compactor, call `EvictForBudget` first; the returned eviction list tells the section-shrinker which skills to drop. Wire-tests in `microcompact_skill_test.go`: 1) compactor=nil → behaves like today (no eviction emission); 2) 3 loaded skills + budget cut → compactor produces 2 evictions + section shrinks correspondingly.
- [ ] T3 — Add the wiring in `internal/app` (or wherever the daemon constructs its components — grep for `skilltracker.New`): when constructing the SkillInjector with a non-nil Tracker, also construct a SkillCompactor with the same Tracker. Pass it to whatever holds microcompact's eviction hook. Test in `app_skill_compactor_test.go`: boot the app, exercise a budget overflow, assert a SkillUnloaded ledger node appears with `Reason="compactor_evicted"` + `LoadRef` matches the original SkillLoaded.

### workflow.SkillScopeCloser

- [ ] T4 — Write `internal/workflow/skill_scope_closer.go` with `type SkillScopeCloser struct { Tracker *skilltracker.Tracker }`. Method `OnPhaseExit(ctx, stanceID, taskScope) error` calls `Tracker.CloseScope`. If `Tracker == nil` or `taskScope == ""`, returns nil immediately. Tests in `skill_scope_closer_test.go`: 4 cases — happy path drops all matching, scope-mismatch is no-op, idempotent re-close returns 0, nil-tracker is no-op.
- [ ] T5 — Hook the closer into the workflow phase machine. Find the phase-exit callback (grep `PhaseExited` or `OnPhaseDone` — exists in `internal/workflow/phase_*.go` based on the existing phase handlers Spec 1-9 ships). Add the call: after the phase finishes, if `closer != nil`, `closer.OnPhaseExit(ctx, phase.StanceID, phase.TaskID)`. Test asserts: completing a phase that loaded 2 skills emits 2 `SkillUnloaded(scope_exit)` events.

### Integration

- [ ] T6 — Wire both into `internal/app` next to the SkillInjector + Tracker construction (T3's site). Test in `app_skill_lifecycle_test.go`: full load → workflow phase → exit cycle, verify 1 SkillLoaded + 1 SkillUnloaded(scope_exit) per skill.
- [ ] T7 — Document the new wiring in `cmd/r1-server/README.md` (after Spec 4's table) under a new "Skill lifecycle" section: lifecycle diagram (load → SkillLoaded → use → unload triggers (compactor / scope-exit / explicit) → SkillUnloaded), pointer to skilltracker package, pointer to the v2 dashboard's side-panel rendering.

### Acceptance

- [ ] T8 — Acceptance test in `cmd/r1-server/integration_test.go`: boot a fixture session, load 2 skills via SkillInjector, force a budget compaction, observe 1 compactor_evicted SkillUnloaded ledger node; trigger a phase exit, observe 1 scope_exit SkillUnloaded ledger node; render the side panel for one of the skills, assert the rendered HTML contains both load + unload events with the right LoadRef pairing.

## 8. Acceptance

- `go build ./... && go test ./internal/concern/... ./internal/workflow/... ./internal/skilltracker/... ./internal/hub/builtin/... ./cmd/r1-server/...` clean.
- The v2 dashboard side panel for a skill that was loaded + compactor-evicted shows both events in chronological order (full audit trail per RT-REDACTION-UI-PATTERNS-style "specific cause" copy from `HumanSkillReason`).
