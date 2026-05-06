# Deployment

Operations guide for r1, covering both the **local agent runtime** (CLI + daemon + UIs) and the **hosted SaaS** (`r1.run` — 9 Cloud Run services on `relayone-488319`).

A new operator should be able to deploy this stack at 3 a.m. during an outage following only this doc.

---

## Audience

- DevOps / SRE deploying or operating r1 in production
- Engineers running `r1 serve` locally
- The on-call person debugging a `r1.run` incident

---

## Build & verification gate

Three commands are the CI gate. They MUST be green on every PR:

```bash
go build ./...
go test ./... -count=1 -timeout=300s
go vet ./...
```

Web side:

```bash
cd web
npm ci
npm run build      # tsc --noEmit && vite build && node scripts/verify-build-output.mjs
npm run test       # vitest run
```

Desktop side:

```bash
cd desktop
cargo build
cargo test
npm run build      # webview
```

Additional gates:

| Gate | Command | Required? |
|---|---|---|
| Race detector | `go test -race ./... -count=1 -timeout=600s` | required (advisory on flake) |
| chdir-lint | `make lint-chdir` (AST walker) | required (multi-session safety) |
| view-without-api | `make lint-views` | required (after spec 8 merge) |
| antitrunc verify | `r1 antitrunc verify -n 20` | required (post-commit hook + CI) |
| `golangci-lint` | `make lint` | advisory |
| `govulncheck` + `gosec` | `make security` | required (stdlib bumps via Go upgrade PR) |
| Release-rehearsal E2E (push-to-main) | `services/cloudbuild-e2e.yaml` via Cloud Build trigger `r1-agent-e2e-rehearsal-main` | required for any release that gates on it (post-deploy verification — runs *after* merge to main) |
| Release-rehearsal E2E (tag) | `services/cloudbuild-e2e.yaml` via Cloud Build trigger `r1-agent-e2e-rehearsal-tag` | required — red blocks tag promotion |

CI runs all of these via `cloudbuild.yaml`. The web gate runs first (fail-fast on web breakage); the Go gate waits for it via `waitFor`.

---

## Local runtime — `r1 serve`

### Prerequisites

- Go 1.25+ (CGO enabled — required for SQLite)
- Node 20+ (for `web/`)
- Rust + Tauri 2 toolchain (only if building desktop)
- ~/.r1/ directory writable
- For multi-session: kernel that supports advisory file locks (`gofrs/flock`)

### Install paths

```bash
# 1. Hosted binary CDN — production channel
curl -fsSL https://downloads.r1.run/prod/r1-$(uname -s | tr A-Z a-z)-$(uname -m | sed s/x86_64/amd64/) -o r1
chmod +x r1 && sudo mv r1 /usr/local/bin/

# 2. From source (Go 1.25+, CGO for SQLite)
git clone https://github.com/RelayOne/r1-agent && cd r1-agent
go build ./cmd/r1
sudo mv r1 /usr/local/bin/

# 3. One-line installer (legacy; verifies cosign signature when cosign is on PATH)
curl -fsSL https://raw.githubusercontent.com/RelayOne/r1-agent/main/install.sh | bash

# 4. Verify a signed release tarball
cosign verify-blob \
  --certificate-identity-regexp 'https://github\.com/(RelayOne/r1|ericmacdougall/Stoke)/\.github/workflows/release\.yml@refs/tags/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature r1_<ver>_<os>_<arch>.tar.gz.sig \
  r1_<ver>_<os>_<arch>.tar.gz
```

### Run the daemon

```bash
r1 serve                              # spawn-on-demand (Watchman pattern); single-instance via gofrs/flock
r1 serve --install                    # install per-OS service unit (launchd / systemd-user / Windows SCM)
r1 serve --uninstall                  # remove service unit
r1 serve --status                     # report service unit state
```

`r1 serve` is idempotent — the second invocation exits non-zero with a clear message if a daemon is already running.

### Files the daemon writes

```
~/.r1/daemon.lock           # advisory file lock (gofrs/flock)
~/.r1/daemon.json           # mode 0600 — port + 32-byte hex token (rotated on every start)
~/.r1/sessions-index.json   # atomic + fsync — list of known sessions
$XDG_RUNTIME_DIR/r1/r1.sock # unix socket (Linux/macOS); peer-cred check
~/.r1/cortex/curator-audit.jsonl # MemoryCurator's auto-curate audit log
audit/antitrunc/post-commit-<sha>.md # post-commit hook output (per-repo)
<workdir>/.r1/sessions/<id>/journal.ndjson  # per-session journal (fsync on terminal events)
```

### Connecting a UI

```bash
# Web — loopback only by default; CSP locked
open http://127.0.0.1:7777/

# TUI
r1 chat --interactive

# Desktop (Tauri 2)
r1 desktop      # if installed; or open the platform-specific bundle
```

### Authentication for connecting UIs

| Surface | Auth mechanism |
|---|---|
| Unix socket (CLI) | Peer-cred check; UID must match daemon owner |
| Windows named pipe (CLI) | SDDL granting current SID + LocalSystem |
| Loopback HTTP | 256-bit Bearer token; Origin pin + Host pin |
| Loopback WS | `Sec-WebSocket-Protocol: r1.lanes.v1, <token>` (WS subprotocol auth) |

---

## Hosted SaaS — `r1.run`

### Prerequisites

- GCP project: `relayone-488319` with these APIs enabled:
  - Cloud Run (`run.googleapis.com`)
  - Cloud SQL Admin (`sqladmin.googleapis.com`)
  - Secret Manager (`secretmanager.googleapis.com`)
  - Artifact Registry (`artifactregistry.googleapis.com`)
  - Cloud Build (`cloudbuild.googleapis.com`)
  - IAM Service Account Credentials (`iamcredentials.googleapis.com`)
- Cloudflare DNS zone for `r1.run`
- Domain ownership verified at Search Console (TXT record on root)
- Operator with `roles/run.admin`, `roles/secretmanager.admin`, `roles/cloudsql.admin`, `roles/artifactregistry.admin` on the project

### Cloud Run services (live)

| Service | Image | Domain |
|---|---|---|
| r1-coord-api-{prod,staging,dev} | us-central1-docker.pkg.dev/relayone-488319/r1/r1-coord-api:<sha> | api.{,staging.,dev.}r1.run |
| r1-docs-{prod,staging,dev} | us-central1-docker.pkg.dev/relayone-488319/r1/r1-docs:<sha> | platform.{,staging.,dev.}r1.run |
| r1-downloads-cdn-{prod,staging,dev} | us-central1-docker.pkg.dev/relayone-488319/r1/r1-downloads-cdn:<sha> | downloads.{,staging.,dev.}r1.run |

Per the standing GCP rules:
- Region: `us-central1` (Tier 1 pricing)
- Min-instances: 1 (no cold starts)
- Billing: instance-based (no CPU throttling)
- Memory: 512 Mi minimum (required by `--no-cpu-throttling`)
- Concurrency: 80
- `--allow-unauthenticated` (auth handled at app layer once Path-A Go port lands)

### Cloud SQL instances (live)

| Instance | Tier | Version | Notes |
|---|---|---|---|
| r1-prod-pg | db-g1-small | POSTGRES_16 | ~$10/mo; us-central1-c |
| r1-staging-pg | db-f1-micro | POSTGRES_16 | ~$7/mo; us-central1-c |
| r1-dev-pg | db-f1-micro | POSTGRES_16 | ~$7/mo; us-central1-c |

All ENTERPRISE edition. Backups daily at 09:00 UTC. Storage SSD 10 GB, auto-increase enabled. Public IPs only — no VPC peering yet (operator follow-up).

### Secret Manager (placeholders — operator must populate)

```
r1-prod-shared-DATABASE_URL          (placeholder — set the real connection string)
r1-prod-shared-ANTHROPIC_API_KEY     (placeholder)
r1-staging-shared-DATABASE_URL
r1-staging-shared-ANTHROPIC_API_KEY
r1-dev-shared-DATABASE_URL
r1-dev-shared-ANTHROPIC_API_KEY
```

To set a real value:
```bash
gcloud secrets versions add r1-prod-shared-DATABASE_URL \
  --data-file=- <<<'postgresql://r1-prod:<pw>@<ip>/r1-prod?sslmode=require'
```

### Artifact Registry

```
us-central1-docker.pkg.dev/relayone-488319/r1/r1-coord-api:<sha>      # 3.2 MB
us-central1-docker.pkg.dev/relayone-488319/r1/r1-docs:<sha>           # 4.3 MB
us-central1-docker.pkg.dev/relayone-488319/r1/r1-downloads-cdn:<sha>  # 7.0 MB
```

All distroless static (`gcr.io/distroless/static-debian12:nonroot`). Multi-stage builds; no glibc; no shell.

### GCS

```
gs://relayone-488319-r1-releases/{prod,staging,dev}/<asset>
```

Per-channel binaries land here via `cloudbuild-binaries.yaml` on tag push (existing trigger). The `r1-downloads-cdn` Cloud Run service streams them out via a service account with `roles/storage.objectViewer`.

### Domain mappings

```bash
gcloud beta run domain-mappings list --region=us-central1 --filter="domain~r1.run"
```

9 domains (created; pending DNS):
- platform.r1.run / platform.staging.r1.run / platform.dev.r1.run
- api.r1.run / api.staging.r1.run / api.dev.r1.run
- downloads.r1.run / downloads.staging.r1.run / downloads.dev.r1.run

Each maps to its Cloud Run service via CNAME → `ghs.googlehosted.com.`.

### Cloudflare DNS — operator action

Add 9 CNAME records to the `r1.run` zone:

| Host | Type | Value | Proxy |
|---|---|---|---|
| `platform` | CNAME | `ghs.googlehosted.com.` | OFF (gray cloud) |
| `api` | CNAME | `ghs.googlehosted.com.` | OFF |
| `downloads` | CNAME | `ghs.googlehosted.com.` | OFF |
| `platform.staging` | CNAME | `ghs.googlehosted.com.` | OFF |
| `api.staging` | CNAME | `ghs.googlehosted.com.` | OFF |
| `downloads.staging` | CNAME | `ghs.googlehosted.com.` | OFF |
| `platform.dev` | CNAME | `ghs.googlehosted.com.` | OFF |
| `api.dev` | CNAME | `ghs.googlehosted.com.` | OFF |
| `downloads.dev` | CNAME | `ghs.googlehosted.com.` | OFF |

**Critical**: Proxy MUST be OFF (gray cloud, not orange). Cloud Run provisions Google-managed TLS certs; Cloudflare proxy mode strips them.

After CNAMEs propagate (5-15 min), Google-managed certs auto-provision on each domain mapping.

---

## Auto-deploy

`services/cloudbuild-deploy.yaml` — one Cloud Build pipeline, three triggers (one per env). Each trigger fires on push to its branch (`main` → prod, `staging` → staging, `dev` → dev).

Pipeline:
1. Three image builds in parallel (Docker layer cache via Artifact Registry).
2. Three pushes in parallel.
3. Three Cloud Run deploys in parallel (after all pushes), with env-specific secret bindings.
4. Smoke check — curl `/livez` on each service, 5 retries × 2 s.

Operator action — create the 3 triggers after PR #128 merges:

```bash
./services/scripts/setup-cloudbuild-triggers.sh
```

Or manually:
```bash
gcloud builds triggers create github \
  --name=r1-services-prod-deploy \
  --repo-owner=RelayOne --repo-name=r1-agent \
  --branch-pattern='^main$' \
  --build-config=services/cloudbuild-deploy.yaml \
  --substitutions=_ENV=prod
```

---

## Manual deploy

For ad-hoc deploys (e.g. hotfix, before triggers are wired):

```bash
TAG=$(git rev-parse --short HEAD) ./services/deploy.sh dev
TAG=$(git rev-parse --short HEAD) ./services/deploy.sh staging
TAG=$(git rev-parse --short HEAD) ./services/deploy.sh prod
TAG=$(git rev-parse --short HEAD) ./services/deploy.sh all   # all 3 envs sequentially
```

The script runs `gcloud run deploy` for each of the 3 services × N envs, then smoke-checks `/livez` on each deployed service.

---

## Branch protection

After PR #128 merges, run:

```bash
./scripts/setup-branch-protection.sh
```

This:
1. Creates `staging` and `dev` branches from current `main` (idempotent — won't overwrite if they exist).
2. Applies branch protection:
   - **main**: requires PR + 1 reviewer + status checks + no force-push
   - **staging**: requires PR + 1 reviewer + status checks + no force-push
   - **dev**: status checks only; allows direct commits for "minor one-off bug fixes" per the standing rules

---

## Monitoring

### Health endpoints

Cloud Run org policy intercepts `/healthz` on this project, so r1 services use:

```
GET /livez       — liveness
GET /readyz      — readiness
GET /v1/version  — version + env
GET /            — service metadata
```

Smoke-check all 9 services:

```bash
for ENV in dev staging prod; do
  for SVC in r1-coord-api r1-docs r1-downloads-cdn; do
    URL=$(gcloud run services describe $SVC-$ENV --region=us-central1 --format='value(status.url)')
    CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 10 "$URL/livez")
    printf "%-30s %s %s\n" "$SVC-$ENV" "$CODE" "$URL"
  done
done
```

Expected: 9 × `200`.

### Logs

```bash
gcloud run services logs read r1-coord-api-prod --region=us-central1 --limit=100
gcloud run services logs read r1-docs-staging --region=us-central1 --limit=100 --severity=ERROR
```

### Alerting

Not yet wired. Recommended (operator follow-up):
- Cloud Monitoring uptime check on each `/livez` endpoint at 1-min interval.
- Alerting policy on 5xx rate > 1% over 5-min window.
- Alerting on Cloud SQL CPU > 80% for 5 min.

### CodeRadar dogfood (planned)

Once Path-A auth + CodeRadar DSN secrets land, every panic + recovered error in the Go services will route to the in-house CodeRadar instance. DSN per env in Secret Manager: `r1-{env}-shared-CODERADAR_DSN`.

---

## Rollback

### Cloud Run service rollback

```bash
# List revisions
gcloud run revisions list --service=r1-coord-api-prod --region=us-central1

# Roll back traffic to a previous revision
gcloud run services update-traffic r1-coord-api-prod \
  --region=us-central1 \
  --to-revisions=r1-coord-api-prod-00005-abc=100
```

### Branch rollback

```bash
# Identify the bad commit
git log --oneline -10 main

# Revert (preserves history; safer than reset)
git revert <bad-sha>
git push origin main
```

The auto-deploy trigger fires on the new `main` HEAD and rolls forward to a fresh deployment with the revert.

### Cloud SQL rollback

Daily backups at 09:00 UTC. To restore:

```bash
gcloud sql backups list --instance=r1-prod-pg
gcloud sql backups restore <backup-id> --restore-instance=r1-prod-pg
```

⚠ Restore overwrites the live instance. Take a fresh on-demand backup first if you need to forensic the bad state.

---

## Disaster recovery

| Scenario | Recovery path | RTO | RPO |
|---|---|---|---|
| Cloud Run service crash-looping | `gcloud run services update-traffic ... --to-revisions=<prev>=100` | <5 min | 0 |
| Cloud SQL instance unhealthy | Backup restore | 15-30 min | 24 hours (next-day backup) |
| Bad commit on main | `git revert` + auto-deploy | 5-10 min | 0 |
| GCP project compromised | Recreate from terraform (TODO: terraform doesn't exist yet — operator follow-up) | days | 24 hours |
| `r1.run` zone hijacked | Rotate Cloudflare credentials + restore from backup zone export | 15 min after detection | 0 |
| Loss of `relayone-488319-r1-releases` GCS bucket | `gsutil rsync` from a backup project (TODO: cross-region replication not set up — operator follow-up) | 30-60 min | last sync |

---

## Cost reference

Approximate monthly cost for the SaaS surface (idle / light usage):

| Component | Monthly |
|---|---|
| 9 Cloud Run services (min-instances=1, instance billing) | ~$15 |
| Cloud SQL r1-prod-pg (db-g1-small) | ~$10 |
| Cloud SQL r1-staging-pg + r1-dev-pg (db-f1-micro × 2) | ~$14 |
| Artifact Registry storage | <$1 |
| Secret Manager | <$1 |
| GCS releases bucket | <$1 |
| Cloud Build (assuming ~50 builds/mo) | ~$2 |
| **Total idle baseline** | **~$45/mo** |

At 1k req/day across the SaaS surface, total stays under $50/mo. Scaling adds CPU/memory/network cost but Cloud Run instance billing keeps it predictable.

---

## Operator runbook — fresh-machine bring-up

Assuming a brand-new GCP project + Cloudflare zone:

```bash
# 1. Enable APIs
gcloud services enable run.googleapis.com sqladmin.googleapis.com \
  secretmanager.googleapis.com artifactregistry.googleapis.com \
  cloudbuild.googleapis.com iamcredentials.googleapis.com

# 2. Create Artifact Registry repo
gcloud artifacts repositories create r1 --location=us-central1 \
  --repository-format=docker

# 3. Provision Cloud SQL (~5-10 min each; can run in parallel)
gcloud sql instances create r1-prod-pg --edition=ENTERPRISE \
  --database-version=POSTGRES_16 --tier=db-g1-small --region=us-central1 \
  --storage-type=SSD --storage-size=10GB --storage-auto-increase \
  --backup --backup-start-time=09:00 --availability-type=ZONAL --async
gcloud sql instances create r1-staging-pg --edition=ENTERPRISE \
  --database-version=POSTGRES_16 --tier=db-f1-micro --region=us-central1 \
  --storage-type=SSD --storage-size=10GB --storage-auto-increase \
  --backup --backup-start-time=09:00 --availability-type=ZONAL --async
gcloud sql instances create r1-dev-pg --edition=ENTERPRISE \
  --database-version=POSTGRES_16 --tier=db-f1-micro --region=us-central1 \
  --storage-type=SSD --storage-size=10GB --storage-auto-increase \
  --backup --backup-start-time=09:00 --availability-type=ZONAL --async

# 4. Create secret skeletons
for ENV in prod staging dev; do
  for KEY in DATABASE_URL ANTHROPIC_API_KEY; do
    echo "placeholder-set-by-operator" | \
      gcloud secrets create r1-$ENV-shared-$KEY \
        --replication-policy=automatic --data-file=-
  done
done

# 5. Build + push images
TAG=$(git rev-parse --short HEAD)
for SVC in r1-coord-api r1-docs r1-downloads-cdn; do
  gcloud builds submit services/$SVC \
    --tag=us-central1-docker.pkg.dev/$PROJECT/r1/$SVC:$TAG \
    --machine-type=e2-medium --timeout=600
done

# 6. Deploy to Cloud Run
TAG=$TAG ./services/deploy.sh all

# 7. Verify domain ownership
gcloud domains verify r1.run    # opens browser; add returned TXT to Cloudflare

# 8. Create domain mappings
for ENV in prod staging dev; do
  SUB=""
  [ "$ENV" = "staging" ] && SUB=".staging"
  [ "$ENV" = "dev" ] && SUB=".dev"
  gcloud beta run domain-mappings create --service=r1-docs-$ENV \
    --domain=platform$SUB.r1.run --region=us-central1
  gcloud beta run domain-mappings create --service=r1-coord-api-$ENV \
    --domain=api$SUB.r1.run --region=us-central1
  gcloud beta run domain-mappings create --service=r1-downloads-cdn-$ENV \
    --domain=downloads$SUB.r1.run --region=us-central1
done

# 9. Add 9 CNAMEs to Cloudflare (ghs.googlehosted.com., proxy OFF)

# 10. Wire deploy triggers
./services/scripts/setup-cloudbuild-triggers.sh

# 11. After PR merges, set up branch protection
./scripts/setup-branch-protection.sh

# 12. Final smoke
for ENV in dev staging prod; do
  for SVC in r1-coord-api r1-docs r1-downloads-cdn; do
    URL=$(gcloud run services describe $SVC-$ENV --region=us-central1 --format='value(status.url)')
    curl -sSf "$URL/livez" >/dev/null && echo "$SVC-$ENV OK" || echo "$SVC-$ENV FAIL"
  done
done
```

Expected: 9 × OK.

---

## Release-rehearsal lane

The release-rehearsal lane runs the full **Playwright + axe-core E2E** flow against a freshly-built `r1-server`. It is the post-deploy + release-gate quality bar for `cmd/r1-server` and the v2 web UI; the Go gate (`go build` + `go test` + `go vet`) covers the agent runtime, but the v2 web UI requires browser execution that the Go gate can't provide.

The lane runs in **three modes**, all firing the same `services/cloudbuild-e2e.yaml` Cloud Build pipeline:

| Mode | Trigger | When it fires | Purpose |
|---|---|---|---|
| Push-to-main | Cloud Build trigger `r1-agent-e2e-rehearsal-main` | every push to `main` | Post-deploy verification: confirms the just-shipped `main` is e2e-clean. |
| Tag-push | Cloud Build trigger `r1-agent-e2e-rehearsal-tag` | every `^v.*$` tag push | Release gate: red blocks tag promotion to staging / main / production rollouts. |
| Manual | GitHub Actions workflow `e2e-rehearsal-manual` (dispatch via Actions UI) | operator-initiated | On-demand rehearsal without local gcloud — `gcloud builds triggers run r1-agent-e2e-rehearsal-main --branch=$BRANCH` from the GitHub runner. |

### What runs

`services/cloudbuild-e2e.yaml` orchestrates four steps:

1. `golang:1.25` — `go build -mod=vendor` produces a fresh `r1-server` binary.
2. `node:22.13-bookworm-slim` — `npm install` + `npx playwright install --with-deps chromium`.
3. `golang:1.25` — `go test -tags=e2e ./cmd/r1-server/e2e/...` exercises the full Playwright + axe flow with `R1_SERVER_UI_V2=1` + `R1_SERVER_SHARE_ENABLED=1`.
4. `cloud-sdk:slim` — publishes the rehearsal result back to GitHub via Cloud Build's native commit-status integration.

Both Cloud Build triggers run under the BYOSA service account `cloud-build-byosa@relayone-488319.iam.gserviceaccount.com`. The `r1-agent-e2e-rehearsal-main` trigger is path-filtered to `cmd/r1-server/**`, `internal/server/**`, `web/**`, and `services/cloudbuild-e2e.yaml` (changes anywhere else don't fire the rehearsal — keeps build minutes proportional to risk).

### What "red" means

A red rehearsal blocks any release that gates on this check. Investigate the failed step in the Cloud Build console (the workflow summary links straight to it) before tagging. Common red causes:

- A real regression — the just-shipped change broke a flow.
- A flaky test — the deadline-bumps in `cmd/r1-server/cortex/lobes/...` cover the timing-sensitive cases, but new tests can drift. Bump deadline + retry; if the retry passes, file a follow-up to investigate the underlying flake.
- Playwright/chromium upstream breakage — pin the Playwright version in `web/package.json` if regression confirms upstream.

### Running locally

```bash
cd cmd/r1-server/e2e
R1_SERVER_UI_V2=1 R1_SERVER_SHARE_ENABLED=1 go test -tags=e2e ./...
```

Prerequisite: `cd web && npx playwright install --with-deps chromium` (one-time).

### Setting up the triggers (one-time)

```bash
# Requires roles/cloudbuild.builds.editor on relayone-488319
bash scripts/setup-cloudbuild-e2e-trigger.sh
gcloud builds triggers list --project=relayone-488319 --filter='name~e2e-rehearsal'
```

Re-running the script updates the existing triggers in-place — does not create duplicates. The trigger descriptor is `services/cloudbuild-e2e-trigger.yaml`.

### Manual rehearsal from GitHub Actions

For operators without local `gcloud` (or when triggering from a non-developer machine):

1. Open `https://github.com/RelayOne/r1-agent/actions/workflows/e2e-rehearsal-manual.yml` in the browser.
2. Click **Run workflow**, optionally set `branch` (default `main`), submit.
3. The runner authenticates to GCP via `secrets.GCP_SA_JSON` and calls `gcloud builds triggers run r1-agent-e2e-rehearsal-main --branch=$BRANCH`.
4. Workflow summary prints the build ID + a link to the Cloud Build console for live logs.

The `GCP_SA_JSON` repo secret must hold a JSON service-account key with `roles/cloudbuild.builds.editor`. Rotate per the standard secret-rotation cadence.

---

## Tracebundle v2 export — operator notes

`GET /api/session/{id}/export.tracebundle` produces a portable per-session audit artifact (chain nodes + edges + content + canonical-signed manifest with `chain_root_hash`). V2-flag-gated: returns `404` unless `R1_SERVER_UI_V2=1` is set on the `r1-server` process.

Operational considerations:

- **Set `R1_SERVER_UI_V2=1`** as a Cloud Run env var (or local env when running `r1-server` standalone) to enable the export route. The flag also enables the v2 web UI.
- **Bundle size** scales with session length. Expect a few hundred KB for a typical mission, MB-range for long-running multi-day sessions. Stream with `--output` rather than `-O` so curl doesn't buffer in memory.
- **Distributing the bundle**: gzip first (`tracebundle` is a JSON-shaped archive; gzip ratios are typically 4-6×), then attach to a ticket / share via cloud storage. Recipients verify with the canonical manifest body (`ledger.CanonicalManifestSignBody`) plus the operator-published public key.
- **Redacted content**: the bundle preserves the redaction structure — chain-tier metadata is present, content tier is empty, and the per-node `<store-root>/redactions/<nodeID>.ndjson` log is included. `Store.RedactionsForVerified` is the read path the dashboard uses; downstream auditors run the same `VerifyRecord` check against the public key.
- **Key distribution for verification**: by default, the ed25519 keypair lives under `<r1-server-store-root>/redactions/sign-{priv,pub}.pem`. To let an external auditor verify without daemon access, copy `sign-pub.pem` (mode 0644) to them out-of-band; the manifest's `signer` field carries the 12-char hex fingerprint they cross-check against.

---

## Status

### Done
- 9 Cloud Run services deployed + answering `/livez` 200
- 3 Cloud SQL instances RUNNABLE
- Artifact Registry repo + 3 images
- 6 Secret Manager placeholders
- 9 domain mappings created
- Auto-deploy yaml + ops scripts shipped
- `cloudbuild.yaml` CI gate (build + test + vet + race + chdir-lint + view-without-api + antitrunc verify)
- **Release-rehearsal CI** (PR #170): Cloud Build triggers `r1-agent-e2e-rehearsal-main` (push-to-main) + `r1-agent-e2e-rehearsal-tag` (`^v.*$`) + manual GitHub Actions workflow (`e2e-rehearsal-manual.yml`). Idempotent setup via `scripts/setup-cloudbuild-e2e-trigger.sh`.
- **Tracebundle v2 export route** (PR #171): `GET /api/session/{id}/export.tracebundle` v2-flag-gated; per-session filtered chain + edges + canonical-signed manifest with `chain_root_hash`. Production source at `cmd/r1-server/tracebundle_source.go`.
- **Signed redaction events** (PR #169): ed25519 keypair persisted at `<store-root>/redactions/sign-{priv,pub}.pem`; `Store.RedactionsForVerified` returns per-entry `Verified` flag for the dashboard side panel.

### In Progress
- DNS propagation (operator: add Cloudflare CNAMEs)
- Real values for 6 secret placeholders (operator)
- Branch protection (operator runs `./scripts/setup-branch-protection.sh`)
- Cloud Build deploy triggers (operator runs `./services/scripts/setup-cloudbuild-triggers.sh`)

### Scoped
- Cloud Monitoring uptime checks on `/livez` endpoints
- Alerting policies (5xx rate, Cloud SQL CPU, lane-event throughput)
- Cross-region replication for `relayone-488319-r1-releases` GCS bucket
- Terraform module for the whole stack (recreate in another project from one command)

### Scoping
- Disaster-recovery drill cadence (quarterly?)
- Cost-budget alerting (hard cap on monthly spend)

### Potential — On Horizon
- Anycast TCP load balancer for hot regions
- VPC peering for Cloud SQL (no public IP on the prod instance)
- Multi-region Cloud Run (failover from us-central1 to northamerica-northeast2 on outage)
