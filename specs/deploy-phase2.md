<!-- STATUS: done -->
<!-- CREATED: 2026-04-20 -->
<!-- BUILD_STARTED: 2026-04-21 -->
<!-- BUILD_COMPLETED: 2026-04-21 -->
<!-- DEPENDS_ON: spec-3 (Executor), spec-4 (browser), spec-6 (Deployer interface + Fly adapter), spec-7 (Operator.Ask) -->
<!-- BUILD_ORDER: 9 -->

# Deploy Phase 2 — Vercel + Cloudflare Adapters — Implementation Spec

## Overview

Extends spec-6 (`internal/deploy/`) with two additional providers — Vercel and Cloudflare Workers — that implement the same `Deployer` interface established for Fly.io. Adds a provider registry so future adapters drop in via init-time registration, extends the stack detector with Vercel/Cloudflare signals, adds config-file templates (`vercel.json`, `wrangler.toml`), and wires `stoke deploy --provider <name>` + `--auto` for selection. Per D-2026-04-20-03, this is the follow-on to Fly-only v1. Reuses the spec-6 verification cascade, `HealthStatus`, auto-rollback triple predicate, and event taxonomy unchanged — the only per-provider differences are CLI contract, URL extraction, and rollback call shape.

## Stack & Versions

- Go 1.22+ (Stoke's toolchain; same as spec-6)
- `vercel` CLI on `$PATH`; Stoke shells out — no official Go SDK (RT-10 §2). Community `chronark/vercel-go` intentionally NOT imported.
- `wrangler` CLI on `$PATH`; `WRANGLER_OUTPUT_FILE_PATH` env for structured NDJSON (RT-10 §3). `github.com/cloudflare/cloudflare-go` is intentionally NOT imported in this spec (rollback/list via CLI only; SDK reserved for later post-deploy ops per RT-10 recommendation).
- Reuses `internal/deploy/types.go` from spec-6 — same `Deployer`, `DeployTarget`, `DeployResult`, `HealthStatus`.
- Reuses `internal/browser/` (spec-4) verification pool.
- Reuses `internal/bus/` + `internal/streamjson/` event taxonomy from spec-6 verbatim.

## Existing Patterns to Follow

- Deployer interface: `internal/deploy/types.go` (spec-6) — implement identically, no interface change.
- Process spawn + stream parsing: `engine/` runners + spec-6 `internal/deploy/fly.go` — `cmd.Dir`, `Setpgid: true`, 3-tier timeouts.
- NDJSON parsing: `stream/` — drain-on-EOF, tolerate malformed lines, retry budget.
- Token redaction: `logging/redact.go` extended in spec-6 for `FLY_API_TOKEN` — add patterns here for Vercel + Cloudflare tokens.
- Verification cascade: `internal/deploy/verify.go` (spec-6) — consumed as-is; adapters only supply URL + rollback.
- Executor wrapper: `internal/executor/deploy.go` (spec-6) — extended to accept any `Deployer` from the registry, not a hard-coded `FlyDeployer`.
- Operator prompt: `Operator.Ask` from spec-7 for multi-choice disambiguation.

## Library Preferences

- Shell out to `vercel` and `wrangler`; do NOT add third-party SDKs in this spec.
- JSON parsing of Wrangler NDJSON: stdlib `encoding/json` + line-splitter from `stream/`.
- URL regex for Vercel stdout scrape: stdlib `regexp`.
- File writes for `vercel.json` / `wrangler.toml`: `atomicfs/` (transactional; same as fly.toml in spec-6).
- Do NOT import `github.com/cloudflare/cloudflare-go` in this spec (reserved for a later post-deploy ops spec).

## Provider Registry (`internal/deploy/registry.go`)

Solves "multiple providers" without interface churn. Init-time registration; spec-6's Fly adapter calls `Register("fly", newFlyDeployer)` in its own `init()`; this spec adds two more registrants.

```go
type Factory func(cfg map[string]string) (Deployer, error)

var registry = map[string]Factory{}

func Register(name string, f Factory) { /* panic on dup */ }
func Get(name string) (Factory, bool) { /* lookup */ }
func Names() []string                 { /* sorted */ }
```

Rules:
- Each provider package calls `Register` from its own `init()`; `internal/executor/deploy.go` imports `internal/deploy/fly`, `internal/deploy/vercel`, `internal/deploy/cloudflare` for side effects.
- `Factory` takes a flat `map[string]string` (the `DeployTarget.Config` map) — adapters parse provider-specific keys. Keeps the interface open for future adapters without broadening `DeployTarget`.
- `Names()` drives `--provider` flag validation and error messages (`"unknown provider 'foo'; known: cloudflare, fly, vercel"`).

## Vercel CLI Contract

All shell-outs use `cmd.Dir = workspaceRoot` and `Setpgid: true`. `VERCEL_TOKEN` passed via child env only; never on argv, never logged.

| Command | When | Expected stdout | Expected exit |
|---|---|---|---|
| `vercel whoami --token $VERCEL_TOKEN` | pre-deploy sanity | account or team slug | 0 on auth ok; 1 on bad token |
| `vercel ls --token $VERCEL_TOKEN --json` | capture previous deployment id | JSON array of deployments with `uid`, `url`, `state` | 0 on ok |
| `vercel deploy --yes --token $VERCEL_TOKEN` (preview) or `vercel deploy --prod --yes --token $VERCEL_TOKEN` | primary deploy | **stdout is the deployment URL** (one line, trimmed) | 0 on success; 1 on build fail |
| `vercel rollback <url-or-id> --yes --token $VERCEL_TOKEN` | rollback | `Rolled back to <id>` | 0 on success; 1 on failure |
| `vercel promote <url> --yes --token $VERCEL_TOKEN` | promote preview to prod (optional) | confirmation line | 0 / 1 |
| `vercel inspect <url> --token $VERCEL_TOKEN --json` | resolve `readyState`, capture image-equivalent id | JSON with `uid`, `readyState`, `url`, `createdAt` | 0 / 1 |

### URL extraction

Vercel CLI has no `--json` on `deploy` (RT-10 §2). Reliable rule: **stdout is always the URL; everything else is stderr.** Extraction:

1. Read child stdout fully.
2. `strings.TrimSpace` — expect a single line.
3. Validate with regex: `^https://[a-z0-9-]+(?:-[a-z0-9]{9})?(?:-[a-z0-9-]+)?\.vercel\.app$` (captures both preview `project-hash-team.vercel.app` and production aliases).
4. If regex fails, scan stdout+stderr for any `https://…vercel.app` and emit a warning event; treat first match as URL. If no match, return `stokerr.E("DEPLOY_URL_PARSE", …)`.

### Deployment id for rollback

`vercel deploy` prints only a URL. Capture `DeployResult.ImageTag` by running `vercel inspect <url> --json` post-deploy and reading `.uid`. Capture `DeployResult.PrevImage` by running `vercel ls --json` **before** deploy and taking the newest `.uid` with `state=="READY"` on the relevant target (preview or prod matching the current run). Empty → first-ever deploy → rollback skipped.

### Exit codes

- 0 — success
- 1 — generic failure (auth, build, deploy error)
- Other non-zero — treated as flag/usage error; surface stderr verbatim through `logging.Redact`.

### Gotchas

- **Framework detection.** Vercel zero-configs Next.js / SvelteKit / Nuxt / Astro / Remix / Vite (RT-10 §2). Stoke detects stack from package.json but **defers the build config to Vercel** — does not pass framework presets on argv. Only writes `vercel.json` when operator confirms via `--write-config` or when the stack is unrecognized and the operator opts in via `Operator.Ask`.
- **Preview vs production.** `vercel deploy` (no `--prod`) yields a preview URL; `vercel deploy --prod` targets the production alias. `--env` is a Vercel env-var flag (not environment-name); Stoke's `--env <name>` maps to `vercel deploy --target <name>` when name ∉ {"preview","production"}.
- **Team/org linking.** If `.vercel/project.json` is absent, `vercel deploy` prompts interactively. Stoke must export `VERCEL_ORG_ID` + `VERCEL_PROJECT_ID` (from DeployTarget.Config) to force non-interactive; when either is missing, call `Operator.Ask` with options `["link existing", "create new", "abort"]`.
- **No rollback across framework changes.** Vercel docs note rollback preserves the immutable function bundle; a rollback across a `vercel.json` `builds` change still works (same bundle pointer). Do not special-case.
- **`--prebuilt`.** Skip server build by uploading `.vercel/output/` (RT-10 §2). Not used by default; surface via `--prebuilt` flag on `stoke deploy` for advanced callers.

## Wrangler NDJSON Contract

Cloudflare's stable programmatic channel is `WRANGLER_OUTPUT_FILE_PATH=<path>` → Wrangler appends NDJSON events (RT-10 §3). Stoke creates the temp file, sets the env, tails the file as Wrangler writes, and ignores stdout except for human-readable fallback.

### Event shape

Each line is a JSON object. Observed event `type` values (from RT-10 + Wrangler source as of 2026-04):

| `type` | Meaning | Payload keys of interest |
|---|---|---|
| `wrangler-session` | Session start; opens the stream | `wrangler_version`, `command`, `timestamp` |
| `deployment-started` | Upload phase begin | `worker_name`, `script_name` |
| `version-uploaded` | Worker version stored | `version_id`, `worker_tag` |
| `deployment-complete` | Deploy finalized | `url`, `version_id`, `deployment_id`, `preview_url` (optional), `targets[]` |
| `rollback-complete` | `wrangler rollback` finished | `version_id`, `rolled_back_from` |
| `error` | Any failure | `code`, `message`, `cause` |
| `warning` | Non-fatal | `message` |

Stoke does NOT assume this list is exhaustive — every unknown `type` is logged and ignored so Wrangler's 2026 CLI churn (below) doesn't break the parser.

### Parser sketch (`internal/deploy/cloudflare/ndjson.go`)

```go
type WranglerEvent struct {
    Type      string          `json:"type"`
    Timestamp string          `json:"timestamp"`
    Raw       json.RawMessage `json:"-"` // full line, for fallback decoding
}

func TailNDJSON(ctx context.Context, path string, sink chan<- WranglerEvent) error
// - opens path O_RDONLY|O_CREATE (wrangler appends)
// - uses fsnotify when available, 250ms poll fallback
// - each complete line → json.Unmarshal into WranglerEvent; Raw preserved
// - sends to sink; closes sink on ctx.Done() or process exit signal from caller
// - tolerates malformed lines (skip + warn); 5 consecutive malformed → error
```

### URL extraction

Canonical source: `deployment-complete.url`. Fallback cascade if NDJSON absent or malformed:
1. Parse stdout for `Published .* → (https://\S+)` regex (Wrangler human line).
2. Derive from config: `https://{name}.{subdomain}.workers.dev` (subdomain fetched via `wrangler whoami`; cached per `CLOUDFLARE_ACCOUNT_ID`).
3. If all three fail → `stokerr.E("DEPLOY_URL_PARSE", …)`.

### Version id for rollback

`DeployResult.ImageTag` = `deployment-complete.version_id`. `DeployResult.PrevImage` captured pre-deploy via `wrangler versions list --json` → newest active version's `id`. Empty list → first-ever deploy → rollback skipped.

### Gotchas

- **`deployment_id` ≠ `version_id`.** `wrangler rollback` takes `version_id`. Do not confuse the two.
- **Partial writes.** `WRANGLER_OUTPUT_FILE_PATH` is append-only but Wrangler can die mid-line; always keep a holdback buffer and only emit once `\n` is observed.
- **Concurrent writes.** Stoke owns the path; `internal/fileutil/` creates it with `O_EXCL`; clean up in `defer`.

## Cloudflare Flag Churn Mitigation

Per RT-10 §3, Cloudflare announced in April 2026 they are rebuilding Wrangler's CLI codegen pipeline; Pages is folding into Workers. Design for churn:

1. **Target Workers + static assets as primary.** `wrangler deploy` (Workers) — not `wrangler pages deploy`. Pages path kept behind `--cf-mode=pages` for legacy projects (detected via `functions/` dir or `_worker.js`); emits a deprecation warning event.
2. **Version-gate by detected CLI.** On startup, `wrangler --version` → parse semver. Stoke maintains a small compatibility table:
   - `>= 4.x`: expected current; full NDJSON + Workers-first.
   - `3.x`: older NDJSON schema; some events missing → fallback to stdout regex.
   - `< 3.x`: warn + refuse; operator must upgrade.
3. **Unknown-event tolerance.** Parser never fails on unrecognized `type` strings; only on JSON malformation. A new event type is logged and ignored, not a build break.
4. **Flag whitelist, not passthrough.** Stoke translates `DeployTarget.Config` into a fixed whitelist of flags (`--env`, `--var`, `--compatibility-date`, `--dry-run`, `--outdir`, `--name`). Operators who need exotic flags use a `pre_deploy_hook` shell command (spec-6 extension point) rather than magic passthrough.
5. **Pinned minimum feature set.** Stoke depends on these wrangler capabilities only: `deploy`, `rollback`, `versions list --json`, `whoami`, `--version`, `WRANGLER_OUTPUT_FILE_PATH`. Any feature outside this set is out of scope.
6. **Per-release smoke test.** CI has a nightly job that runs `stoke deploy --provider cloudflare --dry-run` against a fixture worker using the latest Wrangler; failures open a tracking issue before operator-visible breakage.

## Vercel Adapter (`internal/deploy/vercel/vercel.go`)

Implements `deploy.Deployer`.

### Data (config keys parsed from `DeployTarget.Config`)

| Key | Required | Meaning | Default |
|---|---|---|---|
| `token` | yes (or env `VERCEL_TOKEN`) | API token | — |
| `org_id` | recommended | `VERCEL_ORG_ID` | read from `.vercel/project.json` |
| `project_id` | recommended | `VERCEL_PROJECT_ID` | read from `.vercel/project.json` |
| `prod` | no | if `"true"`, deploy to production | `"false"` (preview) |
| `target` | no | custom target name (mapped to `--target`) | unset |
| `prebuilt` | no | `"true"` → `--prebuilt` | `"false"` |
| `scope` | no | team slug | unset |
| `force` | no | `"true"` → `--force` (bypass build cache) | `"false"` |

### Deploy

1. `vercel whoami --token $VERCEL_TOKEN` — error → env-fix.
2. `vercel ls --json --token $VERCEL_TOKEN` — capture newest `READY` deploy id as `PrevImage`. Missing project / empty list → `PrevImage=""`.
3. Build argv: `vercel deploy` + `--yes` + (`--prod` if prod) + (`--target <t>` if target) + (`--prebuilt` if prebuilt) + (`--scope <s>` if scope) + (`--force` if force). Pass `VERCEL_TOKEN`, `VERCEL_ORG_ID`, `VERCEL_PROJECT_ID` via `cmd.Env`.
4. Capture stdout to buffer; trim; validate URL (regex above).
5. `vercel inspect <url> --json` → `.uid` → `DeployResult.ImageTag`.
6. Return `DeployResult{URL, ImageTag, PrevImage, ReleaseID: .uid, Latency}`.

### Verify

Delegate to `internal/deploy/verify.go` (spec-6) unchanged. Vercel DNS is instant; warm-up 2s default.

### Rollback

1. If `prevImage == ""` → `stokerr.E("ROLLBACK_NO_PREV_IMAGE", …)`.
2. `vercel rollback <prevImage> --yes --token $VERCEL_TOKEN --timeout 3m`.
3. Poll `vercel inspect <prevImage> --json` for `readyState == "READY"`, 2s interval, 3m budget.
4. Do not re-verify inside Rollback — caller decides.

## Cloudflare Adapter (`internal/deploy/cloudflare/cloudflare.go`)

Implements `deploy.Deployer`.

### Data (config keys)

| Key | Required | Meaning | Default |
|---|---|---|---|
| `api_token` | yes (or env `CLOUDFLARE_API_TOKEN`) | token | — |
| `account_id` | yes (or env `CLOUDFLARE_ACCOUNT_ID`) | account | — |
| `name` | yes | Worker script name | from wrangler.toml |
| `config_path` | no | path to `wrangler.toml` | `./wrangler.toml` |
| `env` | no | Wrangler environment name | unset |
| `mode` | no | `"workers"` or `"pages"` | `"workers"` |
| `vars` | no | comma-separated `K=V,K2=V2` | unset |

### Deploy

1. `wrangler --version` → version gate (see §Flag Churn Mitigation).
2. `wrangler whoami` (with `CLOUDFLARE_API_TOKEN` in env) — error → env-fix.
3. `wrangler versions list --json` → newest `active` version's `id` → `PrevImage`. Empty → `""`.
4. Create temp NDJSON file; set `WRANGLER_OUTPUT_FILE_PATH=<temp>`.
5. Spawn wrangler:
   - Workers mode: `wrangler deploy [--config <path>] [--env <env>] [--var K:V …]`.
   - Pages mode (legacy): `wrangler pages deploy <out_dir> --project-name <name> [--branch <env>]`.
6. Concurrently tail temp NDJSON; forward `deployment-started` / `version-uploaded` / `deployment-complete` events to `deploy.progress` bus subtype.
7. On `deployment-complete`: capture `url`, `version_id`, `deployment_id`.
8. Wait for child exit; error if non-zero or no `deployment-complete` received within budget.
9. Return `DeployResult{URL, ImageTag: version_id, PrevImage, ReleaseID: deployment_id, Latency}`.

### Verify

Delegate to `internal/deploy/verify.go` (spec-6). CF anycast DNS instant; warm-up <50ms typical; keep the 2s shared post-deploy wait.

### Rollback

1. If `prevImage == ""` → `stokerr.E("ROLLBACK_NO_PREV_IMAGE", …)`.
2. `wrangler rollback <prevImage> --message "stoke auto-rollback" --yes` with `WRANGLER_OUTPUT_FILE_PATH` set.
3. Wait for `rollback-complete` event in NDJSON OR child exit 0. 3m budget.
4. Do not re-verify inside Rollback.

## Config File Templates

Stoke writes these only when operator confirms via `Operator.Ask` or `--write-config`. Never overwrites an existing file; uses `atomicfs.WriteFile`.

### `vercel.json` — minimal Next.js (most projects omit entirely)

Vercel auto-detects Next.js; the default is "no vercel.json at all." When the operator insists on generating one (e.g., to pin region/env):

```json
{
  "$schema": "https://openapi.vercel.sh/vercel.json",
  "version": 2,
  "framework": "nextjs",
  "regions": ["iad1"],
  "env": {
    "NODE_ENV": "production"
  }
}
```

Docs inline (emitted as comments in a sibling `vercel.json.md` since JSON lacks comments):

- `VERCEL_TOKEN` — deploy auth (env var only).
- `VERCEL_ORG_ID`, `VERCEL_PROJECT_ID` — non-interactive linking.
- `VERCEL_ENV` — auto-set by Vercel per deploy (`production` | `preview` | `development`).

### `vercel.json` — plain Node/Express as serverless

```json
{
  "$schema": "https://openapi.vercel.sh/vercel.json",
  "version": 2,
  "builds": [
    { "src": "server.js", "use": "@vercel/node" }
  ],
  "routes": [
    { "src": "/(.*)", "dest": "/server.js" }
  ]
}
```

### `wrangler.toml` — Workers + static assets (primary target)

```toml
name = "{{NAME}}"
main = "src/index.js"
compatibility_date = "{{DATE}}"   # default: today's date at generation time
compatibility_flags = ["nodejs_compat"]

[assets]
  directory = "./public"
  binding = "ASSETS"

# Env-var docs:
#   CLOUDFLARE_API_TOKEN   — deploy auth; scopes: Workers Scripts:Edit,
#                            Account Settings:Read, User Details:Read
#   CLOUDFLARE_ACCOUNT_ID  — target account
#   WRANGLER_OUTPUT_FILE_PATH — set by Stoke; do not override
```

### `wrangler.toml` — pure Worker (no static assets)

```toml
name = "{{NAME}}"
main = "src/index.js"
compatibility_date = "{{DATE}}"
```

### `wrangler.toml` — legacy Pages (generated only if `--cf-mode=pages`)

Emits a deprecation banner event on first use; content stripped of `[assets]` block because Pages uses a different directory convention.

## Stack Detection Extensions (`internal/deploy/detect.go`)

Extend spec-6's 6-step detector by inserting two provider-specific branches **before** the Fly-native check so provider signals beat stack signals. Revised order:

1. **Vercel-first signals** — any of:
   - `vercel.json` present, OR
   - `next.config.{js,ts,mjs}` present AND `package.json` has `"next"` in deps, OR
   - `svelte.config.js` present AND the file references `@sveltejs/adapter-vercel`, OR
   - `.vercel/project.json` present.

   → `Stack="vercel"`, provider=`vercel`. Emit event `deploy.detect.match` with signal list.

2. **Cloudflare-first signals** — any of:
   - `wrangler.toml` or `wrangler.jsonc` present, OR
   - `_worker.js` at repo root (Pages advanced mode → still suggests CF), OR
   - `functions/` directory with `[[route]]` entries in wrangler-compatible shape.

   → `Stack="cloudflare"`, provider=`cloudflare`, `mode="workers"` (or `"pages"` if only `functions/` + no `wrangler.toml`).

3. **Fly-native** (existing): `fly.toml` → `Stack="fly-native"`, provider=`fly`.

4. **Fly-compatible stack branches** (Docker / Next.js / Node / Go / fallback) — unchanged from spec-6.

### Ambiguity rules

- `vercel.json` + `fly.toml` both present → prefer Vercel (more specific intent); emit warning; operator can override via `--provider fly`.
- `wrangler.toml` + `vercel.json` both present → prompt `Operator.Ask` with choices `["vercel", "cloudflare", "abort"]`.
- Next.js + no provider config → recommended=Vercel, but call `Operator.Ask` with `["vercel", "fly"]` and default=vercel.
- Static site with no provider config → `Operator.Ask` with `["vercel", "cloudflare", "fly"]`.
- Unrecognized stack AND no provider config → spec-6's fallback (operator-chosen stack + generate Fly config) unchanged.

### Detection result

Returns `DetectResult{Provider, Stack, Mode?, ConfigPath?, Signals []string}`. Auto-mode CLI (`--auto`) uses `.Provider`; explicit `--provider` overrides detection but still logs `Signals` for the ledger.

## Provider Selection Matrix

| Signal | Recommended | Fallback | Notes |
|---|---|---|---|
| `vercel.json` | vercel | — | highest-specificity intent |
| Next.js + `@sveltejs/adapter-vercel` | vercel | fly | operator can override |
| `wrangler.toml` | cloudflare (workers) | — | |
| `_worker.js` + no `wrangler.toml` | cloudflare (pages) | cloudflare (workers) | pages deprecation warning |
| `functions/` only | cloudflare (pages) | cloudflare (workers) | |
| `fly.toml` | fly | — | existing spec-6 |
| `Dockerfile` only | fly | vercel | Vercel can serverless-wrap small Docker |
| `package.json` + Next.js, no `.vercel/` | vercel | fly | default=vercel; prompt in `--auto` |
| `package.json`, no framework config | fly | vercel | generic Node most reliable on Fly |
| `go.mod` | fly | — | Go not supported by Vercel/CF runtimes |
| static (`index.html` only) | vercel | cloudflare (workers+assets) | both excellent |

## Provider-Selection CLI

Extends `stoke deploy` from spec-6. New/changed flags:

```
--provider <name>      "fly" | "vercel" | "cloudflare" | "auto"; default "auto"
--auto                 alias for --provider auto; runs stack detector
--env <name>           environment name; mapped per provider
                       (Vercel: --target; CF: --env; Fly: --env unused (uses fly.toml))
--prod                 alias for --env production; mutually exclusive with --env
--cf-mode <m>          "workers" | "pages"; default workers
--write-config         offer to write provider config file when absent
--prebuilt             Vercel: upload .vercel/output/
```

### Routing

1. Resolve provider:
   - `--provider <x>` where `x != "auto"` → use `x`; validate via `deploy.Get(x)`; unknown → exit 2.
   - `--provider auto` or unset → call `DetectStack(workspaceRoot)`; use `.Provider`; on ambiguity, `Operator.Ask`.
2. Load `Factory` from registry; build `Deployer` with `DeployTarget.Config` populated from flags + env.
3. Reuse spec-6's `Execute` flow unchanged — auto-rollback, event emission, exit codes identical.

### Exit codes (unchanged from spec-6)

- 0 — healthy deploy
- 1 — verify fail + rollback
- 2 — auth/env/usage
- 3 — operator abort

## Post-Deploy Verification Parity

Unchanged from spec-6. All three providers use:

- Same `HealthStatus` struct.
- Same cascade: HTTP 200 → probe cascade (`/healthz` → `/health` → (`/livez`+`/readyz`) → `/_health`) → browser verify.
- Same auto-rollback predicate: `StatusCode != 200 && len(ConsoleErrs) > 0 && elapsed > 30s`.
- Same warm-up: 2s post-deploy wait, retries 1s/2s/4s/8s.
- Provider-specific rollback invocation only; trigger logic and event taxonomy identical.

## Error Handling

| Failure | Strategy | Operator sees |
|---|---|---|
| `VERCEL_TOKEN` unset/invalid | env-fix prompt | "VERCEL_TOKEN missing/invalid; set it and retry" |
| `CLOUDFLARE_API_TOKEN` unset/invalid | env-fix prompt | "CLOUDFLARE_API_TOKEN missing/invalid" |
| `CLOUDFLARE_ACCOUNT_ID` unset | env-fix prompt | "CLOUDFLARE_ACCOUNT_ID required" |
| `wrangler` version < 3.x | abort with guidance | "wrangler 4.x+ required; detected <ver>" |
| Vercel URL regex fails | scan for any `*.vercel.app`; warn | "deploy URL parse uncertain: <url>" |
| Wrangler NDJSON malformed (5 consecutive lines) | fallback to stdout regex | "structured output unavailable; using human output" |
| `deployment-complete` never received within budget | error | "wrangler timeout; no deployment-complete event" |
| Registry lookup miss (unknown provider) | exit 2 | "unknown provider 'foo'; known: cloudflare, fly, vercel" |
| Both `vercel.json` and `wrangler.toml` present | `Operator.Ask` | multi-choice prompt |
| Pages legacy mode used | deprecation warning event | "Pages mode deprecated; consider Workers + assets" |
| Rollback with `prevImage == ""` | skip + emit `deploy.rollback.skipped` | "no previous version to roll back to" |

## Token Security

Extend `logging/redact.go` (spec-6 already patched for Fly) with:

- Vercel: `VERCEL_TOKEN` env name; tokens are opaque ~24-char strings; safer to redact the env-var *value* wherever the literal env assignment appears. Pattern: `VERCEL_TOKEN=\S+` → `VERCEL_TOKEN=<redacted>`; also redact `--token=\S+` and `--token \S+` in stringified argv.
- Cloudflare: `CLOUDFLARE_API_TOKEN=\S+`, `CLOUDFLARE_ACCOUNT_ID=\S+`, `--api-token \S+`.
- Header form: `Authorization: Bearer \S+` (covers both when leaked from HTTP errors).

All `streamjson` events pass through `logging.RedactEvent` before emit. Add test `TestTokenNeverInEvents_PhaseTwo` that injects fake Vercel and Cloudflare tokens into a mock deploy and asserts no event payload or error string contains the fake.

## Boundaries — What NOT To Do

- Do NOT modify `internal/deploy/types.go` (spec-6) — no interface changes.
- Do NOT modify `internal/deploy/verify.go` (spec-6) — share verbatim.
- Do NOT modify `internal/deploy/fly.go` (spec-6) — only extend the registry.
- Do NOT modify `internal/browser/` — consume its pool interface only.
- Do NOT import `github.com/chronark/vercel-go` (third-party, out of scope v2).
- Do NOT import `github.com/cloudflare/cloudflare-go` in this spec (reserved for later post-deploy ops spec).
- Do NOT parse `vercel deploy --json` (flag does not exist).
- Do NOT parse Wrangler stdout as primary channel; the NDJSON file is authoritative.
- Do NOT overwrite an existing `vercel.json` or `wrangler.toml`.
- Do NOT log `VERCEL_TOKEN` or `CLOUDFLARE_API_TOKEN`, even in stderr surfacing.
- Do NOT trigger rollback on single-factor failure (spec-6 predicate is canonical).
- Do NOT ship Pages mode as default (Workers-first per Cloudflare 2026 roadmap).

## Testing

### `internal/deploy/vercel/vercel_test.go`

- [ ] Happy path: mock `vercel` binary via `STOKE_VERCEL_BIN=<testbin>` → stdout `https://app-abc123.vercel.app` → `DeployResult.URL` matches, `ImageTag` captured from `vercel inspect` mock.
- [ ] URL regex validates preview form `project-hash-team.vercel.app` and production alias.
- [ ] `PrevImage == ""` on first deploy (empty `vercel ls --json`).
- [ ] Auth fail: `whoami` exit 1 → `stokerr.E("AUTH")`, deploy not invoked.
- [ ] `--prod` flag produces `--prod` in argv.
- [ ] `target` config maps to `--target <name>`.
- [ ] Rollback happy path: `vercel rollback <uid> --yes` → `rollback-complete` → success.
- [ ] Rollback with empty PrevImage → `ROLLBACK_NO_PREV_IMAGE`.
- [ ] Token never in events (`TestTokenNeverInEvents_Vercel`).
- [ ] Mangled stdout (two URLs on separate lines) → warning event, first-match wins.

### `internal/deploy/cloudflare/cloudflare_test.go`

- [ ] Happy path workers mode: mock `wrangler` writes NDJSON `deployment-complete` with `url` + `version_id` → `DeployResult` populated correctly.
- [ ] NDJSON parser consumes multi-event file: `wrangler-session`, `deployment-started`, `version-uploaded`, `deployment-complete` → events forwarded in order.
- [ ] `TestWranglerNDJSONParser` edge cases: malformed line tolerated (1-4 consecutive); 5+ consecutive → error; partial line held until newline.
- [ ] Unknown event `type` is logged and ignored (forward-compat with flag churn).
- [ ] `PrevImage=""` on empty `versions list --json`.
- [ ] Rollback happy: `wrangler rollback <vid>` + NDJSON `rollback-complete` → success.
- [ ] Version gate: wrangler 2.x → error "wrangler 4.x+ required".
- [ ] Missing `CLOUDFLARE_ACCOUNT_ID` → env-fix.
- [ ] Pages mode produces `wrangler pages deploy …` argv + deprecation warning event.
- [ ] Token never in events (`TestTokenNeverInEvents_Cloudflare`).

### `internal/deploy/detect_test.go` (extend spec-6's)

- [ ] `vercel.json` only → `Provider="vercel", Stack="vercel"`.
- [ ] `next.config.js` + `"next"` dep, no `vercel.json` → `Provider="vercel"` with signal list.
- [ ] `svelte.config.js` referencing `@sveltejs/adapter-vercel` → `Provider="vercel"`.
- [ ] `wrangler.toml` → `Provider="cloudflare", Mode="workers"`.
- [ ] `_worker.js` + no `wrangler.toml` → `Provider="cloudflare", Mode="pages"` with deprecation warning.
- [ ] `vercel.json` + `wrangler.toml` → `Operator.Ask` invoked.
- [ ] `vercel.json` + `fly.toml` → prefer vercel; warning event.

### `internal/deploy/registry_test.go`

- [ ] `Register("foo", factory)` then `Get("foo")` returns it.
- [ ] Duplicate registration panics.
- [ ] `Names()` returns sorted slice.
- [ ] Default registrations after all inits: `["cloudflare", "fly", "vercel"]`.

### `cmd/stoke/deploy_cmd_test.go` (extend spec-6's)

- [ ] `--provider vercel --dry-run` prints vercel argv preview; no network.
- [ ] `--provider cloudflare --dry-run` prints wrangler argv + NDJSON temp path preview.
- [ ] `--auto --dry-run` with `vercel.json` fixture → picks vercel.
- [ ] `--auto --dry-run` with `wrangler.toml` fixture → picks cloudflare.
- [ ] `--provider bogus` → exit 2 with message listing known providers.
- [ ] `--prod --env staging` → exit 2 (mutually exclusive).

## Acceptance Criteria

Run from repo root; all must exit 0.

```bash
# 1. Packages build and vet
go build ./internal/deploy/vercel/... ./internal/deploy/cloudflare/... ./cmd/stoke
go vet ./internal/deploy/... ./cmd/stoke

# 2. Adapter tests
go test ./internal/deploy/... -run TestVercelDeployer
go test ./internal/deploy/... -run TestCloudflareDeployer
go test ./internal/deploy/... -run TestWranglerNDJSONParser

# 3. Registry and detection
go test ./internal/deploy/... -run TestRegistry
go test ./internal/deploy/... -run TestStackDetection_Phase2

# 4. CLI surface
./stoke deploy --provider vercel --dry-run | grep -q 'vercel deploy'
./stoke deploy --provider cloudflare --dry-run | grep -q 'wrangler deploy'
./stoke deploy --auto --dry-run --repo /tmp/nextjs-app | grep -q 'vercel'
./stoke deploy --provider bogus --dry-run; test $? -eq 2

# 5. Token redaction
go test ./internal/deploy/... -run TestTokenNeverInEvents_PhaseTwo

# 6. Executor integration unchanged
go test ./internal/executor/... -run TestDeployExecutor_MultiProvider
```

## Implementation Checklist

1. [ ] Create `internal/deploy/registry.go` with `Factory`, `Register`, `Get`, `Names`. Panics on duplicate; sorted `Names()`. Unit tests in `registry_test.go`.
2. [ ] In spec-6's `internal/deploy/fly/fly.go` (or wherever the Fly adapter lives post-spec-6), add an `init()` that calls `deploy.Register("fly", newFlyDeployer)`. No behavior change; test that registry contains `"fly"` after import.
3. [ ] Create `internal/deploy/vercel/vercel.go` implementing `deploy.Deployer`. Includes `vercelDeployer` struct, `Deploy`, `Verify`, `Rollback`, private `captureCurrentDeployment`, `runVercel`. Use `os/exec`, `Setpgid`, token via `cmd.Env`. Register via `init()`.
4. [ ] Create `internal/deploy/vercel/url.go` — URL regex + extraction with fallback scan; unit-test preview + production URL shapes.
5. [ ] Create `internal/deploy/cloudflare/ndjson.go` implementing `TailNDJSON` with fsnotify-or-poll; tolerate malformed lines; hold back partial lines. Unit tests for every documented event type + unknown types.
6. [ ] Create `internal/deploy/cloudflare/cloudflare.go` implementing `deploy.Deployer`. Wire NDJSON tail to bus `deploy.progress`. Workers-first; pages behind `mode="pages"`. Version gate via `wrangler --version`. Register via `init()`.
7. [ ] Extend `internal/deploy/detect.go` (spec-6) with Vercel + Cloudflare signal branches BEFORE Fly-native. Add `DetectResult.Signals []string`. Implement ambiguity rules (vercel+fly → vercel+warn; vercel+cf → Operator.Ask).
8. [ ] Create `internal/deploy/templates_phase2.go` with Vercel + Cloudflare config templates (verbatim from §Config File Templates). `Render(provider, stack, params) (path, content string, err error)`. Never overwrite; use `atomicfs`.
9. [ ] Extend `logging/redact.go` with Vercel + Cloudflare token patterns (literal env assignments, `--token`/`--api-token` flags, Bearer). Unit test `TestRedact_PhaseTwo`.
10. [ ] Extend `cmd/stoke/deploy_cmd.go` (spec-6) with `--provider` validation against `deploy.Names()`, `--auto`, `--env`/`--prod` mapping, `--cf-mode`, `--write-config`, `--prebuilt`. Dispatch via registry. Mutual exclusion checks.
11. [ ] Extend `internal/executor/deploy.go` (spec-6) to accept a `Deployer` selected from the registry (remove the hard-coded FlyDeployer construction; keep spec-6 behavior identical when `provider=="fly"`). All event payloads stay identical; only adapter implementation differs.
12. [ ] Golden fixtures: `cmd/stoke/testdata/deploy/phase2/*.golden.txt` for `--dry-run` output of each provider.
13. [ ] Mock binaries: `internal/deploy/vercel/vercel_mock_test.go` + `internal/deploy/cloudflare/cloudflare_mock_test.go` that stamp fake `vercel`/`wrangler` into `testing.TempDir()` and set `STOKE_VERCEL_BIN` / `STOKE_WRANGLER_BIN`. No real network in tests.
14. [ ] Update `CLAUDE.md` package map: under the "DEPLOYMENT" section added in spec-6, add `deploy/vercel/` (Vercel adapter), `deploy/cloudflare/` (Cloudflare Workers adapter + NDJSON parser), `deploy/registry.go` (provider factory registry). One-line descriptions only.
