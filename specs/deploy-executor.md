<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-3 (Executor), spec-4 (browser for verification) -->
<!-- BUILD_ORDER: 6 -->

# Deploy Executor (Fly.io) — Implementation Spec

## Overview

Add a deploy executor that ships the current workspace to Fly.io, verifies the result via the browser tool from spec-4, and auto-rolls-back on a strict triple-condition failure. Surfaces as `stoke deploy` and plugs into the `Executor` interface from spec-3 so deploy tasks slot into the existing plan/execute/verify machinery. Fly.io is the only provider in v1 (per D-2026-04-20-03); Vercel and Cloudflare are follow-on specs.

## Stack & Versions

- Go 1.22+ (Stoke's existing toolchain)
- `flyctl` (current stable) on `$PATH`; Stoke shells out — no `github.com/superfly/fly-go` import in v1
- `internal/browser/` from spec-4 (`go-rod` driver) for post-deploy verification
- `internal/bus/` for deploy lifecycle events
- `internal/streamjson/` for external deploy event wire (per C1)

## Existing Patterns to Follow

- Process spawn + stream parsing: `engine/` (Claude/Codex runners) — use `cmd.Dir`, process group, 3-tier timeouts
- NDJSON stream parsing: `stream/` (drain-on-EOF, 3-tier timeouts)
- Event emission: `internal/bus/` (publish) + `internal/streamjson/` (wire out)
- Executor shape: `internal/executor/code.go` (from spec-3) — Deploy mirrors Code's Execute/BuildCriteria/BuildRepairFunc/BuildEnvFixFunc shape
- Token redaction: `logging/` helpers already redact `CostUSD`/`APIKey`; extend for `FLY_API_TOKEN`
- Command factory: `cmd/stoke/run_cmd.go` (from spec-3) for cobra wiring

## Library Preferences

- Shell out to `flyctl`; do NOT import `fly-go` in v1 (authors self-doc: no stability guarantee)
- TOML write/read: `github.com/BurntSushi/toml` (already indirect in Stoke)
- HTTP probe: stdlib `net/http` with `context.WithTimeout`
- JSON parsing of `flyctl status --json`: stdlib `encoding/json`
- Browser ops: `internal/browser/` interface (spec-4) — do not import `rod` directly from `internal/deploy/`

## Data Models

### `internal/deploy/types.go`

```go
type DeployTarget struct {
    Provider string            // "fly" in v1
    Config   map[string]string // provider-specific keys: "app", "region", "org", "config_path", "strategy"
}

type DeployResult struct {
    URL         string         // e.g. "https://sentinel-api.fly.dev"
    CommitHash  string         // git HEAD at deploy time
    ImageTag    string         // registry.fly.io/<app>:deployment-<ts>, captured for rollback
    PrevImage   string         // pre-deploy current image, captured for one-call rollback
    ReleaseID   string         // flyctl release id (from status --json)
    HealthCheck bool           // Verify() passed
    Latency     time.Duration  // deploy wall time (Deploy call start → status "running")
}

type HealthStatus struct {
    URL         string
    StatusCode  int           // from GET /
    HealthProbe string        // probe path that won, or "" if none
    ProbeStatus int
    ConsoleErrs []string
    TTFB        time.Duration
    DNSReadyAt  time.Time
    Healthy     bool          // derived: StatusCode==200 && len(ConsoleErrs)==0 && TTFB<sla
}

type Deployer interface {
    Deploy(ctx context.Context, cfg DeployTarget) (*DeployResult, error)
    Verify(ctx context.Context, url string) (*HealthStatus, error)
    Rollback(ctx context.Context, prevImage string) error
}
```

## Fly.io CLI Contract

All shell-outs use `cmd.Dir = workspaceRoot` and `Setpgid: true`. `FLY_API_TOKEN` is passed via the child's env only; never logged.

| Command | When | Expected stdout | Expected exit |
|---|---|---|---|
| `flyctl auth whoami --access-token $FLY_API_TOKEN` | pre-deploy sanity | `<email>` or org token label | 0 on auth ok; 1 on bad token |
| `flyctl status --json --app <app>` | capture `PrevImage` | JSON with `.ImageRef`, `.Hostname`, `.Status` | 0 on ok; non-zero if app missing |
| `flyctl deploy --detach --config <path> --app <app> --strategy rolling --access-token $FLY_API_TOKEN` | primary deploy | release id in stderr line `--> image: ...` and `release v<N> created` | 0 on submit (detached); polls continue |
| `flyctl status --json --app <app>` (poll) | every 3s up to 5m | `.Status == "running"` and `.DeploymentStatus.Status == "successful"` | 0 on ok |
| `flyctl deploy --image <prevImage> --app <app> --access-token $FLY_API_TOKEN` | rollback | same as deploy | 0 on submit |
| `flyctl logs --app <app> --no-tail -n 200` | on 502/503 during verify | log lines | 0 |

Exit code conventions (observed):
- 0 — success
- 1 — generic failure (auth, build, config invalid)
- 2 — flag/usage error (treat as programmer bug, surface stderr verbatim)

Parse contract:
- `status --json`: `[{"ID","Status","ImageRef":{"Repository","Tag"},"Hostname","DeploymentStatus":{"Status","Description"}}]`
- Treat unmarshal failure as retryable; up to 3 consecutive malformed responses before erroring.

## fly.toml Templates

Stoke writes to `<workspaceRoot>/fly.toml` only if absent. Never overwrite a user-authored `fly.toml`.

### Docker template (Dockerfile present)

```toml
app = "{{APP}}"
primary_region = "{{REGION}}"  # default "iad"

[build]
  dockerfile = "Dockerfile"

[http_service]
  internal_port = {{PORT}}   # default 8080
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

### Node template (package.json + Next.js or generic Node)

```toml
app = "{{APP}}"
primary_region = "{{REGION}}"

[build]
  builder = "paketobuildpacks/builder:base"

[env]
  PORT = "3000"
  NODE_ENV = "production"

[http_service]
  internal_port = 3000
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0

[[http_service.checks]]
  grace_period = "15s"
  interval = "30s"
  method = "GET"
  path = "/"
  timeout = "5s"
```

### Go template (go.mod, no Dockerfile)

```toml
app = "{{APP}}"
primary_region = "{{REGION}}"

[build]
  builder = "paketobuildpacks/builder:base"
  buildpacks = ["gcr.io/paketo-buildpacks/go"]

[env]
  PORT = "8080"

[http_service]
  internal_port = 8080
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

## Stack Detection (`internal/deploy/detect.go`)

Order of checks; first match wins:

1. `fly.toml` exists at workspace root → `Stack="fly-native"`; parse `app`, `primary_region`; skip generation.
2. `Dockerfile` exists → `Stack="docker"`; use Docker template; sniff Dockerfile for `EXPOSE <port>` to fill `{{PORT}}` (default 8080).
3. `package.json` exists AND (`next.config.{js,ts,mjs}` present OR `"next"` in deps) → `Stack="nextjs"`; use Node template, internal_port 3000.
4. `package.json` exists, no Next.js → `Stack="node"`; Node template, internal_port 3000.
5. `go.mod` exists → `Stack="go"`; Go template, internal_port 8080.
6. Fallback → call `Operator.Ask` (from spec-7) with options: `["docker", "node", "go", "abort"]`.

App name default: kebab-case of workspace basename + short SHA of git HEAD (first 6 chars) to avoid collisions. Operator can override with `--app`.
Region default: `iad` (US East). Configurable via `--region` or `STOKE_FLY_REGION`.

## Verification Cascade

`Deployer.Verify(ctx, url)` runs in order, all with a shared deadline derived from `ctx` (default 45s wall):

1. **DNS + GET /** — `http.Head` with 8s timeout; retry 200-class with exponential backoff 1s/2s/4s/8s. Record TTFB from first successful request. `*.fly.dev` is wildcard so DNS is instant; warm-up is the machine cold-start, not DNS.
2. **Health probe cascade** — try in order, first 2xx wins: `/healthz` → `/health` → (`/livez` AND `/readyz` both 2xx) → `/_health`. 2s timeout each. Record winning probe path in `HealthProbe`. All-404 → `HealthProbe=""` and rely on step 1's 200.
3. **Browser verify** — acquire `Browser` from spec-4 pool; `Navigate(url)` with `networkidle` wait 10s; drain `ConsoleErrors()`; screenshot to `<session>/.stoke/deploy/<ts>.png` for the ledger.
4. **TTFB SLA** — default 3s; configurable via `DeployTarget.Config["ttfb_sla_ms"]` or `STOKE_DEPLOY_TTFB_MS`. `TTFB > sla` populates `ConsoleErrs` with `["stoke: ttfb=<x>ms exceeds sla=<y>ms"]` only when the fail would otherwise pass; do not double-count.
5. **Healthy** = `StatusCode==200 && len(ConsoleErrs)==0 && TTFB<sla`.

## Auto-Rollback Decision Tree

Per D24/D25. Computed inside `executor/deploy.go` after `Verify` returns.

```
elapsed := time.Since(deployStart)
trigger := (hs.StatusCode != 200) && (len(hs.ConsoleErrs) > 0) && (elapsed > 30*time.Second)
```

- All three must be true. Single-factor failure does NOT rollback (e.g., 502 at t=8s is still warm-up).
- On trigger:
  1. Emit `deploy.rollback` bus event with `{app, prev_image, reason, elapsed_ms, status_code, console_err_count}`.
  2. Call `Deployer.Rollback(ctx, result.PrevImage)` with a fresh 5-minute ctx.
  3. Emit `deploy.rollback.complete` or `deploy.rollback.failed`.
  4. Return error `stokerr.E("DEPLOY_VERIFY_FAILED", ...)` — the executor reports failure, repair/env-fix decide next step.
- If `result.PrevImage == ""` (first-ever deploy for the app), skip rollback step and emit `deploy.rollback.skipped` with reason `"no_prev_image"`.
- Timing is measured from `Deploy()` call start (captured in `deployStart`), NOT from submit time. This matches operator intuition.

## Token Security

- `FLY_API_TOKEN` is read once via `os.Getenv` at executor construction; stored in an unexported field.
- All `exec.Cmd` invocations pass it via `cmd.Env = append(os.Environ(), "FLY_API_TOKEN=…")` — never on argv.
- Logging redaction: extend `logging/redact.go` with patterns for `FLY_API_TOKEN`, `fo1_…` (Fly deploy tokens), `fm1_…` (Fly user tokens), `fm2_…`, and generic `Bearer fo[12m]_[A-Za-z0-9_-]{20,}`.
- streamjson events must pass through `logging.RedactEvent(evt)` before being written; add a `TestTokenNeverInEvents` test that injects a fake token and asserts no event payload contains it.
- On error paths that surface stdout/stderr, run `logging.Redact(output)` first.

## Deploy Executor (`internal/executor/deploy.go`)

Implements `Executor` from spec-3.

### Execute

```
Execute(ctx, plan, effort):
  1. DetectStack(workspaceRoot) → Stack
  2. If Stack != "fly-native": WriteTemplate(Stack, app, region, port) → fly.toml
  3. deployer := NewFlyDeployer(token, binPath)
  4. deployStart := time.Now()
  5. emit bus "deploy.start"   {app, stack, provider:"fly", commit}
  6. result, err := deployer.Deploy(ctx, target) — captures PrevImage first
     emit "deploy.progress" on each poll tick (status, message)
  7. emit "deploy.url" {url: result.URL}
  8. hs, verr := deployer.Verify(ctxVerify, result.URL)
     emit "deploy.verify.start" / "deploy.verify.end"
  9. If autoRollback(hs, time.Since(deployStart)):
        emit "deploy.rollback"
        deployer.Rollback(ctx, result.PrevImage)
        return nil, stokerr.E("DEPLOY_VERIFY_FAILED", …)
  10. result.HealthCheck = hs.Healthy
      result.Latency = time.Since(deployStart)
      emit "deploy.complete"
  11. return Deliverable{URL:result.URL, Artifacts:[screenshotPath, fly.toml]}, nil
```

### BuildCriteria(task, deliverable) → []AcceptanceCriterion

All four AC use `VerifyFunc` (per D13):

| ID | Criterion | VerifyFunc |
|---|---|---|
| `DEPLOY-URL-LIVE` | Deploy URL returns 200 | `GET url → StatusCode == 200` (3 retries, 8s each) |
| `HEALTH-ENDPOINT` | Health probe cascade passes | Run cascade; pass if any probe 2xx OR `/` 200 with no probe found |
| `NO-CONSOLE-ERRS` | Zero console errors at t=30s | Browser navigate; `len(ConsoleErrs) == 0` after networkidle |
| `TTFB-WITHIN-SLA` | TTFB < configured SLA | `TTFB < sla`; default 3000ms |

### BuildRepairFunc(plan)

Invoked when AC fails (pre-rollback or post-rollback analysis):
- Detect 502/503 in `Verify` — run `flyctl logs --app <app> --no-tail -n 200`; feed tail to LLM with prompt "Diagnose fly.io deploy failure; propose fly.toml patch or env/secrets change."
- Common patches the repair loop may apply:
  - Internal port mismatch — edit fly.toml `internal_port`
  - Missing `[env]` vars surfaced in logs — `flyctl secrets set KEY=... --app <app>`
  - Health check path wrong — edit `[[http_service.checks]].path`
  - `release command timeout` — raise `[deploy] release_command_timeout`
- Redeploy after patch; respects per-file repair cap `MaxRepairsPerFile = 3` (D2) for `fly.toml`.

### BuildEnvFixFunc

Environment-class failures (per D4; these skip repair and go straight to env fix):
- `Unauthorized` / `401` / `token is invalid` → surface operator prompt for new token; update `FLY_API_TOKEN`; retry once.
- `rate limit` / `429` → back off 60s; retry once.
- `DNS not ready` / GET-hits-502-during-warmup-for >60s despite `[[http_service.checks]]` reporting pass → wait 30s, retry once.
- `region not enabled for org` → surface to operator; abort.

## `stoke deploy` Command (`cmd/stoke/deploy_cmd.go`)

Cobra subcommand; reuses the `Operator` interface from spec-7 for terminal vs NDJSON output.

### Flags

```
--provider <name>      default "fly" (v1: only "fly" accepted; others error)
--app <name>           app name override (else derived from workspace)
--region <code>        default "iad"
--config <path>        fly.toml path override (default ./fly.toml)
--strategy <name>      rolling|bluegreen|canary|immediate (default rolling)
--verify-only          skip deploy, run Verify against --url
--url <url>            required with --verify-only
--rollback             call Rollback using last-deployed image tag from ledger
--dry-run              print planned fly.toml + flyctl argv; no network calls
--ttfb-sla <ms>        override TTFB SLA (default 3000)
--output <fmt>         "text" (terminal) | "stream-json" (CloudSwarm); default "text"
```

### Modes

1. **Primary deploy** (`stoke deploy --provider fly --app sentinel-api`):
   - Runs full Execute flow end-to-end.
   - Exits 0 on healthy deploy; 1 on verify fail + rollback; 2 on auth/env; 3 on operator abort.
2. **Verify-only** (`stoke deploy --verify-only --url https://sentinel-api.fly.dev`):
   - Skips Deploy; calls `Verify` only. Emits `deploy.verify.start/end`; no rollback logic.
   - Useful for CI "is the deploy still healthy?" checks.
3. **Manual rollback** (`stoke deploy --rollback --app sentinel-api`):
   - Fetches last two releases via `flyctl status`; rolls back to the prior image.
   - Prompts operator for confirmation unless `--yes` or `--output stream-json` (in which case emits `hitl_required` per D28).

### Event taxonomy (streamjson `_stoke.dev/*` subtype tree per C1)

| Event | When | Payload |
|---|---|---|
| `deploy.start` | before flyctl invoke | `{app, region, provider, stack, commit}` |
| `deploy.progress` | each `status --json` poll | `{status, deployment_status, message}` |
| `deploy.url` | URL known | `{url}` |
| `deploy.verify.start` | Verify begin | `{url, sla_ms}` |
| `deploy.verify.end` | Verify return | `{healthy, status_code, probe_path, ttfb_ms, console_errs}` |
| `deploy.rollback` | rollback trigger fired | `{reason, prev_image, elapsed_ms}` |
| `deploy.rollback.complete` / `deploy.rollback.failed` | rollback finish | `{prev_image, err?}` |
| `deploy.complete` | success | `{url, latency_ms, commit, release_id}` |

All events also publish to `internal/bus/` per C3.

## Business Logic

### FlyDeployer.Deploy

1. `flyctl auth whoami` precheck; error → env-fix.
2. `flyctl status --json --app <app>` → capture `.ImageRef` as `PrevImage`. Missing app → `PrevImage=""`.
3. `flyctl deploy --detach --config fly.toml --app <app> --strategy <s>`; capture child stdout/stderr streams with process-group isolation.
4. Poll `flyctl status --json --app <app>` every 3s, 5m total budget (configurable via `STOKE_FLY_POLL_TIMEOUT`). Break when `DeploymentStatus.Status == "successful"`. `"failed"` → return error with tail of logs (`flyctl logs --no-tail -n 100`).
5. Return `DeployResult` with URL `"https://"+app+".fly.dev"`, `ImageTag` from current `.ImageRef`, `PrevImage` captured in step 2.

### FlyDeployer.Verify

See Verification Cascade above.

### FlyDeployer.Rollback

1. Validate `prevImage != ""` — else return `stokerr.E("ROLLBACK_NO_PREV_IMAGE", ...)`.
2. `flyctl deploy --image <prevImage> --app <app>`.
3. Poll `flyctl status --json` up to 5m; success when `DeploymentStatus.Status == "successful"`.
4. Do not recursively verify inside Rollback — caller decides.

## Error Handling

| Failure | Strategy | Operator sees |
|---|---|---|
| `FLY_API_TOKEN` unset / invalid | BuildEnvFixFunc → prompt | "FLY_API_TOKEN missing/invalid; set it and retry" |
| `fly.toml` absent AND stack unknown | Operator.Ask fallback | multi-choice prompt |
| Build failure inside flyctl | Repair with log tail | "deploy build failed: <last err line>; proposing patch" |
| Health probe all-404 + GET 200 | Pass AC with `HealthProbe=""` | warning note only |
| Verify fail + rollback success | exit 1, ledger records both | "deploy failed verification; rolled back to <prev>" |
| Rollback fail | exit 1, page operator | "rollback failed: <err>; manual intervention required" |
| `prevImage==""` at rollback time | skip, exit 1 | "no previous image to roll back to" |
| Poll timeout (5m) | cancel child; best-effort `flyctl status`; error | "deploy did not reach running state within 5m" |

## Fly.io-Specific Gotchas (from RT-10)

- **Warm-up.** First request to a newly-released machine can be ~10s; retry GET / with exponential backoff 1s/2s/4s/8s before declaring down.
- **Regional variance.** `primary_region` default `iad`; `fra`/`syd` can add 100-200ms TTFB. Don't fail `TTFB-WITHIN-SLA` purely on region choice — document that SLA is latency from the user's viewpoint, set `--ttfb-sla` accordingly.
- **Build cache.** `flyctl deploy` caches builds by layer hash; after a `fly.toml` edit (e.g., buildpack change) add `--no-cache` on the next deploy. Repair loop should set `--no-cache` after any `fly.toml` patch.
- **Min-machines-running=0.** Cold starts are real; for latency-sensitive tasks raise to 1 (emit a warning when detected in fly.toml — not auto-edited).
- **Release command timeout.** Default 5m; long migrations fail silently as "Machine started but didn't become healthy." Repair detects and raises to 10m with operator consent.
- **`*.fly.dev` DNS.** Wildcard, instant. Do not insert artificial DNS wait.
- **Auth token scopes.** Deploy tokens (`fly tokens create deploy`) are preferred over user tokens. `whoami` returns different shapes; tolerate either.

## Boundaries — What NOT To Do

- Do NOT import `github.com/superfly/fly-go` in v1 (deferred).
- Do NOT build a Vercel or Cloudflare adapter in this spec.
- Do NOT modify `internal/executor/code.go`; add the deploy executor alongside it.
- Do NOT modify `internal/browser/` — consume its interface only.
- Do NOT define a new Executor interface — reuse spec-3's.
- Do NOT parse `flyctl deploy --json` stdout stream (mixed NDJSON + plaintext per RT-10); use `--detach` + poll `flyctl status --json` instead.
- Do NOT overwrite a user's existing `fly.toml`.
- Do NOT log `FLY_API_TOKEN` or raw `flyctl` stderr without `logging.Redact`.
- Do NOT trigger rollback on single-factor failure; all three of `status!=200`, `consoleErrs>0`, `elapsed>30s` must hold.

## Testing

### `internal/deploy/fly_test.go`

- [ ] Happy path: mock `flyctl` via `STOKE_FLYCTL_BIN=<testbin>` → `Deploy` returns `{URL:"https://app.fly.dev", PrevImage:"registry.fly.io/app:old"}`.
- [ ] `PrevImage=""` on first deploy: `flyctl status --json --app app` returns exit 1 with "Could not find App" → Deploy proceeds; `PrevImage==""`.
- [ ] Auth fail: fake `whoami` exits 1 → returns `stokerr.E("AUTH")`, no deploy invoked.
- [ ] Poll timeout: fake status always reports `"pending"` → error after 5m simulated ticks (use injected clock).
- [ ] Malformed JSON tolerance: 2 malformed → retry, 3rd success → pass; 3 consecutive malformed → error.
- [ ] Rollback requires `prevImage != ""`; empty → `stokerr.E("ROLLBACK_NO_PREV_IMAGE")`.
- [ ] Token never appears in emitted events or logs (`TestTokenNeverInEvents`).

### `internal/deploy/detect_test.go`

- [ ] `fly.toml` present → `fly-native`, reads existing app name.
- [ ] `Dockerfile` only → `docker`, port from `EXPOSE 5000` → 5000, default 8080 when absent.
- [ ] `package.json` with `"next"` → `nextjs`, port 3000.
- [ ] `package.json` without Next → `node`, port 3000.
- [ ] `go.mod` → `go`, port 8080.
- [ ] All absent → operator.Ask mock returns `"abort"` → error.

### `internal/deploy/verify_test.go`

- [ ] Probe cascade: `/healthz` 200 wins over `/health` (not called). `/healthz` 404 + `/health` 200 → `/health` wins.
- [ ] `/livez` 200 + `/readyz` 404 → cascade does NOT count k8s pair; falls through to `/_health`.
- [ ] All probes 404 + GET / 200 → `HealthProbe=""`, `Healthy` decided by GET.
- [ ] Browser console errors captured: mock browser returns `["ReferenceError: x is undefined"]` → populated in `HealthStatus`.
- [ ] TTFB exceeds SLA → `Healthy=false` and console err appended.

### `internal/executor/deploy_test.go`

- [ ] Execute happy path: healthy → returns `Deliverable`, emits `deploy.complete`.
- [ ] Auto-rollback all three conditions: status=500 + errs=2 + elapsed=45s → Rollback called with `PrevImage`, event emitted.
- [ ] Single-factor fail (status=502 but no console errs) → NO rollback, AC fail, repair path.
- [ ] Elapsed=15s with status=500+errs → NO rollback (warm-up window).
- [ ] `PrevImage=""` with rollback-trigger conditions → emit `deploy.rollback.skipped`, exit 1.
- [ ] Verify-only mode: skips Deploy, no rollback path engaged even on failure.
- [ ] AC `DEPLOY-URL-LIVE` calls `VerifyFunc` not Command (exercises D13).

### `cmd/stoke/deploy_cmd_test.go`

- [ ] `--help` lists `provider`, `app`, `region`, `verify-only`, `rollback`.
- [ ] `--dry-run` prints fly.toml to stdout and exits 0; no flyctl invoked.
- [ ] `--provider vercel` → exits 2 with "only 'fly' supported in v1".
- [ ] `--verify-only` without `--url` → exits 2 usage.
- [ ] `--output stream-json` emits all taxonomy events in documented order.

## Acceptance Criteria

Run from repo root; all must exit 0.

```bash
# 1. Package builds and tests pass
go build ./internal/deploy/...
go build ./internal/executor/...
go build ./cmd/stoke
go vet ./internal/deploy/... ./internal/executor/... ./cmd/stoke

# 2. Unit tests
go test ./internal/deploy/... -run TestFlyDeployer
go test ./internal/deploy/... -run TestStackDetection
go test ./internal/deploy/... -run TestVerify
go test ./internal/executor/... -run TestDeployExecutor
go test ./cmd/stoke -run TestDeployCmd

# 3. CLI surface
./stoke deploy --help | grep -q -- '--provider'
./stoke deploy --help | grep -q -- '--verify-only'
./stoke deploy --help | grep -q -- '--rollback'

# 4. Dry-run generates fly.toml preview without network
./stoke deploy --dry-run --provider fly --app stoke-test | grep -q 'fly.toml'
./stoke deploy --dry-run --provider fly --app stoke-test | grep -q 'primary_region'

# 5. Simulated verify-only (no real flyctl call; mock server)
FLY_API_TOKEN=fo1_test STOKE_DEPLOY_MOCK_URL=http://127.0.0.1:0/mock \
  ./stoke deploy --verify-only --url https://example.fly.dev --output stream-json \
  | grep -q '"deploy.verify.end"'

# 6. Unknown provider rejected
./stoke deploy --provider vercel --app x; test $? -eq 2

# 7. Token redaction lands in tests
go test ./internal/deploy/... -run TestTokenNeverInEvents
```

## Implementation Checklist

1. [ ] Create `internal/deploy/types.go` with `DeployTarget`, `DeployResult`, `HealthStatus`, `Deployer`. Add godoc on each. No imports beyond stdlib + `time`.
2. [ ] Create `internal/deploy/detect.go` implementing the 6-step stack detection. Table-drive templates keyed by `Stack`. Template rendering via `text/template`. Return `Stack`, `App`, `Region`, `Port`. Tests cover every branch (see `detect_test.go` list).
3. [ ] Create `internal/deploy/templates.go` with the three fly.toml templates as raw-string constants; exported `Render(stack, app, region, port) (string, error)`.
4. [ ] Create `internal/deploy/fly.go` — `FlyDeployer` with `Deploy`, `Verify`, `Rollback`, plus private `pollStatus`, `captureCurrentImage`. Use `os/exec`; set `Setpgid`; pass token via `cmd.Env`. Poll interval 3s; budget 5m (env-overridable).
5. [ ] Extend `logging/redact.go` (or create if absent) with regex patterns for `FLY_API_TOKEN`, `fo1_…`, `fm1_…`, `fm2_…`, and generic bearer tokens. Expose `RedactEvent`. Add unit tests.
6. [ ] Create `internal/deploy/verify.go` with the 4-step Verification Cascade; takes a `browser.Pool` injected via constructor so tests pass a mock.
7. [ ] Create `internal/executor/deploy.go` implementing spec-3's `Executor`. Wire `Deployer`, `browser.Pool`, bus, streamjson emitter. Implement `Execute`, `BuildCriteria`, `BuildRepairFunc`, `BuildEnvFixFunc`. Auto-rollback block uses the exact triple predicate from §Auto-Rollback.
8. [ ] Emit the 9 event types via `internal/bus/` publisher; mirror to `internal/streamjson/` with `_stoke.dev/deploy.*` subtype. All payloads pass through `logging.RedactEvent`.
9. [ ] Create `cmd/stoke/deploy_cmd.go` — cobra command with flags from §Flags; mode dispatch: primary / verify-only / rollback / dry-run. Wire `Operator` from spec-7 for prompts. Exit codes per §Modes.
10. [ ] Integrate with `internal/executor/` registry so `stoke run` routes deploy intents to this executor (router keyword: `deploy`, `ship to fly`, etc. — registered in spec-3's `router.Classify`).
11. [ ] Add golden tests in `cmd/stoke/testdata/deploy/*.golden.txt` for `--dry-run` output across the three templates.
12. [ ] Add `internal/deploy/fly_mock_test.go` providing a `MockFlyctl` helper that stamps a fake `flyctl` binary into a `testing.TempDir()` and sets `STOKE_FLYCTL_BIN`. All fly tests use it — no real network.
13. [ ] Document the package in `internal/deploy/doc.go` with a top-level usage example and a pointer back to this spec.
14. [ ] Update `CLAUDE.md` package map: add `deploy/` under "DEPLOYMENT" (new section) and `executor/deploy.go` under the existing executor listing. One-line descriptions only.
