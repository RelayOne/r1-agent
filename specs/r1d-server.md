<!-- STATUS: ready -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: lanes-protocol (for WS event subprotocol) -->
<!-- BUILD_ORDER: 5 -->

# r1d-server — `r1 serve` Per-User Singleton Daemon (Multi-Session)

## 1. Overview

This spec evolves the existing `r1 daemon` and `r1 agent-serve` modes into a single unified subcommand `r1 serve` — a **per-user singleton on-demand daemon** that hosts N concurrent r1 sessions, each bound to a specific working directory. Modeled after Watchman: one process per user, many "watched roots" (= bound workdirs / sessions), spawned on-demand by the CLI and surviving the spawning shell. UIs (TUI, web, desktop Tauri) connect via JSON-RPC 2.0 over **WebSocket** (browsers + desktop) or **unix socket / Windows named pipe** (CLI). Sessions are **goroutines, not subprocesses**.

This is **not a greenfield rewrite**. It refactors and consolidates three existing surfaces:

- `cmd/r1/daemon_cmd.go` — current `r1 daemon` queue+WAL+workers shell (kept as inner queue engine; CLI subcommand becomes the inner mode of `r1 serve`).
- `cmd/r1/agent_serve_cmd.go` — current `r1 agent-serve` HTTP facade (TrustPlane register + bearer auth + executor registry — moves intact behind a `/v1/agent/...` namespace).
- `cmd/r1/daemon_http.go` — tiny HTTP client (gets extended with token + Origin pinning, NOT replaced).
- `internal/server/server.go` — current SSE+HTTP server (gains a WebSocket sibling under `internal/server/ws/`, keeps SSE for read-only embeds).

The thesis: the codebase already standardized on `cmd.Dir` for worktree binding (CLAUDE.md design decision #1), so **goroutine-per-session is feasible if and only if we hold the line on `os.Chdir`**. One stray `os.Chdir`, `os.Open("./relative")`, or `filepath.Abs("relative")` from any of the 132 internal packages will leak workdir between concurrent sessions — silently and catastrophically. This spec therefore makes the `os.Chdir` audit + CI lint a **hard gate before multi-session is enabled**.

## 2. Stack & Versions

- **Go 1.26.1** (CI/build image — `cloudbuild-binaries.yaml`, commit 004d648). Note: `go.mod` currently pins `go 1.25.5`; the `go.mod` bump to match the build image is tracked in §15 Out of Scope and will land in a follow-up before Phase E enables multi-session. Repo has no `go.work` file at present.
- **`github.com/gofrs/flock` v0.12.x** — single-instance enforcement on `~/.r1/daemon.lock` (cross-platform, handles Windows `LockFileEx`).
- **`github.com/kardianos/service` v1.2.x** — opt-in `--install` mode for launchd (macOS), systemd (Linux), Windows Service Control Manager.
- **`github.com/coder/websocket` v1.8.x** (formerly `nhooyr/websocket`) — minimal API, `context.Context`-native, single allocation per message, built-in compression. Justification below.
- **`github.com/adrg/xdg` v0.5.x** — XDG runtime dir resolution (Linux `$XDG_RUNTIME_DIR`, macOS `$TMPDIR/r1-$UID/`, Windows named pipe).
- **`github.com/google/uuid`** — already a dep, used for session IDs.

### Why `coder/websocket` (formerly `nhooyr/websocket`) over `gorilla/websocket` and `net/http+websocket`

| Library | Pros | Cons | Verdict |
|---|---|---|---|
| `gorilla/websocket` | Battle-tested (10+ years), most StackOverflow answers | Archived 2022, no `context.Context` support on read/write, manual ping-loop required, last release Feb 2024 | No — archive status disqualifying |
| `coder/websocket` (`nhooyr/websocket`) | `context.Context` on Read/Write, `wsjson.Read/Write` helpers, RFC-6455 + permessage-deflate, single allocation per frame, actively maintained (Coder fork keeps the repo alive) | Less SO surface area | **Picked.** Idiomatic Go-2026, native ctx, smaller surface |
| `net/http` + custom upgrader | Zero deps | Re-implementing RFC 6455, framing, ping/pong, close codes | No — wheel-reinvention |

The `nhooyr/websocket` repo was migrated to `coder/websocket` after its author retired the project; the API is identical and the Coder org provides ongoing security patches.

## 3. Existing Patterns to Follow / Refactor Targets

### Keep, extend
- **`internal/bus/`** — durable WAL-backed event bus (already supports `Replay(pattern, fromSeq, handler)` — exactly what `Last-Event-ID` reconnect needs). Each session subscribes to its own scope (`Scope{TaskID: sessionID}`). Daemon adds **no** new persistence layer.
- **`internal/server/server.go`** — keep the SSE bus + Bearer auth shape. Add a `ServeMux` route table prefix (`/v1/...`) and mount the new WS handler.
- **`cmd/r1/daemon_cmd.go`** — its `daemon.New` queue+WAL+workers engine is reused as one of the modes the new `serve` command can host. The CLI dispatcher entries (`enqueue/status/workers/pause/resume/wal/tasks`) move to `r1 ctl ...` and gain JSON-RPC equivalents.
- **`cmd/r1/agent_serve_cmd.go`** — TrustPlane register + buildExecutorRegistry + buildSettlementCallback move to `internal/agentserve/` (already mostly there), surfaced under `/v1/agent/...`.
- **`internal/desktopapi/desktopapi.go`** — typed `Handler` interface stays the source of truth for desktop method names; daemon binds these to JSON-RPC routes.

### Refactor (not replace)
- `daemon_cmd.go`'s monolithic `--addr 127.0.0.1:9090` listener split into: control listener (unix socket / named pipe), WS listener (random ephemeral loopback port).
- `daemon_http.go`'s tiny HTTP client gains `--token` resolution from `~/.r1/daemon.json` and Origin/Host pin awareness on the server side.
- `server/server.go`'s `EventBus` (broadcast SSE) is retained for read-only consumers; new per-session WS subscriptions live in a parallel `SessionHub` under `internal/server/sessionhub/`.

## 4. Architecture Diagram

```
+-------------------------+        +-------------------------+        +-------------------------+
|  Browser / Desktop UI   |        |  CLI (r1 chat, r1 ctl)  |        |   Tauri WebView         |
+-----------+-------------+        +-----------+-------------+        +-----------+-------------+
            |  ws://127.0.0.1:<p>             |  unix socket /                     |  same as Browser
            |  + Authorization: Bearer        |  named pipe (no auth,              |  via tauri-plugin-
            |  + Origin: http://localhost     |  peer-cred check)                  |  websocket
            v                                  v                                    v
+-----------------------------------------------------------------------------------------+
|                        r1 serve (single process, per-user)                              |
|                                                                                         |
|  +-------------------------+        +-------------------------+                         |
|  |  IPC Listener Mux       |        |  Loopback HTTP+WS       |                         |
|  |  - unix socket .sock    |        |  - random ephemeral port|                         |
|  |  - npipe r1-<USER>      |        |  - Origin pin (loopback)|                         |
|  |  - peer-cred (uid)      |        |  - Token (Bearer + WS   |                         |
|  |                         |        |    subprotocol)         |                         |
|  +-----------+-------------+        +-----------+-------------+                         |
|              |   JSON-RPC 2.0   ←---- shared dispatcher ---→  |                         |
|              v                                                v                         |
|  +-----------------------------------------------------------------+                    |
|  |                    SessionHub (sync.Map<id, *Session>)          |                    |
|  +-----+-----------------+----------------+----------------+-------+                    |
|        |                 |                |                |                            |
|        v                 v                v                v                            |
|   +---------+      +---------+      +---------+      +---------+                        |
|   |Session A|      |Session B|      |Session C|      |Session D|                        |
|   |  goroutine     |  goroutine     |  goroutine     |  goroutine                       |
|   |  SessionRoot   |  SessionRoot   |  SessionRoot   |  SessionRoot                     |
|   |  =/path/to/A   |  =/path/to/B   |  =/path/to/C   |  =/path/to/D                     |
|   |  agentloop     |  agentloop     |  agentloop     |  agentloop                       |
|   |  cortex.Workspace                                                                   |
|   |  journal.ndjson (per-session, under <SessionRoot>/.r1/sessions/<id>/)               |
|   +----+----+      +----+----+      +----+----+      +----+----+                        |
|        |                |                |                |                             |
|        +----------------+----------------+----------------+                             |
|                                  |                                                      |
|                                  v                                                      |
|                       +--------------------+                                            |
|                       |  internal/bus WAL  |  ← shared. Per-session events keyed        |
|                       |  ~/.r1/bus/        |    by Scope{TaskID: sessionID}.            |
|                       +--------------------+                                            |
|                                                                                         |
|  +------------------------+    +------------------------+   +---------------------+    |
|  |  Single-Instance Lock  |    |  Discovery File         |   |  Token Vault       |    |
|  |  ~/.r1/daemon.lock     |    |  ~/.r1/daemon.json     |   |  256-bit random,   |    |
|  |  gofrs/flock           |    |  {pid,sock,port,token, |   |  rotated on start  |    |
|  |                        |    |   version}, mode 0600   |   |                    |    |
|  +------------------------+    +------------------------+   +---------------------+    |
+-----------------------------------------------------------------------------------------+
                                          |
                                          v
                               +-----------------------+
                               |   Cortex per session  |
                               |   (Workspace, Lobes,  |
                               |    Notes, Spotlight)  |
                               +-----------------------+
                                          |
                                          v
                                +-------------------+
                                |  os/exec.Cmd      |
                                |  with cmd.Dir =   |
                                |  SessionRoot      |
                                +-------------------+
```

## 5. Public HTTP/WS API

Bind: `127.0.0.1:<random ephemeral>`. All routes require `Authorization: Bearer <token>` (HTTP) or `Sec-WebSocket-Protocol: r1.bearer, <token>` (WS). All HTTP responses set `Vary: Origin` and reject any non-loopback `Host` / `Origin` header.

### 5.1 Discovery / health

| Method | Path | Body | Response |
|---|---|---|---|
| `GET` | `/v1/health` | — | `{"status":"ok","version":"<sha>","sessions":N,"uptime_s":N}` (no auth) |
| `GET` | `/v1/discover` | — | `{"socket":"/run/user/1000/r1/r1.sock","port":54123,"protocol_version":1}` (no auth, used by `r1 ctl discover`) |

### 5.2 Sessions (CRUD)

| Method | Path | Body | Response |
|---|---|---|---|
| `POST` | `/v1/sessions` | `{"workdir":"/abs/path","model":"sonnet-4.5","skill_pack"?,"budget_usd"?}` | `201 {"session_id":"sess_<uuid>","workdir":"...","started_at":"<iso>"}` |
| `GET` | `/v1/sessions` | — | `{"sessions":[{"id":..,"workdir":..,"state":"running"\|"paused"\|"ended","started_at":..,"last_seq":N}]}` |
| `GET` | `/v1/sessions/:id` | — | `{...same shape as create...,"cost_usd":..,"messages_count":N}` |
| `DELETE` | `/v1/sessions/:id` | — | `204` (cancels session ctx, flushes journal, removes from hub) |
| `POST` | `/v1/sessions/:id/pause` | — | `{"paused_at":"<iso>"}` |
| `POST` | `/v1/sessions/:id/resume` | — | `{"resumed_at":"<iso>"}` |

### 5.3 Streaming

| Method | Path | Auth | Notes |
|---|---|---|---|
| `GET (Upgrade)` | `/v1/sessions/:id/ws` | Subprotocol `r1.bearer, <token>` | JSON-RPC 2.0 over WS. Subscribe semantics. |
| `GET` | `/v1/sessions/:id/sse?token=<t>&since_seq=<n>` | Token via query (EventSource cannot set headers) | Read-only fallback for IDE badges and dashboards. Honors `Last-Event-ID` header. |

### 5.4 Agent-serve compatibility (preserved)

Mounted at `/v1/agent/*`. Existing `/api/capabilities`, `/api/task`, `/api/task/{id}` routes from `internal/agentserve/` re-mount under the new prefix; the old `/api/...` paths continue to work for one minor version with a `Deprecation` header.

### 5.5 Daemon-engine compatibility (preserved)

Mounted at `/v1/queue/*`. The existing `/enqueue`, `/status`, `/workers`, `/pause`, `/resume`, `/wal`, `/tasks` from `daemon_cmd.go` re-mount under the prefix.

## 6. JSON-RPC 2.0 Method Catalog

Wire envelope per `desktop/IPC-CONTRACT.md` §1 (jsonrpc / id / method / params / result / error). Server-pushed events use `method: "$/event"` with `params: {sub, seq, type, data}` per `specs/research/synthesized/transport.md`.

### 6.1 Session control (aligns with desktop §2.1 + lanes-protocol)

| Method | Params | Result | Notes |
|---|---|---|---|
| `session.start` | `{prompt?, workdir, model?, skill_pack?, budget_usd?}` | `{session_id, started_at}` | Equivalent to `POST /v1/sessions` |
| `session.pause` | `{session_id}` | `{paused_at}` | |
| `session.resume` | `{session_id}` | `{resumed_at}` | |
| `session.cancel` | `{session_id, reason?}` | `{ended_at}` | |
| `session.send` | `{session_id, text}` | `{accepted: true, message_id}` | User input mid-turn (router-LLM decides per D-2026-05-02-04) |
| `session.subscribe` | `{session_id, since_seq?}` | `{sub: <int>, current_seq: N}` | Server begins pushing `$/event` notifications |
| `session.unsubscribe` | `{sub}` | `{}` | |

### 6.2 Lane / cortex (lanes-protocol — see spec 3)

| Method | Params | Result |
|---|---|---|
| `lanes.list` | `{session_id}` | `{lanes: [{id, name, status, last_event_seq}]}` |
| `lanes.kill` | `{session_id, lane_id}` | `{killed: true}` |
| `cortex.notes` | `{session_id, since_seq?}` | `{notes: [{lobe, body, critical, seq}]}` |

### 6.3 Ledger / memory / cost / descent (existing desktop verbs)

Routed unchanged from `internal/desktopapi/desktopapi.go`:
- `ledger.get_node`, `ledger.list_events`
- `memory.list_scopes`, `memory.query`
- `cost.get_current`, `cost.get_history`
- `descent.current_tier`, `descent.tier_history`

### 6.4 Daemon control (privileged — token + uid match required)

| Method | Params | Result |
|---|---|---|
| `daemon.info` | `{}` | `{version, pid, uptime_s, sessions, port, sock_path}` |
| `daemon.shutdown` | `{grace_s?: int}` | `{accepted: true}` |
| `daemon.reload_config` | `{}` | `{reloaded_at}` |

### 6.5 Server-pushed event types

| `type` | `data` shape |
|---|---|
| `session.delta` | `{role, payload}` (assistant text / tool-use block) |
| `session.tool_started` | `{tool, args, lane_id}` |
| `session.tool_completed` | `{tool, lane_id, ok, duration_ms}` |
| `session.ended` | `{reason: "ok"\|"cancelled"\|"error"}` |
| `lane.delta` | `{lane_id, status, fragment}` |
| `cost.tick` | `{usd_delta, tokens_delta}` |
| `ledger.appended` | `{hash, type}` |
| `descent.tier_changed` | `{ac_id, from, to, status}` |
| `daemon.reloaded` | `{at}` (sent once after restart, before resumed deltas) |

Each event carries a monotonic `seq` per subscription; clients persist `seq` and replay via `since_seq` on reconnect.

## 7. Auth + Security

### 7.1 Token

- 256-bit (32-byte) cryptographic random, generated via `crypto/rand`.
- Written to `~/.r1/daemon.json` with mode **0600** (file) and **0700** (`~/.r1/` dir).
- **Rotated on every daemon start** — no persistence across restarts. Old clients see 401, reconnect via discovery file.
- HTTP: `Authorization: Bearer <token>`.
- WS: `Sec-WebSocket-Protocol: r1.bearer, <token>` (the token is sent as a *subprotocol value*, not a header — browsers cannot set custom WS headers but can set subprotocols).
- SSE fallback (read-only): `?token=<t>` query param (EventSource cannot set headers; mitigated by Origin pin + token rotation).

### 7.2 Origin / Host pinning (CSWSH + DNS-rebind defense)

- **Origin allowlist**: WS upgrade rejects unless `Origin` is `null`, missing (CLI clients), `http://127.0.0.1:<port>`, `http://localhost:<port>`, or `tauri://localhost`. Configurable via `~/.r1/daemon.toml`.
- **Host pinning**: HTTP rejects unless `Host` ∈ {`127.0.0.1:<port>`, `localhost:<port>`}. Defeats DNS rebinding (attacker can't trick browser into sending `Host: evil.com` with same destination IP).
- **Subprotocol negotiation**: server requires `r1.bearer` subprotocol on WS upgrade. `new WebSocket(url)` from a malicious page **without** the subprotocol fails the handshake; with the subprotocol, the malicious page must already know the token.

### 7.3 Unix socket / named pipe

- Linux/macOS: socket at `$XDG_RUNTIME_DIR/r1/r1.sock` (fallback `$TMPDIR/r1-$UID/r1.sock`). Created with mode **0600**, parent dir **0700**.
- Linux: defence-in-depth via `SO_PEERCRED` — server verifies `uid == os.Getuid()` of the connecting peer; reject otherwise.
- macOS: same — uses `LOCAL_PEERCRED` socket option.
- Windows: named pipe `\\.\pipe\r1-<USERNAME>` with `SECURITY_ATTRIBUTES` granting only the current SID.
- **No token required** on unix-socket / named-pipe connections — peer-credential check already proves identity.

### 7.4 File modes (verified at startup)

- `~/.r1/` = 0700.
- `~/.r1/daemon.json` = 0600.
- `~/.r1/daemon.lock` = 0600.
- Socket file = 0600 with parent dir 0700.
- On startup, daemon **chmod**s and **fchmod**s these; refuses to start if a wider mode is detected after the chmod (e.g. EROFS or ACL inheritance). Fail-closed.

## 8. Session Lifecycle

### 8.1 Create

1. Client `POST /v1/sessions {workdir, model, ...}` (or `session.start`).
2. Server validates `workdir`: `filepath.Abs`, `os.Stat`, must be directory, must be writable, must not be the daemon's `~/.r1/` dir (anti-recursion).
3. Server generates `session_id = "sess_" + uuid.New()`, allocates `*Session` struct with `SessionRoot string` field set to `workdir`, calls `cortex.NewWorkspace(SessionRoot)`, opens `<workdir>/.r1/sessions/<id>/journal.ndjson` for append.
4. Server spawns `go session.run(ctx)` — the goroutine drives `agentloop.Loop` with a `WorkspaceFunc` and an `OnEvent` hook that appends every event to the journal **before** broadcasting to subscribers.
5. Server emits `session.started` event on the bus and returns `{session_id, workdir, started_at}`.

### 8.2 Attach (subscribe)

1. Client opens WS to `/v1/sessions/:id/ws` with subprotocol token.
2. Server sends `JSON-RPC` request handshake; client sends `session.subscribe {session_id, since_seq?}`.
3. If `since_seq` is set, server reads `journal.ndjson` from offset `since_seq` and replays events **before** streaming live deltas.
4. Subscription gets a monotonic `seq` counter (per-subscription, not global).

### 8.3 Detach

- Client closes WS. Session goroutine **continues**.
- Bus events buffer in WAL (already durable per `internal/bus/`).
- Journal continues to be written.

### 8.4 Resume

- Client reconnects WS, sends `session.subscribe {session_id, since_seq: <last>}`.
- Server replays journal from `since_seq + 1` then resumes live streaming.

### 8.5 Pause / Resume (workflow-level)

- `POST /v1/sessions/:id/pause` flips session state to `paused`; agentloop honors `ctx.Done()` (sub-context) but session goroutine stays alive.
- `POST /v1/sessions/:id/resume` re-arms the sub-context.

### 8.6 Kill

- `DELETE /v1/sessions/:id` cancels session ctx, drains in-flight tool calls (with 5s grace), flushes journal `fsync`, writes final `session.ended {reason:"cancelled"}` event, removes from `SessionHub`.

### 8.7 Daemon restart resume

- On startup, daemon scans `~/.r1/sessions-index.json` (an index of `{session_id → workdir}` updated on every Create/Delete) and re-opens each session's `journal.ndjson`.
- For sessions whose final journal record is **not** `session.ended`, daemon marks them `paused-reattachable` and replays the journal into a new `*Session` struct (workspace + Lobe state rebuilt from journal events).
- First post-restart event broadcast: `daemon.reloaded {at}` to every subscriber.

## 9. Journal Format (`journal.ndjson` schema)

One JSON object per line. Path: `<workdir>/.r1/sessions/<session_id>/journal.ndjson`. Append-only, fsync on terminal events.

```jsonc
// First record (always — session genesis)
{"v":1,"seq":0,"at":"2026-05-02T15:00:00Z","kind":"session.created",
 "session_id":"sess_xxx","workdir":"/abs/path","model":"sonnet-4.5",
 "skill_pack":null,"budget_usd":10.0,"r1_version":"<sha>"}

// User input
{"v":1,"seq":1,"at":"...","kind":"user.input","text":"..."}

// Assistant delta
{"v":1,"seq":2,"at":"...","kind":"session.delta",
 "payload":{"role":"assistant","content":[{"type":"text","text":"..."}]}}

// Tool started
{"v":1,"seq":3,"at":"...","kind":"session.tool_started",
 "tool":"bash","args":{"cmd":"go build ./..."},"lane_id":"L-1"}

// Tool completed
{"v":1,"seq":4,"at":"...","kind":"session.tool_completed",
 "lane_id":"L-1","ok":true,"duration_ms":1234,"output_ref":"<hash>"}

// Cost tick
{"v":1,"seq":5,"at":"...","kind":"cost.tick","usd_delta":0.012,"tokens_delta":1024}

// Lane delta (cortex)
{"v":1,"seq":6,"at":"...","kind":"lane.delta",
 "lane_id":"L-1","status":"running","fragment":{"text":"..."}}

// Pause
{"v":1,"seq":7,"at":"...","kind":"session.paused"}

// Resume
{"v":1,"seq":8,"at":"...","kind":"session.resumed"}

// Terminal — last record before goroutine exits (fsync'd)
{"v":1,"seq":N,"at":"...","kind":"session.ended","reason":"ok"}
```

**Versioning:** `v: 1`. On schema break, daemon refuses to replay journals from older versions — emits `daemon.reloaded` with `data.skipped_sessions=[id, ...]` and a `data.migration_hint` URL.

**Index file:** `~/.r1/sessions-index.json` (mode 0600) maps `{session_id → {workdir, started_at, last_seq}}`. Updated on Create / Kill / on every flush. Used by daemon restart to know which journals to scan without walking the filesystem.

## 10. `os.Chdir` Audit + CI Lint Design

This is the **mandatory gate** before multi-session is enabled. Until this lint is green and the audit findings are resolved, `r1 serve` runs in `--single-session` mode (rejects 2nd `POST /v1/sessions`).

### 10.1 Snapshot of current state (informational, not the audit)

`grep -rn "os\.Chdir\|os\.Getwd" internal/ cmd/` returns 27 hits today:
- `os.Chdir` (3): all in `cmd/r1/mcp_cmd_test.go` test setup with `t.Cleanup` restore. **Test-only — allowed.**
- `os.Getwd` (24): split between non-test (18) and test (6). Most non-test occurrences are CLI entry points (`cmd/r1/main.go`, `cmd/r1/task_cmd.go`, etc.) reading the user's cwd as a default workdir — also allowed when wrapped behind `daemon.SessionRoot()`.

### 10.2 Audit pass (manual + scripted)

For each occurrence found by `tools/lint-no-chdir.sh`:

1. Classify into one of:
   - **Allowed (CLI entry)** — top-level command in `cmd/r1/*` reading cwd to compute a default `--workdir` flag value. Must be wrapped in `// LINT-ALLOW chdir-cli-entry: <reason>` comment.
   - **Allowed (test setup)** — `_test.go` file with restore via `t.Cleanup`. Must have `// LINT-ALLOW chdir-test: <reason>` comment.
   - **Forbidden** — anything in `internal/` outside `_test.go` files, OR any goroutine-spawned context. **Must be refactored to take `repoRoot string` as a parameter.**
2. Refactor each forbidden occurrence to thread `SessionRoot` through the call chain (signature: `func XxxAt(ctx, root string, ...) error`).
3. Add a regression test that boxes the package: `TestNoChdirInPkg` — uses `go/parser` to AST-walk every `.go` file in the package, fail if `os.Chdir` is found without the `LINT-ALLOW` annotation.

### 10.3 CI lint script outline

`tools/lint-no-chdir.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
# Scan: emit ALL os.Chdir / os.Getwd / filepath.Abs("") occurrences not annotated.
violations=$(go run ./tools/cmd/chdir-lint ./internal/... ./cmd/...)
if [ -n "$violations" ]; then
  echo "$violations" >&2
  echo
  echo "FAIL: os.Chdir / os.Getwd / relative-path violations." >&2
  echo "Each call must either:" >&2
  echo "  - be in a _test.go file with t.Cleanup restore + // LINT-ALLOW chdir-test: reason" >&2
  echo "  - be in a cmd/r1/*.go top-level command + // LINT-ALLOW chdir-cli-entry: reason" >&2
  echo "  - be refactored to thread SessionRoot via cmd.Dir / function param" >&2
  exit 1
fi
echo "ok: 0 chdir violations"
```

`tools/cmd/chdir-lint/main.go` (Go AST walker):

- Loads packages via `golang.org/x/tools/go/packages`.
- For each file, walks AST looking for `*ast.SelectorExpr` matching `os.Chdir`, `os.Getwd`, `filepath.Abs` with a string-literal `"."`, `os.Open` / `os.Stat` / `os.ReadFile` etc. with a non-absolute string-literal first arg.
- Reads the line's leading `//` comments; if any contains `LINT-ALLOW chdir-` skip it.
- Emits each unannotated hit as `<file>:<line>: <funcname>(<arg>)`.

### 10.4 Per-session sentinel (runtime defense)

Independent of CI lint, every session goroutine runs a sentinel before each tool dispatch:

```go
// In session.dispatchTool — runs on the session goroutine.
expected := s.SessionRoot
got, err := os.Getwd()
if err != nil || got != expected {
    panic(fmt.Sprintf(
        "FATAL: session %s expected cwd=%s got=%s — process-global chdir leaked. Refusing to dispatch.",
        s.ID, expected, got))
}
```

If a stray `os.Chdir` slipped past CI, the sentinel **panics loudly** before tool dispatch, killing the whole daemon. This is intentional: silent cross-session contamination is far worse than a noisy crash that journal-replay can recover from.

### 10.5 Packages to audit

Priority order (highest leak risk first):
1. `engine/`, `agentloop/`, `tools/`, `bash/`, `patchapply/` — runners that exec processes.
2. `worktree/`, `verify/`, `baseline/`, `git*/` — git operations.
3. `goast/`, `repomap/`, `symindex/`, `chunker/`, `tfidf/`, `vecindex/` — code-analysis with file IO.
4. `memory/`, `wisdom/`, `research/`, `replay/` — persistent-state packages.
5. `lsp/`, `mcp/` — anything with subprocess servers.
6. `cmd/r1/*` — CLI entrypoints.
7. Remaining 100+ packages — best-effort, weekly cron audit.

## 11. Hot-Upgrade Contract

**Restart-required, transparent.** No `tableflip`, no FD-pass, no plugin tricks.

1. Operator runs `r1 update` (downloads new binary to `~/.r1/bin/r1` atomic-rename).
2. Operator runs `r1 serve restart` — daemon receives `daemon.shutdown {grace_s: 30}` JSON-RPC call, broadcasts `session.paused` to every active subscriber, fsyncs every journal, exits 0.
3. New binary spawned (init via the same on-demand path or `kardianos/service` if installed).
4. New daemon scans `~/.r1/sessions-index.json`, re-opens each `journal.ndjson`, rebuilds `*Session` (workspace + lobe state from journal).
5. New daemon starts WS listener, broadcasts `daemon.reloaded {at, version: <new-sha>}` to any reconnecting subscriber.
6. Clients reconnect with `Last-Event-ID` (or `since_seq`) → server replays from journal.
7. **Protocol-version handshake**: WS subprotocol negotiates `r1.proto.v1`. If client sends `r1.proto.v2` and server only knows v1, server closes with code 1002 and a `migration_hint` close reason; UI prompts user to refresh.

**`r1 doctor`** sub-command detects "installed binary newer than running daemon" and prompts user to `r1 serve restart`.

## 12. `--install` Mode (Cross-Platform Service Installation)

Optional. Default on-demand spawn path is preferred. `r1 serve --install` writes a platform-appropriate service unit via `kardianos/service`:

- **macOS** (launchd): `~/Library/LaunchAgents/dev.relayone.r1.plist`. `RunAtLoad=true`, `KeepAlive=true`, `StandardOutPath=~/.r1/log/r1.out`, `StandardErrorPath=~/.r1/log/r1.err`.
- **Linux** (systemd user): `~/.config/systemd/user/r1.service`. `Restart=on-failure`, `RestartSec=5`, `Environment="R1_HOME=%h/.r1"`. `loginctl enable-linger $USER` for headless boxes.
- **Windows** (Service Control Manager): service name `r1.daemon`, run as current user, `DelayedAutoStart=true`.

Sub-commands:
- `r1 serve --install` — write unit + enable + start.
- `r1 serve --uninstall` — stop + disable + remove unit.
- `r1 serve --status` — shows whether installed, running, last exit code.

## 13. Test Plan

### 13.1 Unit tests
- `internal/server/sessionhub_test.go` — `SessionHub.Create / Get / Delete` with concurrent goroutines, race detector enabled.
- `internal/server/ws_test.go` — `wstest` round-trip: subscribe, receive `$/event`, reconnect with `since_seq`, replay correctness.
- `internal/server/auth_test.go` — Origin/Host rejection matrix; token-mismatch 401; subprotocol-missing handshake fail.
- `internal/journal/journal_test.go` — `Append → Replay → Truncate(at_seq)` invariants; corruption recovery (truncate-at-last-valid-line).

### 13.2 Integration tests
- `cmd/r1/serve_integration_test.go`:
  - **Multi-session race test** — spawn 8 concurrent sessions in 8 different temp dirs, run a benign tool (`bash echo $PWD`) in each, assert each session's tool output matches its `SessionRoot`. Run with `-race -count=10`.
  - **Chdir sentinel test** — inject a forbidden `os.Chdir` via a test-only build tag; assert sentinel panics with `expected cwd ...` substring.
  - **Kill-and-resume test** — start 3 sessions, exchange messages, send `SIGTERM` to daemon, wait 2s, restart daemon, verify journal replay reconstructs each session's last 10 events, verify reconnecting WS clients see `daemon.reloaded` then resumed deltas.
  - **Single-instance test** — start daemon, attempt second `r1 serve`, assert second exits 1 with `"daemon already running, pid=N"`.
  - **Token rotation test** — start, capture token, restart, capture new token, assert old token now 401.
  - **Origin pinning test** — fake `Origin: http://evil.com`, assert WS upgrade fails 403.

### 13.3 Lint gate test
- `tools/cmd/chdir-lint/main_test.go` — feed it a fixture file with annotated + unannotated `os.Chdir` calls; assert it flags only the unannotated ones.
- CI workflow `.github/workflows/r1d-server.yml` — runs `tools/lint-no-chdir.sh` and fails the build on any violation. Wired in `Makefile` target `make lint-chdir`.

### 13.4 Benchmark / soak
- `bench/r1d_serve_bench_test.go` — 50 sessions × 100 messages each, measure: median tool dispatch latency, p99, journal write throughput, FD count growth. SLOs: p99 dispatch < 50ms, FD growth = 0 over 1 hour.

## 14. Migration Plan from Existing Code

### 14.1 What stays (no behavior change for users on this iteration)

- `cmd/r1/daemon_cmd.go` — keep all subcommands working. Internally, `daemon start` becomes a thin wrapper around `r1 serve --inner-mode=queue-engine` (no flag rename for users).
- `cmd/r1/agent_serve_cmd.go` — keep `r1 agent-serve --addr ...` as an alias that internally calls `r1 serve --enable-agent-routes --addr ...`.
- `internal/agentserve/server.go` — unchanged. `internal/daemon/*` unchanged.
- `internal/server/server.go` — keep `EventBus`, `New`, `Handler`, `ListenAndServe` intact. New code lives in sibling files.
- `desktop/IPC-CONTRACT.md` — wire format already matches; daemon implements it.

### 14.2 What consolidates

| Old | New | Notes |
|---|---|---|
| `r1 daemon start --addr` | `r1 serve --addr` | `daemon` becomes alias |
| `r1 agent-serve --addr` | `r1 serve --enable-agent-routes` | `agent-serve` becomes alias |
| `r1 daemon enqueue/status/...` | `r1 ctl <verb>` (also keeps old form) | New `ctl` is JSON-RPC client over socket |
| Two separate listeners (port 9090 + port 8440) | Single listener (random port) + unix socket | Discovery via `~/.r1/daemon.json` |
| Hard-coded `127.0.0.1:9090` token | Token in `~/.r1/daemon.json` mode 0600 | Backwards-compatible: `--token` flag still honored |

### 14.3 New files

- `cmd/r1/serve_cmd.go` — new `r1 serve` dispatcher (small file; mostly flag parsing + delegation).
- `internal/server/sessionhub/sessionhub.go` — `SessionHub` type.
- `internal/server/sessionhub/session.go` — `Session` struct (carries `SessionRoot`).
- `internal/server/ws/handler.go` — WS handler using `coder/websocket`.
- `internal/server/ipc/socket_unix.go` + `internal/server/ipc/pipe_windows.go` — unix socket + named pipe listeners.
- `internal/journal/journal.go` — append-only NDJSON journal writer/reader.
- `internal/daemonlock/lock.go` — `gofrs/flock` wrapper.
- `internal/daemondisco/discovery.go` — `~/.r1/daemon.json` read/write helpers.
- `internal/serviceunit/service.go` — `kardianos/service` wrapper for `--install`.
- `tools/cmd/chdir-lint/main.go` + `tools/lint-no-chdir.sh` — the CI lint.

### 14.4 Removed (deferred — none in this iteration)

Nothing is removed. Old commands stay as aliases for one minor version. Removal happens in a follow-up cleanup spec only after telemetry shows no users on the old paths.

## 15. Out of Scope

The following are deliberately **not** addressed by this spec. Each is a follow-up work item, named here to forestall scope creep during review.

1. **Remote / LAN access.** Daemon binds `127.0.0.1` only. Network-exposed multi-host operation requires mTLS + an authentication front-end (e.g., a reverse proxy with client certs) and is the subject of a separate `r1d-remote.md` spec.
2. **Multi-tenant per-host (multiple uids).** One daemon per `os.Getuid()`. Shared-host operation (one binary, many users) is deferred. The flock + peer-cred + 0700 dir model assumes single-uid.
3. **Federation across hosts.** No cross-machine session migration. Session IDs are unique only per-daemon, not globally.
4. **Plugin-loaded session types.** Sessions are first-class Go structs in this iteration; dynamic plugin loading via `plugin.Open` (or any in-process plugin scheme) is not part of this work.
5. **`tableflip` / FD-pass / live-binary hot-upgrade.** §11 explicitly chose restart-required + journal replay. Zero-downtime FD-pass is not implemented and is not on the roadmap.
6. **Encrypted journals at rest.** Journals are plain NDJSON mode 0600. Encryption is the subject of `specs/encryption-at-rest.md` (existing) and is layered on top of this design without changing wire formats.
7. **Windows service auto-start as `LocalSystem` / SYSTEM account.** `--install` mode runs only as the current user.
8. **`go.mod` version bump from 1.25.5 → 1.26.1.** Out-of-band cleanup. Will land before Phase E (multi-session) but is not gated by this spec — tracked separately to keep the migration history clean.
9. **Browser cookie / SameSite-strict auth.** Tokens are bearer-only on HTTP and via WS subprotocol. Cookie-based auth (with all its CSRF surface) is intentionally not adopted.
10. **Per-session resource limits (cgroups, ulimit).** Sessions share daemon's process limits. Per-session cgroup isolation is a deferred hardening pass.

## 16. Risks & Mitigations

| # | Risk | Likelihood | Severity | Mitigation | Spec ref |
|---|---|---|---|---|---|
| R1 | Stray `os.Chdir` in `internal/` leaks workdir between concurrent sessions | High (132 packages) | Catastrophic (silent cross-session contamination) | CI lint blocks merge; per-session sentinel panics on mismatch; `--single-session` mode is the floor until lint is green | §10.1-10.5, items 1-10 |
| R2 | Token leakage via discovery file | Medium (file mode misconfig) | High (full session takeover) | Mode 0600 + 0700 parent + chmod-and-verify on startup + rotate-on-start (no persistence across restarts); fail-closed if mode is wider than expected after the chmod (e.g. ACL inheritance / EROFS) | §7.1, §7.4, item 12 |
| R3 | DNS rebind / CSWSH hijacks browser session | Medium (any malicious tab) | High (full RPC) | Origin allowlist (loopback + tauri only) + Host pin + WS subprotocol gate; state-changing methods always Origin-checked | §7.2, items 19-20 |
| R4 | Journal NDJSON corruption (partial write on crash) | Low (fsync on terminal events) | Medium (data loss for one session) | Truncate-at-last-valid-line on Open; per-line `v: 1` schema version; refuse to replay future versions with `migration_hint` URL | §9, items 23, 46 |
| R5 | Daemon-restart race — operator runs `r1 ctl` before listeners ready | Medium | Low (transient connection refused) | Discovery file written **after** all listeners (`ipc` + WS + HTTP) accept connections; CLI retries 2s with backoff | §11, items 17, 42 |
| R6 | Two `r1 serve` instances start in the same millisecond | Low (mostly user-error) | Medium (duplicate listeners, port collision) | `gofrs/flock.TryLock` is atomic; second instance reads stale discovery, fails fast with `"daemon already running, pid=N"` | §11, items 11, 50 |
| R7 | macOS Gatekeeper / SMC quarantines daemon binary on first launch | Medium (macOS notarization gap) | Low (one-time setup friction) | `r1 doctor` detects quarantine attribute (`xattr -l com.apple.quarantine`) and prints `xattr -d` instructions; CI signs releases via the platform team's existing notarization flow | §11 (`r1 doctor`) |
| R8 | Hot-upgrade journal-version skew (new daemon refuses old journals) | Low (only on schema break) | Medium (sessions stranded) | Schema version `v: 1` enforced; on mismatch daemon emits `daemon.reloaded {data.skipped_sessions: [...], data.migration_hint: <URL>}`; user-visible non-fatal | §9 versioning paragraph, §11 step 7 |
| R9 | `coder/websocket` upstream archives like `gorilla/websocket` did | Low (Coder org actively maintaining) | Low (drop-in replacements exist) | Library is wrapped behind `internal/server/ws/handler.go`; replacement requires editing one file | §3 refactor list, §2 table |
| R10 | Single discovery file becomes contention point on rapid daemon restarts | Low | Low | Atomic `tmp+rename` write semantics; readers tolerate `ENOENT` with retry; consumers (`r1 ctl`) idempotent | §7.4, item 12 |

## Implementation Checklist

### Phase A — `os.Chdir` audit gate (BLOCKING — must finish before Phase E)

1. [ ] Build `tools/cmd/chdir-lint/main.go` — Go AST walker that flags `os.Chdir`, `os.Getwd`, and `filepath.Abs("")` / `os.Open("./...")` calls without `// LINT-ALLOW chdir-*: reason` annotation. Loads packages via `golang.org/x/tools/go/packages`.
2. [ ] Add `tools/lint-no-chdir.sh` wrapper that runs the AST tool over `./internal/...` and `./cmd/...` and exits non-zero on violations.
3. [ ] Wire `make lint-chdir` target in `Makefile`. Add to `make ci` (the `go build` + `go test` + `go vet` triple per CLAUDE.md).
4. [ ] Add `.github/workflows/r1d-server.yml` step that runs `make lint-chdir` and fails the build on red.
5. [ ] Audit pass 1: `engine/`, `agentloop/`, `tools/`, `bash/`, `patchapply/`. Annotate or refactor every hit. Add `// LINT-ALLOW chdir-cli-entry` only at top-level `cmd/r1/*` callers.
6. [ ] Audit pass 2: `worktree/`, `verify/`, `baseline/`, `gitblame/`, `git*/`. Same treatment.
7. [ ] Audit pass 3: `goast/`, `repomap/`, `symindex/`, `chunker/`, `tfidf/`, `vecindex/`. Refactor `repoRoot string` plumbing where missing.
8. [ ] Audit pass 4: `memory/`, `wisdom/`, `research/`, `replay/`, `lsp/`, `mcp/`.
9. [ ] Audit pass 5: remaining packages (best-effort).
10. [ ] Implement `internal/server/sessionhub/sentinel.go` — `assertCwd(expected string)` helper that calls `os.Getwd()` and panics on mismatch. Document why it panics.

### Phase B — Single-instance + discovery

11. [ ] Add `internal/daemonlock/lock.go` — wraps `gofrs/flock` on `~/.r1/daemon.lock`. Tries `TryLock`; on failure, reads `~/.r1/daemon.json` for the existing pid and prints `"daemon already running, pid=N, sock=...\nuse 'r1 ctl' to talk to it."`.
12. [ ] Add `internal/daemondisco/discovery.go` — `WriteDiscovery(pid, sockPath, port, token, version)` writes `~/.r1/daemon.json` atomically (tmp+rename) with mode 0600. `ReadDiscovery()` returns the same struct, validating the file mode (refuses to read a world-readable file — fail-closed).
13. [ ] Token vault: `internal/daemondisco/token.go` — `MintToken()` returns 32 random bytes hex-encoded via `crypto/rand`. Token regenerates on every daemon start (no persistence).

### Phase C — Listeners

14. [ ] Add `internal/server/ipc/listen_unix.go` (build tag `!windows`) — opens `$XDG_RUNTIME_DIR/r1/r1.sock` (fallback `$TMPDIR/r1-$UID/r1.sock`), chmods to 0600, parent dir 0700. On startup, `connect()` first; if successful, abort (stale-but-live owner). If `ECONNREFUSED`, `unlink` + `bind`.
15. [ ] Add `internal/server/ipc/listen_windows.go` (build tag `windows`) — opens named pipe `\\.\pipe\r1-<USERNAME>` with `SECURITY_ATTRIBUTES` granting only the current SID.
16. [ ] Linux peer-cred check: `internal/server/ipc/peercred_linux.go` reads `SO_PEERCRED`, asserts `uid == os.Getuid()`. macOS sibling uses `LOCAL_PEERCRED`.
17. [ ] Loopback HTTP+WS listener: `internal/server/serve.go` binds `127.0.0.1:0` (random ephemeral), captures the resolved port, writes it to discovery file.
18. [ ] HTTP middleware `requireBearer` — returns 401 if `Authorization: Bearer <token>` doesn't match. Sets `WWW-Authenticate: Bearer realm="r1"`.
19. [ ] HTTP middleware `requireLoopbackHost` — returns 403 if `r.Host` not in `{"127.0.0.1:<port>", "localhost:<port>"}`.
20. [ ] HTTP middleware `requireLoopbackOrigin` — for state-changing methods (POST/PUT/DELETE) and for WS upgrade, reject any non-loopback `Origin`. Allow `null` and missing for CLI HTTP clients.

### Phase D — Session hub + journal

21. [ ] `internal/server/sessionhub/sessionhub.go` — `SessionHub` with `sync.Map` of sessions, `Create(workdir, model, ...) (*Session, error)`, `Get(id)`, `Delete(id)`, `List()`. Validates `workdir` is absolute, exists, is a directory, is writable, and is not under `~/.r1/`.
22. [ ] `internal/server/sessionhub/session.go` — `Session` struct with `ID, SessionRoot, Workspace, journal *journal.Writer, ctx, cancel, started_at, model`. `Run(ctx)` method drives `agentloop.Loop` with a `WorkspaceFunc` and an `OnEvent` hook.
23. [ ] `internal/journal/journal.go` — `Writer` (append + fsync on terminal kinds), `Reader` (line-buffered, validate each `v: 1`), `Replay(handler)`, `Truncate(at_seq)` for crash recovery.
24. [ ] `Session.OnEvent` hook — append every bus event to journal **before** broadcasting to subscribers (consistency: subscribers can never see an event the journal lost).
25. [ ] Wire `Session.dispatchTool` to call `assertCwd(s.SessionRoot)` before invoking any tool runner. Document with a top-of-file comment block explaining why.
26. [ ] `~/.r1/sessions-index.json` — append-only on Create, mark-deleted on Delete. Format: `{sessions: [{id, workdir, started_at, journal_path}, ...]}`. Updated atomically (tmp+rename + fsync parent dir).
27. [ ] On daemon start, after Phase A passes, scan `sessions-index.json`, re-open each journal, replay into a fresh `*Session`, mark `state: paused-reattachable`. Emit `daemon.reloaded` to subscribers as soon as WS is up.

### Phase E — JSON-RPC dispatcher + WS handler

28. [ ] `internal/server/jsonrpc/dispatch.go` — JSON-RPC 2.0 envelope decode/encode. Handles request, response, error (per `desktop/IPC-CONTRACT.md` §3 codes), notification (no `id`), batch.
29. [ ] `internal/server/ws/handler.go` — `coder/websocket` upgrade. Subprotocol must include `r1.bearer`. Token is the second value in the comma-separated subprotocol list. 30s ping watchdog (`SetReadDeadline` + ping/pong handler that bumps the deadline).
30. [ ] Map JSON-RPC methods to `internal/desktopapi.Handler` for the existing desktop verbs (ledger.*, memory.*, cost.*, descent.*).
31. [ ] Implement `session.start / pause / resume / cancel / send / subscribe / unsubscribe`, `lanes.list / kill`, `cortex.notes`, `daemon.info / shutdown / reload_config`.
32. [ ] Subscribe semantics: per-subscription monotonic `seq`; server pushes `$/event` notifications with `{sub, seq, type, data}`. On `since_seq`, replay from journal **before** live deltas (do not interleave).
33. [ ] SSE bridge `/v1/sessions/:id/sse` — read-only, supports `Last-Event-ID` header, supports `?token=<t>` query param. Set `X-Accel-Buffering: no` for nginx-fronted setups.

### Phase F — Mounting agent-serve + queue-engine routes

34. [ ] Mount `internal/agentserve.Server.Handler()` under `/v1/agent/` with the same Bearer auth flowing through. Preserve `/api/...` aliases for one minor version with `Deprecation: true` header.
35. [ ] Mount `internal/daemon` queue/WAL endpoints under `/v1/queue/`. Same alias treatment.
36. [ ] CLI `r1 ctl <verb>` — connects to the unix socket / named pipe (no token needed thanks to peer-cred), translates to JSON-RPC, prints result. Sub-verbs: `discover`, `info`, `sessions list/get/start/kill`, `enqueue`, `status`, `workers`, `wal`, `tasks`, `pause`, `resume`, `shutdown`.

### Phase G — `--install` (opt-in)

37. [ ] `internal/serviceunit/service.go` — `kardianos/service` wrapper exposing `Install()`, `Uninstall()`, `Start()`, `Stop()`, `Status()`. Cross-platform unit content templated per OS.
38. [ ] `r1 serve --install` writes the unit, enables, starts. `--uninstall` reverses. `--status` reports.
39. [ ] On systemd-user Linux, document `loginctl enable-linger $USER` requirement for headless / SSH-only boxes.

### Phase H — `cmd/r1/serve_cmd.go` + alias wiring

40. [ ] New `cmd/r1/serve_cmd.go` — flags: `--addr`, `--no-tcp` (unix-only), `--no-unix` (TCP-only), `--token` (override generated), `--install`, `--uninstall`, `--status`, `--single-session`, `--enable-agent-routes`, `--enable-queue-routes`, `--config <path>`.
41. [ ] In `cmd/r1/main.go` switch, register `"serve"` → `serveCmd(args)`. Keep `"daemon"` → `daemonCmd(args)` and `"agent-serve"` → `agentServeCmd(args)` as alias paths (each prints a one-line deprecation hint to stderr and forwards args to `serveCmd` with the right flag prefix).
42. [ ] `cmd/r1/daemon_http.go` extended: when `--addr` is empty, read `~/.r1/daemon.json` for port + token; if missing, attempt to spawn `r1 serve` and retry with 2s timeout.

### Phase I — Tests + benchmarks

43. [ ] `internal/server/sessionhub/sessionhub_test.go` — concurrent Create/Get/Delete with race detector.
44. [ ] `internal/server/ws/ws_test.go` — round-trip subscribe / receive / reconnect with `since_seq`. Uses `httptest.NewServer` + `coder/websocket.Dial`.
45. [ ] `internal/server/auth_test.go` — Origin / Host / token rejection matrix.
46. [ ] `internal/journal/journal_test.go` — append, replay, truncate-at-last-valid-line on corruption, fsync semantics on terminal events.
47. [ ] `cmd/r1/serve_integration_test.go::TestMultiSession_RaceFree` — 8 concurrent sessions × 8 distinct workdirs, run `bash echo $PWD` in each, assert correct PWD for each session. Run with `go test -race -count=10`.
48. [ ] `cmd/r1/serve_integration_test.go::TestChdirSentinel_PanicsOnStrayChdir` — under build tag `chdirleak_test`, inject a goroutine that calls `os.Chdir`; assert the per-session sentinel panics with the expected message.
49. [ ] `cmd/r1/serve_integration_test.go::TestKillAndResume` — start daemon, create 3 sessions, exchange events, SIGTERM daemon, restart, verify journal replay reconstructs sessions, verify reconnecting WS clients see `daemon.reloaded` then resumed deltas with monotonic seq.
50. [ ] `cmd/r1/serve_integration_test.go::TestSingleInstance` — second `r1 serve` exits non-zero with "already running" message.
51. [ ] `tools/cmd/chdir-lint/lint_test.go` — fixture file with mixed annotated/unannotated calls; assert exact violation list.
52. [ ] `bench/r1d_serve_bench_test.go` — 50 sessions × 100 messages soak; assert p99 dispatch < 50ms, FD count stable, journal write throughput >= 5MB/s.

### Phase J — Documentation + decisions

53. [ ] Update `docs/decisions/index.md` with `D-D6` confirming `coder/websocket` choice + rationale.
54. [ ] Update `docs/architecture.md` with the new `r1 serve` topology diagram (cross-link the ASCII diagram in §4 of this spec).
55. [ ] Write `docs/r1-serve.md` operator guide: discovery, install, troubleshooting "daemon already running", token rotation, journal location.
