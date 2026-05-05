# Deploy Executor

Track B Task 22 ships the first real deploy adapter for R1:
fly.io via `flyctl`. This document is the operator-facing reference
for the `r1 deploy` command and the `internal/executor`
`DeployExecutor`.

## Provider status

| Provider    | Status            | Notes                                             |
|-------------|-------------------|---------------------------------------------------|
| fly.io      | GA (this commit)  | Shells out to `flyctl`; dry-run + verify-only    |
| Vercel      | Deferred          | See `specs/deploy-phase2.md`                      |
| Cloudflare  | Deferred          | See `specs/deploy-phase2.md`                      |
| Docker      | Deferred          | Named in the provider enum so callers compile     |
| Kamal       | Deferred          | Named in the provider enum so callers compile     |

Selecting a deferred provider fails fast with exit code 2 and a
message pointing at `specs/deploy-phase2.md`; no partial invocation
leaks through.

## Required environment

`flyctl` must be on `$PATH` (or passed via `--flyctl /path/to/flyctl`).
Authentication is delegated to `flyctl`'s existing mechanisms:

- Interactive: `flyctl auth login` (stored under `~/.fly/`).
- CI / non-interactive: export `FLY_API_TOKEN` (or `FLY_ACCESS_TOKEN`)
  before invoking `r1 deploy`. R1 does NOT read this token
  itself — it is passed through to the child `flyctl` process via
  the inherited environment.

R1 never logs the token, never writes it to a file, and never
passes it on the command line.

## Dry-run

```bash
r1 deploy --provider fly --app my-app --dry-run
```

Renders a minimal `fly.toml` preview to stdout and exits 0. No
subprocess, no network, no filesystem writes. Use this to review
what R1 would ship before committing:

```toml
# fly.toml preview (provider=fly, dry-run)
app = "my-app"
primary_region = "iad"

[build]
  dockerfile = "Dockerfile"

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

When `--image <ref>` is supplied, the `[build]` block references the
registry image instead of a `Dockerfile`, matching what flyctl
itself would emit.

## Verify-only

```bash
r1 deploy --verify-only --health-url https://my-app.fly.dev --expected-body "OK"
```

Skips the deploy entirely and runs a single HTTP GET against the
supplied URL. Exit codes:

- `0` — 200 response, non-empty body, (optional) `--expected-body`
  substring matched
- `1` — health check failed (non-200, empty body, substring miss,
  transport error)
- `2` — usage error (`--verify-only` without `--health-url` or
  `--app`)

This is the CI-friendly "is prod still up?" recipe — pair it with
your scheduler to alert on exit code 1.

## Real deploy

```bash
r1 deploy --provider fly --app my-app --region iad
```

Runs in cwd by default; pass `--dir path/to/service` when the
`fly.toml` lives in a subdirectory. Flow:

1. Resolve `flyctl` (from `--flyctl` or `exec.LookPath`).
2. Invoke `flyctl deploy --app <name> --region <region>` (plus
   `--image <ref>` when `--image` is set) with the child's `cmd.Dir`
   set to `--dir`.
3. Capture stdout + stderr; on non-zero exit, include the stderr
   tail in the error (first 500 chars, prefixed with `...`).
4. On success, run a single `net/http.Get` against the deployed URL
   (derived as `https://<app>.fly.dev` unless `--health-url`
   overrides it). Non-200 → exit 1; all good → exit 0.

## Acceptance criteria (executor integration)

When the DeployExecutor is driven through `internal/descent`, it
exposes two acceptance criteria, both using `VerifyFunc` rather
than a shell `Command`:

| ID                    | What it checks                                                     |
|-----------------------|--------------------------------------------------------------------|
| `DEPLOY-COMMIT-MATCH` | Local `git rev-parse HEAD` agrees with the deploy's captured SHA. |
| `DEPLOY-HEALTH-200`   | GET deployed URL → 200, non-empty body, substring match.          |

`DEPLOY-COMMIT-MATCH` soft-passes in dry-run mode so `r1 deploy
--dry-run` stays a read-only preview.

The descent engine's repair tier (`BuildRepairFunc`) retries the
deploy with `DryRun` forced off; the env-fix tier
(`BuildEnvFixFunc`) returns `true` for transient failure signals
(`timeout`, `502`, `503`, `504`, `temporary failure`, `i/o
timeout`, `connection reset`, `no such host`) so the engine knows
a retry is worth attempting, and `false` for permanent failures
(auth, 4xx, config) so the operator is surfaced immediately.

## Error taxonomy

| Exit code | Condition                                                                 |
|-----------|---------------------------------------------------------------------------|
| `0`       | Healthy deploy (or dry-run / verify-only success).                        |
| `1`       | Deploy succeeded but post-deploy health check failed.                     |
| `2`       | Usage error; unsupported provider; `flyctl` missing; `--app` missing.     |
| `3`       | `flyctl deploy` itself returned non-zero (auth, build, invalid config).   |

Sentinel errors exposed on the `deploy` package (for programmatic
callers):

- `deploy.ErrFlyctlNotFound`
- `deploy.ErrAppNameMissing`
- `deploy.ErrProviderUnsupported`

## What this MVP intentionally does NOT do

Per the task spec, the following are deferred to
`specs/deploy-executor.md` and `specs/deploy-phase2.md`:

- Stack auto-detection (Docker / Node / Go / Next.js templates).
- `flyctl status --json` polling for release readiness.
- Auto-rollback on the strict triple-condition failure predicate.
- Browser-based verification cascade (`/healthz` → `/health`,
  console-error capture).
- Token redaction regexes for `fo1_…` / `fm1_…` / `fm2_…`.
- Vercel and Cloudflare adapters.

These are scoped into follow-up commits so this MVP stays a
surgical, verifiable change.
