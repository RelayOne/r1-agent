# HANDOFF — verified state

**Last updated:** 2026-05-19

Use [plans/PROGRAM-DAG-2026-05-15.md](/home/eric/repos/r1-agent/plans/PROGRAM-DAG-2026-05-15.md:1) as the canonical supervisor file for a fresh Codex session. Use [plans/TRUTH-STATE-2026-05-15.md](/home/eric/repos/r1-agent/plans/TRUTH-STATE-2026-05-15.md:1) as historical truth-audit context, not as the live execution DAG.

## Spec queue

- No active `ready`, `draft`, or `in-progress` specs remain under `specs/`.
- The formal completion-SOW queue is closed.
- That does **not** mean the product is fully complete. Significant backlog remains outside the closed spec queue.

## Confirmed live state

- `r1.run` DNS is already live in Cloudflare.
- 12 public `r1.run` domain mappings are healthy in GCP and return `200` on `/livez`.
- The live public SaaS footprint is:
  - `r1-coord-api`
  - `r1-docs`
  - `r1-downloads-cdn`
  - `r1-admin`
  - each across `prod`, `staging`, and `dev`
- `origin/dev` is currently `f13576f7db384444cae8a4522e6087aa07451588`.
- Public `dev` is confirmed live on `f13576f`.

## Confirmed partial / overstated areas

- Hosted admin is live and now has real operator JWT verification plus runtime/coord-api summary, but business/session/user data surfaces remain scaffold-heavy.
- Desktop is not runtime-complete despite earlier “done” claims.
- The current promotion-critical desktop issue is not vague “desktop incompleteness”; it is a concrete CI mismatch:
  - `.github/workflows/desktop-augmentation.yml` makes e2e a required merge gate
  - the e2e helper assumes custom tauri-driver HTTP endpoints from `desktop/tests/e2e/helpers/tauri-driver-session.ts`
  - the corresponding app-side hooks/events are not present in `desktop/src` or `desktop/src-tauri`
  - the result is failing required checks on Linux, macOS, and Windows
- PostHog / Customer.io / CodeRadar GTM claims were overstated:
  - client/subscriber code exists
  - hosted `coord-api` now emits real CodeRadar `/v1/track` telemetry events, including browser-attribution properties on `/v1/telemetry/opt-in`
  - broader marketing/browser rollout and lifecycle messaging remain partial
  - CodeRadar is the best candidate for R1 product analytics, but it does not yet replace lifecycle messaging

## Real remaining backlog

- Promotion-critical:
  - clear the Cloud Build `go test -race` blocker on PR `#310`
  - resolve the required desktop e2e gate truthfully on PR `#310`
  - merge `dev -> staging`
  - confirm public staging matches `origin/staging`
  - promote `staging -> main`
  - confirm public prod matches `origin/main`
- Deferred 95-mission TruthfulCompletion corpus (`plans/corpus-100.md`)
- Desktop runtime completion (`desktop/PLAN.md`)
- Marketing / GTM / attribution / retention backlog
- Live `sites/r1` browser rollout plus broader GTM/lifecycle cutover if reporting is to move fully onto CodeRadar
- Cloud Build trigger creation beyond the base `r1-agent-pr` / `r1-agent-ci` pair

## Current promotion truth

- PR `#310` (`dev -> staging`) is open and blocked.
- `origin/staging` and `origin/main` are both still `c3bfbee2bbab56bc21d4f115a6dc7bcaf1d1116e`.
- Public `staging` is still serving `4dcd5ef`, not `c3bfbee`.
- Public `prod` is still serving `d536eb0`, not `c3bfbee`.
- Any fresh session should start from the canonical DAG and clear the `CI-1` and `CI-2` nodes before attempting further promotion.

## Historical files kept for reference

| File | Status |
|---|---|
| `HANDOFF.md` (this file) | current snapshot |
| `PROGRAM-DAG-2026-05-15.md` | canonical multi-agent execution DAG |
| `corpus-100.md` | deferred roadmap (operator-curated) |
| `PROGRAM-EXECUTION-SHEET-2026-05-15.md` | historical bootstrap sheet; defer to `PROGRAM-DAG-2026-05-15.md` |
| `build-plan.md` | superseded by merged commits; left for history |
| `C5-bitbucket-pipelines-build-report.md` | historical |
| `SCOPE-AUDIT-2026-05-04.md` | historical; items either merged or operator-action |
| `HANDOFF-deploy-state.md` | deployment-state snapshot from 2026-05-05 |
| `LAUNCH-E1-E4.sh` | operator launch script |
| subdirs (archive, audits, monitor, self-fix, scope-suite-*) | historical artifacts |
