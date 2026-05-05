# r1.run Deployment State — Snapshot 2026-05-05 ~05:25 UTC (12/12 LIVE, prod auth wired)

## Live URLs (all 12 services 200 on /livez + /readyz)

| Env     | r1-coord-api                                  | r1-docs                                  | r1-downloads-cdn                                  | r1-admin                                  |
|---------|-----------------------------------------------|------------------------------------------|---------------------------------------------------|-------------------------------------------|
| dev     | r1-coord-api-dev-2sobff3gmq-uc.a.run.app      | r1-docs-dev-2sobff3gmq-uc.a.run.app      | r1-downloads-cdn-dev-2sobff3gmq-uc.a.run.app      | r1-admin-dev-2sobff3gmq-uc.a.run.app      |
| staging | r1-coord-api-staging-2sobff3gmq-uc.a.run.app  | r1-docs-staging-2sobff3gmq-uc.a.run.app  | r1-downloads-cdn-staging-2sobff3gmq-uc.a.run.app  | r1-admin-staging-2sobff3gmq-uc.a.run.app  |
| prod    | r1-coord-api-prod-2sobff3gmq-uc.a.run.app     | r1-docs-prod-2sobff3gmq-uc.a.run.app     | r1-downloads-cdn-prod-2sobff3gmq-uc.a.run.app     | r1-admin-prod-2sobff3gmq-uc.a.run.app     |

Cloud Run reserves `/healthz` on this org's frontend; r1 services additionally answer `/livez`, `/readyz`, `/v1/version`, and `/`.

## Deployed image tags (all 3 envs identical)

| Service            | Tag        | Notes                                                                |
|--------------------|------------|----------------------------------------------------------------------|
| r1-coord-api       | `244f87d8` | Includes JwtService, RelayOneSsoClient, PostHog/CustomerIO/CodeRadar |
| r1-docs            | `bf49ec45` | Static-rendered Markdown docs                                        |
| r1-downloads-cdn   | `bf49ec45` | Streams gs://relayone-488319-r1-releases                             |
| r1-admin           | `57f88598` | Server-rendered Go admin (9 routes; `requireOperator` middleware)    |

`/v1/version` confirms `244f87d8` reporting from coord-api in all 3 envs.

## Secrets in Secret Manager (Cloud Run SA `188548470397-compute@…` has `secretAccessor`)

Per env (`{prod,staging,dev}`):
- `r1-<env>-shared-DATABASE_URL` — Cloud SQL DSN (placeholder; operator must populate before real DB use)
- `r1-<env>-shared-ANTHROPIC_API_KEY` — Anthropic key (placeholder)
- `r1-<env>-shared-AUTH_JWT_SECRET` — 48 random base64 bytes generated 2026-05-05 (live; coord-api uses these for HS256)

Wiring in `services/deploy.sh`:
```
r1-coord-api → DATABASE_URL=…:latest, AUTH_JWT_SECRET=…:latest
```

## Pending operator actions

### 1. Domain mappings to r1.run (DNS only)
After `gcloud domains verify r1.run` is complete in Search Console, run:
```bash
for ENV in prod staging dev; do
  SUB=""
  [ "$ENV" = "staging" ] && SUB=".staging"
  [ "$ENV" = "dev" ]     && SUB=".dev"
  gcloud beta run domain-mappings create --service=r1-docs-$ENV          --domain=platform$SUB.r1.run    --region=us-central1
  gcloud beta run domain-mappings create --service=r1-coord-api-$ENV     --domain=api$SUB.r1.run         --region=us-central1
  gcloud beta run domain-mappings create --service=r1-downloads-cdn-$ENV --domain=downloads$SUB.r1.run   --region=us-central1
  gcloud beta run domain-mappings create --service=r1-admin-$ENV         --domain=admin$SUB.r1.run       --region=us-central1
done
```
Each mapping returns CNAME records — add them to Cloudflare with **proxy=OFF (gray cloud)**, otherwise Cloud Run cannot terminate TLS.

### 2. Populate real secret values (currently placeholders)
- `DATABASE_URL` — once Cloud SQL is finished provisioning, run `gcloud sql users set-password` and form the DSN: `postgresql://r1@/r1?host=/cloudsql/relayone-488319:us-central1:r1-<env>-pg`
- `ANTHROPIC_API_KEY` — paste the workspace key
- `AUTH_JWT_SECRET` — already populated with a 48-byte random value (rotate via `gcloud secrets versions add`)

### 3. Wire Cloud Build triggers
```bash
./services/scripts/setup-cloudbuild-triggers.sh
```
Creates 3 triggers: `r1-deploy-prod` (push to `main`), `r1-deploy-staging` (push to `staging`), `r1-deploy-dev` (push to `dev`).

### 4. Apply branch protection
```bash
./scripts/setup-branch-protection.sh
```
Creates `dev` + `staging` if missing; sets required-PR-review + status-checks on `main` and `staging`; leaves `dev` open for direct commits.

### 5. CLAUDE.md package map (harness blocks agent edits to CLAUDE.md)
Insert the following line after the existing `handoff/` line in `/home/eric/repos/r1-agent/CLAUDE.md`:
```
antitrunc/                         Anti-truncation enforcement (layered defense against scope self-reduction)
```

## Smoke check output (most recent run)

```
=== /v1/version on coord-api (all 3 envs) ===
r1-coord-api-dev      {"env":"dev","service":"r1-coord-api","version":"244f87d8"}
r1-coord-api-staging  {"env":"staging","service":"r1-coord-api","version":"244f87d8"}
r1-coord-api-prod     {"env":"prod","service":"r1-coord-api","version":"244f87d8"}

=== /livez + /readyz on all 12 services ===
r1-coord-api-dev          livez=OK readyz=OK
r1-docs-dev               livez=OK readyz=OK
r1-downloads-cdn-dev      livez=OK readyz=OK
r1-admin-dev              livez=OK readyz=OK
r1-coord-api-staging      livez=OK readyz=OK
r1-docs-staging           livez=OK readyz=OK
r1-downloads-cdn-staging  livez=OK readyz=OK
r1-admin-staging          livez=OK readyz=OK
r1-coord-api-prod         livez=OK readyz=OK
r1-docs-prod              livez=OK readyz=OK
r1-downloads-cdn-prod     livez=OK readyz=OK
r1-admin-prod             livez=OK readyz=OK
```

## Recent fixes (this session)

- `services/deploy.sh`: per-service tag resolution via `resolve_tag()` (was applying one global TAG, broke services not yet rebuilt at the requested SHA).
- `services/deploy.sh`: bound `AUTH_JWT_SECRET=r1-<env>-shared-AUTH_JWT_SECRET:latest` to all coord-api envs (prev only DATABASE_URL).
- Created `r1-{prod,staging,dev}-shared-AUTH_JWT_SECRET` in Secret Manager + granted Cloud Run SA `secretAccessor`. Without this, `r1-coord-api-prod` Fatalfs at startup with `AUTH_JWT_SECRET must be set in prod`.
- Upgraded coord-api in all 3 envs from `bf49ec45` (no auth) to `244f87d8` (full auth + tracking).
- Created `r1-admin-{dev,staging,prod}` services with image `57f88598`.

## Known pre-existing test failures (separate triage)

- `internal/coderadar`: `TestParseDSNRawKey` expects `https://ingest.coderadar.app/v1`, code returns `https://api.coderadar.app/v1`. Either the test fixture is stale or the parsing code regressed; pick whichever matches the actual coderadar contract.
- `internal/scan`: `TestSelfScan` blocks the `//nolint:gocyclo` directive at `internal/server/sessionhub/sessionhub.go:351`. Either refactor `validateWorkdir` to fall under the gocyclo threshold OR add a selfscan exception for this exact directive.
