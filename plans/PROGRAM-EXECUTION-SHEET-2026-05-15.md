# Program Execution Sheet

**Historical note:** this file was the original bootstrap sheet. The canonical live supervisor file is now [plans/PROGRAM-DAG-2026-05-15.md](/home/eric/repos/r1-agent/plans/PROGRAM-DAG-2026-05-15.md:1), which carries the current branch/live-state truth and the promotion-critical DAG.

Use this file only as historical bootstrap detail when the canonical DAG points back here for older implementation context.

Fresh Codex session instruction:

```text
Do this: /home/eric/repos/r1-agent/plans/PROGRAM-DAG-2026-05-15.md
```

If the current checkout does not contain this file, use the same path in the clean worktree created from `origin/dev`.

## 1. Mission

Complete and truthfully reconcile the remaining work across:

- `r1-agent`
- `coderadar`
- `sites`
- live `relayone-488319` GCP / Cloudflare state

Read-only reference repos:

- `/home/eric/repos/actium-studio`
- `/home/eric/repos/actium-git`

This is not only a coding task. It is a coordinated build, integration, deploy, verification, truth-sync, and handoff task. No doc may overclaim the code. No deploy doc may disagree with live GCP. No analytics claim may exceed what CodeRadar actually supports.

## 2. Current Known Truth

These are the last confirmed truths from the previous audit. Re-verify them before relying on them:

- `origin/dev` in `r1-agent` was `3af86d109f4402b35d200bd493e92d12513a46a0`.
- Public `r1.run` footprint was 12 hostnames across `prod`, `staging`, `dev`, covering `platform`, `api`, `downloads`, and `admin`.
- All 12 public hostnames returned `200` on `/livez`.
- Cloudflare `r1.run` DNS was already correct and did not need repair.
- `coderadar` was good enough for product analytics, flags, funnels, cohorts, and surveys.
- `coderadar` was not yet a full replacement for lifecycle automation or revenue/support GTM surfaces.
- `sites` is a real deploy surface for `r1` marketing pages and has two deploy patterns:
  - Cloud Run build-and-deploy via [cloudbuild-sites.yaml](/home/eric/repos/sites/cloudbuild-sites.yaml:1)
  - VM rsync + nginx reload via [cloudbuild-deploy-sites.yaml](/home/eric/repos/sites/cloudbuild-deploy-sites.yaml:1)
- `r1-agent` still had real remaining work in GTM migration, admin hardening, desktop runtime/panels, infra normalization, benchmark pipeline, and docs truth reconciliation.

Treat every item above as a starting hypothesis, not a substitute for re-checking.

## 3. Success Criteria

The program is only complete when all of the following are true:

1. `coderadar` has the minimum required ingest, person/attribution, and any newly-built lifecycle/revenue features needed by `r1-agent` and `sites`.
2. `sites/r1` emits real browser-side product/marketing events into CodeRadar with tested attribution continuity.
3. `r1-agent` emits real backend/product events into CodeRadar with tested attribution continuity.
4. Hosted `r1-admin` is hardened enough that docs no longer describe it as more complete than it is.
5. The worst desktop truth gaps are closed: fake session output removed from normal flow, IPC/panel support truthful, and remaining gaps explicitly documented.
6. Cloud Build, secret wiring, deploy scripts, and trigger inventory match live GCP truth.
7. Benchmark pipeline truth is corrected and missing operational pieces are implemented where feasible.
8. `README`, docs, plans, handoff, and affected specs trail the live code and live infra truth.
9. Each merged lane has package-local tests, integration checks, and reviewer confirmation.
10. The resulting integration branch is merged to `origin/dev` only after final verification.

## 4. Controller Rules

The controller is the only actor allowed to:

- decide merge order
- merge worker branches
- deploy live changes
- advance `origin/dev`
- update the final truth-state docs

Workers may edit code in their own worktrees only. They must not merge each other.

Every worker must:

1. own a disjoint write set
2. start with a failing test or a concrete reproduced gap
3. make the smallest real fix
4. run focused tests
5. run one integration or smoke check
6. report:
   - branch
   - worktree
   - files changed
   - tests added
   - tests run
   - live checks run
   - blockers
   - exact truth now made true

## 5. Bootstrap Sequence

Run these steps first in a fresh controller session.

### 5.1 Repo Truth Freeze

In `/home/eric/repos/r1-agent`:

```bash
git fetch --all --prune
git rev-parse origin/dev
git worktree list
```

In `/home/eric/repos/coderadar`:

```bash
git fetch --all --prune
git branch -r
git symbolic-ref refs/remotes/origin/HEAD || true
git log --oneline --decorate -n 10 origin/main || true
git log --oneline --decorate -n 10 origin/dev || true
```

In `/home/eric/repos/sites`:

```bash
git fetch --all --prune
git branch -r
git symbolic-ref refs/remotes/origin/HEAD || true
git log --oneline --decorate -n 10 origin/main || true
git log --oneline --decorate -n 10 origin/dev || true
```

In GCP:

```bash
gcloud config get-value project
gcloud run services list --region=us-central1 --project=relayone-488319
gcloud beta run domain-mappings list --region=us-central1 --project=relayone-488319
gcloud builds triggers list --project=relayone-488319
gcloud secrets list --project=relayone-488319
```

In Cloudflare:

- Read the existing token secret from GCP Secret Manager only if needed for a fresh DNS verification.
- Do not rotate or replace DNS entries unless the live checks prove a real defect.

### 5.2 Create Clean Worktrees

Never run this from a dirty feature branch checkout. Create fresh worktrees from the canonical integration branches discovered in 5.1.

Suggested worktrees and branches:

`r1-agent`

```bash
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-program-controller -b codex/program-integration-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-gtm-core -b codex/r1-gtm-core-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-gtm-surface -b codex/r1-gtm-surface-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-admin-hardening -b codex/r1-admin-hardening-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-desktop-runtime -b codex/r1-desktop-runtime-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-desktop-panels -b codex/r1-desktop-panels-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-infra-rollout -b codex/r1-infra-rollout-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-benchmark-ops -b codex/r1-benchmark-ops-2026-05-15 origin/dev
git -C /home/eric/repos/r1-agent worktree add /home/eric/repos/r1-agent-docs-truth -b codex/r1-docs-truth-2026-05-15 origin/dev
```

`coderadar`

Replace `origin/main` below if 5.1 proves another branch is the live integration base.

```bash
git -C /home/eric/repos/coderadar worktree add /home/eric/repos/coderadar-ingest-contract -b codex/cr-ingest-contract-2026-05-15 origin/main
git -C /home/eric/repos/coderadar worktree add /home/eric/repos/coderadar-person-attribution -b codex/cr-person-attribution-2026-05-15 origin/main
git -C /home/eric/repos/coderadar worktree add /home/eric/repos/coderadar-lifecycle-automation -b codex/cr-lifecycle-automation-2026-05-15 origin/main
git -C /home/eric/repos/coderadar worktree add /home/eric/repos/coderadar-revenue-support -b codex/cr-revenue-support-2026-05-15 origin/main
```

`sites`

Replace `origin/main` below if 5.1 proves another branch is the live integration base.

```bash
git -C /home/eric/repos/sites worktree add /home/eric/repos/sites-r1-analytics -b codex/sites-r1-analytics-2026-05-15 origin/main
git -C /home/eric/repos/sites worktree add /home/eric/repos/sites-r1-funnels -b codex/sites-r1-funnels-2026-05-15 origin/main
git -C /home/eric/repos/sites worktree add /home/eric/repos/sites-pipeline-truth -b codex/sites-pipeline-truth-2026-05-15 origin/main
```

## 6. Program DAG

```text
ROOT-0 truth-freeze
  -> CR-1 ingest-contract
  -> CR-2 person-attribution
  -> CR-3 lifecycle-automation
  -> CR-4 revenue-support
  -> SITES-1 deploy-topology-audit
  -> SITES-2 r1-surface-event-inventory
  -> R1-GTM-1 backend-event-inventory
  -> R1-ADM-1 admin-auth-hardening
  -> R1-DSK-1 desktop-ipc-base
  -> R1-DSK-2 desktop-session-streaming
  -> R1-INF-1 trigger-secret-audit
  -> R1-BEN-1 benchmark-trigger-audit

CR-1 -> SITES-3 browser-coderadar-loader
CR-1 -> R1-GTM-2 backend-coderadar-transport

CR-2 -> SITES-4 attribution-propagation
CR-2 -> R1-GTM-3 backend-attribution-capture

CR-3 -> R1-GTM-5 lifecycle-cutover
CR-4 -> R1-GTM-6 revenue-support-cutover

SITES-1 -> SITES-5 pipeline-normalization
SITES-2 -> SITES-3
SITES-3 -> SITES-4
SITES-4 -> SITES-6 funnel-coverage
SITES-5 -> REVIEW-2 live-smoke
SITES-6 -> R1-GTM-4 dashboards-funnels

R1-GTM-1 -> R1-GTM-2
R1-GTM-2 -> R1-GTM-3
R1-GTM-2 -> R1-INF-2 deploy-secret-wiring
R1-GTM-3 -> R1-GTM-4 dashboards-funnels
R1-GTM-4 -> R1-ADM-3 operator-views
R1-GTM-4 -> R1-DOCS
R1-GTM-5 -> R1-DOCS
R1-GTM-6 -> R1-DOCS

R1-ADM-1 -> R1-ADM-2 real-data-surfaces
R1-ADM-2 -> R1-ADM-3 operator-views
R1-ADM-3 -> R1-DOCS

R1-DSK-1 -> R1-DSK-3 skill-catalog
R1-DSK-1 -> R1-DSK-4 ledger-memory
R1-DSK-1 -> R1-DSK-5 settings-mcp
R1-DSK-1 -> R1-DSK-6 observability-scheduler
R1-DSK-2 -> R1-DOCS
R1-DSK-3 -> R1-DOCS
R1-DSK-4 -> R1-DOCS
R1-DSK-5 -> R1-DOCS
R1-DSK-6 -> R1-DSK-7 signing-store
R1-DSK-6 -> R1-DOCS

R1-INF-1 -> R1-INF-2
R1-INF-2 -> R1-INF-3 dev-deploy-live-smoke
R1-INF-3 -> R1-DOCS

R1-BEN-1 -> R1-BEN-2 trigger-fixup
R1-BEN-2 -> R1-BEN-3 corpus-pipeline
R1-BEN-3 -> R1-BEN-4 corpus-curation
R1-BEN-4 -> R1-DOCS

all merged code nodes -> REVIEW-1 code-audit
all deploy nodes -> REVIEW-2 live-smoke
REVIEW-1 + REVIEW-2 -> CTRL-MERGE -> origin/dev
```

## 7. Repo Ownership Map

`coderadar` owns:

- ingest contract
- person/identity merge behavior
- attribution storage/query semantics
- lifecycle automation if built
- revenue/support analytics primitives if built

`sites` owns:

- landing pages, docs pages, pricing pages, downloads pages under [sites/r1](/home/eric/repos/sites/r1)
- browser-side event capture
- marketing attribution capture and propagation
- marketing-site deploy pipeline truth

`r1-agent` owns:

- backend event emission
- CodeRadar transport for hosted services
- admin hardening
- desktop runtime/panels
- Cloud Build + deploy scripts for hosted services
- benchmark pipeline truth and implementation
- final docs, handoff, and plan truth

## 8. Per-Node Execution Sheets

Each node below is written so it can be handed directly to a worker or a new Codex session.

### ROOT-0

Mission:

- Re-verify repo and infra truth before any implementation begins.

Outputs:

- exact base SHAs for `r1-agent`, `coderadar`, and `sites`
- exact Cloud Run service inventory
- exact domain-mapping inventory
- exact trigger inventory
- exact secret inventory
- exact note on whether `sites/r1` is currently Cloud Run or VM-rsync in each env

Completion proof:

- one controller note with exact command output summaries

### CR-1

Repo and worktree:

- `/home/eric/repos/coderadar-ingest-contract`
- `codex/cr-ingest-contract-2026-05-15`

Mission:

- Make the CodeRadar product-analytics ingest contract explicit, stable, and tested for both browser and backend R1 traffic.

Owned paths:

- [apps/ingest-api/src/routes/analytics.ts](/home/eric/repos/coderadar/apps/ingest-api/src/routes/analytics.ts:1)
- [apps/ingest-api/tests/analytics.test.ts](/home/eric/repos/coderadar/apps/ingest-api/tests/analytics.test.ts:1)
- [packages/sdk-node/src/analytics.ts](/home/eric/repos/coderadar/packages/sdk-node/src/analytics.ts:1)
- [sdks/browser/src/analytics.ts](/home/eric/repos/coderadar/sdks/browser/src/analytics.ts:1)
- related contract fixtures/tests

Forbidden paths:

- dashboard GTM views
- lifecycle automation
- revenue/support features

Required test additions:

- explicit R1 event fixtures for:
  - `telemetry_opt_in`
  - `session_started`
  - `mission_started`
  - `mission_completed`
- auth rejection tests
- malformed payload tests

Required commands:

```bash
pnpm --dir /home/eric/repos/coderadar test --filter ingest-api || true
pnpm --dir /home/eric/repos/coderadar test --filter sdk-node || true
pnpm --dir /home/eric/repos/coderadar test --filter browser || true
```

Completion proof:

- one accepted curl fixture
- one stable event contract note for `sites` and `r1-agent`

### CR-2

Repo and worktree:

- `/home/eric/repos/coderadar-person-attribution`
- `codex/cr-person-attribution-2026-05-15`

Mission:

- Make person resolution and attribution fields queryable and reliable.

Owned paths:

- [sql/postgres/027_coderadar_persons.sql](/home/eric/repos/coderadar/sql/postgres/027_coderadar_persons.sql:1)
- [apps/ingest-api/src/services/persons.ts](/home/eric/repos/coderadar/apps/ingest-api/src/services/persons.ts:1)
- [apps/ingest-api/tests/persons.test.ts](/home/eric/repos/coderadar/apps/ingest-api/tests/persons.test.ts:1)
- [apps/dashboard/src/lib/analytics-postgres.ts](/home/eric/repos/coderadar/apps/dashboard/src/lib/analytics-postgres.ts:1)
- [apps/dashboard/src/lib/analytics-clickhouse.ts](/home/eric/repos/coderadar/apps/dashboard/src/lib/analytics-clickhouse.ts:1)
- [apps/dashboard/src/app/api/analytics/persons/[distinctId]/route.ts](/home/eric/repos/coderadar/apps/dashboard/src/app/api/analytics/persons/[distinctId]/route.ts:1)
- [apps/dashboard/src/app/api/analytics/persons/export/route.ts](/home/eric/repos/coderadar/apps/dashboard/src/app/api/analytics/persons/export/route.ts:1)

Forbidden paths:

- lifecycle automation
- revenue/support work

Required test additions:

- person API mismatch regression
- attribution property visibility in reads and exports
- dashboard query regression for `url`, `referrer`, and UTM properties

Required commands:

```bash
pnpm --dir /home/eric/repos/coderadar test --filter ingest-api || true
pnpm --dir /home/eric/repos/coderadar test --filter dashboard || true
```

Completion proof:

- one end-to-end sample where a browser event with UTM/referrer can be queried back

### CR-3

Repo and worktree:

- `/home/eric/repos/coderadar-lifecycle-automation`
- `codex/cr-lifecycle-automation-2026-05-15`

Mission:

- Build the minimum real lifecycle automation engine required to retire Customer.io assumptions for R1.

Owned paths:

- `apps/alerter/**`
- lifecycle or campaign routes under `apps/dashboard/**`
- new SQL migrations under `sql/postgres/**`
- any shared packages created specifically for campaign state/delivery

Forbidden paths:

- unrelated incident or replay subsystems

Required test additions:

- trigger to queued-campaign transition
- suppression/opt-out behavior
- delivery state progression

Completion proof:

- at least one real journey path exists and is test-backed

### CR-4

Repo and worktree:

- `/home/eric/repos/coderadar-revenue-support`
- `codex/cr-revenue-support-2026-05-15`

Mission:

- Build the minimum real revenue/support analytics primitives needed for R1 operator views.

Owned paths:

- revenue or billing routes/models
- Stripe-related analytics support such as [packages/sdk-stripe](/home/eric/repos/coderadar/packages/sdk-stripe)
- support/ticket correlation surfaces if present

Required test additions:

- revenue aggregation regression
- billing event dedupe or summarization regression
- support linkage regression if implemented

Completion proof:

- operator/business-facing telemetry exists in a queryable form, not just raw events

### SITES-1

Repo and worktree:

- `/home/eric/repos/sites-pipeline-truth`
- `codex/sites-pipeline-truth-2026-05-15`

Mission:

- Audit and normalize the `sites` deploy topology for `r1`.

Owned paths:

- [cloudbuild-sites.yaml](/home/eric/repos/sites/cloudbuild-sites.yaml:1)
- [cloudbuild-deploy-sites.yaml](/home/eric/repos/sites/cloudbuild-deploy-sites.yaml:1)
- any `sites/r1` deploy README if it exists

Required checks:

- trigger inventory in GCP
- whether `r1` uses Cloud Run, VM rsync, or both per environment
- whether GA4/PostHog-only assumptions are hardcoded into the pipeline

Completion proof:

- one exact deployment matrix by environment and domain

### SITES-2

Repo and worktree:

- `/home/eric/repos/sites-r1-analytics`
- `codex/sites-r1-analytics-2026-05-15`

Mission:

- Inventory the browser/user events that `sites/r1` must emit.

Owned paths:

- [r1/index.html](/home/eric/repos/sites/r1/index.html:1)
- [r1/pricing/index.html](/home/eric/repos/sites/r1/pricing/index.html:1)
- [r1/downloads/index.html](/home/eric/repos/sites/r1/downloads/index.html:1)
- [r1/docs/index.html](/home/eric/repos/sites/r1/docs/index.html:1)
- [r1/why-r1/index.html](/home/eric/repos/sites/r1/why-r1/index.html:1)
- [r1/how-it-works/index.html](/home/eric/repos/sites/r1/how-it-works/index.html:1)
- [r1/partials/head.html](/home/eric/repos/sites/r1/partials/head.html:1)
- [r1/partials/footer.html](/home/eric/repos/sites/r1/partials/footer.html:1)

Required output:

- one canonical event map for:
  - landing page views
  - CTA clicks
  - docs clicks
  - download intent
  - pricing funnel clicks
  - contact/lead actions if present
  - outbound transitions to `platform`, `admin`, `downloads`, or `api`

Completion proof:

- event map committed as code comments, tests, or lane-local implementation notes

### SITES-3

Repo and worktree:

- `/home/eric/repos/sites-r1-analytics`
- `codex/sites-r1-analytics-2026-05-15`

Depends on:

- `CR-1`
- `SITES-2`

Mission:

- Implement CodeRadar-first browser capture for `sites/r1`.

Owned paths:

- [_shared/ga4-events.js](/home/eric/repos/sites/_shared/ga4-events.js:1)
- [_shared/posthog-init.js](/home/eric/repos/sites/_shared/posthog-init.js:1)
- [_shared/utm-capture.js](/home/eric/repos/sites/_shared/utm-capture.js:1)
- [r1/public/_partials/ga4-events.js](/home/eric/repos/sites/r1/public/_partials/ga4-events.js:1)
- [r1/scripts/replace-ga4.mjs](/home/eric/repos/sites/r1/scripts/replace-ga4.mjs:1)
- relevant `r1` page partials/templates

Forbidden paths:

- Cloud Build pipeline files owned by `SITES-1`

Required test additions:

- built-output assertions that CodeRadar loader/event calls are present
- regression that the site still builds when CodeRadar env is absent

Required commands:

```bash
cd /home/eric/repos/sites/r1 && npm install --no-workspaces
cd /home/eric/repos/sites/r1 && npm run build --no-workspaces || true
cd /home/eric/repos/sites/r1 && node scripts/check-links.js || true
```

Completion proof:

- one browser-side synthetic event reaches CodeRadar dev

### SITES-4

Repo and worktree:

- `/home/eric/repos/sites-r1-funnels`
- `codex/sites-r1-funnels-2026-05-15`

Depends on:

- `CR-2`
- `SITES-3`

Mission:

- Preserve attribution from `sites/r1` into product surfaces and CodeRadar.

Owned paths:

- [_shared/utm-capture.js](/home/eric/repos/sites/_shared/utm-capture.js:1)
- `r1` outbound links and any browser persistence helpers

Required test additions:

- UTM/referrer persistence regression
- outbound-link propagation regression

Completion proof:

- a user journey beginning on `sites/r1` arrives in CodeRadar with queryable attribution fields

### SITES-5

Repo and worktree:

- `/home/eric/repos/sites-pipeline-truth`
- `codex/sites-pipeline-truth-2026-05-15`

Depends on:

- `SITES-1`

Mission:

- Make the `sites` deploy pipeline truthful and reproducible for `r1`.

Owned paths:

- [cloudbuild-sites.yaml](/home/eric/repos/sites/cloudbuild-sites.yaml:1)
- [cloudbuild-deploy-sites.yaml](/home/eric/repos/sites/cloudbuild-deploy-sites.yaml:1)

Required work:

- normalize env substitutions for `dev`, `staging`, and `prod`
- make CodeRadar browser key/host secret wiring explicit if added
- remove pipeline comments that imply only GA4/PostHog paths exist if that becomes false

Completion proof:

- exact documented path for deploying `sites/r1` in each environment

### SITES-6

Repo and worktree:

- `/home/eric/repos/sites-r1-funnels`
- `codex/sites-r1-funnels-2026-05-15`

Depends on:

- `SITES-4`

Mission:

- Define and prove the R1 browser acquisition funnel in CodeRadar.

Required funnel checkpoints:

- page view
- CTA click
- docs visit
- downloads visit
- transition toward product/start flow

Completion proof:

- controller can query a real funnel view that starts on `sites/r1`

### R1-GTM-1

Repo and worktree:

- `/home/eric/repos/r1-agent-gtm-core`
- `codex/r1-gtm-core-2026-05-15`

Mission:

- Inventory backend/product events that are actually emitted and remove any fiction about missing ones.

Owned paths:

- [internal/analytics/taxonomy.go](/home/eric/repos/r1-agent/internal/analytics/taxonomy.go:1)
- [internal/analytics/analytics.go](/home/eric/repos/r1-agent/internal/analytics/analytics.go:1)
- [internal/hub/builtin/analytics_subscriber.go](/home/eric/repos/r1-agent/internal/hub/builtin/analytics_subscriber.go:1)
- [internal/hub/builtin/coderadar_subscriber.go](/home/eric/repos/r1-agent/internal/hub/builtin/coderadar_subscriber.go:1)
- [internal/hub/builtin/lifecycle_subscriber.go](/home/eric/repos/r1-agent/internal/hub/builtin/lifecycle_subscriber.go:1)
- [cmd/r1/main.go](/home/eric/repos/r1-agent/cmd/r1/main.go:1)

Required test additions:

- missing-event or dead-path regression tests in analytics/hub packages

Required commands:

```bash
cd /home/eric/repos/r1-agent && go test ./internal/analytics ./internal/hub/builtin -count=1 || true
cd /home/eric/repos/r1-agent && go test ./cmd/r1 -run TestMCPServeRuntime_NoCortex -count=1 -timeout=300s || true
```

Completion proof:

- exact event inventory classified as:
  - emitted now
  - code exists but not emitted
  - blocked by missing runtime path

### R1-GTM-2

Repo and worktree:

- `/home/eric/repos/r1-agent-gtm-core`
- `codex/r1-gtm-core-2026-05-15`

Depends on:

- `CR-1`
- `R1-GTM-1`

Mission:

- Make CodeRadar the real backend analytics transport in hosted R1 surfaces.

Owned paths:

- [services/r1-coord-api/main.go](/home/eric/repos/r1-agent/services/r1-coord-api/main.go:1)
- [services/r1-coord-api/internal/tracking/coderadar.go](/home/eric/repos/r1-agent/services/r1-coord-api/internal/tracking/coderadar.go:1)
- [services/r1-coord-api/internal/tracking/posthog.go](/home/eric/repos/r1-agent/services/r1-coord-api/internal/tracking/posthog.go:1)
- [services/r1-coord-api/internal/tracking/customerio.go](/home/eric/repos/r1-agent/services/r1-coord-api/internal/tracking/customerio.go:1)
- [services/r1-coord-api/internal/tracking/tracking_test.go](/home/eric/repos/r1-agent/services/r1-coord-api/internal/tracking/tracking_test.go:1)

Required test additions:

- payload shape tests for CodeRadar
- token-missing no-op behavior
- `/v1/telemetry/opt-in` fanout behavior

Required commands:

```bash
cd /home/eric/repos/r1-agent && go test ./services/r1-coord-api/... -count=1 || true
cd /home/eric/repos/r1-agent && go test ./internal/analytics ./internal/hub/builtin -count=1 || true
```

Completion proof:

- one backend synthetic event visible in CodeRadar dev

### R1-GTM-3

Repo and worktree:

- `/home/eric/repos/r1-agent-gtm-surface`
- `codex/r1-gtm-surface-2026-05-15`

Depends on:

- `CR-2`
- `R1-GTM-2`

Mission:

- Capture and persist browser-propagated attribution on the product/backend side.

Owned paths:

- browser-entry or request-handling code in `r1-agent` that receives product starts or telemetry opt-in
- any relevant auth/start handlers discovered during implementation

Required test additions:

- attribution ingestion regression
- no-consent/no-token safe behavior if required

Completion proof:

- product-side events are queryable in CodeRadar with linked attribution

### R1-GTM-4

Repo and worktree:

- `/home/eric/repos/r1-agent-gtm-surface`
- `codex/r1-gtm-surface-2026-05-15`

Depends on:

- `SITES-6`
- `R1-GTM-3`

Mission:

- Build the actual stitched R1 funnel and dashboard views.

Required outputs:

- visit -> CTA -> docs/download -> first session -> mission started -> mission completed

Completion proof:

- one saved query, dashboard, or test-backed query path that answers this flow

### R1-GTM-5

Repo and worktree:

- `/home/eric/repos/r1-agent-gtm-core`
- `codex/r1-gtm-core-2026-05-15`

Depends on:

- `CR-3`

Mission:

- Cut lifecycle triggers away from Customer.io assumptions and onto CodeRadar-native automation.

Completion proof:

- one end-to-end lifecycle trigger path running without Customer.io dependency

### R1-GTM-6

Repo and worktree:

- `/home/eric/repos/r1-agent-gtm-core`
- `codex/r1-gtm-core-2026-05-15`

Depends on:

- `CR-4`

Mission:

- Move operator/business reporting surfaces toward CodeRadar-backed revenue/support telemetry.

Completion proof:

- admin/operator views can retrieve the needed revenue/support data from CodeRadar paths

### R1-ADM-1

Repo and worktree:

- `/home/eric/repos/r1-agent-admin-hardening`
- `codex/r1-admin-hardening-2026-05-15`

Mission:

- Replace weak admin auth behavior with real verification.

Owned paths:

- [services/r1-admin/main.go](/home/eric/repos/r1-agent/services/r1-admin/main.go:1)
- [services/r1-admin/main_test.go](/home/eric/repos/r1-agent/services/r1-admin/main_test.go:1)

Required test additions:

- invalid bearer rejected
- malformed token rejected
- valid operator accepted
- regression that prefix-only auth no longer grants access

Required commands:

```bash
cd /home/eric/repos/r1-agent && go test ./services/r1-admin/... -count=1 || true
```

Completion proof:

- auth path is materially stronger than the audited partial implementation

### R1-ADM-2

Repo and worktree:

- `/home/eric/repos/r1-agent-admin-hardening`
- `codex/r1-admin-hardening-2026-05-15`

Depends on:

- `R1-ADM-1`

Mission:

- Replace easy placeholders with real existing data surfaces.

Completion proof:

- each visible admin section is either real or explicitly labeled partial

### R1-ADM-3

Repo and worktree:

- `/home/eric/repos/r1-agent-admin-hardening`
- `codex/r1-admin-hardening-2026-05-15`

Depends on:

- `R1-ADM-2`
- `R1-GTM-4`
- `R1-GTM-6`

Mission:

- Build the operator views that depend on the final GTM/business data.

Completion proof:

- hosted admin truthfully exposes the operator surfaces it claims

### R1-DSK-1

Repo and worktree:

- `/home/eric/repos/r1-agent-desktop-runtime`
- `codex/r1-desktop-runtime-2026-05-15`

Mission:

- Establish a truthful IPC base for the desktop app.

Owned paths:

- [desktop/src-tauri/src/ipc.rs](/home/eric/repos/r1-agent/desktop/src-tauri/src/ipc.rs:1)
- [desktop/src/types/ipc.d.ts](/home/eric/repos/r1-agent/desktop/src/types/ipc.d.ts:1)
- [desktop/src/types/ipc-const.ts](/home/eric/repos/r1-agent/desktop/src/types/ipc-const.ts:1)

Required test additions:

- registered command parity regression

Required commands:

```bash
cd /home/eric/repos/r1-agent/desktop && npm test || true
cd /home/eric/repos/r1-agent/desktop/src-tauri && cargo test || true
```

Completion proof:

- advertised IPC verbs align with real Tauri support or explicit unsupported-state handling

### R1-DSK-2

Repo and worktree:

- `/home/eric/repos/r1-agent-desktop-runtime`
- `codex/r1-desktop-runtime-2026-05-15`

Mission:

- Remove simulated assistant output from normal desktop session flow.

Owned paths:

- [desktop/src/panels/session-view.ts](/home/eric/repos/r1-agent/desktop/src/panels/session-view.ts:1)
- [desktop/src/state/sessionStore.ts](/home/eric/repos/r1-agent/desktop/src/state/sessionStore.ts:1)
- [desktop/src-tauri/src/transport.rs](/home/eric/repos/r1-agent/desktop/src-tauri/src/transport.rs:1)

Required test additions:

- regression that fake output is not used in real mode
- event-stream rendering test

Completion proof:

- normal session output comes from real runtime events

### R1-DSK-3

Repo and worktree:

- `/home/eric/repos/r1-agent-desktop-panels`
- `codex/r1-desktop-panels-2026-05-15`

Depends on:

- `R1-DSK-1`

Mission:

- Make skill catalog truthful and backed by real IPC, or explicitly partial.

Owned paths:

- [desktop/src/panels/skill-catalog.ts](/home/eric/repos/r1-agent/desktop/src/panels/skill-catalog.ts:1)

### R1-DSK-4

Repo and worktree:

- `/home/eric/repos/r1-agent-desktop-panels`
- `codex/r1-desktop-panels-2026-05-15`

Depends on:

- `R1-DSK-1`

Mission:

- Make ledger and memory views truthful and backed by real IPC, or explicitly partial.

Owned paths:

- [desktop/src/panels/ledger-viewer.ts](/home/eric/repos/r1-agent/desktop/src/panels/ledger-viewer.ts:1)
- [desktop/src/panels/ledger-node-drawer.ts](/home/eric/repos/r1-agent/desktop/src/panels/ledger-node-drawer.ts:1)
- [desktop/src/panels/memory-inspector.ts](/home/eric/repos/r1-agent/desktop/src/panels/memory-inspector.ts:1)

### R1-DSK-5

Repo and worktree:

- `/home/eric/repos/r1-agent-desktop-panels`
- `codex/r1-desktop-panels-2026-05-15`

Depends on:

- `R1-DSK-1`

Mission:

- Make settings/vault and MCP views truthful and backed by real IPC, or explicitly partial.

Owned paths:

- [desktop/src/panels/settings.ts](/home/eric/repos/r1-agent/desktop/src/panels/settings.ts:1)
- [desktop/src/panels/mcp-servers.ts](/home/eric/repos/r1-agent/desktop/src/panels/mcp-servers.ts:1)

### R1-DSK-6

Repo and worktree:

- `/home/eric/repos/r1-agent-desktop-panels`
- `codex/r1-desktop-panels-2026-05-15`

Depends on:

- `R1-DSK-1`

Mission:

- Make observability and scheduler views truthful and backed by real IPC, or explicitly partial.

Owned paths:

- [desktop/src/panels/observability.ts](/home/eric/repos/r1-agent/desktop/src/panels/observability.ts:1)
- [desktop/src/panels/scheduler.ts](/home/eric/repos/r1-agent/desktop/src/panels/scheduler.ts:1)

### R1-DSK-7

Nature:

- partially operator-gated

Mission:

- finish signing, notarization, and store release truth only after runtime and panel work is stable

### R1-INF-1

Repo and worktree:

- `/home/eric/repos/r1-agent-infra-rollout`
- `codex/r1-infra-rollout-2026-05-15`

Mission:

- Reconcile deploy scripts with live triggers and secrets.

Owned paths:

- [services/cloudbuild-deploy.yaml](/home/eric/repos/r1-agent/services/cloudbuild-deploy.yaml:1)
- `services/cloudbuild-bench-*.yaml`
- [services/deploy.sh](/home/eric/repos/r1-agent/services/deploy.sh:1)
- `services/scripts/**`

Required checks:

- `gcloud builds triggers list --project=relayone-488319`
- `gcloud secrets list --project=relayone-488319`
- `gcloud run services describe ...`

Completion proof:

- exact list of missing or stale triggers and secret bindings

### R1-INF-2

Repo and worktree:

- `/home/eric/repos/r1-agent-infra-rollout`
- `codex/r1-infra-rollout-2026-05-15`

Depends on:

- `R1-INF-1`
- `R1-GTM-2`

Mission:

- Normalize deploy wiring for the real analytics path and the live service footprint.

Completion proof:

- dev deploy can be executed with truthful env and trigger assumptions

### R1-INF-3

Owner:

- controller plus live-smoke reviewer

Depends on:

- `R1-INF-2`

Mission:

- Deploy changed hosted services to dev and verify them live.

Required checks:

```bash
curl -fsS https://api.dev.r1.run/livez
curl -fsS https://admin.dev.r1.run/livez
curl -fsS https://platform.dev.r1.run/livez
curl -fsS https://downloads.dev.r1.run/livez
curl -fsS https://api.dev.r1.run/v1/version
```

If GTM changed:

- send one synthetic browser event from `sites`
- send one synthetic backend event from `r1-agent`
- prove both arrive in CodeRadar dev

### R1-BEN-1

Repo and worktree:

- `/home/eric/repos/r1-agent-benchmark-ops`
- `codex/r1-benchmark-ops-2026-05-15`

Mission:

- Audit which TruthfulCompletion benchmark triggers and scripts actually exist.

### R1-BEN-2

Depends on:

- `R1-BEN-1`

Mission:

- Create or repair the missing benchmark trigger flow.

### R1-BEN-3

Depends on:

- `R1-BEN-2`

Mission:

- Make corpus curation workflow operational for the remaining missions.

### R1-BEN-4

Nature:

- partly operator-gated

Mission:

- execute the remaining corpus work in shards once the pipeline is sound

### R1-DOCS

Repo and worktree:

- `/home/eric/repos/r1-agent-docs-truth`
- `codex/r1-docs-truth-2026-05-15`

Depends on:

- all merged code and infra nodes

Mission:

- make docs trail the code and live state exactly

Owned paths:

- [README.md](/home/eric/repos/r1-agent/README.md:1)
- [docs/README.md](/home/eric/repos/r1-agent/docs/README.md:1)
- [docs/DEPLOYMENT.md](/home/eric/repos/r1-agent/docs/DEPLOYMENT.md:1)
- [docs/FEATURE-MAP.md](/home/eric/repos/r1-agent/docs/FEATURE-MAP.md:1)
- [docs/ARCHITECTURE.md](/home/eric/repos/r1-agent/docs/ARCHITECTURE.md:1)
- [plans/HANDOFF.md](/home/eric/repos/r1-agent/plans/HANDOFF.md:1)
- [plans/HANDOFF-deploy-state.md](/home/eric/repos/r1-agent/plans/HANDOFF-deploy-state.md:1)
- affected specs that mention GTM, admin, desktop, infra, or benchmark state

Completion proof:

- no doc claims more than the code and live checks support

## 9. Reviewer Nodes

### REVIEW-1

Purpose:

- code audit on each merge candidate

Reject if:

- stubbed or fake runtime path is still presented as real
- new path lacks tests
- docs overclaim code
- auth weakened
- deploy script lies about live infra

Review format:

- exact file path
- exact line number
- specific bug/risk
- actionable fix

### REVIEW-2

Purpose:

- live smoke on deploy-impacting merges

Checks:

- domain mappings
- touched `/livez` endpoints
- synthetic CodeRadar event visibility if analytics changed
- `sites/r1` public response and CTA path if marketing deploy changed

## 10. Merge Protocol

`coderadar`

- merge worker branches into the repo’s discovered integration branch first
- record the resulting SHA in the controller notes
- do not merge dependent `sites` or `r1-agent` lanes until the contract branch is fixed

`sites`

- merge worker branches into the repo’s discovered integration branch
- deploy only after browser analytics and pipeline changes pass review

`r1-agent`

- merge worker branches into `codex/program-integration-2026-05-15`
- run focused validation after every merge
- batch deploy-impacting changes where possible
- fast-forward `origin/dev` only after final verification

Merge command style:

```bash
git merge --no-ff <worker-branch>
```

Never:

- force-push shared branches
- rewrite history on integration branches
- merge unreviewed worker branches directly to `dev`

## 11. Final Verification

On the final `r1-agent` integration branch:

```bash
cd /home/eric/repos/r1-agent-program-controller && go build ./... || true
cd /home/eric/repos/r1-agent-program-controller && go test ./... -count=1 -timeout=300s || true
cd /home/eric/repos/r1-agent-program-controller/web && npm run lint || true
cd /home/eric/repos/r1-agent-program-controller/web && npm run typecheck || true
cd /home/eric/repos/r1-agent-program-controller/web && npm test || true
cd /home/eric/repos/r1-agent-program-controller/web && npm run build || true
cd /home/eric/repos/r1-agent-program-controller/desktop && npm test || true
cd /home/eric/repos/r1-agent-program-controller/desktop/src-tauri && cargo test || true
cd /home/eric/repos/r1-agent-program-controller && git diff --check
```

Live checks:

```bash
curl -fsS https://api.dev.r1.run/livez
curl -fsS https://admin.dev.r1.run/livez
curl -fsS https://platform.dev.r1.run/livez
curl -fsS https://downloads.dev.r1.run/livez
curl -fsS https://api.dev.r1.run/v1/version
```

For `sites/r1` if touched:

- load the live dev/staging/prod target depending on the actual deploy topology found in `SITES-1`
- verify browser event emission
- verify attribution continuity

For `coderadar` if touched:

- rerun ingest/person/dashboard/lifecycle/revenue package tests
- prove one synthetic browser event and one synthetic backend event are queryable

## 12. Honest Scope Boundary

These items are in the DAG because they are real remaining scope, but they may not all be finishable in one uninterrupted automation pass:

- full lifecycle automation if `coderadar` lacks major foundation pieces
- full revenue/support operator stack if the underlying primitives are still absent
- desktop signing/notarization/store release
- the remaining benchmark corpus curation shards

If any of these remain partial, the docs and handoff must say so plainly.

## 13. Exact Worker Handoff Format

Every worker must return exactly this:

```text
Branch:
Worktree:
Depends on:
Files changed:
Tests added:
Tests run:
Live checks run:
Blockers:
Truth now made true:
Merge risks:
```

## 14. Ready-To-Paste Worker Prompts

Use these prompts verbatim or with only branch/worktree substitutions.

### Prompt A: Coderadar Worker

```text
You own this lane only. You are not alone in the codebase. Do not revert other work. Adjust to existing changes instead of fighting them.

Repo/worktree:
<fill>

Branch:
<fill>

Node:
<fill>

Mission:
<fill from this execution sheet>

Owned paths:
<fill from this execution sheet>

Forbidden paths:
<fill from this execution sheet>

Requirements:
1. Start by reproducing the gap or writing the failing test.
2. Make the smallest real implementation.
3. Run focused tests.
4. Run one integration or smoke check if the node requires it.
5. Do not edit docs outside lane-local code comments or tiny test notes.
6. Return the handoff format exactly.
```

### Prompt B: Sites Worker

```text
You own this lane only. You are not alone in the codebase. Do not revert other work. Adjust to existing changes instead of fighting them.

Repo/worktree:
<fill>

Branch:
<fill>

Node:
<fill>

Mission:
<fill from this execution sheet>

Owned paths:
<fill from this execution sheet>

Forbidden paths:
<fill from this execution sheet>

Requirements:
1. Start by proving the current behavior in built output or runtime.
2. Add the smallest failing test or assertion possible.
3. Implement the minimum real fix.
4. Build the site and run the lane checks.
5. Return the handoff format exactly.
```

### Prompt C: R1 Worker

```text
You own this lane only. You are not alone in the codebase. Do not revert other work. Adjust to existing changes instead of fighting them.

Repo/worktree:
<fill>

Branch:
<fill>

Node:
<fill>

Mission:
<fill from this execution sheet>

Owned paths:
<fill from this execution sheet>

Forbidden paths:
<fill from this execution sheet>

Requirements:
1. Start with a failing test or a reproduced concrete gap.
2. Implement the smallest real fix.
3. Run focused tests.
4. If the node is deploy-impacting, note exactly what the controller must smoke after merge.
5. Do not edit broad docs.
6. Return the handoff format exactly.
```

## 15. Fresh Session Instruction

If starting from zero in a new Codex session, do this:

1. Open this file.
2. Run `ROOT-0`.
3. Create the worktrees in section 5.2.
4. Spawn workers only for nodes that are executable now.
5. Keep the controller on the critical path:
   - `CR-1`
   - `CR-2`
   - `SITES-2`
   - `R1-GTM-1`
   - `R1-ADM-1`
   - `R1-DSK-1`
   - `R1-DSK-2`
   - `R1-INF-1`
   - `R1-BEN-1`
6. Merge upstream dependencies before downstream lanes.
7. Run review and live smoke before advancing `origin/dev`.

If the session ever discovers that this sheet is stale, update the sheet first, then continue. The point of this file is to remain the executable truth of the program.
