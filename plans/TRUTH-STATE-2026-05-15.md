# Truth State — 2026-05-15

This file is the authoritative repo+infra snapshot after a direct code audit, live GCP inspection, Cloudflare zone inspection, and sibling-repo review of CodeRadar/Actium.

## Verified live infrastructure

- `r1.run` DNS is not pending. The Cloudflare `r1.run` zone already contains 12 `CNAME` records for `platform|api|downloads|admin` across `prod|staging|dev`, all pointing at `ghs.googlehosted.com` with proxy disabled.
- GCP domain mappings are healthy for all 12 public hostnames. `gcloud beta run domain-mappings list --project=relayone-488319 --region=us-central1` reports `Ready=True` for every `*.r1.run` mapping.
- All 12 public endpoints return `200` on `/livez`:
  - `platform.{,staging.,dev.}r1.run`
  - `api.{,staging.,dev.}r1.run`
  - `downloads.{,staging.,dev.}r1.run`
  - `admin.{,staging.,dev.}r1.run`
- The public Cloud Run footprint is 4 services × 3 envs:
  - `r1-coord-api-{prod,staging,dev}`
  - `r1-docs-{prod,staging,dev}`
  - `r1-downloads-cdn-{prod,staging,dev}`
  - `r1-admin-{prod,staging,dev}`
- `r1-browser` is not part of the live public footprint. No `r1-browser-{env}` Cloud Run services were present in `us-central1` during this audit.

## CodeRadar / GTM reality

- The canonical CodeRadar analytics repo is `/home/eric/repos/coderadar`, not `/home/eric/repos/CodeRadar`.
- Lowercase `coderadar` has real product-analytics primitives:
  - `/v1/track`, `/v1/identify`, `/v1/group`, `/v1/alias`
  - funnels, cohorts, feature flags, surveys, saved-query dashboards
- CodeRadar is a plausible replacement for lightweight product analytics in R1.
- The current `coderadar` integration branch now fixes the concrete person/profile and attribution read-path bugs found in the first audit, but lifecycle messaging/journey automation and revenue/support integrations are still absent.
- CodeRadar is not yet a full replacement for a PostHog + Customer.io stack:
  - broader lifecycle messaging/journey automation is not there
  - revenue/support integrations are not there

## Actium integration reality

- `actium-studio` is the cleanest real-world shipped CodeRadar integration:
  - browser DSN wiring
  - worker DSN wiring
  - health/smoke/reporting paths
- `actium-git` contains a richer CodeRadar code path, but the canonical deploy path is only partially wired for browser/server analytics.
- The reusable pattern for R1 is:
  - CodeRadar DSN for error/observability capture
  - CodeRadar project token for product-analytics `/v1/track` style events
  - graceful no-op when secrets are absent

## R1 tracking reality

- `services/r1-coord-api` has a small hosted tracking surface today: `/v1/telemetry/opt-in`.
- `/v1/telemetry/opt-in` now emits real CodeRadar `/v1/track` events when `CODERADAR_DSN` is present and flattens browser-attribution payloads into queryable properties such as `utm_*`, `referrer`, `landing_path`, and `attribution_ts`.
- The main Cloud Run deploy file now prefers `r1-<env>-shared-CODERADAR_DSN` and falls back to `relayone-coderadar-dsn`; it still does not wire PostHog or Customer.io secrets for the public services.
- Secret inventory visible during this audit showed:
  - `r1-{dev,staging,prod}-shared-{DATABASE_URL,AUTH_JWT_SECRET,ANTHROPIC_API_KEY}`
  - shared fallback `relayone-coderadar-dsn`
  - no visible `r1-*POSTHOG*`
  - no visible `r1-*CUSTOMERIO*`
- Result: any repo docs claiming shipped PostHog funnels or shipped Customer.io lifecycle for the hosted R1 SaaS are overstated.

## R1 code truth gaps found

- `internal/hub/builtin/coderadar_subscriber.go`, `analytics_subscriber.go`, and `lifecycle_subscriber.go` existed, but the repo docs overstated how completely they were wired in production.
- Desktop docs overstated runtime completeness:
  - several Tauri IPC verbs advertised in TypeScript are not registered in Rust
- Hosted admin docs overstated completeness:
  - `services/r1-admin` still renders placeholder sections
  - the hosted surface now verifies operator JWTs locally, but the repo docs still described the older bearer-prefix gate

## Remaining backlog that is real

- Desktop runtime completion remains largely open in `desktop/PLAN.md`, especially the post-scaffold IPC/runtime work and most of R1D-4 through R1D-12.
- Marketing / GTM / attribution / retention work remains open. CodeRadar now covers more of the hosted `coord-api` telemetry slice, but the live `sites/r1` browser rollout and lifecycle messaging remain incomplete.
- Operator-side infra still remains:
  - Cloud Build trigger creation beyond the basic `r1-agent-pr` / `r1-agent-ci` pair
  - the deferred 95-mission TruthfulCompletion corpus

## What changed in this truth-sync pass

- Docs/handoffs were updated to stop claiming DNS was pending.
- Docs/handoffs were updated to stop claiming the hosted GTM stack was fully shipped.
- The main `cmd/r1` event bus now actually registers the shipped analytics, lifecycle, and CodeRadar subscribers in the real binary, while remaining env-driven and no-op safe when credentials are absent.
- Hosted `coord-api` telemetry now emits CodeRadar `/v1/track` events with flattened browser attribution when `CODERADAR_DSN` is present.
- Public-service deploy wiring now prefers env-specific CodeRadar DSN secrets and falls back to `relayone-coderadar-dsn`.
- The TruthfulCompletion monthly/PR configs are documented as checked-in but not the current live GCP automation.
