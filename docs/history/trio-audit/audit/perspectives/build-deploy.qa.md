# Build-Deploy Engineer Audit

**Auditor**: BUILD-DEPLOY ENGINEER
**Date**: 2026-04-01
**Scope**: ember/devbox, flare, stoke -- build, deploy, runtime readiness
**Question**: Can I git clone these repos on a fresh machine and build/run them?

---

## Summary

Three repos, three deployment targets: ember/devbox is a TypeScript web app on Fly.io, flare is a Go microVM platform on GCP, stoke is a Go CLI tool. Ember has the most mature deployment pipeline (Dockerfile, fly.toml, CI). Flare has Terraform+Packer but no CI and manual migrations. Stoke is a local CLI tool with minimal deploy concerns. Cross-service dependencies exist: stoke depends on ember's v1 API, ember depends on flare (indirectly, via Fly.io API -- not the flare control plane yet).

---

## Findings

### CRITICAL -- Will prevent build or deploy

- [ ] **CRITICAL** [ember/devbox/Dockerfile:24] Vite build runs from project root (`npx vite build`) but vite.config.ts sets `root: "web"`, so vite outputs to `web/dist/`. The Dockerfile then copies `web/dist/` in line 30. However, the root-level `npm run build` script does `tsc && vite build` which builds the vite project with `root: "web"`, outputting to `web/dist`. This works. But the CI workflow (ci.yml:40) runs `npx vite build` from the root *without* `cd web && npm ci` happening before the vite build step for the frontend assets. Actually, CI does `cd web && npm ci` on line 23. This is fine. **RETRACTED after deeper read -- Dockerfile sequence is correct.** Removing from findings.

- [ ] **CRITICAL** [stoke/go.mod:8] Stoke depends on `github.com/mattn/go-sqlite3` which is a CGO package requiring a C compiler. A plain `go build ./cmd/r1` will fail on machines without gcc/clang installed. The install.sh does not check for or install a C compiler. -- fix: Add `apt-get install -y gcc` or equivalent to install.sh, or document CGO requirement. Alternatively, consider replacing go-sqlite3 with a pure-Go SQLite driver like `modernc.org/sqlite`. -- effort: small

- [ ] **CRITICAL** [flare: no migration runner] Flare has SQL migration files in `migrations/` but no automated migration runner. The control plane binary (`cmd/control-plane/main.go`) does NOT run migrations on startup. Migrations must be applied manually via `psql`. If you deploy a new control plane instance to a fresh database, it will crash immediately because the tables don't exist. -- fix: Add an auto-migration step to main.go that reads and applies `migrations/*.sql` files in order on startup, or add a separate `migrate` subcommand. Ember does this well (see `src/migrate.ts`). -- effort: small

- [ ] **CRITICAL** [ember/devbox: no .env.example] No `.env.example` or `.env.template` file exists anywhere. Ember requires 15+ env vars in production (DATABASE_URL, FLY_API_TOKEN, CANONICAL_IMAGE_DIGEST, STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, STRIPE_PRICE_4X/8X/16X, FLY_ORG, RECONCILE_SECRET, APP_ENCRYPTION_KEY, INTERNAL_API_URL, EMAIL_PROVIDER, APP_URL, MACHINE_HOST_SUFFIX, GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET). A new developer will have to read every source file to discover required vars. -- fix: Create `.env.example` with all required vars and comments. The `validateProductionConfig()` in index.ts already lists the critical ones -- extract that into a documented file. -- effort: trivial

### HIGH -- Will cause runtime failures or broken features

- [ ] **HIGH** [ember/devbox/src/connection.ts:7] `process.env.DATABASE_URL!` is used at module load time with a non-null assertion. If DATABASE_URL is not set in development, the app will crash on import before any helpful error message. The production validator only runs in production mode. -- fix: Add early check before module-level postgres() call, e.g. `if (!process.env.DATABASE_URL) throw new Error("DATABASE_URL is required")`. -- effort: trivial

- [ ] **HIGH** [flare: no CI pipeline] Flare has zero CI configuration -- no GitHub Actions, no Dockerfile for the control plane, no automated testing on push. The only test is an integration test that requires a running Postgres. A broken commit can be deployed via Packer without any gates. -- fix: Add `.github/workflows/ci.yml` with `go build ./...`, `go vet ./...`, and integration test with a Postgres service container (same pattern as ember's ci.yml). -- effort: small

- [ ] **HIGH** [flare: no Dockerfile for control plane] The placement daemon gets baked into a Packer image, but the control plane has no Dockerfile or deployment mechanism documented. How does it get deployed? The README says `go run ./cmd/control-plane` which is a development command, not a deployment strategy. -- fix: Add a Dockerfile for the control plane (simple multi-stage Go build) and document deployment (Cloud Run, GCE instance, etc.). -- effort: small

- [ ] **HIGH** [ember/devbox/fly.toml + Dockerfile] The Dockerfile does not run migrations. The `CMD` is `node dist/index.js` which starts the server directly. If schema changes are deployed, they must be run manually or via a release command. fly.toml has no `[deploy.release_command]` configured. -- fix: Add `[deploy] release_command = "npx tsx src/migrate.ts"` to fly.toml (Fly runs this before swapping traffic). Or add a `release_command` entry. -- effort: trivial

- [ ] **HIGH** [stoke + ember: cross-service env vars undocumented] Stoke's `internal/managed/proxy.go` and `internal/remote/session.go` require `EMBER_API_KEY` and `EMBER_API_URL` to connect to ember's v1 API. Ember's v1 API requires `ENABLE_V1_WORKERS=true` and `ENABLE_MANAGED_AI=true` feature flags to be set. Neither repo documents this cross-service dependency. A user running stoke with `EMBER_API_KEY` set but ember's feature flags off will get 501 errors. -- fix: Document in both READMEs. In stoke, add a `doctor` check that validates the ember API is reachable and feature flags are on. -- effort: small

- [ ] **HIGH** [ember/devbox/src/routes/billing.ts:17-19] Stripe price IDs (`STRIPE_PRICE_4X`, `STRIPE_PRICE_8X`, `STRIPE_PRICE_16X`) are accessed with non-null assertions `process.env.STRIPE_PRICE_4X!`. In development without these set, any billing route hit will pass `undefined` to Stripe, causing cryptic Stripe API errors. The production validator catches this, but dev doesn't. -- fix: Add to `validateProductionConfig()` OR add dev-mode fallback/early error. -- effort: trivial

- [ ] **HIGH** [ember/devbox/Dockerfile:13] The Dockerfile runs `npx tsc` but does NOT copy the `drizzle.config.ts` before the build. Wait -- line 9 copies `drizzle.config.ts`. The issue is that `src/migrate.ts` imports from `./connection.js` which tries to connect to DATABASE_URL at import time. During Docker build, there's no database. But since migrate.ts is only compiled (not executed) during `npx tsc`, this is fine. **RETRACTED -- tsc only compiles, doesn't execute.**

- [ ] **HIGH** [ember/devbox: machine-image/Dockerfile:28] Machine image installs Go 1.22.5 which is a minor version behind the 1.23 required by both flare and stoke go.mod files. If users try to build flare or stoke inside an ember machine, it will fail. -- fix: Update to `go1.23.4.linux-amd64.tar.gz` to match the Go version in flare's go.mod and the Packer template. -- effort: trivial

### MEDIUM -- Operational risk, won't block initial deploy

- [ ] **MEDIUM** [flare/deploy/packer/firecracker-host.pkr.hcl:91] Packer provisioner references `/tmp/flare-src/guest/flare-init.sh` but the file upload (line 127-129) copies `../../` to `/tmp/flare-src`. This means the file provisioner must be run from `deploy/packer/` directory for the relative path `../../` to resolve to the flare repo root. If run from any other directory, the build fails silently or copies the wrong tree. -- fix: Document that `packer build` must be run from `deploy/packer/` or use an absolute path variable. README already shows `cd deploy/packer && packer build` so this is documented but fragile. -- effort: trivial

- [ ] **MEDIUM** [flare/deploy/packer/firecracker-host.pkr.hcl:72] The quickstart kernel and rootfs are fetched from AWS S3 (`s3.amazonaws.com/spec.ccfc.min`). This is the Firecracker project's example bucket. These URLs can break without notice and the images are minimal (hello-rootfs.ext4 is a toy). Production needs a custom rootfs. -- fix: Replace with your own GCS bucket for kernel/rootfs artifacts. The Packer template already has an `artifact_bucket` variable in Terraform but the Packer file doesn't use it. -- effort: medium

- [ ] **MEDIUM** [ember/devbox: OPENROUTER_API_KEY not in production validator] The managed AI feature (`/v1/ai/chat`) requires `OPENROUTER_API_KEY` but this env var is not checked in `validateProductionConfig()`. If `ENABLE_MANAGED_AI=true` is set but `OPENROUTER_API_KEY` is missing, the route returns 503 at runtime instead of failing at startup. -- fix: Add to production validator: `if (process.env.ENABLE_MANAGED_AI === "true" && !process.env.OPENROUTER_API_KEY) errors.push(...)`. -- effort: trivial

- [ ] **MEDIUM** [ember/devbox: GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY, GITHUB_APP_SLUG not in production validator] GitHub App integration (`github-app.ts`) uses these env vars. The `isConfigured()` function silently disables the feature if they're missing, but there's no startup warning. If someone deploys expecting GitHub App integration and forgets these, clone-with-token silently falls back to OAuth (or fails). -- fix: Add optional-but-warned check in production validator for GitHub App env vars. -- effort: trivial

- [ ] **MEDIUM** [flare: startup order dependency] The placement daemon requires the control plane to be running first (`requireEnv("FLARE_CONTROL_PLANE")`). On `register()` failure, the daemon calls `log.Fatalf`. If the control plane isn't up when hosts boot, the MIG instances will crash-loop. The GCE health check will mark them unhealthy. Auto-healing will replace them. This creates a thundering herd on control plane startup. -- fix: Add retry with backoff to `register()` instead of `log.Fatalf`. 5 retries with exponential backoff (2s, 4s, 8s, 16s, 32s) before giving up. -- effort: small

- [ ] **MEDIUM** [stoke: no CI pipeline] Stoke has no `.github/workflows/` directory. The CLAUDE.md documents `go build ./cmd/r1 && go test ./... && go vet ./...` as the CI gate, but there's no automation. -- fix: Add CI workflow. Since stoke uses CGO (go-sqlite3), the CI runner needs `apt-get install gcc` or use a Go image with gcc. -- effort: small

- [ ] **MEDIUM** [ember/devbox/src/rate-limit.ts:114] Rate limiting backend is configurable via `RATE_LIMIT_BACKEND=postgres` but defaults to in-memory. In a multi-instance Fly deployment, in-memory rate limiting is per-instance and trivially bypassed by round-robin. -- fix: Default to postgres in production, or document that single-instance is required for in-memory rate limiting to work. -- effort: trivial

---

## Cross-Service Dependency Map

```
stoke --[EMBER_API_KEY/EMBER_API_URL]--> ember /v1/sessions (session reporting)
stoke --[EMBER_API_KEY/EMBER_API_URL]--> ember /v1/ai/chat  (managed AI proxy)
stoke --[EMBER_API_KEY/EMBER_API_URL]--> ember /v1/workers   (burst workers)

ember --[FLY_API_TOKEN]--> Fly.io Machines API (machine lifecycle)
ember --[STRIPE_SECRET_KEY]--> Stripe (billing)
ember --[DATABASE_URL]--> Postgres (state)
ember --[OPENROUTER_API_KEY]--> OpenRouter (AI proxy)

flare control-plane --[DATABASE_URL]--> Postgres (state)
flare control-plane <--[FLARE_INTERNAL_KEY]--> flare placement (mutual auth)
flare placement --[FLARE_CONTROL_PLANE]--> flare control-plane (registration)

ember machines --[API_URL/MACHINE_TOKEN]--> ember API (startup script, auth)
```

## Required Env Vars (Complete List)

### ember/devbox (production)
| Var | Required | Source |
|-----|----------|--------|
| DATABASE_URL | yes | Postgres connection string |
| FLY_API_TOKEN | yes | Fly.io dashboard |
| CANONICAL_IMAGE_DIGEST | yes | Machine image digest |
| STRIPE_SECRET_KEY | yes | Stripe dashboard |
| STRIPE_WEBHOOK_SECRET | yes | Stripe webhook config |
| STRIPE_PRICE_4X | yes | Stripe price ID |
| STRIPE_PRICE_8X | yes | Stripe price ID |
| STRIPE_PRICE_16X | yes | Stripe price ID |
| FLY_ORG | yes | Fly.io org slug |
| RECONCILE_SECRET | yes | Generated secret |
| APP_ENCRYPTION_KEY | yes | 32-byte hex or base64 |
| INTERNAL_API_URL | yes | Fly 6PN address |
| EMAIL_PROVIDER | yes | "resend" in prod |
| APP_URL | yes | https://your-domain |
| MACHINE_HOST_SUFFIX | yes | Custom domain suffix |
| RESEND_API_KEY | if EMAIL_PROVIDER=resend | Resend dashboard |
| EMAIL_FROM | if EMAIL_PROVIDER=resend | Sender address |
| GITHUB_CLIENT_ID | for OAuth | GitHub OAuth app |
| GITHUB_CLIENT_SECRET | for OAuth | GitHub OAuth app |
| GOOGLE_CLIENT_ID | for Google login | Google Cloud Console |
| GOOGLE_CLIENT_SECRET | for Google login | Google Cloud Console |
| GITHUB_APP_ID | for GitHub App | GitHub App settings |
| GITHUB_APP_PRIVATE_KEY | for GitHub App | GitHub App settings |
| GITHUB_APP_SLUG | for install URL | GitHub App settings |
| OPENROUTER_API_KEY | if ENABLE_MANAGED_AI | OpenRouter dashboard |
| ENABLE_V1_WORKERS | optional | "true" to enable |
| ENABLE_MANAGED_AI | optional | "true" to enable |
| RATE_LIMIT_BACKEND | optional | "postgres" for multi-instance |

### flare/control-plane
| Var | Required | Source |
|-----|----------|--------|
| DATABASE_URL | yes | Postgres connection string |
| FLARE_API_KEY | yes | Generated secret |
| FLARE_INTERNAL_KEY | optional | Shared secret with placement |
| PORT | optional | Default 8090 |
| INGRESS_PORT | optional | Default 8080 |

### flare/placement
| Var | Required | Source |
|-----|----------|--------|
| FLARE_CONTROL_PLANE | yes | Control plane URL |
| HOST_ID | yes | GCE instance ID |
| HOST_ZONE | optional | GCE zone |
| HOST_IP | optional | GCE internal IP |
| FLARE_INTERNAL_KEY | optional | Shared secret |
| PLACEMENT_PORT | optional | Default 9090 |
| INGRESS_PORT | optional | Default 8080 |
| VM_DIR | optional | Default /var/lib/flare/vms |

### stoke
| Var | Required | Source |
|-----|----------|--------|
| EMBER_API_KEY | for managed features | Ember API key |
| EMBER_API_URL | for managed features | Default https://api.ember.dev |
| CLAUDE_CONFIG_DIR | optional | Claude Code config |

---

## Startup Order (Deploy Sequence)

1. **Postgres** -- both ember and flare need it first
2. **ember/devbox** -- run migrations (`npx tsx src/migrate.ts`), then start server
3. **flare/control-plane** -- apply SQL migrations manually, then start binary
4. **flare/placement** -- needs control plane running; hosts register on boot
5. **stoke** -- local CLI, needs ember API key if using managed features

---

## Verdict

**Can you build from a fresh clone?**
- **ember/devbox**: Yes, with `npm ci && cd web && npm ci && npx tsc && npx vite build`. Needs Node 22, Postgres for dev. Missing .env.example is the biggest friction.
- **flare**: Yes, `go build ./...` works. But deploying requires Packer + GCP + Terraform + manual Postgres migration. No CI to catch regressions.
- **stoke**: **No** -- `go build ./cmd/r1` will fail without a C compiler (gcc) due to go-sqlite3 CGO dependency. Once gcc is installed, it builds fine.
