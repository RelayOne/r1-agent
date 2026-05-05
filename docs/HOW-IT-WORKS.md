# How It Works

A vivid walkthrough for someone who wants to understand r1 deeply — both as an operator and as a curious engineer.

## Two narratives

r1 is two products glued together:

1. **The local agent runtime** — a CLI + daemon + multi-surface UI you run on your own machine to do real coding work.
2. **The hosted SaaS** — `*.r1.run` — public docs, license/telemetry coordination, and binary distribution. Operators install r1 from the SaaS; the agent itself runs locally.

This doc walks you through both, end-to-end, in the order a new user would encounter them.

---

## User journey — from zero to first shipped task

### Step 1. Discover the project

You land on `https://platform.r1.run/`. The site is a static docs renderer — Go service `r1-docs` running on Cloud Run, serving an embedded snapshot of `docs/*.md` rendered to HTML by a small dependency-free markdown engine (headings, code blocks, tables, links, bold, italic, inline code; CSP locked, no JS execution beyond what Cloud Run injects).

You see the README's quick-start block. You copy:

```bash
curl -fsSL https://downloads.r1.run/prod/r1-linux-amd64 -o r1
chmod +x r1 && sudo mv r1 /usr/local/bin/
```

### Step 2. Install

`downloads.r1.run` is `r1-downloads-cdn` on Cloud Run. It's not a real CDN — it's a thin Go reverse-proxy backed by `gs://relayone-488319-r1-releases/{prod,staging,dev}/<asset>`. The Cloud Run service account holds `roles/storage.objectViewer` on the bucket; the proxy streams the requested object back to your `curl` with caching headers. The same channel-namespacing model that powers `homebrew tap` or `apt repository` works here without operating either of those.

You verify the install worked:

```bash
r1 --version    # prints e.g. 0.18.0+bf49ec45
```

### Step 3. Start the daemon

```bash
r1 serve
```

What just happened, in technical detail:

1. **Single-instance enforcement** — `internal/daemonlock/lock.go` acquires a `gofrs/flock` advisory lock on `~/.r1/daemon.lock`. If a second `r1 serve` is already running, this one exits non-zero with a clear error.
2. **Discovery file** — `internal/daemondisco/discovery.go` writes `~/.r1/daemon.json` mode 0600, atomically (`writeFile` + `rename`). The file holds the daemon's loopback HTTP+WS port and a 32-byte hex token (`crypto/rand`) regenerated on every start.
3. **Listeners** — three at once:
   - **CLI IPC**: unix socket at `$XDG_RUNTIME_DIR/r1/r1.sock` mode 0600 (Linux/macOS) or named pipe with current-SID + LocalSystem SDDL (Windows). Peer-cred check on the socket; the named pipe relies on Windows ACL.
   - **Loopback HTTP+WS**: bound to `127.0.0.1:0` (port captured at start). Origin pinning + Host pinning + WS subprotocol-token auth. WebSocket subprotocol is `r1.lanes.v1`, with the token offered as a second protocol entry (Sec-WebSocket-Protocol contains both).
   - **Per-OS service unit** (only if `r1 serve --install` was used): launchd plist on macOS / systemd-user unit on Linux / Windows SCM on Windows, all via `kardianos/service`.
4. **Session hub** — `internal/server/sessionhub/sessionhub.go` initializes the in-memory + on-disk session catalog. Sessions index lives at `~/.r1/sessions-index.json` (atomic + fsync). Per-session journals live at `<workdir>/.r1/sessions/<id>/journal.ndjson`.
5. **Replay** — if a previous daemon process exited cleanly or crashed, the new daemon replays each session's journal and emits `daemon.reloaded` to any reconnecting clients. This is what makes daemon restarts invisible to the user — your in-flight chat keeps going.

### Step 4. Open a UI

Three options; same daemon backs all three.

**Web (browser)**
```bash
open http://127.0.0.1:7777/   # port shown by `r1 serve`
```
The browser loads `internal/server/static/dist/index.html` (embedded by `//go:embed static`). The bundle is the React 18 + Vite 6 + Tailwind 3 + shadcn web app from `web/`. On first connect:
- It calls `r1d.mintWsTicket()` to get a short-lived WS auth token.
- `ResilientSocket` from `web/src/lib/api/ws.ts` opens `ws://127.0.0.1:7777/v1/sessions/<id>/ws` with `Sec-WebSocket-Protocol: r1.lanes.v1, <token>`.
- `useDaemonSocket` routes incoming envelopes into the per-daemon zustand store (`web/src/lib/store/daemonStore.ts`).
- The store's `EnvelopeCoalescer` buffers high-frequency `lane.delta` events and flushes them once per animation frame — clamping to ~10 Hz visible rerender even under a 200 Hz event firehose.
- The Cursor-3-Glass `<ThreeColumnShell>` renders. Left: `<SessionList>`. Center: `<ChatPane>` switching between `<MessageLog>+<Composer>` and `<TileGrid>` based on pinned lane count. Right: `<LanesSidebar>` with `<LaneRow>` per lane.

**TUI (terminal)**
```bash
r1 chat --interactive
```
Bubble Tea v2 + bubblelayout + lipgloss v2. Three panes (dashboard / focus / detail). Lanes panel is adaptive: columns when terminal width >= n*32, vertical stack otherwise. 250 ms coalesce on `chan laneTickMsg`; per-lane render-string cache; diff-only repaint. Keys `1`–`9` jump-to-lane, `tab` cycles, `enter` focuses, `x` kills, `K` kills-all, `?` opens help.

**Desktop (Tauri 2)**
```bash
r1 desktop      # if installed; or open the .dmg / .msi / .deb / .rpm bundle
```
Discovery-or-spawn:
1. Probe `~/.r1/daemon.json`. If a healthy daemon answers `daemon.info` within 500 ms → use it.
2. Otherwise spawn the bundled `r1` binary as a Tauri sidecar via `ShellExt::sidecar`. Sidecar binaries live under `desktop/src-tauri/binaries/` per-platform.
3. Webview wraps the same React components from `packages/web-components/`. Lane events arrive via per-session `tauri::ipc::Channel<LaneEvent>` at 10 Hz (sidesteps Tauri's global event bus for high-frequency streams).

### Step 5. Run a task

You type "Add a request ID middleware" and press Cmd+Enter (or Ctrl+Enter, or click Send).

What happens:

1. **Composer disables** — `<Composer>` sets `streaming=true`. Send button swaps to the destructive `<StopButton>`. The textarea + Send disable. The hint flips to "Streaming a response — use the Stop button to interrupt."
2. **Daemon receives `session.send`** — JSON-RPC call over the WS. The session pushes the user message into its history, schedules a turn.
3. **Cortex pre-warm** — 4 minutes before the round, `internal/cortex/prewarm.go` fires a `max_tokens=1` cache request to keep the prompt-cache breakpoint warm.
4. **Round starts** — `internal/cortex/Workspace.Run()` kicks off main thread + 5 Lobes in parallel.
5. **Main thread plans** — Claude (or your provider chain's first available model) generates a plan. Each step becomes a task in the mission graph.
6. **Lobes work in parallel**:
   - `MemoryRecallLobe` searches your memory + wisdom store; surfaces 3 hits as `info` Notes.
   - `WALKeeperLobe` drains every event into the durable bus WAL.
   - `RuleCheckLobe` watches for supervisor-rule fires.
   - `PlanUpdateLobe` watches for plan deltas; auto-applies edits, queues adds and removes for confirmation.
   - `ClarifyingQLobe` notices ambiguity; drafts up to 3 clarifying questions; surfaces at idle.
   - `MemoryCuratorLobe` runs every 5 turns; extracts "should-remember" facts.
7. **Tools fire** — every tool call routes through `internal/hooks/` PreToolUse + PostToolUse guards. Honeypot gate aborts on canary leaks, markdown-image exfil, destructive-without-consent shell. Each tool call becomes its own lane.
8. **Verification descent** — when the model thinks it's done: `internal/verify/` runs build + test + vet; `internal/critic/` runs the adversarial pre-commit critic; `internal/convergence/` runs adversarial self-audit.
9. **Cross-model review** — if Claude implemented, Codex reviews; if Codex implemented, Claude reviews. The reviewer reads diff + AC + tool-call log.
10. **Anti-truncation gate** — `internal/agentloop/antitrunc.go` composes BEFORE every other end-turn hook:
    - Phrase scan: did the model emit any of 14 cataloged phrases? If yes → refuse `end_turn`, append a forcing-continuation system message, loop.
    - Scope check: are there unchecked items in the active plan or any in-progress spec? If yes → refuse.
    - Commit body check: does the most recent commit body claim completion when the corresponding spec/task isn't actually checked off? If yes → refuse.
11. **Persist** — every event in the WAL, every node in the ledger, every cost tick journaled. Lane events stream to all subscribers (TUI/web/desktop/MCP) with monotonic per-session `seq`.
12. **UI updates** — `<MessageLog>` renders new bubbles via `react-virtual`. The currently-streaming bubble has `aria-live="polite"`. `<ToolCard>` auto-collapses once a tool reaches `output-available`. `<PlanCard>` updates live as PlanUpdateLobe publishes deltas. `<DiffCard>` shows the consolidated per-lane diff. `<StatusBar>` ticks cost USD + lane counts + WS latency.

### Step 6. Mid-stream interrupt

You type "wait, also use the X-Request-ID header if it's already there" while the model is still streaming.

The Router fires:
1. Haiku 4.5 call with 4 tools — `interrupt`, `steer`, `queue_mission`, `just_chat`.
2. Router decides "steer" (your message refines the in-flight task; not a new task).
3. **Drop-partial protocol**: cancel the turn context, drain SSE, **never persist the partial assistant message**, append a synthetic user message describing the interrupt, restart the turn with the augmented user input.
4. The 30-second ping watchdog ensures a stuck stream doesn't hold the lane open.

### Step 7. Stop streaming

You click `<StopButton>` (or press Esc — global keybinding).

`onInterrupt(dropPartial=true)` fires `r1d.session.interrupt(sessionId, {drop_partial: true})`. Same drop-partial protocol; partial assistant turn vanishes, composer re-enables.

### Step 8. Pin a lane

`<LaneRow>`'s pin button toggles `aria-pressed`, calls `pinLane(sessionId, laneId)` on the store, and `<ChatPane>` flips `data-tile-mode="true"`. `<TileGrid>` mounts. With one pin, it's a 1×1; pin a second, 1×2; third, 1×3; fourth, 2×2.

You drag-reorder by gripping a tile header. `aria-grabbed`/`aria-dropeffect` flip during the drag. Cmd+Shift+←/→ moves the focused tile keyboard-only (WCAG 2.1.1). Double-click → `onFocusLane(laneId)` → routes to `/d/<daemon>/sessions/<sid>/lanes/<lid>` (deep-linkable).

### Step 9. Verify the work

```bash
r1 antitrunc verify -n 20
```

Reads the last 20 commits via `git log`. For each, parses any "spec N done" or "TASK-N done" claim and cross-references the active plan + matching spec checklist. Each commit gets classified Verified / Unverified / Lying. Exit non-zero if any Lying. CI runs this gate; the post-commit git hook also runs it locally.

---

## Technical overview — what each file does

| User action | Code path |
|---|---|
| `r1 serve` | `cmd/r1/serve_cmd.go` → `internal/daemonlock/Lock` → `internal/daemondisco/Write` → `internal/server/{ipc,ws,sse}/Listen` → `internal/server/sessionhub/Run` |
| Web bundle served at `/` | `internal/server/embed.go` reads `static/dist/index.html` directly + falls back to `static/index.html` if dist is absent |
| Web bundle built | `web/scripts/verify-build-output.mjs` (in `npm run build`) verifies dist landed at `internal/server/static/dist/` |
| WS connect from browser | `web/src/lib/api/ws.ts` `ResilientSocket` → exponential backoff 250 ms→8 s, jitter ±20% → 10-attempt hard cap → `<ConnectionLostBanner>` |
| WS auth | `web/src/lib/api/auth.ts` `mintWsTicket` → 90 s skew-based refresh → token offered as 2nd `Sec-WebSocket-Protocol` entry |
| Mid-stream interrupt | `internal/cortex/interrupt.go` (drop-partial pattern) + agentloop cancel context + 30 s ping watchdog |
| Cortex pre-warm | `internal/cortex/prewarm.go` — `max_tokens=1` warming request every 4 min |
| Anti-truncation gate | `internal/antitrunc/{phrases,gate,scopecheck}.go` + `internal/agentloop/antitrunc.go` |
| Lane FSM | `internal/streamjson/lane.go` — single-writer goroutine allocates monotonic seq |
| Mission ledger | `internal/ledger/` — content-addressed (`sha256:<hex>`); 22 node types |
| Cross-model review | `internal/model/CrossModelReviewer` |
| Anti-truncation CLI | `cmd/r1/antitrunc_cmd.go` (verify, list-patterns, tail) |
| MCP catalog | `internal/mcp/r1_server.go` + `r1_server_catalog.go` — 38 tools / 10 categories |

---

## Hosted SaaS — how `r1.run` actually works

### `platform.r1.run` (r1-docs)

A 350-line Go service. Three things:

1. **Embeds `docs/*.md` at compile time** via `//go:embed all:docs/*`. Container ships docs frozen at build time; redeploying is the publish step.
2. **Renders markdown to HTML** with a dependency-free pure-Go renderer covering headings, code fences (with `data-lang`), inline code, lists, tables, links, **bold**, *italic*, horizontal rules. We deliberately avoid heavy markdown libs to keep the binary tiny (~4 MB) and start-up sub-100 ms.
3. **CSP-lock the response** via `<meta http-equiv="Content-Security-Policy">` in the HTML template.

Routes: `/`, `/<doc>.html`, `/raw/<doc>.md`, `/livez`.

### `api.r1.run` (r1-coord-api)

A 150-line Go service. License-verify and telemetry-opt-in stubs are deliberate — they answer 200 with well-formed JSON so the DNS chain can be smoke-tested before the real auth integration lands.

Routes: `/`, `/livez` `/readyz` `/v1/version`, `POST /v1/license/verify`, `POST /v1/telemetry/opt-in`.

The container binds `r1-{env}-shared-DATABASE_URL` from Secret Manager (placeholder; operator action pending).

### `downloads.r1.run` (r1-downloads-cdn)

A 200-line Go service using the Cloud Storage Go SDK to:

1. List objects under `gs://relayone-488319-r1-releases/{channel}/` for the index.
2. Stream a single object's bytes back to the caller.
3. Return content-hash metadata.

Cloud Run service account holds `roles/storage.objectViewer` on the bucket. Channels constrained to `prod`/`staging`/`dev` at the handler layer.

### Auto-deploy

`services/cloudbuild-deploy.yaml` runs on push to `main` (prod), `staging` (staging), or `dev` (dev). Three image builds in parallel → three pushes → three Cloud Run deploys (with env-specific secret bindings) → `/livez` smoke check (5 retries × 2 s).

---

## Key technical decisions

### Why a Watchman-pattern daemon
Spec 5 D-D1: spawn-on-demand means zero idle resource cost on machines not running r1. Single-instance via `gofrs/flock` ensures two installs don't race over `~/.r1/`. `r1 serve --install` is the explicit opt-in for always-on.

### Why drop-partial interrupt
Spec 1 D-C4: never persist partial assistant messages on cancellation. A persisted partial-then-resumed conversation is non-deterministic. Drop-partial means each turn is atomic — completes fully or never happened.

### Why pre-warm the cache
Spec 1 D-C5: Anthropic prompt-cache TTL is 5 minutes. The pre-warm pump fires `max_tokens=1` every 4 min during a session, keeping the cache breakpoint warm at ~$0.001/min. With 5 Lobes per round, this saves ~50% of prompt cost on long missions.

### Why coder/websocket instead of gorilla
Spec 5 D-D6: gorilla/websocket archived in 2022; coder/websocket actively maintained, supports `context.Context` natively, has `wsjson.Read/Write` helpers, single-allocation per frame. 50-session × 100-message soak: 262 MB/s journal throughput, 852 µs p99 dispatch latency.

### Why streamdown is load-bearing
Spec 6 §Stack: vercel/streamdown handles partial-Markdown gracefully (unclosed code fences, half-rendered tables) — essential for streaming agent output. Pinned at `~1.2.0`; partial-markdown handling has subtle regressions across minors.

### Why Tauri 2 with sidecar fallback
Spec 7 §6.2: external `r1 serve` is primary transport; sidecar is the first-run fallback so the desktop app works the moment a user installs it without requiring a separate install step.

### Why every UI action has an MCP equivalent
Spec 8: humans drive r1 via three UIs; agents drive via MCP. If the surfaces diverge, agents fail in non-obvious ways. `lint-view-without-api` enforces this as a build break.

### Why anti-truncation is machine-mechanical
Spec 9 D-2026-05-04-01: the model demonstrably ignores prompt-level instructions to defeat self-truncation. Reliable enforcement is at the host process layer in deterministic Go code, in seven independently-effective layers. Operator can override (with a flag); LLM cannot.

### Why no `/healthz` on the SaaS services
Cloud Run org policy on `relayone-488319` intercepts `/healthz` and returns 404 from the load balancer before the request hits the container. r1 services answer `/livez` + `/readyz` + `/v1/version` instead. Documented in D-2026-05-04-03.

### Why the embed.go fix
Originally `RegisterDashboardUI` rewrote `r.URL.Path = "/index.html"` and called `http.FileServer`. FileServer auto-canonicalizes back to `/`, creating a 301 loop. The fix (D-2026-05-04-02) reads `static/dist/index.html` directly via `fs.ReadFile`. Legacy `static/index.html` fallback preserved.

### Why Path A (Go reimpl) for auth
Operator decision: 3 Go SaaS services already; adding a 4th in Node creates an additional toolchain to operate. Drift risk is bounded because `@relayone/auth-core`'s contract is stable; performance win (one fewer hop on every authenticated request) is real even if small.
