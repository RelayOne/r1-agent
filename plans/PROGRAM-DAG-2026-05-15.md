# PROGRAM DAG — multi-repo, multi-agent execution sheet

**Last updated:** 2026-05-15
**Canonical branch baseline:** `origin/dev`
**Current observed branch tip when this file was written:** `3545054f`
**Purpose:** give a fresh Codex supervisor a single source of truth for running a large parallel program across `r1-agent`, `coderadar`, and `sites`, with explicit write ownership, hard dependencies, validation gates, and end conditions.

This file replaces older narrative planning. It is written as a real DAG for many subagents, not as a roadmap memo.

## 1. Ground truth snapshot

These statements are the baseline assumptions the DAG is built on.

- `r1-agent` is the primary integration repo.
- `coderadar` is the in-house analytics system of record we are trying to extend far enough to replace third-party GTM/product analytics surfaces.
- `sites` owns the public marketing/browser-side `r1.run` event surface and deploy pipeline.
- Public dev rollout may lag `origin/dev` by one revision; deployment confirmation is a separate node from code push.
- Desktop truth work has already landed substantial **read-only / unavailable-state corrections** for unsupported host surfaces.
- The desktop host already exposes some real verbs; the UI must only claim those verbs.
- Cloudflare DNS was previously verified healthy; DNS is not the current critical path.
- Deploy-path CodeRadar secret fallback was previously verified truthful in source and in live GCP.

## 2. Program goal

Drive the repos from "partially truthful, partially scaffolded, mixed live/stub state" to:

1. product analytics and attribution truth across `sites` + `r1-agent`
2. desktop host/UI parity to the end of the currently planned desktop scope
3. operator/admin/runtime/deploy truth in docs and code
4. reproducible dev -> staging -> main promotion gates
5. explicit handling of operator-gated and curator-gated work that cannot be honestly delegated away

## 3. Repo set

Primary write repos:

- `r1-agent`
- `coderadar`
- `sites`

Read-only reference repos unless a concrete bug proves otherwise:

- `actium-git`
- `actium-studio`

Infra surface, not a code repo:

- GCP project `relayone-488319`
- Cloudflare `r1.run` zone

## 4. Supervisor rules

All agents and worktrees follow these rules.

### 4.1 File ownership

- One lane owns one disjoint write set.
- No worker reverts or rewrites another worker's changes.
- If a lane needs another lane's files, it stops and reports a dependency instead of crossing the boundary.

### 4.2 TDD discipline

Every implementation lane follows this order:

1. add or tighten a failing test for the exact gap
2. implement the smallest real fix
3. run focused local validation
4. run one lane-specific smoke or integration check
5. hand back exact truth now made true

### 4.3 Merge rules

- Workers never merge to `dev`.
- Workers merge only into the integration branch in their repo.
- The controller merges one lane at a time after reviewer validation.
- Public docs merge only after the code they describe is merged or live.

### 4.4 Validation rules

- Any test or lint command that may fail should be run with `|| true` when collecting output.
- Every lane must finish with `git diff --check`.
- Any deploy-impacting lane requires a live verification node after merge.

## 5. Branch and worktree conventions

### 5.1 Controller branches

- `r1-agent`: `codex/program-integration-YYYY-MM-DD`
- `coderadar`: `codex/coderadar-program-integration-YYYY-MM-DD`
- `sites`: `codex/sites-program-integration-YYYY-MM-DD`

### 5.2 Worker branch naming

Use `codex/<lane-id>-YYYY-MM-DD`.

Examples:

- `codex/r1-desktop-memory-2026-05-15`
- `codex/cr-lifecycle-2026-05-15`
- `codex/sites-funnel-stitch-2026-05-15`

### 5.3 Worktree naming

Use `/home/eric/repos/<repo>-<lane-name>`.

Examples:

- `/home/eric/repos/r1-agent-desktop-runtime`
- `/home/eric/repos/coderadar-lifecycle`
- `/home/eric/repos/sites-funnels`

## 6. Status legend

- `DONE`: already merged and validated in the program baseline
- `READY`: executable now with no unmet hard dependency
- `SOFT-BLOCKED`: executable now against a temporary contract, but must rebase before merge
- `BLOCKED`: do not start until dependency lands
- `OPERATOR`: cannot be honestly completed by coding agents alone
- `CURATION`: human corpus/content work, not a pure coding lane

## 7. Current completed baseline

These are already complete enough that new agents should treat them as baseline, not rediscover them.

### 7.1 `r1-agent` completed truth lanes

- `R1D-4` current truthful slice: skill catalog is read-only and only claims `skill_list` + `skill_get`
- `R1D-5` current truthful slice: ledger browser is read-only and only claims `ledger_list_events` + `ledger_get_node`
- `R1D-6` current truthful slice: memory browser is read-only and only claims `memory_list_scopes` + `memory_query`
- `R1D-7` current truthful slice: settings only claims daemon host support; other sections are explicit unavailable/read-only
- `R1D-8` current truthful slice: MCP panel is explicit unavailable
- `R1D-9` current truthful slice: observability is explicit unavailable
- `R1D-10` current truthful slice: approval queue and scheduler are explicit unavailable
- `R1D-11.6` current truthful slice: onboarding uses the real folder picker, demo is unavailable, provider key persistence is local-only in this build
- `R1D-2` regression coverage now proves live mode does not fabricate assistant output before runtime events

### 7.2 `coderadar` completed baseline

- ingest contract work landed earlier
- person/attribution read-path repair landed earlier

### 7.3 `sites` completed baseline

- browser CodeRadar loader and attribution propagation landed earlier
- deploy-topology truth was already corrected earlier

## 8. End-to-end DAG

```text
ROOT-0 freeze-truth
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

all code lanes -> REVIEW-1 code-review
deploy-impacting lanes -> REVIEW-2 live-smoke
desktop lanes -> REVIEW-3 desktop-e2e

REVIEW-1 + REVIEW-2 + REVIEW-3 -> DOCS-1 final-truth-sync
DOCS-1 -> PROMOTE-1 dev-confirm
PROMOTE-1 -> PROMOTE-2 staging
PROMOTE-2 -> PROMOTE-3 main
PROMOTE-3 -> OP-1 release-and-store
PROMOTE-3 -> CUR-1 benchmark-corpus
```

## 9. Immediate maximum-parallel frontier

These lanes can all start now without waiting on each other.

### Ready now

- `CR-1` CodeRadar lifecycle engine
- `CR-2` CodeRadar revenue/support
- `CR-3` CodeRadar dashboard stitch
- `SITES-1` marketing funnel stitch
- `SITES-2` sites deploy verification
- `R1-ADM-1` operator panel hardening
- `R1-DSK-1` runtime event stream / live session transport
- `R1-DSK-4` skill mutation host support
- `R1-DSK-5` ledger write-path support
- `R1-DSK-6` memory write-path support
- `R1-DSK-7` provider/vault host support
- `R1-DSK-8` MCP host support
- `R1-DSK-9` observability host support
- `R1-INF-1` promotion pipeline
- `R1-BEN-1` benchmark infra

### Soft-blocked but can prototype now

- `R1-GTM-1` backend lifecycle cutover against provisional CodeRadar contract
- `R1-GTM-2` revenue/support cutover against provisional CodeRadar contract
- `R1-DSK-2` SOW graph completion can start on UI side before full runtime stream lands
- `R1-DSK-3` failure-classification UI can start on UI side before full runtime stream lands
- `R1-DSK-10` approval/scheduler host work can start on Tauri registration side before full multi-session runtime work lands

## 10. Node registry

Each node below is written so a supervisor can hand it to a subagent with minimal extra interpretation.

### ROOT-0 — freeze-truth

- Status: `READY`
- Repo: all
- Owner: controller only
- Goal: record the exact SHAs, live revisions, current deploy versions, and current open-lane state before starting a new wave
- Outputs:
  - `r1-agent` branch SHA
  - `coderadar` branch SHA
  - `sites` branch SHA
  - public dev versions for `api`, `admin`, `platform`, `downloads`
  - current dashboard of completed nodes
- Tests:
  - `git rev-parse`
  - `git ls-remote`
  - `curl /livez` or `/v1/version`

### CR-1 — CodeRadar lifecycle engine

- Status: `READY`
- Repo: `coderadar`
- Own paths:
  - lifecycle tables
  - journey/campaign state machine
  - lifecycle APIs
  - delivery event models
- Forbidden paths:
  - `sites`
  - `r1-agent`
- Goal:
  - make a real minimal lifecycle engine inside CodeRadar so `r1-agent` can stop pretending Customer.io-like flows are already replaced
- Tests:
  - trigger -> queued delivery -> sent / failed transition tests
  - suppression / opt-out tests
  - query visibility tests
- Exit:
  - one real lifecycle journey path exists and is queryable

### CR-2 — CodeRadar revenue/support

- Status: `READY`
- Repo: `coderadar`
- Own paths:
  - revenue/billing aggregation
  - support/ticket linkage
  - dashboard query surfaces for business metrics
- Goal:
  - provide enough operator-facing revenue/support telemetry for `r1-admin`
- Tests:
  - billing aggregation tests
  - support event linkage tests
- Exit:
  - business/operator reporting can be fed from CodeRadar data

### CR-3 — CodeRadar dashboard stitch

- Status: `READY`
- Repo: `coderadar`
- Own paths:
  - dashboard query views
  - funnel/cohort dashboards
  - attribution and cross-surface reporting
- Depends on:
  - `SITES-1` for funnel events
- Goal:
  - make acquisition -> activation -> mission-success views actually explorable

### SITES-1 — marketing funnel stitch

- Status: `READY`
- Repo: `sites`
- Own paths:
  - `sites/r1` browser event capture
  - attribution propagation
  - funnel naming alignment with CodeRadar and `r1-agent`
- Goal:
  - make browser-side acquisition and CTA events stitch cleanly into product analytics
- Tests:
  - site build
  - event helper checks
  - attribution propagation checks

### SITES-2 — sites deploy verification

- Status: `READY`
- Repo: `sites`
- Own paths:
  - sites Cloud Build config
  - rsync deploy scripts
  - site deploy docs comments only if needed
- Goal:
  - keep marketing deploy truth reproducible to the end
- Exit:
  - controller can confirm exactly which deploy path is canonical for each public site surface

### R1-GTM-1 — backend lifecycle cutover

- Status: `SOFT-BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - backend lifecycle subscribers
  - lifecycle transport wiring
  - service env config
- Depends on:
  - `CR-1`
- Goal:
  - stop relying on third-party lifecycle assumptions where CodeRadar can now own them

### R1-GTM-2 — revenue/support cutover

- Status: `SOFT-BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - revenue/support reporting integration
  - operator-facing telemetry use
- Depends on:
  - `CR-2`
  - `SITES-1`
- Goal:
  - route operator/business surfaces to the new in-house telemetry backend

### R1-ADM-1 — operator panels

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - `services/r1-admin`
  - admin handlers and templates
- Depends on:
  - `CR-3` for full funnel/business depth, but can start earlier for structural hardening
- Goal:
  - finish real operator panels and stop leaving obvious placeholders
- Tests:
  - focused `go test ./services/r1-admin/...`
  - authenticated route smoke

### R1-DSK-1 — runtime event stream

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - `desktop/src-tauri/src/*` runtime transport
  - `desktop/src/panels/session-view.ts`
  - focused `r1d-2` tests if behavior changes
- Goal:
  - satisfy the real critical path for desktop: streamed runtime events and end-to-end session truth
- Blocks:
  - `R1-DSK-2`
  - `R1-DSK-3`
  - `R1-DSK-10`

### R1-DSK-2 — SOW graph completion

- Status: `SOFT-BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - `desktop/src/panels/sow-tree.ts`
  - graph visualization files
- Depends on:
  - `R1-DSK-1` for full live AC
- Goal:
  - land `R1D-3.2` dependency graph visualization and complete the SOW surface

### R1-DSK-3 — failure-classification UI

- Status: `SOFT-BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - verification descent and failure UI
  - associated test files
- Depends on:
  - `R1-DSK-1` for full live AC
- Goal:
  - finish `R1D-3.5`

### R1-DSK-4 — skill mutation host support

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - skill host verbs in `desktop/src-tauri/src/ipc.rs`
  - `desktop/src/panels/skill-catalog.ts`
  - `desktop/src/types/ipc.d.ts`
  - focused `r1d-4` tests
- Goal:
  - either implement real skill mutation/test verbs or keep the panel read-only and explicitly mark the unresolved subfeatures

### R1-DSK-5 — ledger write-path support

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - ledger mutation verbs
  - ledger verify/export/shred surfaces
  - focused `r1d-5` tests
- Goal:
  - move `R1D-5` from read-only truthful slice to full planned capability

### R1-DSK-6 — memory write-path support

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - memory history/import/delete host verbs
  - `desktop/src/panels/memory-inspector.ts`
  - focused `r1d-6` tests
- Goal:
  - move `R1D-6` from read-only truthful slice to full planned capability

### R1-DSK-7 — provider/vault host support

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - settings provider/vault/governance/autostart handlers in Rust host
  - `desktop/src/panels/settings.ts`
  - focused `r1d-7` tests
- Goal:
  - make the settings panel actually host-backed rather than permanently downgraded

### R1-DSK-8 — MCP host support

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - MCP handler registration
  - `desktop/src/panels/mcp-servers.ts`
  - focused `r1d-8` tests
- Goal:
  - turn MCP from unavailable to real host-backed functionality

### R1-DSK-9 — observability host support

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - observability dashboard host verbs
  - `desktop/src/panels/observability.ts`
  - focused `r1d-9` tests
- Goal:
  - turn observability from unavailable to real host-backed telemetry views

### R1-DSK-10 — approval/scheduler host support

- Status: `SOFT-BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - approval/scheduler verbs
  - queue/scheduler panels
  - focused `r1d-10` tests
- Depends on:
  - `R1-DSK-1` for full runtime correctness
- Goal:
  - finish `R1D-10` beyond unavailable placeholders

### R1-DSK-11 — packaging/signing/release

- Status: `BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - desktop packaging
  - signing/notarization/release automation
  - first-launch polish where needed
- Depends on:
  - `R1-DSK-4`
  - `R1-DSK-5`
  - `R1-DSK-6`
  - `R1-DSK-7`
  - `R1-DSK-8`
  - `R1-DSK-9`
  - `R1-DSK-10`
- Goal:
  - move from truthful partial desktop to releasable desktop

### R1-INF-1 — promotion pipeline

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - deploy scripts
  - Cloud Build triggers
  - promotion docs comments only if needed
- Goal:
  - make dev -> staging -> main promotion reproducible and test-gated
- Exit:
  - controller can promote with a scriptable, validated path

### R1-BEN-1 — benchmark infra

- Status: `READY`
- Repo: `r1-agent`
- Own paths:
  - benchmark trigger scripts
  - benchmark YAML
  - corpus tooling only
- Goal:
  - get benchmark infra truthful and runnable
- Note:
  - corpus authoring itself remains `CURATION`

### REVIEW-1 — code review

- Status: `READY`
- Owner: reviewer agent
- Goal:
  - reject fake capabilities, missing tests, wrong contracts, and docs-over-code

### REVIEW-2 — live smoke

- Status: `READY`
- Owner: reviewer agent
- Goal:
  - confirm deploy-impacting merges actually show up at live endpoints

### REVIEW-3 — desktop end-to-end

- Status: `READY`
- Owner: reviewer agent
- Goal:
  - run aggregated desktop tests and, where possible, manual/runtime smoke for Tauri-backed flows

### DOCS-1 — final truth sync

- Status: `BLOCKED`
- Repo: `r1-agent`
- Own paths:
  - `README.md`
  - `docs/*`
  - `plans/HANDOFF.md`
  - any new truth-state plan files
- Depends on:
  - all merged code and live verification nodes
- Goal:
  - docs become a trailing truth pass, not speculative project management

### PROMOTE-1 — dev confirm

- Status: `BLOCKED`
- Owner: controller
- Goal:
  - confirm `origin/dev` SHA equals live dev version for touched services

### PROMOTE-2 — staging

- Status: `BLOCKED`
- Owner: controller + operator
- Goal:
  - merge/push staging only after dev-confirm is green and smoke-tested

### PROMOTE-3 — main

- Status: `BLOCKED`
- Owner: controller + operator
- Goal:
  - merge/push main only after staging is green and smoke-tested

### OP-1 — release and store

- Status: `OPERATOR`
- Goal:
  - app store submissions, signing keys, notarization approvals, release toggles

### CUR-1 — benchmark corpus

- Status: `CURATION`
- Goal:
  - complete the deferred benchmark corpus authoring effort

## 11. Validation matrix

### `r1-agent` desktop lanes

- `cd desktop && npx vitest run <focused-tests> || true`
- `cd desktop && npm run typecheck || true`
- `git diff --check`

### `r1-agent` Go/backend lanes

- focused `go test ./path/... -count=1 || true`
- `go build` for touched entrypoints when relevant
- live endpoint smoke for deploy-impacting changes

### `coderadar`

- focused ingest/query/unit tests
- dashboard query smoke

### `sites`

- `npm run build`
- event helper checks
- attribution propagation checks
- edge confirmation when deployed

## 12. Merge order

Recommended merge order for the next major waves:

1. `R1-DSK-1`, `R1-DSK-4`, `R1-DSK-5`, `R1-DSK-6`, `R1-DSK-7`, `R1-DSK-8`, `R1-DSK-9`
2. `R1-DSK-2`, `R1-DSK-3`, `R1-DSK-10`
3. `CR-1`, `CR-2`, `CR-3`
4. `SITES-1`, `SITES-2`
5. `R1-GTM-1`, `R1-GTM-2`
6. `R1-ADM-1`
7. `R1-INF-1`, `R1-BEN-1`
8. `R1-DSK-11`
9. `DOCS-1`
10. `PROMOTE-1`, `PROMOTE-2`, `PROMOTE-3`

## 13. Handoff template for each worker

Every worker must return exactly this shape:

- `lane_id`
- `repo`
- `worktree`
- `branch`
- `files_changed`
- `tests_added`
- `tests_run`
- `live_checks_run`
- `dependencies_hit`
- `merge_risks`
- `exact_truth_now_made_true`

## 14. Fresh-session command

For a new Codex session, the supervisor prompt should be:

```text
Do this: /home/eric/repos/r1-agent/plans/PROGRAM-DAG-2026-05-15.md
```

The supervisor should then:

1. read this file first
2. freeze current SHAs and live versions
3. open controller worktrees
4. spawn the maximum non-overlapping `READY` lanes
5. keep the critical path moving locally while side lanes run
6. merge only after review gates pass

