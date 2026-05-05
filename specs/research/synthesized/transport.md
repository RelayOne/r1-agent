# Synthesized — r1d Daemon + Transport

Source: RT-R1D-DAEMON.md.

## Architecture: per-user singleton on-demand daemon (Watchman pattern)

- No launchd/systemd at first run. `r1 chat` calls `connect()` to the IPC endpoint; if it fails, forks `r1 serve` itself.
- `r1 serve --install` (uses `kardianos/service`) for users who want always-on. Writes platform-appropriate unit/plist/service.
- One daemon process. N detachable sessions, each a goroutine with a `SessionRoot string` field.
- All cmd execution threads `SessionRoot` through `cmd.Dir` (CLAUDE.md design decision #1 already established).

## IPC

| Surface | Endpoint | Protocol | Auth |
|---------|----------|----------|------|
| CLI (`r1 chat`, `r1 ctl`) | `$XDG_RUNTIME_DIR/r1/r1.sock` (Linux/macOS) / `\\.\pipe\r1` (Windows) | JSON-RPC 2.0 over Unix socket / Named pipe | Socket file mode 0600 |
| Web/Desktop | `ws://127.0.0.1:<port>` + `http://127.0.0.1:<port>` | JSON-RPC 2.0 over WS + REST for control | Token in `~/.r1/daemon.json` mode 0600; sent via `Sec-WebSocket-Protocol` subprotocol or `Authorization: Bearer` HTTP header |

Port: random ephemeral on first start, written to `~/.r1/daemon.json`. Discovery: `r1 ctl discover` reads the file.

## Single-instance enforcement

- `gofrs/flock` advisory lock on `~/.r1/daemon.lock`.
- Plus the bind-is-exclusive property of the socket path / port.

## Session lifecycle

- Create: `POST /v1/sessions { workdir, model, … }` → `session_id`.
- Bind: each session goroutine runs the existing `agentloop.Loop` + cortex `Workspace`.
- Detach: client disconnects but session goroutine continues. Lane events buffer in bus/ WAL.
- Resume: client reconnects with `Last-Event-ID` header (RFC 8895 SSE-style) → server replays from WAL since that seq.
- Persist: `<workdir>/.r1/sessions/<id>/journal.ndjson`. Daemon restart replays journal.
- Kill: `DELETE /v1/sessions/:id` → cancel session ctx + flush WAL.

## Wire envelope

JSON-RPC 2.0 with subscription pattern + monotonic per-subscription `seq`:

```json
// request
{"jsonrpc":"2.0","id":1,"method":"session.subscribe","params":{"session_id":"…"}}
// streaming events
{"jsonrpc":"2.0","method":"$/event","params":{"sub":1,"seq":42,"type":"lane.delta","data":{…}}}
// reconnect with replay
{"jsonrpc":"2.0","id":2,"method":"session.subscribe","params":{"session_id":"…","since_seq":42}}
```

Compatible with the existing `desktop/IPC-CONTRACT.md` envelope (R1D phase). Augment, don't replace.

## Security

- Token: 256-bit random in `~/.r1/daemon.json` (mode 0600). Server requires it on every WS/HTTP request.
- Origin pinning: WS upgrade rejects unless `Origin` is `null`, missing, or in the allowlist (defaults: empty + the daemon's own served origin).
- Host header pin: HTTP rejects unless `Host` is `127.0.0.1:<port>` or `localhost:<port>`.
- DNS rebinding defense: combination of Origin + Host pinning.
- CSWSH defense: subprotocol token requirement makes `new WebSocket(url)` from a malicious page fail (browser silently drops connections lacking the subprotocol the server requires).

## Hot upgrade

- Restart-required, transparent.
- On restart: replay each session's `journal.ndjson` to rebuild Workspace + message history.
- Clients reconnect with `Last-Event-ID` and resume mid-stream.

## Critical risk

**`os.Chdir` is process-global.** Goroutine-per-session works **only because** `cmd.Dir` is the established pattern (design decision #1). One stray `os.Chdir`, `os.Open("./relative")`, or `filepath.Abs("relative")` from a tool/handler will leak workdir between concurrent sessions — silent and catastrophic.

Mitigations (must ship in spec 5):
- Audit all 132 internal packages for `os.Chdir`/`os.Getwd`/`./` relative paths.
- CI lint that fails on `os.Chdir` outside of `os/exec.Cmd.Dir` setup.
- Per-session sentinel: each session goroutine asserts current dir via `os.Getwd()` matches expected before tool dispatch (panic if not — fail loud).

## Reconciliation with existing specs

- `specs/r1-server.md` (done) — Visual Execution Trace Server. Covers HTTP+SSE for build progress. r1d generalizes this to multi-session.
- `specs/r1-server-ui-v2.md` (done) — UI v2 retrofit for the trace server.
- `specs/agent-serve-async.md` (done) — Async worker pool for agent serve.

r1d builds **on top of** these — augments `r1 serve` to a multi-session daemon, reuses the existing HTTP server scaffold, adds WS layer.
