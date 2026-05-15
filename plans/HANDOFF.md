# HANDOFF — verified state

**Last updated:** 2026-05-15

Use [`plans/TRUTH-STATE-2026-05-15.md`](TRUTH-STATE-2026-05-15.md) as the canonical source of truth. Older deploy handoffs in this directory are historical snapshots and contain stale claims.

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

## Confirmed partial / overstated areas

- Hosted admin is live but still scaffold-heavy.
- Desktop is not runtime-complete despite earlier “done” claims.
- PostHog / Customer.io / CodeRadar GTM claims were overstated:
  - client/subscriber code exists
  - hosted public deploy wiring is partial
  - CodeRadar is the best candidate for R1 product analytics, but it does not yet replace lifecycle messaging

## Real remaining backlog

- Deferred 95-mission TruthfulCompletion corpus (`plans/corpus-100.md`)
- Desktop runtime completion (`desktop/PLAN.md`)
- Marketing / GTM / attribution / retention backlog
- Real CodeRadar analytics project-token wiring for R1 if GTM reporting is to move off third-party tools
- Cloud Build trigger creation beyond the base `r1-agent-pr` / `r1-agent-ci` pair

## Historical files kept for reference

- `HANDOFF-deploy-state.md`
- `SCOPE-AUDIT-2026-05-04.md`
- `build-plan.md`
- `C5-bitbucket-pipelines-build-report.md`
- archive / audit subdirectories
