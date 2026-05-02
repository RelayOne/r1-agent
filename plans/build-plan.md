# Build Plan — cortex-core (BUILD_ORDER 1)

Spec: `specs/cortex-core.md`
Branch: `build/cortex-core`
Started: 2026-05-02

Each item from the spec checklist becomes one subagent dispatch + one commit (`feat(TASK-N): description`). Items are dispatched in dependency batches for parallelism.

## Dependency batches

**B1 — foundation (parallel):** 1, 2, 10
**B2 — Workspace primitives (depends on 1, 2):** 3
**B3 — Workspace ops (depends on 3):** 4, 5, 6, 7 (parallel)
**B4 — Lobe primitives (depends on 1, 3):** 8
**B5 — LobeRunner (depends on 8, 20):** 9
**B6 — Round + Workspace bridge (depends on 10, 3):** 11
**B7 — Cortex bundle (depends on 1, 3, 9, 10, 17, 19, 20):** 12
**B8 — lifecycle (depends on 12, 19):** 13, 14, 15 (parallel after 12 lands)
**B9 — agentloop wiring (depends on 12-15):** 16
**B10 — Router (mostly independent):** 17
**B11 — Interrupt (mostly independent):** 18
**B12 — Pre-warm pump (mostly independent):** 19
**B13 — Budget (depends on 9):** 20, 21 (parallel)
**B14 — Persistence (depends on 3, 4):** 22
**B15 — Replay (depends on 22):** 23
**B16 — Token accounting (depends on 13, 21):** 24
**B17 — Integration (depends on 9, 12, 13, 14, 15):** 25
**B18 — Bench + CI (depends on 25):** 26, 27 (parallel)
**B19 — flag plumbing + docs (independent):** 28, 29 (parallel)
**B20 — package map + final review (depends on all):** 30, 31

## Items

- [x] TASK-1: Create internal/cortex/ skeleton with placeholder New (commit: 5eb67842)
- [x] TASK-2: Note + Severity + Validate (commit: cf0b2dbe)
- [x] TASK-3: Workspace struct (RWMutex, fields, NewWorkspace) (commit: d76a88a4)
- [x] TASK-4: Workspace.Publish + race test (commit: 858ef715)
- [x] TASK-5: Workspace.Snapshot/UnresolvedCritical/Drain (commit: 692d0b81)
- [x] TASK-6: Workspace.Subscribe (commit: df327f02)
- [x] TASK-7: Spotlight + maybeUpdate (commit: c4975776)
- [x] TASK-8: Lobe interface + WorkspaceReader + EchoLobe stub (commit: b0170882)
- [x] TASK-9: LobeRunner Start/Stop/panic-recover (commit: 21807b85)
- [x] TASK-10: Round struct + Open/Done/Wait/Close (commit: 119dec27)
- [x] TASK-11: Wire Round to Workspace via SetRound (commit: 8c79809b)
- [x] TASK-12: Cortex.New (validate config, build sub-systems) (commit: 784083f9)
- [x] TASK-13: Cortex.Start/Stop with pre-warm + LobeRunner.Start (commit: e2da8cbd)
- [x] TASK-14: Cortex.MidturnNote (Round.Open → Wait → Drain → format) (commit: c6273963)
- [x] TASK-15: Cortex.PreEndTurnGate (UnresolvedCritical → format) (commit: db50c976)
- [x] TASK-16: agentloop.Config CortexHook interface + composition (commit: 805f2f37)
- [x] TASK-17: Router with 4 tools + system prompt + DecisionXxx parsing (commit: 6debe919)
- [x] TASK-18: RunTurnWithInterrupt (drop-partial pattern + 30s watchdog) (commit: 86161cb0)
- [x] TASK-19: Pre-warm pump (runPreWarmOnce + runPreWarmPump) (commit: 745d6c2f)
- [x] TASK-20: LobeSemaphore (capacity 1-8, ctx-aware) (commit: f10639c7)
- [x] TASK-21: BudgetTracker (Charge/Exceeded/ResetRound/RecordMainTurn) (commit: 4a4e86d4)
- [x] TASK-22: persist.go writeNote (durable Bus.Publish) (commit: 728ca8e7)
- [x] TASK-23: Workspace.Replay (rebuild from WAL) (commit: b65a8aa0)
- [x] TASK-24: Wire main-turn token accounting via hub.EventModelPostCall (commit: 54ac74a3)
- [x] TASK-25: cortex_integration_test.go (3 fake Lobes, full cycle) (commit: 29b21051)
- [x] TASK-26: cortex_bench_test.go (Workspace publish, Round cycle) (commit: e8c1e6b5)
- [x] TASK-27: Race-detector CI ensures internal/cortex/ included (commit: 1c83a7d4)
- [x] TASK-28: cmd/r1 --cortex flag plumbing (commit: 2c06005a)
- [x] TASK-29: internal/cortex/doc.go package-level doc (commit: 0239b437)
- [x] TASK-30: Update CLAUDE.md package map (BLOCKED: permission settings prevent edit; commit: 3c35c026)
- [x] TASK-31: Self-review cross-reference pass (this commit)

## Pre-existing skipped failures (USER-SKIPPED at gate)

- TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency in cmd/r1 — git safe.directory propagation through `t.Setenv("HOME")` isolation; not blocker to cortex work; documented and proceeding.

## Status

DONE 2026-05-02. 30 of 31 tasks complete; TASK-30 BLOCKED on CLAUDE.md permission settings (1 line of pure documentation, can be added manually).

## Commit map

- TASK-1  5eb67842 — package skeleton
- TASK-2  cf0b2dbe — Note + Severity + Validate
- TASK-3  d76a88a4 — Workspace struct
- TASK-4  858ef715 — Workspace.Publish
- TASK-5  692d0b81 — Snapshot/UnresolvedCritical/Drain
- TASK-6  df327f02 — Workspace.Subscribe
- TASK-7  c4975776 — Spotlight
- TASK-8  b0170882 — Lobe interface
- TASK-9  21807b85 — LobeRunner
- TASK-10 119dec27 — Round
- TASK-11 8c79809b — SetRound
- TASK-12 784083f9 — Cortex.New
- TASK-13 e2da8cbd — Start/Stop
- TASK-14 c6273963 — MidturnNote
- TASK-15 db50c976 — PreEndTurnGate
- TASK-16 805f2f37 — agentloop CortexHook
- TASK-17 6debe919 — Router
- TASK-18 86161cb0 — RunTurnWithInterrupt
- TASK-19 745d6c2f — pre-warm pump
- TASK-20 f10639c7 — LobeSemaphore
- TASK-21 4a4e86d4 (and 3cd3249d) — BudgetTracker
- TASK-22 728ca8e7 (and a664f065 docs) — persist writeNote
- TASK-23 b65a8aa0 — Replay
- TASK-24 54ac74a3 — token accounting via hub
- TASK-25 29b21051 — integration test
- TASK-26 e8c1e6b5 — benchmarks
- TASK-27 1c83a7d4 — race CI marker
- TASK-28 2c06005a — --cortex flag
- TASK-29 0239b437 — doc.go
- TASK-30 3c35c026 — BLOCKED (CLAUDE.md permission)
- TASK-31 (this commit) — self-review
