# Deployment

## Cycle 9 deployment posture

Deployment planning for R1 should now assume beacon-era runtime
surfaces are part of the shipped product:

- Promote builds that include PRs `#45` through `#49` together, not as
  isolated features. Protocol foundation without trust-layer or missing
  primitives leaves the runtime story incomplete.
- Treat documentation alignment from PRs `#50` and `#52` as part of the
  release artifact. The deploy is not just binaries; it is also the
  operator contract.
- For downstream consumers, validate CLI, desktop, IDE, and CI entry
  points against the same trunk version so beacon/session semantics stay
  consistent across surfaces.

## W36 deployment update

The new planning story does not change the base deployment topology, but it does change what operators should verify after deployment:

1. the parity evidence surfaces are present and current,
2. the evaluation skill remains runnable,
3. deterministic skill manifests still load and resolve correctly.

Status snapshot:

- Done: deployable parity and deterministic-skill foundation.
- In Progress: broader integration verification.
- Scoped: stronger release checks around skill packaging.
- Scoping: superiority publishing workflow.
- Potential-On Horizon: cross-product skill distribution pipelines.

This document covers deploying R1 in development, on a single
operator host, on a shared workstation with pool isolation, in a
container, and in the managed-cloud configuration. It also covers
every environment variable R1 reads, where each one comes from,
and how to verify a deployment is healthy.

## Wave 2 (2026-04-26) Deployment Surface

Wave 2 added new install surfaces and a new CI cutover:

1. **VS Code + JetBrains IDE plugins** (`ide/vscode/`, `ide/jetbrains/`).
   Build via `npm run package` (VS Code) and `./gradlew buildPlugin`
   (JetBrains). Both plugins ship the LSP client; the LSP server binary
   `stoke-lsp` must be on `$PATH`.
2. **Tauri desktop GUI** (`desktop/`). Build via `npm install && npm run tauri build`.
   Real `robotgo` backend ships in PR #19 — the GUI drives real input/output
   instead of stubs.
3. **Multi-CI adapters.**
   - **GitHub Actions:** `.github/workflows/r1-pr.yml` (template lives at
     `cmd/stoke/templates/cicd/github.yml`).
   - **GitLab CI:** `.gitlab-ci.yml` snippet at `cmd/stoke/templates/cicd/gitlab.yml`.
   - **CircleCI:** orb at `cmd/stoke/templates/cicd/circleci.yml`.
4. **Cloud Build CI cutover (PR #11, commit `a883825`).** GitHub Actions
   removed in favour of Cloud Build. Pipeline configs at
   `cloudbuild.yaml` and `cloudbuild-release.yaml`. Local pre-push
   verification via `scripts/install-pre-push-hook.sh`.
5. **Veritize-Verity dual-send headers (PR #8, commit `6ed5bb8`).** Outbound
   HTTP carries both `X-Veritize-Client` and `X-Verity-Client` for the
   30-day rename dual-accept window.

> R1 ships as the `stoke` binary today (rename to `r1` tracked in
> `plans/work-orders/work-r1-rename.md` §S2-3). Every CLI invocation,
> `STOKE_*` environment variable, `.stoke/` path, and `X-Stoke-*`
> header below remains the literal on-disk / on-the-wire identifier
> throughout the dual-accept windows.

For the architecture reference, see [ARCHITECTURE.md](ARCHITECTURE.md).
For the operator walkthrough, see [HOW-IT-WORKS.md](HOW-IT-WORKS.md).

## Prerequisites

### Required

| Item | Version | Purpose |
|------|---------|---------|
| Go toolchain | 1.25.5+ (build time only; pre-built binaries ship with matching Go) | Build `./cmd/stoke` and satellites with CGO enabled for SQLite |
| Git | 2.x | Worktree operations (create, merge-tree, merge, prune, remove --force) |
| `claude` CLI | Latest | One of the two primary execution engines |
| `codex` CLI | Latest | The other primary execution engine + cross-model reviewer |

Either `claude` or `codex` is sufficient to run R1 — the fallback
chain handles missing providers by demoting to the next tier — but
the full cross-model review gate requires both.

### Recommended

| Item | Purpose |
|------|---------|
| `cosign` | Verify the signature on prebuilt release tarballs |
| `golangci-lint` | Run the advisory lint locally before pushing |
| `gosec` | Run the advisory security scanner locally |
| `govulncheck` | Run the Go vulnerability scanner locally |
| `goreleaser` | Cut releases locally (`make release`) |
| `flyctl` | Required if `stoke deploy` is used against Fly.io |

### Optional cloud accounts

| Account | Purpose |
|---------|---------|
| Anthropic API key | Direct API path in the 5-provider fallback chain |
| OpenRouter API key | Third-tier fallback across many providers |
| TrustPlane instance | Identity anchoring for A2A federation (defaults to a local stub) |
| GitHub Container Registry | If publishing your own Docker image fork |

## Environment variables

R1 reads a substantial list of environment variables. Most have
sane defaults; only a few are ever strictly required.

### Core configuration

| Variable | Purpose | Required | Default | Example |
|---|---|---|---|---|
| `STOKE_CONFIG` | Explicit path to the policy YAML (skips auto-discovery) | No | auto-discover `stoke.policy.yaml` by walking up from CWD | `/etc/stoke/policy.yaml` |
| `STOKE_DATA_DIR` | Base directory for ledger, sessions, wisdom SQLite | No | `$XDG_DATA_HOME/stoke` or `~/.local/share/stoke` | `/var/lib/stoke` |
| `STOKE_STATE_DIR` | Per-repo ephemeral state | No | `<repo>/.stoke` | `/tmp/stoke-state` |
| `STOKE_PROVIDERS` | Override the provider fallback chain | No | `claude,codex,openrouter,api,lintonly` | `claude,api,lintonly` |
| `STOKE_NO_R1_SERVER` | Suppress auto-spawn of r1-server on startup | No | unset | `1` |
| `STOKE_CANARY_DO_NOT_EMIT` | System-prompt canary string for the honeypot gate | No | built-in default | pick any long random string |

### Execution flags (equivalent to CLI flags)

| Variable | Equivalent flag | Purpose |
|---|---|---|
| `STOKE_DESCENT=1` | `--descent` | Enable the verification descent engine |
| `STOKE_SPECEXEC=1` | `--specexec` | Enable speculative parallel execution |
| `STOKE_MCP_STRICT=1` | — | Upgrade MCP ghost-call detection from advisory to hard failure |
| `STOKE_PERFLOG=1` | — | Enable microsecond timing traces |
| `STOKE_PERFLOG_FILE=<path>` | — | Perflog output destination |
| `STOKE_SOW_REVIEW_MODE={eager,lazy,milestone}` | — | Per-task reviewer scheduling mode (H-48) |
| `STOKE_SOW_ENABLE_DECOMP_OVERFLOW=1` | — | Promote decomposition overflow into fan-out |

### Provider credentials

These are **stripped from the child environment** in Mode 1 (Claude
Code OAuth via `CLAUDE_CONFIG_DIR`), so they don't leak into the
worker. They apply to the Direct API fallback path or to a
non-Mode-1 deployment.

| Variable | Purpose | Required |
|---|---|---|
| `ANTHROPIC_API_KEY` | Direct API path for Claude models | No — only if Claude Code CLI is not in use |
| `OPENAI_API_KEY` | Direct API path for OpenAI models | No |
| `OPENROUTER_API_KEY` | OpenRouter proxy to many providers | No — only for the third-tier fallback |
| `CLAUDE_CONFIG_DIR` | Pool-isolated Claude Code OAuth dir | No — required per pool if running multi-pool |
| `CODEX_HOME` | Pool-isolated Codex CLI dir | No — required per pool |
| `GEMINI_API_KEY` | Gemini models via the provider package | No |
| `OLLAMA_HOST` | Local Ollama URL (default `http://localhost:11434`) | No |

### TrustPlane / A2A

| Variable | Purpose | Required |
|---|---|---|
| `STOKE_TRUSTPLANE_MODE` | `stub` (default, local) or `real` (hits the gateway) | No |
| `STOKE_TRUSTPLANE_PRIVKEY` | Ed25519 private key (inline) | No — one of PRIVKEY/PRIVKEY_FILE required for `real` mode |
| `STOKE_TRUSTPLANE_PRIVKEY_FILE` | Ed25519 private key file path | No |
| `STOKE_TRUSTPLANE_GATEWAY_URL` | Gateway base URL | No — defaults baked in |
| `STOKE_A2A_HMAC_SECRET` | Agent-to-Agent token signing secret | No |

### MCP

| Variable | Purpose | Required |
|---|---|---|
| `LINEAR_API_KEY`, `GITHUB_TOKEN`, etc. | Per-MCP-server auth envs declared in `stoke.policy.yaml` | Per configured server |

### Session control

| Variable | Purpose | Required |
|---|---|---|
| `STOKE_SESSION_STORE={json,sqlite}` | Session persistence backend | No — defaults `json` |
| `STOKE_BUDGET_USD` | Per-session budget cap | No — defaults unset (no cap) |
| `STOKE_BUDGET_POLICY={fail,downgrade,pause}` | What to do when budget is exceeded | No — defaults `downgrade` |

## Build

### From source

```bash
# One-shot build (primary + ACP adapter)
make build

# Or explicitly
go build ./cmd/stoke
go build ./cmd/stoke-acp

# Full CI gate locally
make                    # build + test + vet
make test-race          # build + tests with -race
make lint               # golangci-lint
make security           # govulncheck + gosec
make check-pkg-count    # assert 180 internal packages
```

### Release artifacts via goreleaser

```bash
# Dry-run (no publish)
goreleaser release --snapshot --clean

# Real release — requires a signed git tag (vX.Y.Z) + GitHub token + cosign
make release
```

`.goreleaser.yml` produces:

- Cross-platform archives (linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64) containing `stoke` and `stoke-acp`.
- Docker images (multi-arch) pushed to `ghcr.io/RelayOne/r1`
  (canonical, post work-r1-rename.md §S2-2), with legacy
  `ghcr.io/ericmacdougall/{r1,stoke}` tags dual-published for 60d.
- Homebrew formula pushed to `RelayOne/homebrew-r1-agent` (canonical)
  and mirrored to the legacy `ericmacdougall/homebrew-stoke` tap for
  the transition window.
- cosign keyless OIDC signatures for every archive
  (`*.tar.gz.sig` + certificate bundle), verifiable via:

```bash
# The cert-identity regex accepts BOTH repo paths so archives signed
# before and after the §S2-2 GitHub repo rename verify identically.
cosign verify-blob \
  --certificate-identity-regexp 'https://github\.com/(RelayOne/r1|ericmacdougall/Stoke)/\.github/workflows/release\.yml@refs/tags/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature stoke_<ver>_<os>_<arch>.tar.gz.sig \
  stoke_<ver>_<os>_<arch>.tar.gz
```

### Docker

```bash
docker build -t stoke:local .
docker build -t stoke-pool:local -f Dockerfile.pool .
```

The primary image is multi-stage with a distroless runtime (no
shell, no package manager, minimal attack surface). `Dockerfile.pool`
builds a worker image for the macOS Keychain isolation workaround
(Docker-volume-based pools let operators run multiple pre-authenticated
Claude Code environments on a single macOS host without keychain
collisions).

## Deploy

### Single operator host

The simplest deployment. A plain machine with Git, `claude`,
optionally `codex`, and the R1 binary (named `stoke` on disk).

```bash
# Install via the one-line installer (auto-detects platform,
# verifies cosign signature if cosign is present, falls back
# to source build if no prebuilt binary exists for the platform).
# GitHub preserves a redirect from the legacy URL.
curl -fsSL https://raw.githubusercontent.com/RelayOne/r1/main/install.sh | bash
# Legacy (redirected): https://raw.githubusercontent.com/ericmacdougall/Stoke/main/install.sh

# Or via Homebrew
brew install RelayOne/r1-agent/r1          # canonical (post §S2-2)
# Legacy tap still works during transition: ericmacdougall/stoke/stoke

# Verify
stoke doctor
```

No config file needed for a first run. `stoke` auto-detects
build/test/lint commands from the repo structure and uses the
subscription OAuth on `claude` / `codex`.

### Shared workstation with pool isolation

When one operator host drives multiple concurrent missions, pool
isolation prevents the Claude Code CLI OAuth state from colliding.

```bash
# Register pre-authenticated pool directories
mkdir -p ~/pools/claude-1 ~/pools/claude-2 ~/pools/codex-1
CLAUDE_CONFIG_DIR=~/pools/claude-1 claude login
CLAUDE_CONFIG_DIR=~/pools/claude-2 claude login
CODEX_HOME=~/pools/codex-1 codex login

stoke add-claude ~/pools/claude-1
stoke add-claude ~/pools/claude-2
stoke add-codex  ~/pools/codex-1

# R1's subscriptions.Acquire() round-robins across pools,
# consults per-pool circuit breakers, and polls OAuth usage for
# live load information.
stoke build --plan stoke-plan.json --workers 4
```

### Docker

```bash
docker run --rm \
  -v "$(pwd):/workspace" \
  -v ~/pools:/pools \
  -w /workspace \
  -e CLAUDE_CONFIG_DIR=/pools/claude-1 \
  ghcr.io/RelayOne/r1:latest \
  build --plan stoke-plan.json
# Legacy image: ghcr.io/ericmacdougall/stoke:latest (dual-published 60d post §S2-2)
```

For long-running managed deployments, mount the data dir to a
persistent volume so the ledger and session state survive container
restarts:

```bash
docker volume create stoke-data
docker run -d --restart unless-stopped \
  -v "$(pwd):/workspace" \
  -v stoke-data:/var/lib/stoke \
  -e STOKE_DATA_DIR=/var/lib/stoke \
  ghcr.io/RelayOne/r1:latest \
  serve --listen :8080
# Legacy image: ghcr.io/ericmacdougall/stoke:latest (dual-published 60d post §S2-2)
```

### Managed cloud (opt-in)

```bash
# Register your operator identity with the cloud gateway
stoke cloud register

# Subsequent builds automatically opt into hosted session state,
# centralized pool management, and cross-agent audit consolidation.
stoke build --plan stoke-plan.json
```

The stewardship commitment (`STEWARDSHIP.md`) guarantees that
functional capability never moves from self-hosted to cloud-only.
The managed path is convenience, not a feature tier.

### r1-server

r1-server auto-spawns on R1 startup unless `STOKE_NO_R1_SERVER=1`
is set. To install it as a long-running service:

```bash
# r1-server is built alongside the primary binary in release artifacts
# and ships in the Homebrew bottle / Docker image.
r1-server --listen :3948 --data-dir ~/.local/share/stoke-r1

# Visit http://localhost:3948/
```

It runs read-only against R1 instances — no write access, no
shared database. Pure HTTP + SSE. Nothing to operate.

## Infrastructure requirements

### Storage

- **Session + ledger**: `~/.local/share/stoke/` (or `$STOKE_DATA_DIR`).
  SQLite databases + append-only NDJSON logs. Grows with activity.
  Budget ~100MB per 1,000 non-trivial tasks including full ledger
  history.
- **Per-repo state**: `<repo>/.stoke/` contains the bus WAL,
  `r1.session.json` signature, reports, and checkpoints. Safe to
  delete when the repo is idle.
- **Worktrees**: created per task under `.git/worktrees/`. Auto-
  cleaned on merge. Force-cleaned with `os.RemoveAll` fallback if
  `git worktree remove --force` leaves anything behind.

### Network

- **Outbound only.** R1 initiates HTTPS connections to provider
  APIs (Anthropic, OpenAI, OpenRouter, Gemini, Ollama if remote) and
  to any configured MCP servers.
- **Inbound: optional.** `stoke serve` or `stoke-server` open a
  listening socket if you want programmatic access.
- **Inbound: r1-server (localhost only by default).** Binds to
  `localhost:3948`. Do not expose to the public internet without a
  reverse proxy and auth; the local API is unauthenticated by
  design (it trusts local `POST /api/register` signatures from
  R1 startup).

### Compute

- A single concurrent task uses ~200MB of memory plus whatever the
  LLM CLI subprocess needs (~400MB for `claude`, less for `codex`).
- Scale `--workers` horizontally to the number of pool directories
  you have registered; each worker runs one LLM subprocess at a
  time.
- CPU is mostly idle; the LLM subprocess is the bottleneck. The
  orchestrator is Go and streams JSON; CPU usage per worker is
  single-digit percent.

### Compliance posture

- Append-only ledger + event bus satisfy audit-trail requirements
  for SOC 2, HIPAA, GDPR Article 30, and EU AI Act Article 12
  baseline.
- Encryption at rest and retention policies (scoped:
  `specs/encryption-at-rest.md`, `specs/retention-policies.md`)
  complete the compliance-ready picture for regulated deployments.
- The content-addressed Merkle chain with two-level commitment
  (scoped: `specs/ledger-redaction.md`) allows crypto-shredding
  sensitive content without breaking the integrity chain — the
  regulatory unlocker for HIPAA + GDPR Right-to-be-Forgotten.

## Monitoring and health checks

### Process health

- `stoke doctor` exits 0 if all providers in the fallback chain
  respond, exits non-zero on the first failure with a human-readable
  diagnosis.
- `stoke status` lists active sessions with phase, progress, cost
  accrual, and failure counts.
- r1-server dashboard (`http://localhost:3948/`) lists every
  running R1 instance, live phase state, and the ledger DAG.

### Event stream

Every bus event is written to `<repo>/.stoke/bus/events.log` in
NDJSON, parent-hash chained. Tail with:

```bash
tail -F .stoke/bus/events.log | jq -c '{id: .id, type: .type, trace_parent: .trace_parent}'
```

Or via the HTTP tail:

```bash
curl -s "http://localhost:3948/api/session/$SESSION_ID/events?after=0&limit=100"
```

### Cost tracking

`internal/costtrack/` writes per-session cost summaries to the
session store. Budget enforcement via `CostTracker.OverBudget()`
checked before each execute attempt.

```bash
stoke status --format=json | jq '.sessions[].cost'
```

### Metrics

`internal/metrics/` exposes thread-safe counters. `internal/telemetry/`
writes structured metric events to the bus. A Prometheus exporter
adapter lives behind the managed-cloud gateway (`stoke-gateway`).

For local observation, `STOKE_PERFLOG=1 STOKE_PERFLOG_FILE=<path>`
writes microsecond-resolution phase spans:

```bash
STOKE_PERFLOG=1 STOKE_PERFLOG_FILE=/tmp/perflog.txt stoke build --plan stoke-plan.json

awk -F'\t' '/\.end/ {split($2,a,"="); split($3,b,"="); phase=a[2]; dur=b[2]; sub("ms","",dur); totals[phase]+=dur; counts[phase]++} END {for (p in totals) printf "%-32s %8d ms %3d x %6.0f ms\n", p, totals[p], counts[p], totals[p]/counts[p]}' /tmp/perflog.txt | sort -k2 -rn
```

### Alerting

- Sigstore/cosign signature verification failure on installation:
  install.sh exits non-zero with a clear error.
- Bus WAL fsync failure: emitted as a `stoke.bus.fsync_failed` event
  and surfaces in `stoke status`.
- Budget exceeded: emitted as `stoke.cost.budget_exceeded`; behavior
  governed by `STOKE_BUDGET_POLICY`.
- Circuit breaker open on a pool: surfaces in `stoke pool` output.
- Honeypot fire: emitted as `stoke.critic.honeypot_fired` with
  stack, stops the turn.

## Rollback procedure

### Binary rollback

R1 is a single static binary. Rollback is "install the previous
tag":

```bash
# Homebrew (canonical tap; legacy `ericmacdougall/stoke/stoke@...` also accepted during transition)
brew install RelayOne/r1-agent/r1@<previous-version>

# Docker (canonical image; legacy `ghcr.io/ericmacdougall/stoke:<tag>` dual-published)
docker pull ghcr.io/RelayOne/r1:<previous-tag>

# Source
git checkout <previous-tag> -- cmd/ internal/
make build
sudo mv stoke /usr/local/bin/
```

### Session/data rollback

The session store is append-only per-attempt. To abandon an in-flight
session without affecting history:

```bash
stoke ctl cancel <session-id>
```

The ledger remains intact; the cancel is recorded as a new node with
`escalates` edges.

### Worktree rollback

Workers run in git worktrees; the main branch is never modified
outside a serialized merge. To drop an in-flight worktree manually:

```bash
git worktree list
git worktree remove --force .git/worktrees/<task-id>
git worktree prune
```

The pre-merge snapshot (`internal/snapshot/`) captures the protected
baseline manifest before every merge. A restore-on-failure path
reverts the merge automatically if verify fails post-merge.

### Event log truncation

**Not supported.** The append-only bus is intentional for audit.
To start fresh, point `STOKE_DATA_DIR` at a new directory.

---

*Last updated: 2026-04-23 (holistic refresh after 30-PR lint + race + OSS-hub campaign).*
