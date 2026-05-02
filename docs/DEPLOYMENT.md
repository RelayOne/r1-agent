# Deployment

This is the deployment posture for r1, covering today (`main`) plus the
**r1d daemon** posture introduced by `specs/r1d-server.md`, the **web
hosting** model from `specs/web-chat-ui.md`, and the **desktop sidecar
fallback** from `specs/desktop-cortex-augmentation.md`.

## Build and verification gate

```bash
go build ./cmd/r1
go test ./...
go vet ./...
```

These three commands are the CI gate. They must be green on every PR.
CI also runs:

- `race:` — full suite under `-race`. Race-clean across the whole repo.
- `lint:` — `golangci-lint` built from source against the pinned Go
  version. Findings surface as `::warning::` annotations and are
  **advisory** (a 30-PR cleanup campaign closed 600+ findings; new
  findings are welcomed as separate cleanup PRs).
- `security:` — `govulncheck` + `gosec` (built from source). Stdlib
  vulnerabilities trigger a Go-version upgrade PR rather than a code
  change.
- `make check-pkg-count` — internal package count drift gate.

Once spec 5 lands, an additional gate is added:

- `make lint-chdir` — `tools/cmd/chdir-lint/` AST walker fails the build
  on any `os.Chdir`, `os.Getwd`, or `filepath.Abs("")` call without a
  `// LINT-ALLOW chdir-*: reason` annotation. This is the **mandatory
  gate before multi-session is enabled** in r1d.

## Deployment surfaces

Today, on `main`:

| Surface | Best fit | Notes |
|---|---|---|
| CLI install | individual operators and developers | canonical `r1` binary; ~30 subcommands |
| Container / release artifacts | packaged distribution | release automation and signed artifacts via goreleaser |
| Pack registry HTTP service | deterministic skill distribution | `r1 skills pack serve` |
| IDE plugins | VS Code + JetBrains | code in-tree; marketplace publishing pending |
| Tauri 2 desktop shell | per-machine GUI | R1D-1..R1D-12 phases shipped |
| Per-machine dashboard | local observability | `r1-server` on port 3948; live event stream + 3D ledger visualizer |
| Mission API HTTP server | programmatic access | `stoke-server`, `r1 serve` |

After spec 5 lands:

| Surface | Best fit | Notes |
|---|---|---|
| **`r1 serve` daemon** | per-user singleton, hosts N concurrent sessions | Watchman pattern; spawn-on-demand; multi-session goroutines; `cmd.Dir` per session |
| **Web app at `/`** | browser users | served by the daemon from `internal/server/static/dist/` (embedded via `//go:embed static`); CSP locked to loopback |
| **Tauri 2 desktop with sidecar fallback** | offline desktop users | discover-or-spawn: probes `~/.r1/daemon.json`, falls back to bundled `r1` via `ShellExt::sidecar` |
| **MCP endpoint** | external agent integration | every UI action has an MCP equivalent; `internal/mcp/r1_server.go` consolidated catalog |

## Install paths (today)

```bash
# 1. Homebrew (macOS + Linux) — published by goreleaser on each tag.
brew install RelayOne/r1-agent/r1

# 2. One-line installer — detects platform, verifies cosign signature
#    (keyless OIDC via sigstore) when cosign is on PATH, falls back to
#    building from source if no prebuilt binary exists for your target.
curl -fsSL https://raw.githubusercontent.com/RelayOne/r1-agent/main/install.sh | bash

# 3. Docker (linux/amd64 + linux/arm64; distroless, multi-stage).
docker pull ghcr.io/RelayOne/r1:latest

# 4. From source (Go 1.26+; CGO enabled for SQLite).
go build ./cmd/r1
sudo mv r1 /usr/local/bin/

# Verify a signed release tarball.
cosign verify-blob \
  --certificate-identity-regexp 'https://github\.com/(RelayOne/r1|ericmacdougall/Stoke)/\.github/workflows/release\.yml@refs/tags/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature r1_<ver>_<os>_<arch>.tar.gz.sig \
  r1_<ver>_<os>_<arch>.tar.gz
```

## r1d daemon — install and discovery

The daemon is **per-user singleton on-demand**: no launchd / systemd /
SCM unit needed at first run. `r1 chat` calls `connect()` on the IPC
endpoint; if it fails, forks `r1 serve` itself. The first `r1 chat`
becomes the implicit daemon launcher.

For users who want always-on operation:

```bash
# macOS — writes ~/Library/LaunchAgents/dev.relayone.r1.plist
r1 serve --install

# Linux (systemd-user) — writes ~/.config/systemd/user/r1.service
r1 serve --install
# For headless boxes (SSH-only, no graphical login):
loginctl enable-linger $USER

# Windows (Service Control Manager) — registers service "r1.daemon"
r1 serve --install

# Inverse — stop and remove the unit:
r1 serve --uninstall

# Status:
r1 serve --status
```

Behind the scenes: `r1 serve --install` uses
`github.com/kardianos/service` to write a platform-appropriate service
unit. The service runs as the current user; auto-start is per-user, not
system-wide.

### Discovery

On startup, the daemon writes `~/.r1/daemon.json` (mode 0600):

```json
{
  "pid": 12345,
  "sock": "/run/user/1000/r1/r1.sock",
  "port": 54123,
  "token": "<32 random hex bytes>",
  "version": "<git sha>",
  "protocol_version": 1
}
```

Clients discover via:

- `r1 ctl discover` — reads the file, prints the values.
- `GET http://127.0.0.1:<port>/v1/discover` — returns the same struct
  (no auth; loopback-only).
- `GET http://127.0.0.1:<port>/v1/health` — returns `{status, version,
  sessions, uptime_s}` (no auth).

The token **rotates on every daemon start**. Old clients see 401 and
re-discover via the file. Daemon refuses to start if `~/.r1/`,
`~/.r1/daemon.json`, `~/.r1/daemon.lock`, or the socket file have wider
permissions than expected after the chmod (fail-closed).

### Single-instance enforcement

`gofrs/flock` advisory lock on `~/.r1/daemon.lock`. Plus the
bind-is-exclusive property of the socket path / port. If a second
`r1 serve` runs:

```
$ r1 serve
daemon already running, pid=12345, sock=/run/user/1000/r1/r1.sock
use 'r1 ctl' to talk to it.
```

Exit code 1.

## Listeners and ports

The daemon binds three listeners:

| Surface | Endpoint | Auth |
|---|---|---|
| CLI (`r1 chat`, `r1 ctl`) | `$XDG_RUNTIME_DIR/r1/r1.sock` (Linux/macOS) / `\\.\pipe\r1-<USER>` (Windows) | Peer-cred check (`SO_PEERCRED` / `LOCAL_PEERCRED`); no token |
| Web / Desktop / MCP | `ws://127.0.0.1:<port>` + `http://127.0.0.1:<port>` | 256-bit Bearer (HTTP) or `Sec-WebSocket-Protocol: r1.bearer, <token>` (WS); Origin pin + Host pin |

- Linux/macOS socket created with mode **0600**, parent dir **0700**.
- Windows named pipe with `SECURITY_ATTRIBUTES` granting only the
  current SID.
- Loopback HTTP+WS listener binds `127.0.0.1:0` (random ephemeral). Port
  written to discovery file **after** all listeners accept connections
  (CLI clients retry with 2s backoff).

### Origin / Host pinning (CSWSH + DNS-rebind defense)

- WS upgrade rejects unless `Origin` is `null`, missing (CLI clients),
  `http://127.0.0.1:<port>`, `http://localhost:<port>`, or
  `tauri://localhost`. Configurable via `~/.r1/daemon.toml`.
- HTTP rejects unless `Host` ∈ `{127.0.0.1:<port>, localhost:<port>}`.
- WS subprotocol negotiation: server requires `r1.bearer` subprotocol.
  `new WebSocket(url)` from a malicious page without the subprotocol
  fails the handshake.

## Multi-instance management

The daemon hosts N concurrent sessions as goroutines, each with a
`SessionRoot string` field. The `cmd.Dir` discipline is non-negotiable:

```bash
# Create a session
curl -X POST -H 'Authorization: Bearer <token>' \
  http://127.0.0.1:<port>/v1/sessions \
  -d '{"workdir":"/home/eric/repos/foo","model":"claude-sonnet-4-6"}'
# {"session_id":"sess_xxx","workdir":"/home/eric/repos/foo","started_at":"..."}

# List sessions
curl -H 'Authorization: Bearer <token>' \
  http://127.0.0.1:<port>/v1/sessions
# {"sessions":[{"id":"sess_xxx","workdir":"/...","state":"running",...}]}

# Pause / resume / kill
curl -X POST -H 'Authorization: Bearer <token>' \
  http://127.0.0.1:<port>/v1/sessions/sess_xxx/pause
curl -X DELETE -H 'Authorization: Bearer <token>' \
  http://127.0.0.1:<port>/v1/sessions/sess_xxx
```

CLI equivalent (no token needed thanks to peer-cred):

```bash
r1 ctl sessions list
r1 ctl sessions get sess_xxx
r1 ctl sessions start /home/eric/repos/foo
r1 ctl sessions kill sess_xxx
```

Limits:

- One daemon per `os.Getuid()` (single-uid; multi-tenant per-host is out
  of scope for this iteration).
- No hard cap on concurrent sessions; practical ceiling is the daemon's
  process limits (FD count, RAM). The `bench/r1d_serve_bench_test.go`
  soak test asserts 50 sessions × 100 messages stable over an hour.

## Hot upgrade

Restart-required, transparent. **No `tableflip`, no FD-pass, no plugin
tricks.**

```bash
r1 update                      # downloads new binary to ~/.r1/bin/r1 atomic-rename
r1 serve restart               # daemon receives daemon.shutdown {grace_s: 30}
                               #   broadcasts session.paused to every active subscriber
                               #   fsyncs every journal
                               #   exits 0
                               # new binary spawned (init via on-demand path or service)
                               # new daemon scans ~/.r1/sessions-index.json
                               # re-opens each journal.ndjson
                               # rebuilds *Session (workspace + Lobe state from journal)
                               # broadcasts daemon.reloaded {at, version: <new-sha>}
```

`r1 doctor` detects "installed binary newer than running daemon" and
prompts `r1 serve restart`.

Clients reconnect with `Last-Event-ID` (SSE) or `since_seq` (JSON-RPC) →
server replays from the journal. Protocol version handshake: WS
subprotocol negotiates `r1.proto.v1`; if a client requests `r1.proto.v2`
and server only knows v1, server closes with code 1002 and a
`migration_hint` close reason.

## Journal storage paths

Per-session journal:

- Path: `<workdir>/.r1/sessions/<session_id>/journal.ndjson`.
- Format: append-only NDJSON, one record per line, `v: 1` schema.
- fsync on terminal events (`session.ended`, `session.paused`).
- Default retention: **24 hours OR 100 MB per session**, whichever first.
- Configurable via `internal/bus/wal.go` knobs.

Daemon-level files (under `~/.r1/`, mode 0700):

- `daemon.json` (0600) — discovery file.
- `daemon.lock` (0600) — `gofrs/flock` advisory lock.
- `sessions-index.json` (0600) — `{session_id → {workdir, started_at,
  journal_path, last_seq}}`. Updated atomically (tmp+rename + fsync
  parent dir) on Create / Kill / flush.
- `cortex/curator-audit.jsonl` (0600) — append-only audit trail of
  MemoryCuratorLobe auto-writes (`{ts, entry_id, category, content_sha,
  source_msg_id}`).
- `bus/` — durable WAL, scoped per session via `Scope{TaskID:
  sessionID}`.
- `daemon.toml` (0600, optional) — operator config (Origin allowlist,
  retention overrides, etc.).

## Backwards compatibility

Spec 5 consolidates `r1 daemon` and `r1 agent-serve` into `r1 serve`
without breaking either:

- `r1 daemon start --addr` becomes an alias for `r1 serve --addr`.
  `r1 daemon enqueue/status/workers/pause/resume/wal/tasks` become
  aliases for `r1 ctl <verb>`. Both keep working with a one-line
  deprecation hint to stderr.
- `r1 agent-serve --addr` becomes an alias for
  `r1 serve --enable-agent-routes --addr`.
- The 11 JSON-RPC methods in `desktop/IPC-CONTRACT.md` are unchanged.
  Lane events are additive; they share the `event` field convention.
- `session.delta` continues to carry assistant-text deltas for the main
  lane during the lanes-protocol migration window. After one minor
  release, surfaces SHOULD prefer `lane.delta`.

## Web UI hosting

Built artifacts emit to `internal/server/static/dist/` and ship via the
existing `embed.FS` in `internal/server/embed.go`. Build:

```bash
cd web
npm ci
npm run build           # tsc --noEmit && vite build
                        # output: ../internal/server/static/dist/
cd ..
go build ./cmd/r1       # produces a binary that includes the SPA
```

CI gate runs `cd web && npm ci && npm run build && npm run test` before
the existing `go build ./cmd/r1 && go test ./... && go vet ./...`
triple.

Dev workflow:

```bash
cd web
npm run dev             # Vite on :5173; SPA connects to daemon at :7777
                        # cross-origin during dev; daemon Origin allowlist
                        # must include http://127.0.0.1:5173
```

CSP enforced in `index.html`:

```
default-src 'self';
connect-src 'self' ws://127.0.0.1:* http://127.0.0.1:*;
style-src 'self' 'unsafe-inline';
img-src 'self' data: blob:;
script-src 'self';
worker-src 'self' blob:;
frame-ancestors 'none';
```

Test gates: Vitest + jsdom (`npm run test`), Playwright multi-browser
e2e (`npm run test:e2e`), `@axe-core/playwright` accessibility scan
(zero serious/critical violations on every route). Storybook MCP runs
component stories.

## Desktop sidecar fallback

The Tauri 2 desktop bundles the `r1` binary as `bundle.externalBin`:

- `r1-x86_64-unknown-linux-gnu`
- `r1-aarch64-apple-darwin`
- `r1-x86_64-apple-darwin`
- `r1-x86_64-pc-windows-msvc.exe`
- `r1-aarch64-pc-windows-msvc.exe`

Discovery flow on app launch (`tauri::Builder::setup`):

1. `read_daemon_json()` reads `~/.r1/daemon.json`. If present and fresh,
   tries `probe_external()` (1s timeout TCP connect to
   `ws://127.0.0.1:<port>`).
2. On `NotFound | Refused`, `spawn_sidecar()` runs the bundled `r1 serve
   --port=0 --emit-port-stdout` via `ShellExt::sidecar`. Reads the
   chosen port from the child's stdout NDJSON
   `daemon.listening` event.
3. Stores `DaemonHandle{mode, url, token, child}` in
   `tauri::State<Mutex<Option<DaemonHandle>>>`.
4. On window close / `app.exit`: if `mode == Sidecar`, send
   `daemon.shutdown` over WS, wait 5s, then `child.kill()`.

The discovery banner in the UI (`<DaemonStatus>`) shows:

- Green dot + "Connected (external)" — external daemon found.
- Blue dot + "Bundled daemon" — sidecar spawned.
- Yellow dot + "Reconnecting…" — during retry.
- Red dot + "Offline" + retry button — hard fail.

Wizard offers `r1 serve --install` the first time a sidecar is spawned
("Run as a system service so the app starts faster next time").

CSP delta in `tauri.conf.json` adds `connect-src ws://127.0.0.1:*`
(loopback only; explicitly NOT a `ws:` wildcard).

## Daemon vs UI auto-start

Two independent concerns:

- **Daemon auto-start**: `r1 serve --install` (uses `kardianos/service`).
  launchd / systemd-user / Windows SCM. The daemon runs even when no UI
  is attached.
- **UI auto-start (desktop)**: Settings → "Start at login" via
  `tauri-plugin-autostart`. Login Items (macOS) / Run registry key
  (Windows) / `~/.config/autostart/r1-desktop.desktop` (Linux).

You can have either, both, or neither. The desktop's "Reconnect daemon"
button re-runs `discover_or_spawn` if the user installs `r1 serve`
mid-session.

## MCP endpoint exposure

The MCP endpoint is exposed in two ways:

- **Stdio** — for in-process MCP clients spawned by the CLI: `r1 mcp-serve`
  (alias for `stoke-mcp`).
- **Inside the daemon** — `internal/mcp/r1_server.go` registers the
  consolidated tool catalog (`r1.session.*`, `r1.lanes.*`,
  `r1.cortex.*`, `r1.mission.*`, `r1.worktree.*`, `r1.bus.tail`,
  `r1.verify.*`, `r1.tui.*`). MCP tool calls flow through the same
  `JSON-RPC 2.0` dispatcher as session control verbs.

The legacy `stoke_*` aliases (`build_from_sow`, `get_mission_status`,
`get_mission_logs`, `cancel_mission`, `list_missions`) are preserved
verbatim until v2.0.0 per `canonicalStokeServerToolName`.

External MCP servers (GitHub, Linear, Slack, Postgres, custom) are
configured in `stoke.policy.yaml`:

```yaml
mcp_servers:
  - name: linear
    transport: stdio
    command: linear-mcp-server
    auth_env: LINEAR_API_KEY
    trust: untrusted
    max_concurrent: 4
  - name: github
    transport: http
    url: https://api.github.com/mcp
    auth_env: GITHUB_TOKEN
    trust: trusted
    timeout: 30s
```

HTTP/HTTPS enforcement: non-localhost URLs must be `https://` unless the
URL is `http://localhost:*` or `http://127.0.0.1:*`. Trust gating:
`untrusted` workers can only invoke tools from `untrusted` servers.

## Operational runtime inputs

Deployment depends on the existing r1 runtime basics:

- Git.
- At least one execution engine/provider path (Claude Code CLI, Codex
  CLI, OpenRouter API key, direct Anthropic API key, or none for
  lint-only fallback).
- Writable runtime state directories (per-workdir `.r1/`, per-user
  `~/.r1/`).
- Whatever model/provider credentials the chosen execution path needs.

For the cortex / lanes scope, additional inputs:

- Anthropic API for the Haiku 4.5 Lobes and Router (or a compatible
  fallback via `internal/provider/`).
- Sufficient FD budget for N concurrent sessions (each session opens a
  journal, a WAL, a few subprocesses, a WS subscription).

## What operators should verify post-deploy

Today, on `main`:

- The pack libraries are seeded or reachable where expected.
- Signed packs verify correctly before runtime registration.
- Runtime helper surfaces for metrics, audit, timeout, and cancellation
  behave correctly.
- Evaluation artifacts and parity evidence still line up with the build
  being promoted.
- IDE plugins (VS Code, JetBrains) install cleanly.
- The Tauri R1D-1..R1D-12 desktop ships a usable subprocess-mode
  experience.

After the cortex / lanes / multi-surface scope lands:

- `make lint-chdir` is green (no unannotated `os.Chdir` / `os.Getwd` /
  `filepath.Abs("")` calls).
- `r1 serve` starts cleanly; discovery file written with mode 0600;
  token visibly rotates across restarts.
- Web UI loads from the embedded SPA; CSP violations are zero on every
  route.
- Multi-session race test (`cmd/r1/serve_integration_test.go::TestMultiSession_RaceFree`)
  is green under `-race -count=10`.
- Daemon restart resume test
  (`cmd/r1/serve_integration_test.go::TestKillAndResume`) replays
  journals correctly and reconnecting clients see `daemon.reloaded`
  followed by deltas with monotonic seq.
- Token rotation test asserts old token returns 401 after restart.
- Origin pinning test rejects faked `Origin: http://evil.com` with 403.
- Cortex pre-warm cache hit rate ≥80% across the LobeSemaphore (5 LLM
  Lobes hitting the same warmed breakpoint).
- View-without-API CI lint is green (every interactive component has a
  matching MCP tool).

## Status

### Done
- Stable build/test/vet gate.
- Deployable CLI/runtime baseline.
- Pack registry HTTP surface.
- Runtime verification hooks for signed packs and helper functions.
- Wave 2 R1-parity surfaces: browser tools, IDE plugins, multi-CI,
  Tauri R1D-1..R1D-12.

### In Progress
- Broader integration verification across more deterministic-skill use
  cases.
- Race-clean regression sweep.
- LSP feature coverage.

### Scoped
- `r1 serve` per-user singleton daemon (spec 5).
- `--install` mode for launchd / systemd-user / Windows SCM (spec 5).
- `os.Chdir` audit + CI lint as a multi-session gate (spec 5).
- Hot-upgrade contract via journal replay (spec 5).
- Web UI hosted from `internal/server/static/dist/` (spec 6).
- Tauri sidecar fallback + per-session workdir (spec 7).
- Consolidated MCP catalog (`r1_server.go`) + view-without-API CI lint
  (spec 8).
- IDE plugin marketplace publishing.

### Scoping
- Encryption-at-rest for journals (separate spec).
- Cross-machine session migration.
- Per-tool throttling policy in `.stoke/`.

### Potential — On Horizon
- Cloud daemon beyond loopback singleton (requires mTLS + auth front-end).
- Multi-tenant per-host (multiple uids on a shared box).
- Per-session resource limits (cgroups, ulimit).
- BitBucket Pipelines adapter parity with GitLab CI / GitHub Actions.
- OpenTelemetry export of lane events.
