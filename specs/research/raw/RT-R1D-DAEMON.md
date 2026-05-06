# RT-R1D-DAEMON.md — `r1 serve` Long-Running Local Daemon

**Scope:** External research for a long-running `r1 serve` daemon that hosts multiple chat sessions (each bound to a working dir), is connectable from web/desktop UIs, persists across UI restarts, and runs on macOS/Linux/Windows.

**Date:** 2026-05-02. **Sibling specs:** `specs/r1-server.md` (existing read-only visualizer at port 3948), `specs/r1-server-ui-v2.md`, `specs/agent-serve-async.md`. The new `r1 serve` is a *control* daemon, not the existing read-only telemetry server — but they should coexist (and probably be merged later).

---

## 1. Cross-platform Local Daemon — Best Practices

### Auto-start vs on-demand

The dominant 2026 pattern for *developer* CLIs (watchman, tmux, mosh, the proposed Claude daemon) is **on-demand spawn**, not OS-managed always-on:

- The CLI tries to connect to the IPC endpoint (socket / named pipe).
- On `ECONNREFUSED` or missing socket, it forks the daemon and retries.
- Daemon stays alive until last session ends (tmux model) or until idle TTL (Cargo / rust-analyzer model).
- This avoids the launchd/systemd/Windows Service install nightmare on first run.

For users that *want* always-on (e.g. menubar app, web UI is the primary surface), expose `r1 serve --install` which calls into [`github.com/kardianos/service`](https://pkg.go.dev/github.com/kardianos/service). That library abstracts launchd, systemd (+ Upstart, SysV, OpenRC), and the Windows Service Control Manager behind one API. Ansxuman's 2025 walkthrough confirms it as the canonical choice. (`https://medium.com/@ansxuman/building-cross-platform-system-services-in-go-a-step-by-step-guide-5784f96098b4`)

The 2026 Go-daemon best practice is clear: write a normal executable; let the OS service manager handle daemonisation; never `setsid` / double-fork yourself; log to stdout/stderr and let the supervisor capture it ([copyprogramming 2026 guide](https://copyprogramming.com/howto/how-to-create-a-daemon-process-in-golang-duplicate)).

### Discovery

Three competing conventions:

| Approach | Used by | Pros | Cons |
|---|---|---|---|
| Fixed loopback port + `~/.r1/port` fallback | Supabase CLI (per-project ports), VS Code tunnel | Browser-native, trivial | Port collisions; firewalls prompt; cross-user leaking on multi-user macs |
| Unix socket + named pipe at well-known path | Watchman, Docker, tmux | OS-level perms, no port | Browsers can't speak unix sockets directly — needs a small loopback bridge |
| `get-sockname` style helper command | Watchman | Discoverable by other CLIs / IDE plugins | Still need a default location |

**Watchman's model** (the closest analog for a per-user dev daemon): the CLI command `watchman get-sockname` returns the socket path; clients prefer `$WATCHMAN_SOCK` env, otherwise spawn `watchman` to discover. One server **per user, not per repo**, watching N projects. ([Watchman socket-interface](https://facebook.github.io/watchman/docs/socket-interface), [get-sockname](https://facebook.github.io/watchman/docs/cmd/get-sockname))

**Path conventions that work cross-platform** ([adrg/xdg](https://github.com/adrg/xdg)):
- Linux: `$XDG_RUNTIME_DIR/r1/r1.sock` (typically `/run/user/$UID/r1/...`); fallback `$TMPDIR/r1-$UID/r1.sock`.
- macOS: `$TMPDIR/r1-$UID/r1.sock` (Apple's recommended writable per-user runtime dir).
- Windows: named pipe at `\\.\pipe\r1-<USERNAME>`.
- Discovery file: `~/.r1/daemon.json` containing `{pid, sock_path, port, token, version}` written atomically (tmp+rename), readable by UIs.

Watchman is hit by the macOS 104-byte sun_path limit when usernames are long ([Homebrew issue 181152](https://github.com/Homebrew/homebrew-core/issues/181152)) — `r1` should keep socket basenames short and offer a `--socket` override.

### Single-instance enforcement

Three workable mechanisms; the consensus in Go land is to **combine** two of them:

1. **`bind()` on the unix socket / named pipe is exclusive** — second process gets `EADDRINUSE`. (Wayne Marsh's pattern, [medium article](https://wmdev.medium.com/enforcing-a-single-instance-of-a-go-application-using-domain-sockets-1f9d4eb97279)). Caveat: stale socket files survive crashes on Linux — you must `connect()` first, and only `unlink()`+`bind()` if connect fails. The "@" abstract namespace fixes this on Linux but is unavailable on macOS/Windows.
2. **PID-bearing lock file** with `flock`/`LockFileEx` — `github.com/gofrs/flock` is portable and handles Windows. ([gofrs/flock](https://github.com/gofrs/flock), [nightlyone/lockfile](https://pkg.go.dev/github.com/nightlyone/lockfile)). Lets a stale daemon be detected by re-grabbing the lock.
3. Loopback port `bind()` is naturally exclusive; combine with a TXT record on the lock file to publish the chosen port.

**Recommended:** flock on `~/.r1/daemon.lock` (cross-platform, survives crashes) + write `~/.r1/daemon.json` after successful socket bind.

---

## 2. Working Examples — What to Copy

### Watchman (Facebook)
- One persistent process **per user**, not per repo. Watches N project trees within itself.
- Spawned on-demand by CLI; survives the spawning shell.
- IPC: BSER (binary) or JSON over unix socket / named pipe.
- Discovery: `watchman get-sockname` or `$WATCHMAN_SOCK`.
- Authentication: **filesystem permissions on the socket** — only the owning user can connect. No tokens.
- This is the cleanest analog for `r1 serve`: per-user singleton hosting many "watched roots" = many "bound workdirs".

### tmux server
- Single server holds many sessions. `tmux attach`/`detach` decouples client lifetime from session lifetime.
- Default socket `/tmp/tmux-$UID/default`; `tmux -L name` selects an alternate server. ([tao-of-tmux server chapter](https://tao-of-tmux.readthedocs.io/en/latest/manuscript/04-server.html), [tmux(1)](https://man7.org/linux/man-pages/man1/tmux.1.html))
- Server auto-exits when last session closes — great hygiene.
- **Lesson:** model `r1` sessions exactly like tmux: one server, N detachable sessions, each session = one chat × one workdir. UIs are clients that attach/detach.

### mosh
- Not really a "local daemon" — but the **State-Synchronization Protocol** (UDP, sequence-numbered, AES-OCB) is the gold standard for resumable streaming sessions. Single packet from a new client IP "roams" the connection. ([mosh paper](https://mosh.org/mosh-paper-draft.pdf), [DeepWiki](https://deepwiki.com/mobile-shell/mosh))
- For r1, the lesson is: **assign a session ID + monotonic sequence number to every event**; UI sends `Last-Event-ID` on reconnect; server replays from journal.

### devbox (Jetify)
- `devbox.json` per project; `devbox shell` enters per-project Nix env. There is no central daemon — each project is self-contained.
- `devbox services` runs ancillary services (postgres, redis) per-project via process-compose ([devbox 0.2.0 blog](https://www.jetify.com/blog/devbox-0-2-0)).
- **Lesson for r1:** keep workdir state inside `<repo>/.r1/` (already the pattern); the daemon is just an orchestrator that reads/writes those per-repo dirs.

### VS Code CLI tunnel
- `code tunnel` runs **VS Code Server** as a long-running process on the local box, then opens a Microsoft-relayed tunnel so a browser/Desktop VS Code can attach. ([VS Code remote tunnels](https://code.visualstudio.com/docs/remote/tunnels), [VS Code Server](https://code.visualstudio.com/docs/remote/vscode-server))
- Auth is GitHub/Microsoft OAuth (because it goes through MS infra); the local server doesn't trust the network.
- **Lesson:** if r1 ever wants UI-from-anywhere, the model is "local daemon + opt-in tunnel binary", not "expose port on LAN".

### Supabase CLI
- Each project = its own Docker stack with hand-bumped ports (55321, 55322, …). Multi-project = literally multiple stacks. ([Supabase discussion 5968](https://github.com/orgs/supabase/discussions/5968), [Local development guide](https://supabase.com/docs/guides/local-development/cli/getting-started))
- This is the **wrong** model for r1 — Supabase has it because each project needs an isolated Postgres. r1 sessions are cheap (goroutines + a workdir); we want a single daemon hosting many sessions.

### Cursor / Devin (closest commercial analogs)
- **Cursor Background Agents** are explicitly cloud-only — fresh VM per task, repo cloned over GitHub/GitLab. There is no local daemon. ([Cursor docs](https://cursor.com/docs/background-agent), [Futurum](https://futurumgroup.com/insights/cursor-3-2-reframes-the-ide-as-an-agent-execution-runtime/))
- **Cursor Remote Access** keeps the local IDE process running and exposes it through Cursor's relay (similar to VS Code tunnel).
- **Devin for Terminal** (March 2026) is a CLI agent that "keeps working when you close your laptop" — implemented as a local long-running process plus a hand-off path to the cloud agent. ([ChatGate writeup](https://chatgate.ai/post/devin-for-terminal))
- **Claude Code "Chyros" leak** describes an always-on background daemon ([MindStudio writeup](https://www.mindstudio.ai/blog/what-is-claude-code-chyros-background-daemon)); the Anthropic SDK feature request issue #33 proposes daemon-mode with three options: TCP/HTTP, unix socket, or in-process pool. **No decision recorded by maintainers.** ([anthropics/claude-agent-sdk-typescript#33](https://github.com/anthropics/claude-agent-sdk-typescript/issues/33))
- **Claude Code Remote Control** (Feb 2026 preview) bridges a local terminal Claude session to claude.ai/code and the mobile apps; the local terminal stays running and prints a session URL/QR. ([guide](https://claudefa.st/blog/guide/development/remote-control-guide))
- **VS Code Agent Sessions view** (Feb 2026) consolidates local, background, and cloud agent sessions in one IDE pane ([VS Code blog](https://code.visualstudio.com/blogs/2026/02/05/multi-agent-development)).

---

## 3. Multi-session model — goroutine vs subprocess

| | Goroutine per session | Subprocess per session |
|---|---|---|
| Startup cost | µs | 50–200 ms (Go binary cold start) |
| Memory | shared heap, ~few MB/session | 30–80 MB/session (Go runtime + APIClient state) |
| Crash isolation | one panic kills daemon (must `recover()` everywhere) | one crash = one dead session |
| OOM blast radius | whole daemon dies | only one session dies |
| Hot upgrade | impossible without restart | sessions can outlive parent on FD-pass |
| Workdir binding | policy enforcement only (chdir is process-global) | natural via `cmd.Dir` |
| Debuggability | single binary attach | each PID separately |

**`os.Chdir` is per-process, not per-goroutine** — Go issue [27658](https://github.com/golang/go/issues/27658) confirms it isn't reentrant; goroutines share working dir. This is the **dealbreaker** for naive goroutine-per-session if any code paths rely on the cwd.

The r1 codebase already standardised on **`cmd.Dir`** for worktree binding (CLAUDE.md design decision #1: "cmd.Dir for worktree cwd (Claude Code has no `--cd` flag)") — so all the engine/runner paths already pass workdir explicitly. That makes goroutine-per-session feasible *if* we audit for stray `os.Open("./...")` etc.

**Recommendation:** **goroutine per session by default**, with a `--isolate` flag that forks a child r1 binary in `--session-worker` mode for paranoid users. This matches tmux (single process, many sessions) and is the cheapest path. The bridge code (`bridge/`, `agentloop/`, `engine/`) is already structured around explicit cwd plumbing, so the audit cost is bounded. Cursor/Devin's commercial precedent is that they push *cloud* isolation (whole VM) when they want isolation — locally, in-process is fine.

---

## 4. Workdir binding

Hard isolation options ranked by cost:

1. **Policy enforcement at tool layer** (current r1 pattern) — cheapest, already done. `engine/`, `worktree/`, `verify/` all take `repoRoot string` and pass `cmd.Dir`. Tools like `str_replace`, `bash` need to validate paths stay within `repoRoot` (the `fileutil/` package does some of this).
2. **chdir per subprocess** — `exec.Cmd.Dir` is the right knob. Already used.
3. **chroot / pivot_root** — Linux-only, requires root, breaks dev tooling. Skip.
4. **Container per session** — Cursor's choice for cloud, way too heavy for local.

**Recommendation:** stay with policy enforcement + `cmd.Dir`. Add a `concern.SessionRoot` field on every tool invocation, and have `tools/` and `bash` runners reject absolute paths that aren't `filepath.HasPrefix(absRepo)`. There's already `verify.CheckProtectedFiles` and `verify.CheckScope`; extend rather than reinvent.

The Hermes-agent issue ([NousResearch/hermes-agent#4669](https://github.com/NousResearch/hermes-agent/issues/4669)) is a good cautionary tale: per-call workdir overrides leaked through Docker exec because the policy check ran *before* the override was applied. **Validate at the lowest layer (the runner) not the highest.**

---

## 5. Auth for local daemon

Layered model in priority order:

1. **Filesystem permissions on the IPC endpoint** — primary defence.
   - Unix socket created with `0600` (or `0700` on parent dir) so only the owning user can connect. Watchman, Docker (default), MySQL all rely on this. ([MySQL socket peer-credential](https://dev.mysql.com/doc/refman/8.0/en/socket-pluggable-authentication.html), [Docker daemon protect-access](https://docs.docker.com/engine/security/protect-access/))
   - Windows named pipe ACL set to current SID only via `\\.\pipe\r1-<USERNAME>` plus a SECURITY_ATTRIBUTES with `WriteDacl`.
   - On Linux, use `SO_PEERCRED` to verify `uid == getuid()` of the connecting client as defence-in-depth.
2. **Bearer token in `~/.r1/daemon.json` (mode 0600)** for the loopback HTTP/WebSocket port — required because **browsers can't speak unix sockets**, and a localhost port is reachable by any local user / any browser tab on `localhost`.
   - 256-bit random token, regenerated on every daemon start.
   - Sent as `Authorization: Bearer <token>` header (WS) or `?token=...` query string (SSE — only because EventSource can't set headers).
   - **Origin pinning** + `Sec-Fetch-Site: same-origin` enforcement to defeat DNS-rebinding from random web pages. (The OpenClaw gateway docs flag this exact failure mode: "Non-loopback binds expand the attack surface" — [OpenClaw security](https://docs.openclaw.ai/gateway/security).)
3. **OS keychain (`zalando/go-keyring`)** for any persistent secrets the daemon caches (provider API keys). Don't put API keys in `daemon.json`.
4. **Skip auth on unix-socket connections** because peer-credential check already proves identity. Authenticate only the TCP/loopback path. This matches Docker's default and watchman's design.

**Anti-pattern to avoid:** "trust everything on 127.0.0.1". `localhost` is *not* a security boundary — any browser tab, any local user on shared machines can reach it. The token + origin-pin combo is mandatory if the loopback port is exposed.

---

## 6. WebSocket vs SSE vs JSON-RPC

For UIs streaming "lane events" (token-by-token model output, tool-call events, progress, status), here's the matrix from the 2026 surveys ([websocket.org comparisons](https://websocket.org/comparisons/sse/), [softwaremill](https://softwaremill.com/sse-vs-websockets-comparing-real-time-communication-protocols/), [oneuptime 2026](https://oneuptime.com/blog/post/2026-01-27-sse-vs-websockets/view)):

| | SSE | WebSocket | JSON-RPC over WS |
|---|---|---|---|
| Direction | server → client only | full duplex | full duplex with request/response semantics |
| Reconnect | **automatic** via `Last-Event-ID` | manual code required | manual + extra correlation IDs |
| Browser API | `EventSource` (cannot set headers) | `WebSocket` (can set token via subprotocol) | same as WS |
| HTTP/2 multiplex | yes | no (separate TCP) | no |
| Backpressure | poor | TCP-level | TCP-level + per-method |
| Auth | query-string token only | header / subprotocol token | header / subprotocol token |
| Best for | one-way feeds, dashboards | interactive REPL, long-lived control | structured RPC + events on one channel |

**Recommendation:** **JSON-RPC 2.0 over WebSocket** for the UI ↔ daemon channel.

- The UI has to *send* messages (chat input, approve/deny, cancel) and *receive* streams — bidirectional is a hard requirement, ruling out plain SSE.
- JSON-RPC 2.0 gives free request/response correlation (`id` field), notifications (no `id`), and structured errors. `microsoft/vs-streamjsonrpc` and `elpheria/rpc-websockets` are mature reference implementations ([streamjsonrpc](https://github.com/microsoft/vs-streamjsonrpc), [elpheria deepwiki](https://deepwiki.com/elpheria/rpc-websockets/3.1-json-rpc-2.0-protocol)).
- For server → client streams (token deltas, lane events), use the **subscription pattern** (Solana RPC convention): `subscribe → returns subId → server pushes notifications keyed by subId → client unsubscribe`. ([Solana RPC websocket](https://solana.com/docs/rpc/websocket))
- Use **Mosh-style monotonic sequence numbers per subscription** so reconnects can request `replay_from=N` against the on-disk event journal (`bus/wal.go` already exists in the codebase — design decision #28 says v2 events go through the bus).

Provide a thin SSE bridge at `/v1/sse?session=...&token=...` for read-only embeddings (status badges in IDE plugins, dashboards) — much simpler client code, gets `Last-Event-ID` reconnect for free.

---

## 7. Hot-reload safety on binary upgrade

Three options ranked by realism:

1. **Restart-required, sessions resumed from journal** (recommended).
   - Each session's full state (history, tool-call results, event log) lives in `<repo>/.r1/sessions/<id>/journal.ndjson`.
   - `r1 serve` shutdown writes a final `paused` marker; on restart, sessions load from journal.
   - This is the tmux-continuum / mosh pattern, and the only one that works when the *struct shapes* of in-memory state change between releases.
2. **Cloudflare-style `tableflip` graceful upgrade** ([Cloudflare blog](https://blog.cloudflare.com/graceful-upgrades-in-go/), [fvbock/endless](https://github.com/fvbock/endless), [facebookarchive/grace](https://github.com/facebookarchive/grace)) — fork+exec the new binary, pass listener FDs via env. Old daemon drains, new daemon takes over. Listener survives, but **in-process session state does not**. Fine for HTTP servers; useless for stateful agent sessions unless you also serialise state.
3. **True hot reload** — not possible in Go without plugin infrastructure ([Scalingo guide](https://scalingo.com/blog/graceful-server-restart-with-go) explicitly: "Real hot reloading is not possible with Golang yet"). Skip.

**Recommendation:** restart-required, but make restart *invisible to the user* by:
- Persisting full session state to `journal.ndjson` (already the model in `bus/`, `ledger/`, `session.SessionStore` — design decisions #19, #28).
- On `r1 serve` start, replay journals into memory, mark sessions as `paused-reattachable`.
- UI reconnect via WS picks up `Last-Event-ID`; daemon emits a single `daemon.reloaded` event then resumes streaming.
- Add a version handshake: if `client.protocol_version != server.protocol_version`, UI shows "r1 upgraded; click to refresh". Don't try to live-migrate across breaking schema changes.

A small `r1 doctor` command should detect "daemon is older than installed binary" and prompt the user to restart it.

---

## Citations

1. [Building Cross-Platform System Services in Go (Anshuman, 2025)](https://medium.com/@ansxuman/building-cross-platform-system-services-in-go-a-step-by-step-guide-5784f96098b4)
2. [kardianos/service Go package](https://github.com/kardianos/service)
3. [Creating Daemon Processes in Go: 2026 Best Practices](https://copyprogramming.com/howto/how-to-create-a-daemon-process-in-golang-duplicate)
4. [Watchman socket-interface docs](https://facebook.github.io/watchman/docs/socket-interface)
5. [Watchman get-sockname](https://facebook.github.io/watchman/docs/cmd/get-sockname)
6. [Homebrew issue: watchman socket path too long](https://github.com/Homebrew/homebrew-core/issues/181152)
7. [tao-of-tmux: Server chapter](https://tao-of-tmux.readthedocs.io/en/latest/manuscript/04-server.html)
8. [tmux(1) man page](https://man7.org/linux/man-pages/man1/tmux.1.html)
9. [VS Code Remote Tunnels docs](https://code.visualstudio.com/docs/remote/tunnels)
10. [VS Code Server docs](https://code.visualstudio.com/docs/remote/vscode-server)
11. [Mosh paper](https://mosh.org/mosh-paper-draft.pdf)
12. [mobile-shell/mosh on DeepWiki](https://deepwiki.com/mobile-shell/mosh)
13. [Devbox 0.2.0 release blog](https://www.jetify.com/blog/devbox-0-2-0)
14. [Supabase multi-project discussion](https://github.com/orgs/supabase/discussions/5968)
15. [Supabase local-development guide](https://supabase.com/docs/guides/local-development/cli/getting-started)
16. [Cursor Background Agents docs](https://cursor.com/docs/background-agent)
17. [Cursor 3.2 reframes the IDE as agent runtime — Futurum](https://futurumgroup.com/insights/cursor-3-2-reframes-the-ide-as-an-agent-execution-runtime/)
18. [Devin for Terminal — ChatGate](https://chatgate.ai/post/devin-for-terminal)
19. [Claude Code Chyros background daemon — MindStudio](https://www.mindstudio.ai/blog/what-is-claude-code-chyros-background-daemon)
20. [anthropics/claude-agent-sdk-typescript#33: Daemon Mode for Hot Process Reuse](https://github.com/anthropics/claude-agent-sdk-typescript/issues/33)
21. [Claude Code Remote Control guide](https://claudefa.st/blog/guide/development/remote-control-guide)
22. [VS Code Agent Sessions blog (Feb 2026)](https://code.visualstudio.com/blogs/2026/02/05/multi-agent-development)
23. [gofrs/flock](https://github.com/gofrs/flock)
24. [nightlyone/lockfile](https://pkg.go.dev/github.com/nightlyone/lockfile)
25. [Single-instance Go daemon via unix sockets — Wayne Marsh](https://wmdev.medium.com/enforcing-a-single-instance-of-a-go-application-using-domain-sockets-1f9d4eb97279)
26. [adrg/xdg](https://github.com/adrg/xdg)
27. [Atuin issue: socket should live in $XDG_RUNTIME_DIR](https://github.com/atuinsh/atuin/issues/2153)
28. [Cloudflare: Graceful upgrades in Go](https://blog.cloudflare.com/graceful-upgrades-in-go/)
29. [Teleport: Restarting a Go Program Without Downtime](https://goteleport.com/blog/golang-ssh-bastion-graceful-restarts/)
30. [fvbock/endless](https://github.com/fvbock/endless)
31. [facebookarchive/grace](https://github.com/facebookarchive/grace)
32. [Scalingo: Graceful server restart with Go](https://scalingo.com/blog/graceful-server-restart-with-go)
33. [Docker: Protect the Docker daemon socket](https://docs.docker.com/engine/security/protect-access/)
34. [MySQL socket peer-credential auth](https://dev.mysql.com/doc/refman/8.0/en/socket-pluggable-authentication.html)
35. [OpenClaw gateway security docs](https://docs.openclaw.ai/gateway/security)
36. [Go issue 27658: os.Chdir is not reentrant](https://github.com/golang/go/issues/27658)
37. [Go issue 58802: Chdir effect across goroutines on Plan 9](https://github.com/golang/go/issues/58802)
38. [WebSocket vs SSE — websocket.org](https://websocket.org/comparisons/sse/)
39. [SSE vs WebSockets — SoftwareMill](https://softwaremill.com/sse-vs-websockets-comparing-real-time-communication-protocols/)
40. [SSE vs WebSockets — OneUptime 2026](https://oneuptime.com/blog/post/2026-01-27-sse-vs-websockets/view)
41. [Solana RPC WebSocket methods](https://solana.com/docs/rpc/websocket)
42. [microsoft/vs-streamjsonrpc](https://github.com/microsoft/vs-streamjsonrpc)
43. [elpheria/rpc-websockets — JSON-RPC 2.0 protocol](https://deepwiki.com/elpheria/rpc-websockets/3.1-json-rpc-2.0-protocol)
44. [james-barrow/golang-ipc — cross-platform IPC](https://github.com/james-barrow/golang-ipc)
45. [Hermes-agent: workdir override leak](https://github.com/NousResearch/hermes-agent/issues/4669)
