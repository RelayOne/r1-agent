# HANDOFF — Post-merge audit cleanup (in progress)

**Filed:** 2026-05-09 · **Last updated:** this session
**Branch:** `dev` (live integration); cascade `dev → staging → main` in progress.
**Spec in flight:** `specs/post-merge-audit-cleanup.md` (10 tasks; 9 dispatched).

---

## 2026-05-09 — This session

### Closed audit items (PRs merged to dev)

| PR | Audit ref | Headline |
|---|---|---|
| #211 | scan-test-quality | drop 3 sleep barriers in TestDashboardStateBridge |
| #212 | scan-go-stubs | r1skill stages 2 (type-check) + 6 (cycle detection) |
| #213 | scan-rust-stubs | desktop wire apply_menu, lane_channel, folder_picker, transport reconnect |
| #214 | scan-test-quality | drop cargo-cult sleeps from lane_lifecycle no-op tests |
| #216 | scan-test-quality | bump async-subscriber poll deadlines |
| #217 | scan-go-stubs #11 | saga `SettleCompleteThenRevoke` waits for real step boundary |
| #218 | scan-go-stubs #7 | sharedmem `SnapshotValue` at write time → `Rollback` works without `ReplayValue` |
| #219 | scan-go-stubs (cost.go) | bridge cost emit errors observable (`EmitErrorCount`, `LastEmitError`, `OnEmitError`) |
| #220 | scan-go-stubs (sessionctl) | `overrideHandler` actually applies AC override when `OverrideAC` callback wired |
| #221 | scan-go-stubs (topology) | `SupervisorWorker` honors `SupervisorPlan` for worker selection + ordering |
| #222 | scan-go-stubs (verify_lint_wiring) | `r1.verify.lint` MCP handler wired (was advertising-only) |
| #223 | scan-go-stubs (sessionctl Emit) | `NewBusEmitter` + memorycurator stale-doc refresh |
| #224 | scan-governance-gaps cortex-core 17 | router deterministic stop/cancel/abort/halt short-circuit |
| #225 | scan-governance-gaps cortex-concerns 14 | rulecheck severity mapping covers all 9 supervisor rule subdirs |
| #226 | scan-governance-gaps r1d-server 4 | `.github/workflows/r1d-server.yml` running `make lint-chdir` + `go vet` |
| #227 | scan-test-quality (BLOCKED comment) | refresh antitrunc integration-test header — cortex-core is in tree |
| #228 | scan-ci-infra (hardcoded sleep) | tauri-driver `/status` poll instead of fixed 2s sleep |
| #229 | scan-governance-gaps web-chat-ui 50 | `.github/workflows/web.yml` (typecheck + build + vitest) |

### Cascade

- `dev → staging` PR #215 → CI in progress at this writing
- `staging → main` PR #230 → MERGED early (just carried what was on staging at open time, prior to #215)
- A second `staging → main` sync will land after #215 merges, bringing the 18 PRs forward to main.

### Remaining work (specs/post-merge-audit-cleanup.md)

10 tasks dispatched as parallel /build subagents. Each opens its own PR.

| Task | Topic | Branch | Status |
|---|---|---|---|
| TASK-1 | r1skill Stage 5 RuntimeAssertion records | `feat/r1skill-runtime-assertions` | dispatched |
| TASK-2 | bench golden corpus seed | `fix/bench-golden-corpus-seed` | dispatched |
| TASK-3+5 | web ESLint + storybook-mcp version pin | `fix/web-lint-and-storybook-version` | dispatched |
| TASK-4 | re-enable web CI lint + storybook jobs | (waits on T3+T5) | pending |
| TASK-6 | per-API-call AcquireLLMSlot in batched lobes | `fix/cortex-lobes-per-call-acquire-llm-slot` | dispatched |
| TASK-7 | AntiTruncLobe Workspace integration test | `test/antitrunc-workspace-integration` | dispatched |
| TASK-8 | web slice file split | `refactor/web-store-slice-split` | dispatched |
| TASK-9 | membus read_count increments on Recall | `feat/membus-read-count-increment` | dispatched |
| TASK-10 | this HANDOFF.md update | (this commit) | in progress |

### Out of scope (deferred to dedicated specs)

- e2e shim rewrite (real WebDriver protocol) — `desktop/tests/e2e/` — entire layer is a stub per audit
- `internal/executor/code.go` direct task routing (Track B Task 19) — needs SOW pipeline integration
- web pin downgrade per spec literal (React 19 → 18.3, Vite 7 → 6, Vitest 4 → 2.1) — breaking-API risk

---

## Reference: cumulative state on main

The 9-spec post-cortex-core scope (cortex-core, cortex-concerns, lanes-protocol, tui-lanes, r1d-server, web-chat-ui, desktop-cortex-augmentation, agentic-test-harness, anti-truncation) is fully shipped to main as of 2026-05-06 (PR #184 cumulative sync). Plus dep-bumps-post-node22 (#176) and legacy-spa-cleanup (#178).

This session adds 18 audit-driven cleanups on top of that baseline.

---

## Build context for next session

- `git fetch origin --prune` first.
- `gh pr list --state open` to see in-flight specs/post-merge-audit-cleanup work.
- Resume by running `/build` against any unchecked tasks in `specs/post-merge-audit-cleanup.md` if the dispatched agents have failed or stalled.
- Cascade rule (per CLAUDE.md): every new PR targets `dev`. Only the cumulative `dev → staging` and `staging → main` syncs run as separate PRs.
