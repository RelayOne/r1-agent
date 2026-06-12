# PROGRAM DAG — canonical multi-agent supervisor sheet

**Last updated:** 2026-05-19
**Canonical repo for this file:** `r1-agent`
**Canonical controller worktree:** `/home/eric/repos/r1-agent-program-sheet`
**Controller branch:** `codex/program-sheet-2026-05-15`
**Current controller SHA:** `f13576f7db384444cae8a4522e6087aa07451588`

Fresh Codex session instruction:

```text
Do this: /home/eric/repos/r1-agent/plans/PROGRAM-DAG-2026-05-15.md
```

If the primary checkout does not yet contain the latest copy of this file, use the same path in the clean controller worktree above.

This file is the canonical supervisor DAG. Older execution sheets are historical unless they explicitly point back here.

## 1. Current truth snapshot

These are not guesses. They are the currently confirmed baseline as of 2026-05-19.

- `r1-agent origin/dev` and the controller branch are both at `f13576f7db384444cae8a4522e6087aa07451588`.
- `r1-agent origin/staging` and `origin/main` are both at `c3bfbee2bbab56bc21d4f115a6dc7bcaf1d1116e`.
- Public `dev` is live and matches `origin/dev`:
  - `api.dev.r1.run` -> `f13576f`
  - `admin.dev.r1.run` -> `f13576f`
  - `platform.dev.r1.run` -> `f13576f`
  - `downloads.dev.r1.run` -> `f13576f`
- Public `staging` does **not** match `origin/staging`:
  - expected `c3bfbee`
  - actual `api.staging.r1.run` -> `4dcd5ef`
- Public `prod` does **not** match `origin/main`:
  - expected `c3bfbee`
  - actual `api.r1.run` -> `d536eb0`
- Promotion PR `#310` (`dev -> staging`) is open and blocked:
  - `mergeStateStatus: BLOCKED`
  - `reviewDecision: REVIEW_REQUIRED`
  - head SHA `f13576f7db384444cae8a4522e6087aa07451588`
- The current PR blockers are concrete:
  - GitHub Actions `desktop-augmentation` e2e jobs failed on all three OSes
  - Cloud Build required check `r1-agent-pr (relayone-488319)` failed in the `race` step, after the normal `test` step already passed
- The `desktop-augmentation` e2e suite currently assumes custom tauri-driver HTTP endpoints from [desktop/tests/e2e/helpers/tauri-driver-session.ts](/home/eric/repos/r1-agent/desktop/tests/e2e/helpers/tauri-driver-session.ts:106), but the corresponding app-side hooks and events are not present in `desktop/src` or `desktop/src-tauri`.
- Cloudflare DNS is not the current critical path.

## 2. Program objective

Finish the remaining program with maximum safe parallelism, but do it in the correct order:

1. clear the real promotion blockers
2. promote `dev -> staging -> main` truthfully
3. continue the long-tail completion program across `r1-agent`, `coderadar`, and `sites`
4. keep docs and handoffs trailing live truth, never leading it

This means the DAG has **two layers**:

- **Layer A:** promotion-critical DAG
- **Layer B:** long-tail completion DAG after promotion is unblocked

## 3. Repo and environment set

Primary write repos:

- `r1-agent`
- `coderadar`
- `sites`

Read-only reference repos unless a concrete bug proves otherwise:

- `actium-git`
- `actium-studio`

Infra surfaces:

- GCP project `relayone-488319`
- Cloudflare `r1.run`
- GitHub repo `RelayOne/r1-agent`

## 4. Status legend

- `DONE`: merged and validated in the current baseline
- `READY`: executable now with no unmet hard dependency
- `SOFT-BLOCKED`: can start now, but must rebase or wait before merge
- `BLOCKED`: do not start until dependency lands
- `OPERATOR`: requires human/operator approval or external action
- `CURATION`: not a pure coding lane

## 5. Supervisor rules

Every worker and subagent must follow these rules:

1. Own a disjoint write set.
2. Start with a failing test or a concrete reproduced runtime gap.
3. Implement the smallest real fix.
4. Run focused validation plus one integration or smoke check.
5. End with `git diff --check`.
6. Report:
   - repo
   - branch
   - worktree
   - files changed
   - tests added
   - tests run
   - live checks run
   - blockers
   - exact truth now made true

Controller-only actions:

- merge worker branches
- run deploys
- mutate live GCP state
- open or merge promotion PRs
- advance `origin/dev`, `origin/staging`, `origin/main`
- update final docs and handoffs

## 6. Canonical worktrees

Use the clean controller worktree for integration:

- `/home/eric/repos/r1-agent-program-sheet`

Suggested worker worktrees if they are not already present:

`r1-agent`

- `/home/eric/repos/r1-agent-ci-race`
- `/home/eric/repos/r1-agent-desktop-e2e`
- `/home/eric/repos/r1-agent-promotion`
- `/home/eric/repos/r1-agent-docs-truth`
- `/home/eric/repos/r1-agent-desktop-runtime`
- `/home/eric/repos/r1-agent-desktop-host`

`coderadar`

- `/home/eric/repos/coderadar-lifecycle-engine`
- `/home/eric/repos/coderadar-revenue-support`
- `/home/eric/repos/coderadar-dashboard-stitch`

`sites`

- `/home/eric/repos/sites-funnel-stitch`
- `/home/eric/repos/sites-deploy-truth`

## 7. Layer A — promotion-critical DAG

This is the real critical path now. Anything else is secondary until these nodes are green.

```text
ROOT-0 truth-freeze
  -> CI-1 race-gate
  -> CI-2 desktop-e2e-truth
  -> PROMOTE-1 dev-pr-checks

CI-1 -> REVIEW-1 code-review
CI-2 -> REVIEW-1 code-review
PROMOTE-1 -> PROMOTE-2 merge-dev-to-staging

REVIEW-1 -> PROMOTE-1
PROMOTE-2 -> DEPLOY-1 staging-live-confirm
DEPLOY-1 -> PROMOTE-3 open-staging-to-main
PROMOTE-3 -> DEPLOY-2 prod-live-confirm
DEPLOY-2 -> DOCS-1 truth-sync
```

### 7.1 Layer A node meanings

#### ROOT-0 — truth-freeze

- Status: `READY`
- Repo: all
- Owner: controller
- Goal:
  - re-check branch SHAs
  - re-check live versions
  - re-check PR `#310` state
  - re-check Cloud Build failure and GitHub e2e failure truth before changing code
- Required commands:
  - `git fetch --all --prune`
  - `gh pr view 310 --json ...`
  - `bash scripts/promote-r1.sh confirm-live dev || true`
  - `bash scripts/promote-r1.sh confirm-live staging || true`
  - `bash scripts/promote-r1.sh confirm-live prod || true`

#### CI-1 — race-gate

- Status: `READY`
- Repo: `r1-agent`
- Suggested branch: `codex/r1-ci-race-2026-05-19`
- Goal:
  - identify and fix the exact `go test -race ./...` failure that breaks the required Cloud Build `r1-agent-pr` check
- Known truth:
  - Cloud Build `bbb4f91b-1928-4271-a17d-e8f63bf0215c` already passed the normal `test` step
  - the failure is in step `#7` `race`
  - the earlier `cmd/r1` runtime-subprocess blocker is already fixed in `f13576f7`
- Owned paths:
  - any Go test or Go source paths proven to be responsible for the race failure
  - likely candidates include `cmd/r1`, `internal/daemon`, `internal/mcp`, or adjacent packages only if reproduced
- Forbidden paths:
  - desktop TS/Tauri files
  - docs except lane-local scratch notes
- Reproduction commands:
  - `go test -race ./cmd/r1 -count=1 -timeout=600s -v || true`
  - `go test -race ./internal/daemon -count=20 -v || true`
  - if those pass, widen carefully to package groups instead of blindly editing unrelated code
- Exit condition:
  - local reproduction shows the failing race is gone
  - a new PR/check run no longer fails the race step

#### CI-2 — desktop-e2e-truth

- Status: `READY`
- Repo: `r1-agent`
- Suggested branch: `codex/r1-desktop-e2e-truth-2026-05-19`
- Goal:
  - resolve the desktop e2e gate truthfully
- Known failure evidence:
  - Linux/macOS: `fetch failed` / `ECONNREFUSED 127.0.0.1:4444`
  - Windows: `tauri-driver testState failed: 404`
  - test helper assumes driver endpoints in [desktop/tests/e2e/helpers/tauri-driver-session.ts](/home/eric/repos/r1-agent/desktop/tests/e2e/helpers/tauri-driver-session.ts:107)
  - expected events such as `test.windows.list`, `primary-window.closed`, and `test.drive-lanes.completed` appear in e2e tests only, not in app implementation
- Decision rule:
  - if the app already has nearly all required hooks, implement the missing hooks
  - if the hooks are still speculative and the product truth is already “partial desktop”, downgrade the workflow gate so PR merges are not blocked by non-existent app support
- Owned paths:
  - [.github/workflows/desktop-augmentation.yml](/home/eric/repos/r1-agent/.github/workflows/desktop-augmentation.yml:1)
  - [desktop/tests/e2e/helpers/tauri-driver-session.ts](/home/eric/repos/r1-agent/desktop/tests/e2e/helpers/tauri-driver-session.ts:1)
  - [desktop/tests/e2e/helpers/desktop-fixtures.ts](/home/eric/repos/r1-agent/desktop/tests/e2e/helpers/desktop-fixtures.ts:1)
  - app-side desktop paths only if implementing the missing hooks for real
- Forbidden paths:
  - unrelated backend Go packages
  - broad docs except lane-local notes
- Exit condition:
  - the e2e gate is truthful
  - either the tests pass because the hooks are real, or the workflow no longer claims/block-merges on a feature surface that is not implemented

#### PROMOTE-1 — dev-pr-checks

- Status: `BLOCKED`
- Depends on: `CI-1`, `CI-2`, `REVIEW-1`
- Repo: `r1-agent`
- Goal:
  - get PR `#310` to a mergeable state with green required checks
- Required truth:
  - do not merge by bypassing the broken checks unless the check definitions themselves were truthfully changed in-code and pushed

#### PROMOTE-2 — merge-dev-to-staging

- Status: `BLOCKED`
- Depends on: `PROMOTE-1`
- Goal:
  - merge PR `#310`
  - advance `origin/staging`

#### DEPLOY-1 — staging-live-confirm

- Status: `BLOCKED`
- Depends on: `PROMOTE-2`
- Goal:
  - ensure public staging matches `origin/staging`
- Required checks:
  - `bash scripts/promote-r1.sh confirm-live staging || true`

#### PROMOTE-3 — open-staging-to-main

- Status: `BLOCKED`
- Depends on: `DEPLOY-1`
- Goal:
  - open or update the `staging -> main` promotion PR only after staging is actually live

#### DEPLOY-2 — prod-live-confirm

- Status: `BLOCKED`
- Depends on: `PROMOTE-3`
- Goal:
  - confirm public prod matches `origin/main`
- Required checks:
  - `bash scripts/promote-r1.sh confirm-live prod || true`

#### DOCS-1 — truth-sync

- Status: `BLOCKED`
- Depends on: `DEPLOY-2`
- Goal:
  - update handoff/docs after staging and prod truth is confirmed, not before

## 8. Layer B — long-tail completion DAG

These are still real remaining scopes, but they are not the reason promotion is currently blocked.

```text
ROOT-0 truth-freeze
  -> CR-1 lifecycle-engine
  -> CR-2 revenue-support
  -> CR-3 dashboard-stitch
  -> SITES-1 funnel-stitch
  -> SITES-2 deploy-verification
  -> R1-GTM-1 backend-lifecycle-cutover
  -> R1-GTM-2 revenue-support-cutover
  -> R1-ADM-1 operator-panels
  -> R1-DSK-1 runtime-stream
  -> R1-DSK-2 sow-graph
  -> R1-DSK-3 failure-ui
  -> R1-DSK-4 skill-mutations
  -> R1-DSK-5 ledger-write-paths
  -> R1-DSK-6 memory-write-paths
  -> R1-DSK-7 provider-vault-host
  -> R1-DSK-8 mcp-host
  -> R1-DSK-9 observability-host
  -> R1-DSK-10 approval-scheduler-host
  -> R1-DSK-11 packaging-signing
  -> R1-INF-1 promotion-pipeline
  -> R1-BEN-1 benchmark-infra

CR-1 -> R1-GTM-1
CR-2 -> R1-GTM-2
CR-3 -> R1-ADM-1
SITES-1 -> CR-3
SITES-1 -> R1-GTM-2
SITES-2 -> R1-INF-1

R1-DSK-1 -> R1-DSK-2
R1-DSK-1 -> R1-DSK-3
R1-DSK-1 -> R1-DSK-10
R1-DSK-4 -> R1-DSK-11
R1-DSK-5 -> R1-DSK-11
R1-DSK-6 -> R1-DSK-11
R1-DSK-7 -> R1-DSK-11
R1-DSK-8 -> R1-DSK-11
R1-DSK-9 -> R1-DSK-11
R1-DSK-10 -> R1-DSK-11

all long-tail code lanes -> REVIEW-2 code-review
deploy-impacting lanes -> REVIEW-3 live-smoke
desktop lanes -> REVIEW-4 desktop-validation
REVIEW-2 + REVIEW-3 + REVIEW-4 -> DOCS-2 long-tail-truth-sync
```

### 8.1 Long-tail completed baseline

Treat these as already landed baseline, not open work:

- `R1D-2` session stream regression coverage that prevents fabricated output before real events
- `R1D-4` skill catalog truthful read-only slice
- `R1D-5` ledger truthful read-only slice
- `R1D-6` memory truthful read-only slice
- `R1D-7` settings truthful daemon-only slice
- `R1D-8` MCP truthful unavailable slice
- `R1D-9` observability truthful unavailable slice
- `R1D-10` approval/scheduler truthful unavailable slice
- `R1D-11.6` onboarding truthful local-only slice
- CodeRadar ingest contract and person/attribution baseline
- `sites` CodeRadar loader and attribution propagation baseline

### 8.2 Long-tail ready frontier

These lanes can still be worked in parallel once controller bandwidth exists:

- `CR-1` CodeRadar lifecycle engine
- `CR-2` CodeRadar revenue/support
- `CR-3` CodeRadar dashboard stitch
- `SITES-1` funnel stitch
- `SITES-2` deploy verification
- `R1-ADM-1` operator panels
- `R1-DSK-1` runtime stream
- `R1-DSK-4` skill mutations
- `R1-DSK-5` ledger write-paths
- `R1-DSK-6` memory write-paths
- `R1-DSK-7` provider/vault host
- `R1-DSK-8` MCP host
- `R1-DSK-9` observability host
- `R1-INF-1` promotion pipeline
- `R1-BEN-1` benchmark infra

## 9. Exact supervisor handoff format

Every worker should return exactly this:

- `lane`
- `repo`
- `branch`
- `worktree`
- `files changed`
- `tests added`
- `tests run`
- `live checks run`
- `blockers`
- `merge risk`
- `exact truth now made true`

## 10. Current next actions

If a fresh Codex session starts from this file, it should do these first:

1. Re-run `ROOT-0`.
2. Inspect PR `#310` check state and confirm it is still blocked by `CI-1` and `CI-2`.
3. Split work immediately into:
   - one lane for `CI-1 race-gate`
   - one lane for `CI-2 desktop-e2e-truth`
4. Do not start staging/main promotion work until those two are green.
5. Only after `PROMOTE-2` and `DEPLOY-1` succeed should the session spend controller time on long-tail completion lanes.

That is the correct end-to-end ordering from the current truth state.
