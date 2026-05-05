# r1.run Deployment State — Snapshot 2026-05-04 ~20:55 PDT (all envs LIVE)

## Live URLs (all 200 on /livez)

| Env     | r1-coord-api | r1-docs | r1-downloads-cdn |
|---------|--------------|---------|------------------|
| dev     | r1-coord-api-dev-2sobff3gmq-uc.a.run.app | r1-docs-dev-2sobff3gmq-uc.a.run.app | r1-downloads-cdn-dev-2sobff3gmq-uc.a.run.app |
| staging | r1-coord-api-staging-2sobff3gmq-uc.a.run.app | r1-docs-staging-2sobff3gmq-uc.a.run.app | r1-downloads-cdn-staging-2sobff3gmq-uc.a.run.app |
| prod    | r1-coord-api-prod-2sobff3gmq-uc.a.run.app | r1-docs-prod-2sobff3gmq-uc.a.run.app | r1-downloads-cdn-prod-2sobff3gmq-uc.a.run.app |

Cloud Run reserves `/healthz` on this org's frontend; my services additionally answer
`/livez`, `/readyz`, `/v1/version`, and `/`.

## Pending operator actions for r1.run domain mapping

1. Verify ownership of `r1.run`:
   ```bash
   gcloud domains verify r1.run
   ```
   Adds a TXT record requirement at the Search Console; copy the value.

2. Add the TXT record to Cloudflare (your `r1.run` zone, root):
   - Type: TXT
   - Host: `@` (or `r1.run`)
   - Value: `google-site-verification=<TOKEN>`
   - TTL: auto

3. Wait for DNS propagation (~5-10 min), then click verify in Search Console.

4. Create the 9 domain mappings:
   ```bash
   for ENV in prod staging dev; do
     SUB=""
     [ "$ENV" = "staging" ] && SUB=".staging"
     [ "$ENV" = "dev" ] && SUB=".dev"
     gcloud beta run domain-mappings create --service=r1-docs-$ENV          --domain=platform$SUB.r1.run    --region=us-central1
     gcloud beta run domain-mappings create --service=r1-coord-api-$ENV     --domain=api$SUB.r1.run         --region=us-central1
     gcloud beta run domain-mappings create --service=r1-downloads-cdn-$ENV --domain=downloads$SUB.r1.run   --region=us-central1
   done
   ```

5. Each mapping returns CNAME records you add to Cloudflare for the corresponding subdomains.

# Original snapshot 2026-05-04 ~20:30 PDT

## Where we are

### Code
- 4 specs (6/7/8/9) merged into `claude/w521-eliminate-stoke-leftovers-2026-05-02`
- Pushed to `origin/claude/w521-eliminate-stoke-leftovers-2026-05-02`
- `go build ./...` clean, `go vet ./...` clean
- 99% Go tests pass — 2 pre-existing failures unrelated to merges:
  - `internal/coderadar`: `TestParseDSNRawKey` (test/code drift on baseURL)
  - `internal/scan`: `TestSelfScan` (1 nolint flagged in `internal/server/sessionhub/sessionhub.go:351`)

### Infrastructure on `relayone-488319`
- Cloud SQL: `r1-prod-pg` RUNNABLE (POSTGRES_16, db-g1-small, `136.113.29.19`)
- Cloud SQL: `r1-staging-pg` PENDING_CREATE (db-f1-micro, `35.239.73.209`)
- Cloud SQL: `r1-dev-pg` PENDING_CREATE (db-f1-micro, `34.41.150.94`)
- Artifact Registry: `us-central1-docker.pkg.dev/relayone-488319/r1` created
- Secrets: 6 placeholders created
  - `r1-{prod,staging,dev}-shared-{DATABASE_URL,ANTHROPIC_API_KEY}` — values are "placeholder-set-by-operator"
- Container images: 3 builds in progress (Cloud Build IDs 418c3541, 6823f209, 55ba4721)
  - `r1-coord-api:bf49ec45`
  - `r1-docs:bf49ec45`
  - `r1-downloads-cdn:bf49ec45`

### Services scaffolded on `claude/w521-…`
- `services/r1-coord-api/` — Go service, ~150 LOC, /healthz + /v1/license/verify + /v1/telemetry/opt-in stubs
- `services/r1-docs/` — Go service that embeds docs/*.md and renders to HTML
- `services/r1-downloads-cdn/` — Go service streaming gs://relayone-488319-r1-releases/{env}/<asset>
- `services/deploy.sh` — `./services/deploy.sh {prod|staging|dev|all}` driver

### What still needs to happen (in order)
1. ⏳ Cloud SQL r1-staging-pg + r1-dev-pg → RUNNABLE (~5 more min)
2. ⏳ Container image builds → SUCCESS (~3 more min)
3. ⏯ Operator action: set real values for the 6 secret placeholders
4. ⏯ Operator action: add Cloud Run domain-verification TXT record to `r1.run` zone on Cloudflare
5. Run `./services/deploy.sh all` — 9 Cloud Run services come up
6. /healthz smoke check across all 9
7. After DNS verifies: create domain mappings
   - `platform.r1.run` → `r1-docs-prod`
   - `platform.staging.r1.run` → `r1-docs-staging`
   - `platform.dev.r1.run` → `r1-docs-dev`
   - `api.r1.run` → `r1-coord-api-prod`
   - `api.staging.r1.run` → `r1-coord-api-staging`
   - `api.dev.r1.run` → `r1-coord-api-dev`
   - `downloads.r1.run` → `r1-downloads-cdn-prod`
   - `downloads.staging.r1.run` → `r1-downloads-cdn-staging`
   - `downloads.dev.r1.run` → `r1-downloads-cdn-dev`
8. `gcloud beta run domain-mappings create …` × 9
9. Final verification: `curl https://platform.r1.run/healthz` etc.

### Known blockers (need operator action)
- Spec 9 item 21: CLAUDE.md package map line; harness denies CLAUDE.md edits to agents. Operator adds the line manually:
  ```
  antitrunc/                         Anti-truncation enforcement (layered defense against scope self-reduction)
  ```
  Insert after `handoff/` line in `/home/eric/repos/r1-agent/CLAUDE.md`.

- DNS verification on `r1.run`: Cloud Run won't accept domain mappings until ownership is verified.
  Operator needs to:
  1. `gcloud domains verify r1.run` — produces a TXT record value.
  2. Add the TXT record to Cloudflare DNS (`r1.run` zone).
  3. `gcloud run domain-mappings list` will then accept new mappings.

### Pre-existing test failures to triage
- `TestParseDSNRawKey`: expects `https://ingest.coderadar.app/v1`, code returns `https://api.coderadar.app/v1`. Test fixture stale OR coderadar package's parsing logic regressed. Either fix the test or the parsing code (whichever matches actual contract with the coderadar service).
- `TestSelfScan`: `internal/server/sessionhub/sessionhub.go:351` carries `//nolint:gocyclo // straight-line guard sequence — splitting it would obscure the rule list.` The selfscan rule blocks any `//nolint` directive. Resolution: either refactor `validateWorkdir` to avoid gocyclo, OR add an exception to the selfscan rule for this specific case.
