# `r1 serve` — Operator Guide

This guide is for operators bringing up, troubleshooting, and rotating credentials
for the per-user `r1 serve` daemon. The daemon hosts N concurrent sessions over
loopback HTTP/WS for browsers/desktop and over a unix socket / Windows named pipe
for CLI tooling. See `specs/r1d-server.md` §4 for the full topology and
`docs/ARCHITECTURE.md` "Plane 6" for the architectural context.

## Discovery

Every UI/CLI client locates the daemon the same way: by reading
`~/.r1/daemon.json`. The file is mode 0600 and contains:

```json
{
  "pid": 12345,
  "version": "v0.x.y",
  "sock_path": "/run/user/1000/r1/r1.sock",
  "host": "127.0.0.1",
  "port": 49321,
  "token": "<256-bit hex>",
  "cert_fingerprint": null,
  "started_at": "2026-05-02T18:30:00Z"
}
```

CLI users invoke `r1 ctl discover` to print the parsed contents:

```bash
$ r1 ctl discover
pid:          12345
sock:         /run/user/1000/r1/r1.sock
loopback:     127.0.0.1:49321
token:        <redacted; see ~/.r1/daemon.json>
started:      2026-05-02T18:30:00Z (3 minutes ago)
version:      v0.x.y
```

Browsers and desktop clients read the same file via the platform's secure-key
mechanism (Tauri uses `tauri-plugin-store` keyed to the app identifier; the web
extension fetches it through a native helper). When `daemon.json` is missing,
the client invokes `r1 ctl info` (or auto-spawns `r1 serve` — see §
"Auto-spawn") to materialize one.

### Auto-spawn

CLI tools that depend on the daemon (e.g. `r1 chat`, `r1 ctl`) call
`daemon_http.go::Dial` with `--addr=""`. When `daemon.json` is missing or stale
the dialer attempts to spawn `r1 serve` and retry with a 2s timeout. A spawn
failure surfaces a one-line stderr error pointing to this guide.

## Install

For interactive use, no install step is needed — `r1 ctl` and `r1 chat`
auto-spawn the daemon on first invocation. For headless / SSH-only boxes or
"always-on" preference, run:

```bash
r1 serve --install
```

This writes a per-user service unit via `kardianos/service`:

| OS      | Service mechanism                                       | Unit path                                            |
|---------|----------------------------------------------------------|------------------------------------------------------|
| macOS   | `launchd` user agent                                     | `~/Library/LaunchAgents/dev.relayone.r1.plist`      |
| Linux   | `systemd --user` service                                 | `~/.config/systemd/user/r1.service`                  |
| Windows | Service Control Manager service                         | `r1.daemon` (managed by SCM)                         |

Then **starts** the unit immediately (no second command required). Verify with:

```bash
r1 serve --status
```

To remove:

```bash
r1 serve --uninstall
```

### Linux: `loginctl enable-linger` requirement

On systemd-user Linux, the user-scoped service unit only runs while the user
has an active session by default. For headless / SSH-only boxes (CI runners,
build servers, remote desktops), enable lingering so the unit runs at boot
without a login session:

```bash
loginctl enable-linger $USER
```

Without `enable-linger`, `r1 serve --install` succeeds but the daemon stops
when the SSH session ends and does NOT come back at next boot.

## Troubleshooting

### "daemon already running"

If you see:

```
daemon already running, pid=12345, sock=/run/user/1000/r1/r1.sock
use 'r1 ctl' to talk to it.
```

…a previous `r1 serve` is holding the per-user lock. This is BY DESIGN — the
single-instance enforcement uses `gofrs/flock` on `~/.r1/daemon.lock` to
prevent two daemons clobbering each other's WS port and journal directory.

Three resolutions, in order of preference:

1. **You meant to talk to the running daemon.** Use `r1 ctl <verb>` instead of
   spawning a new `r1 serve`. The discovery file points at the right unix
   socket / loopback port.

2. **You want to restart the daemon.** Send the running daemon a graceful
   shutdown:

   ```bash
   r1 ctl shutdown
   ```

   Wait until the lock file is released (the message goes away on the next
   `r1 serve` attempt; the lock file is auto-released on process exit). Then
   re-run `r1 serve`.

3. **The previous daemon crashed and left a stale lock.** This is rare but
   possible (e.g. the process was SIGKILLed, or the user's box rebooted while
   the daemon held the lock and the OS didn't unlink the file). Inspect
   `~/.r1/daemon.lock`:

   ```bash
   cat ~/.r1/daemon.lock      # prints the PID that holds it
   ps -p <pid>                # is that PID alive?
   ```

   If the PID is dead, remove the lock manually:

   ```bash
   rm ~/.r1/daemon.lock
   ```

   Then re-run `r1 serve`. Do NOT remove the lock when a live PID matches —
   you'll race two daemons against the same journal directory.

### "no such file or directory: ~/.r1/daemon.json"

The daemon is not running and auto-spawn was suppressed. Either:

```bash
r1 serve                 # foreground; ctrl-c to stop
r1 serve --install       # background via service unit
```

If `r1 serve` itself fails with "daemon already running", see above.

### Loopback bind fails with `EADDRINUSE`

The daemon picks a random ephemeral port on each start. If the binding fails
your host has likely run out of ephemeral ports (extremely rare) or a strict
firewall blocks loopback. Check:

```bash
ss -tln | grep 127.0.0.1                # what's bound on loopback
sudo lsof -i 4tcp@127.0.0.1 -sTCP:LISTEN # detailed (Linux/macOS)
```

### Journal corruption on resume

When the daemon detects a corrupt tail in a per-session journal, it truncates
to the last valid line and surfaces the error on the `ReloadResult` for that
session (see `internal/server/sessionhub/reload.go`). Symptom: a session
appears with `State: paused-reattachable` but `RecordCount` lower than the
operator expects. The truncation is a deliberate fail-safe — replay never
processes a half-written record.

If you need to inspect a corrupt journal:

```bash
ls ~/.r1/sessions/                       # which session ids exist
head ~/.r1/sessions/<id>/journal.ndjson  # first few records
tail ~/.r1/sessions/<id>/journal.ndjson  # the corrupt tail
```

### TUI / browser cannot reach the daemon

Likely root causes (in observed-frequency order):

1. **Token mismatch.** The browser cached a previous session's token. Reload
   the page — the discovery-file-fetch path will pick up the rotated token.
2. **Origin pin.** The browser is fetching from a non-loopback origin (e.g.
   `http://10.0.0.5:49321/`). The loopback Origin/Host pin rejects this with
   `403 Forbidden`. Use `http://localhost:<port>` or `http://127.0.0.1:<port>`.
3. **Subprotocol missing.** The WS client did not advertise `r1.bearer` in
   `Sec-WebSocket-Protocol`. The handler returns `401 Unauthorized` with
   `WWW-Authenticate: Bearer realm="r1"`.
4. **Single-instance contention.** A second `r1 serve` was started elsewhere
   and the original was killed; the new one's port is different. Re-read
   `~/.r1/daemon.json`.

## Token rotation

The bearer token is regenerated on every `r1 serve` start (256-bit
`crypto/rand` hex). Rotation strategies:

| Strategy                 | Command                              | When to use                                         |
|--------------------------|--------------------------------------|-----------------------------------------------------|
| Restart                  | `r1 ctl shutdown && r1 serve`        | Routine rotation (daily/weekly cron, or on-demand). |
| Force-rotate (no restart)| Not supported in v1                  | Add follow-up: a `r1 ctl rotate-token` JSON-RPC.    |
| Recover compromised      | `rm ~/.r1/daemon.json; r1 ctl shutdown; r1 serve` | If you suspect the token was leaked. Removes any cached copy in the discovery file before the new daemon writes a fresh one. |

After rotation:

- All connected WS clients are dropped (the old token is no longer valid for
  the new daemon's loopback listener).
- Each client re-reads `daemon.json` and reconnects with the new token.
- The unix socket / named pipe surface is unaffected — peer-cred check, no
  token.

### Custom token via `--token`

For CI runners that need a stable predictable token:

```bash
r1 serve --token "$(cat /run/secrets/r1-token)"
```

`--token` overrides the auto-mint. The same token is written into
`daemon.json` so client tools still find it via discovery. Rotate by
re-running with a new value.

## Journal location

| Item                    | Path                                                 | Mode |
|-------------------------|------------------------------------------------------|------|
| Single-instance lock    | `~/.r1/daemon.lock`                                  | 0600 |
| Discovery file          | `~/.r1/daemon.json`                                  | 0600 |
| Sessions index          | `~/.r1/sessions-index.json`                          | 0600 |
| Per-session journal     | `~/.r1/sessions/<id>/journal.ndjson`                 | 0600 |
| Bus WAL (shared)        | `~/.r1/bus/`                                         | 0600 |
| Service unit (Linux)    | `~/.config/systemd/user/r1.service`                  | 0644 |
| Service unit (macOS)    | `~/Library/LaunchAgents/dev.relayone.r1.plist`      | 0644 |

Override the root via `R1_HOME`:

```bash
R1_HOME=/tmp/r1-test r1 serve     # tests + sandboxed runs
```

This is the same env var the unit tests use (see
`internal/server/sessionhub/sessionhub_test.go::withSandbox`). Production
users should not set it.

### Inspecting a journal

Each line is a single NDJSON record:

```bash
head -3 ~/.r1/sessions/sess_abc.../journal.ndjson | jq .
```

Fields are documented in `internal/journal/journal.go`. Notably:

- `seq` — monotonic per-journal; reattach uses this as `since_seq`.
- `kind` — `hub.event`, `tool.start`, `tool.end`, `session.ended`, etc. Terminal
  kinds force fsync.
- `at`  — RFC3339 timestamp.
- `data` — the kind-specific payload.

Replay is read-only and side-effect-free; running a journal through `jq` while
the daemon is live is safe.

## Operational runbook (quick reference)

| Goal                                | Command                                              |
|--------------------------------------|------------------------------------------------------|
| Start daemon (foreground)            | `r1 serve`                                           |
| Start daemon (background, autostart) | `r1 serve --install`                                 |
| Stop daemon (graceful)               | `r1 ctl shutdown`                                    |
| Stop daemon (service unit)           | systemctl --user stop r1   /  launchctl unload …    |
| List sessions                        | `r1 ctl sessions list`                               |
| Inspect a session                    | `r1 ctl sessions get <id>`                           |
| Tail per-session events              | `r1 ctl sessions follow <id>`                        |
| Discover the running daemon          | `r1 ctl discover`                                    |
| Rotate the token                     | `r1 ctl shutdown && r1 serve`                        |
| Uninstall the service unit           | `r1 serve --uninstall`                               |
| Clear stale lock (DEAD pid only)     | `rm ~/.r1/daemon.lock`                               |

## See also

- `specs/r1d-server.md` — the build spec, including the §4 ASCII topology.
- `docs/ARCHITECTURE.md` — Plane 6 (r1d daemon) for architectural context.
- `docs/decisions/index.md` — D-D1 through D-D6 for the design rationale.
- `tools/cmd/chdir-lint/` — the AST-based linter that gates multi-session
  enable.
