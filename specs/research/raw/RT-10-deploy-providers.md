# RT-10: Deploy Providers (Fly.io, Vercel, Cloudflare Pages/Workers)

Research date: 2026-04-20
Target: Stoke `DeployExecutor` — detect stack, deploy, verify via browser tool.

---

## 1. Fly.io

**Deploy mechanism.** CLI-first. `flyctl deploy` is the stable path; it drives the Machines API under the hood. There is an **official Go SDK — [`github.com/superfly/fly-go`](https://github.com/superfly/fly-go)** (v0.4.5, 2026-04-09), which flyctl itself consumes. It exposes packages for apps, machines, deployments, secrets, volumes. No stability guarantee is published — it's driven by flyctl's needs. REST Machines API is documented at <https://fly.io/docs/machines/api/>. Recommendation: **shell out to `flyctl`** for Stoke v1 — it handles auth, builder selection, Dockerfile generation, rolling strategy. Add `fly-go` later if/when we need to poll status without spawning processes.

**Auth.** `FLY_API_TOKEN` env var (deploy-scoped tokens via `fly tokens create deploy`). No browser flow needed when the env var is set. Supports org tokens for multi-app automation.

**Stack detection.** Presence of `fly.toml` at repo root is a strong positive signal. Absence but presence of `Dockerfile` + no Vercel/Cloudflare markers → candidate. `fly launch --no-deploy` is their built-in "detect + scaffold" for Node/Go/Python/Rails/Elixir.

**Deploy command.** `flyctl deploy --remote-only --wait-timeout 5m` is the canonical one-shot. Flags we care about: `--strategy rolling|bluegreen|canary|immediate` (default `rolling`), `--ha`, `--detach`, `--access-token`, `--image` (for rollback), `--config`. **JSON output caveat:** `flyctl deploy --json` produces a *stream* of `{Source, Status, Message}` NDJSON objects mixed with non-JSON lines — the Fly blog itself recommends **`--detach` then poll `flyctl status --json`** for programmatic use. Typical duration: **30–90s for small Node/Go apps; 2–5 min with builds**.

**URL extraction.** `flyctl deploy` does not emit the URL in a machine-readable way. Two reliable approaches:
1. App hostname is deterministic: `https://{app_name}.fly.dev`. Read `app` field from `fly.toml`.
2. Call `flyctl status --json` → parse `.Hostname`.

**Config generation.** Stoke can safely run `fly launch --no-deploy --name <stoke-generated> --org personal --region iad --yes` when `fly.toml` is absent. Minimal hand-written `fly.toml` for a Node/Express app:

```toml
app = "stoke-demo"
primary_region = "iad"
[build]
  builder = "paketobuildpacks/builder:base"
[http_service]
  internal_port = 3000
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0
[[http_service.checks]]
  grace_period = "10s"
  interval = "30s"
  method = "GET"
  path = "/"
  timeout = "5s"
```

**Verification.** After `flyctl status --json` reports `Deployed`, hit `https://{app}.fly.dev/` with HEAD expecting 200 (or 30x). DNS is instantaneous because `*.fly.dev` is a wildcard on their edge. Fly runs internal **smoke checks for ~10s** post-boot; external health checks (from `[[http_service.checks]]`) run continuously. Expected failures: builder OOM, port mismatch (internal_port), missing secrets (`fly secrets set`), release command timeout (5m default).

**Rollback.** No native `fly rollback` command — the documented pattern is `fly releases --image` → find previous image tag → `fly deploy --image registry.fly.io/<app>:<tag>`. Stoke should cache the previous image tag before each deploy for one-command rollback.

**Cost visibility.** `fly-go` has an `orgs` endpoint returning `billing_status`. No per-deploy cost API. Free tier effectively ended 2024; machines cost ~$0.0000008/s when running. Stoke can flag "deploy will spin up N machines @ $X/mo" based on `fly.toml` machine count × region price (static table).

```go
type FlyDeployer struct {
    Token string // FLY_API_TOKEN
    Bin   string // flyctl path
}
type FlyConfig struct { AppName, Region, Org, ConfigPath string; PreDeployImage string }
func (d *FlyDeployer) Deploy(ctx context.Context, c FlyConfig) (*DeployResult, error)
// spawns: flyctl deploy --config c.ConfigPath --strategy rolling --wait-timeout 5m
// tails NDJSON on stdout for progress; captures app name from fly.toml
// returns DeployResult{URL: "https://"+appName+".fly.dev", ID: releaseID}
func (d *FlyDeployer) Verify(ctx context.Context, url string) (*HealthStatus, error)
func (d *FlyDeployer) Rollback(ctx context.Context, prevImage string) error
// spawns: flyctl deploy --image prevImage
```

---

## 2. Vercel

**Deploy mechanism.** CLI (`vercel`) is the de-facto path for developers; the **REST API `POST /v13/deployments`** is stable and documented at <https://vercel.com/docs/rest-api/reference/endpoints/deployments/create-a-new-deployment>. **No official Go SDK.** Community SDK: [`chronark/vercel-go`](https://github.com/chronark/vercel-go) — usable but third-party. Recommendation: **shell out to `vercel` CLI** for v1 (handles build output API, project linking, framework presets); revisit direct REST once we want in-process progress events.

**Auth.** `VERCEL_TOKEN` env var + `--token` flag. Org scope via `--scope <team-slug>` or `VERCEL_ORG_ID` + `VERCEL_PROJECT_ID` (which Vercel writes to `.vercel/project.json` on first `vercel link`).

**Stack detection.** Presence of `vercel.json` → strong. Presence of `next.config.{js,ts,mjs}` → very strong (Vercel builds Next.js). `svelte.config.js` with `@sveltejs/adapter-vercel` or `adapter-auto`, `astro.config.*`, `remix.config.*`, `nuxt.config.*` → Vercel-friendly. Vercel has **zero-config presets** for Next.js, SvelteKit, Nuxt, Astro, Remix, Vite, Gatsby, Hugo, plain static.

**Deploy command.** `vercel deploy --prod --yes --token=$VERCEL_TOKEN`. Key flags: `--prebuilt` (skip server build, upload `.vercel/output/`), `--archive=tgz` (compress upload for large trees), `--target=production|preview|<custom>`, `--skip-domain`, `--no-wait`, `--force` (bypass build cache). Vercel CLI has **no `--json`** on deploy — a long-standing feature request. **stdout is always the deployment URL**; stderr has progress. Typical duration: **20–60s for cached Next.js; 2–5 min cold**. For machine-readable output, use the REST API (`POST /v13/deployments` returns JSON with `url`, `id`, `readyState`).

**URL extraction.** Capture stdout → trim → use directly. Exit code 0 = success. For the stable production domain, use `vercel alias` or the dashboard-configured production domain (not the deployment's `*-<hash>.vercel.app` URL).

**Config generation.** Most projects don't need `vercel.json` — framework auto-detection handles it. Minimal custom `vercel.json` for a Node+Express app served as a serverless function:

```json
{
  "version": 2,
  "builds": [{ "src": "server.js", "use": "@vercel/node" }],
  "routes": [{ "src": "/(.*)", "dest": "/server.js" }]
}
```

For Next.js / static: omit `vercel.json` entirely. For `/api/*` in a plain Node app, drop files in `/api/` and Vercel treats them as functions.

**Verification.** Output URL is immediately resolvable (Vercel manages DNS on `*.vercel.app`). CLI exits only after build + deploy finish by default; `--no-wait` returns early. Expected failures: build error (tsc/webpack), `Function payload too large` (50MB limit), ISR/edge mismatches, missing env vars (`vercel env pull`). Browser verify: hit URL → 200, look for Next.js `__NEXT_DATA__` or framework marker, capture console errors.

**Rollback.** Native: `vercel rollback [deployment-url-or-id]`. Instant (no rebuild). Use `vercel rollback status` to poll. `vercel promote <id>` can roll forward or promote a preview. Stoke should capture the previous deployment ID from `vercel ls --json` before deploying.

**Cost visibility.** REST: `GET /v1/teams/:id/billing` (undocumented/sporadic). No per-deploy cost. Free "Hobby" tier is generous (100GB bandwidth, unlimited deploys). Stoke should flag when user is on Hobby + project exceeds soft limits (functions > 10s, size > 50MB).

```go
type VercelDeployer struct { Token, OrgID, ProjectID string; Bin string }
type VercelConfig struct { ProjectPath string; Prod bool; Prebuilt bool; Env map[string]string }
func (d *VercelDeployer) Deploy(ctx context.Context, c VercelConfig) (*DeployResult, error)
// spawns: vercel deploy --prod --yes --token=$VERCEL_TOKEN --cwd c.ProjectPath
// captures trimmed stdout as URL; parses stderr for build diagnostics
func (d *VercelDeployer) Verify(ctx context.Context, url string) (*HealthStatus, error)
func (d *VercelDeployer) Rollback(ctx context.Context, prevDeployID string) error
// spawns: vercel rollback prevDeployID --yes --timeout 3m
```

---

## 3. Cloudflare Pages / Workers

**Note on convergence.** Cloudflare is **folding Pages into Workers throughout 2026**. New features land on Workers only; `wrangler pages deploy` still works but is in maintenance mode. **Stoke should target Workers-with-static-assets as the primary path** and keep Pages as a legacy-compat code path. Source: <https://developers.cloudflare.com/workers/static-assets/migration-guides/migrate-from-pages/>.

**Deploy mechanism.** Wrangler is the canonical CLI. Cloudflare publicly announced in April 2026 they're **rebuilding Wrangler's CLI codegen pipeline** to cover the full product surface — expect breaking flag churn through 2026. **Official Go SDK exists** ([`github.com/cloudflare/cloudflare-go`](https://github.com/cloudflare/cloudflare-go), requires Go 1.22+) — fully auto-generated from Cloudflare's OpenAPI, covers Workers and Pages deploy endpoints. But **uploading Worker bundles via the API is non-trivial** (multipart with modules, source maps, bindings). Recommendation: **shell out to `wrangler`** for v1; `cloudflare-go` is the right tool for post-deploy operations (list deployments, rollback, query routes).

**Auth.** `CLOUDFLARE_API_TOKEN` + `CLOUDFLARE_ACCOUNT_ID` env vars — both required in CI/non-interactive. Token needs `Workers Scripts:Edit`, `Account Settings:Read`, `User Details:Read` scopes. Pages additionally wants `Pages:Edit`.

**Stack detection.** `wrangler.toml` or `wrangler.jsonc` → strong Workers signal. `functions/` directory → Pages Functions. `_worker.js` at repo root → Pages with advanced mode. `compatibility_date` in config is Workers-specific. Static site with no config → either Cloudflare Pages *or* Vercel — ambiguous; prefer Vercel unless user specified Cloudflare.

**Deploy command.** Workers: `wrangler deploy [script]`. Pages: `wrangler pages deploy <dir> --project-name <name>`. Key flags: `--env`, `--var KEY:VALUE`, `--compatibility-date`, `--dry-run`, `--outdir`. **Structured output:** set `WRANGLER_OUTPUT_FILE_PATH=<file>` env → Wrangler writes NDJSON events (`deployment-started`, `deployment-complete`, `version-uploaded` etc.) — this is the clean programmatic channel as of 2026. Some commands also honor `--json`. Typical duration: **10–30s for Workers; 30–90s for Pages with build**.

**URL extraction.** Workers: `https://{script_name}.{subdomain}.workers.dev` (subdomain is per-account, fetchable via `wrangler whoami` or `GET /accounts/{id}/workers/subdomain`). Parse the structured-output file for `deployment.url` or read stdout (Wrangler prints it: `Published <name> (x.xx sec) → https://...`). Pages: `https://{project_name}.pages.dev` + per-deploy alias `https://{hash}.{project}.pages.dev` — both emitted.

**Config generation.** Minimal `wrangler.toml` for a simple Worker:

```toml
name = "stoke-demo"
main = "src/index.js"
compatibility_date = "2026-04-20"
```

Worker + static assets (the 2026 replacement for Pages):

```toml
name = "stoke-site"
main = "src/index.js"
compatibility_date = "2026-04-20"
[assets]
  directory = "./public"
  binding = "ASSETS"
```

For a pure static site, `main` is optional. For Express-style Node apps, use `nodejs_compat` in `compatibility_flags`; for a heavier Node app, recommend Fly or Vercel instead — Workers has a 10ms CPU / 128MB memory ceiling on the free plan.

**Verification.** DNS propagation on `*.workers.dev` / `*.pages.dev` is effectively instant (Cloudflare anycast). First-request cold start can be ~50ms. Expected failures: exceeding 1MB script size (free) / 10MB (paid), missing `compatibility_date`, KV/D1/R2 binding mismatches between envs, nodejs_compat-missing errors on Node-dependent code.

**Rollback.** `wrangler rollback [version-id]` — native, instant (switches active deployment pointer). Interactive mode shows 10 most recent; can address any of last 100 by ID. Limitation: cannot roll back across incompatible binding changes (KV/D1 schema drift) without confirming loss. `wrangler versions list --json` enumerates candidates. Stoke should capture the current version ID pre-deploy.

**Cost visibility.** `cloudflare-go` exposes `accounts.Billing` (beta). Workers free: 100k req/day, Workers Paid: $5/mo + $0.30/M req. Pages free: unlimited sites, 500 builds/mo, 100 custom domains. Stoke should warn before pushing a Worker that uses Durable Objects / KV / D1 if the account is on free.

```go
type CloudflareDeployer struct { Token, AccountID string; Bin string; Mode string /* "workers"|"pages" */ }
type CFConfig struct { ConfigPath, ProjectName string; Env map[string]string }
func (d *CloudflareDeployer) Deploy(ctx context.Context, c CFConfig) (*DeployResult, error)
// env: WRANGLER_OUTPUT_FILE_PATH=<tmp.ndjson>
// spawns: wrangler deploy  OR  wrangler pages deploy <dir> --project-name ...
// tails the NDJSON file for structured progress; extracts URL from deployment-complete event
func (d *CloudflareDeployer) Verify(ctx context.Context, url string) (*HealthStatus, error)
func (d *CloudflareDeployer) Rollback(ctx context.Context, prevVersionID string) error
// spawns: wrangler rollback prevVersionID --message "stoke auto-rollback" --yes
```

---

## Cross-cutting: verification contract

Shared `HealthStatus`:

```go
type HealthStatus struct {
    URL          string
    StatusCode   int       // from GET /
    HealthProbe  string    // path hit: "/healthz" || "/health" || "" (none found)
    ProbeStatus  int
    ConsoleErrs  []string  // from browser tool
    TTFBms       int
    DNSReadyAt   time.Time
    Healthy      bool
}
```

**Probe convention (2026).** Try in order: `/healthz` (Google/k8s legacy, still widely used), `/health` (Spring/Express default), `/livez` + `/readyz` (modern k8s), `/_health` (Next.js/some frameworks). First 2xx wins; treat 404 on all as "no probe, rely on / 200". Note k8s itself **deprecated `/healthz` in v1.16** in favor of `/livez`/`/readyz`, but the ecosystem still widely ships `/healthz`.

**Browser verify (relies on RT-01 Playwright tool).** Navigate → wait for `networkidle` with 10s timeout → collect `console.error` + failed network requests + unhandled promise rejections → screenshot. Fly DNS: 0s. Vercel DNS: 0s (`*.vercel.app`). Cloudflare DNS: 0s. **Warm-up wait: 2s post-deploy** is sufficient; retry 200-check with exponential backoff 1s/2s/4s/8s before declaring failure.

**Auto-rollback trigger.** Health check `status != 200` **and** `ConsoleErrs > 0` **and** time-since-deploy > 30s → rollback. A single failing check at t=5s is likely warm-up and should **not** trigger rollback.

---

## Recommendation: build order

**Build Fly.io FIRST.** Rationale:

1. **Widest stack coverage.** Node, Go, Python, Rails, Elixir, Docker — Stoke's mission-runner users are polyglot; Vercel is JS-centric, Cloudflare is edge-constrained.
2. **Cleanest URL story.** `https://{app}.fly.dev` is derivable from config without parsing CLI output — most robust for our executor contract.
3. **Deploy semantics match Stoke.** Rolling strategy + `fly status` polling fits our existing NDJSON-stream + phase-machine architecture (cf. `engine/`, `stream/`). Fly's `--detach + poll` pattern mirrors how Stoke runs Claude/Codex engines.
4. **Official Go SDK is usable today** (`fly-go` v0.4.5) — gives Stoke a clean upgrade path from shell-out to in-process calls.
5. **Rollback is explicit & ours.** Re-deploying a prior image is a boring, auditable operation; the ledger can snapshot image tags as `nodes/deploy_release.go`.

**Build second: Vercel.** Simplest one-shot deploy (`vercel --prod --yes`), stdout URL, instant rollback. Highest user demand for Next.js projects. Lack of official Go SDK and no `--json` on deploy is tolerable at v1.

**Build third: Cloudflare (Workers + assets mode).** Valuable but riskiest: CLI is actively being rebuilt in 2026 (flag churn expected), Pages↔Workers migration in flight, Workers runtime constraints limit which Node apps deploy cleanly. Ship once Fly and Vercel prove the `DeployExecutor` interface. Use `WRANGLER_OUTPUT_FILE_PATH` NDJSON channel from day one — don't parse stdout.

---

## Sources

- [fly-go official SDK](https://github.com/superfly/fly-go) (v0.4.5, 2026-04-09)
- [fly deploy docs](https://fly.io/docs/flyctl/deploy/)
- [fly launch docs](https://fly.io/docs/flyctl/launch/)
- [Flyctl meets JSON](https://fly.io/blog/flyctl-meets-json/)
- [Fly Rollback Guide](https://fly.io/docs/blueprints/rollback-guide/)
- [Fly Machines API](https://fly.io/docs/mcp/deploy-with/machines-api/)
- [Vercel CLI deploy](https://vercel.com/docs/cli/deploy)
- [Vercel CLI rollback](https://vercel.com/docs/cli/rollback)
- [Vercel CLI promote](https://vercel.com/docs/cli/promote)
- [Vercel REST API — create deployment](https://vercel.com/docs/rest-api/reference/endpoints/deployments/create-a-new-deployment)
- [Vercel access tokens](https://vercel.com/kb/guide/how-do-i-use-a-vercel-api-access-token)
- [chronark/vercel-go (community)](https://github.com/chronark/vercel-go)
- [Wrangler commands](https://developers.cloudflare.com/workers/wrangler/commands/)
- [Wrangler configuration](https://developers.cloudflare.com/workers/wrangler/configuration/)
- [Wrangler system env vars](https://developers.cloudflare.com/workers/wrangler/system-environment-variables/)
- [Workers rollbacks](https://developers.cloudflare.com/workers/configuration/versions-and-deployments/rollbacks/)
- [Pages → Workers migration](https://developers.cloudflare.com/workers/static-assets/migration-guides/migrate-from-pages/)
- [cloudflare-go SDK](https://github.com/cloudflare/cloudflare-go)
- [Wrangler structured output (community thread)](https://community.cloudflare.com/t/workers-capture-wrangler-command-output-in-structured-format/913777)
- [Cloudflare rebuilds Wrangler CLI (The Register, 2026-04-13)](https://www.theregister.com/2026/04/13/cloudflare_expanding_wrangler_cli_functionality/)
- [Kubernetes health endpoints](https://kubernetes.io/docs/reference/using-api/health-checks/)
