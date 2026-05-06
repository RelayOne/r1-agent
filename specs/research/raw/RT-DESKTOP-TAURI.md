# RT-DESKTOP-TAURI — Tauri 2 architecture for r1-agent desktop

> **Filed:** 2026-05-02. **Status:** RAW research dump for the
> daemon-mode desktop pivot. Owned by the R1D-* phase track in
> `desktop/PLAN.md`. Citations inline with `[n]` keyed to the
> Sources section at the bottom.

## 0. Context recap

Current desktop scaffold (per `desktop/PLAN.md`, `desktop/IPC-CONTRACT.md`,
`desktop/src-tauri/tauri.conf.json` as of 2026-04-23):

- **Tauri 2 + React + TS + Vite + Tailwind + shadcn/ui**, Zustand + TanStack
  Query for state, Monaco for rich text.
- One window declared in `tauri.conf.json` (`title: "R1 Desktop"`).
- IPC architecture is **Tier 1 WebView ⇄ Tier 2 Rust host ⇄ Tier 3 r1 Go
  subprocess**, JSON-RPC 2.0 over NDJSON on stdin/stdout, with Rust
  fanning unsolicited events out via Tauri's typed event bus.
- Each session today spawns its own `r1 --one-shot` subprocess, with no
  shared daemon and no per-session workdir binding.

The pivot under research:
1. Long-running `r1 serve` daemon (already speced under `specs/r1-server.md`
   and `specs/r1-server-ui-v2.md`).
2. WebView talks to that daemon (directly or via the Rust host).
3. Multiple concurrent sessions, each bound to a folder, Claude-chat-style
   UI with live "cognition lanes."

---

## 1. Tauri 2 multi-window vs single-window-multi-tab

### What the docs say

- Tauri 2 cleanly separates **Window** (native OS frame) from **Webview**
  (web content container). A Window can host one *or many* webviews as
  children on desktop, exposed via `WebviewWindowBuilder` / the
  `webviewWindow` JS namespace [1][2].
- Multi-webview-in-one-window is **experimental** in Tauri 2 — most
  shipping apps still use either a single webview or N separate
  WebviewWindows [3][4].
- Tauri does **not** support `window.open()`-style sub-windows that
  inherit the parent context the way Electron does. New windows are
  separate documents with their own JS heap; cross-window state
  requires Rust-side `tauri::State` + events [4][5].
- Per-window capability scoping (`"windows": ["main"]`) is first-class —
  permissions can differ between windows, which matters once you start
  spawning per-session windows that may have different filesystem
  scopes [6].

### What comparable apps do

- **Claude Code Desktop (Apr 2026 redesign)** — single window, sidebar of
  sessions filterable by status/project/env, drag-and-drop panes
  (terminal / preview / diff / chat) inside that one window. Sessions
  archive on PR merge. Three density modes (Verbose / Normal / Summary).
  `⌘+;` opens side chat, `⌘+/` shows shortcut palette [7].
- **Cursor 3 (Apr 2026)** — parallel agents, multi-session interface,
  also single window with tab/sidebar pivots.
- **Zed** — concurrent agent chat *tabs* in the existing editor tab bar,
  explicitly modeled on its native code-editor tabs so it "feels native"
  [8].
- **Slack desktop (Jan 2026)** — single primary window with workspace
  switcher rail; "Open separate window" is a per-conversation escape
  hatch, not the default [9]. Explicit Slack help: pop-out channels into
  separate windows on demand [10].
- **Linear desktop** — single window, project/team switcher in left rail.
  March 2026 changelog added in-app chat (`⌘/Ctrl+J`) inside the same
  window rather than spawning a new one [11].

### Recommendation

**Single primary window + tabs/sidebar of sessions, with optional
"pop-out into new WebviewWindow" for power users.** This matches every
2026-vintage AI agent client (Claude Code, Cursor, Zed) and ships
cleanly on Tauri 2 because:

- Cross-session state stays in one JS heap → Zustand stores trivially
  shared, TanStack Query cache shared, no Rust-side state plumbing
  needed for the common case.
- The "pop out" route uses `WebviewWindow` constructor and reuses the
  existing IPC contract — no schema change.
- Per-window capability scoping is still available if/when a popped-out
  window needs a tighter sandbox.

Rejected: pure multi-window-per-session. It costs ~30 MB RAM per
window, breaks `⌘K`-style global palette, and forces every shared piece
of state through Rust [3][4][12].

---

## 2. Sidecar vs external daemon

### Sidecar (Tauri-bundled binary)

`tauri.conf.json → bundle.externalBin` ships the binary inside the
installer. Runs via `tauri_plugin_shell::ShellExt::sidecar(name)` from
Rust or `Command.sidecar(name)` from JS. Each platform requires the
binary renamed with its target triple (`r1-x86_64-unknown-linux-gnu`,
`r1-aarch64-apple-darwin`, `r1-x86_64-pc-windows-msvc.exe`, etc.) [13].

**Pros:**
- Zero install friction — user double-clicks the .dmg/.msi/.deb and r1
  is there.
- Versions are pinned: app and daemon ship together, no skew.
- Tauri's notarization pipeline can sign the sidecar alongside the host
  on macOS and Windows (caveats below) [14].

**Cons:**
- One binary per target triple; Linux gets ugly (gnu vs musl, x86_64 vs
  aarch64) — at least 5 triples to ship for full coverage.
- Notarization of `externalBin` on macOS is finicky — there's an open
  bug (`tauri-apps/tauri#11992`) where the codesign step needs the
  sidecar embedded with `entitlements.plist` matching the host [15].
- App size grows by the size of the r1 Go binary (currently ~30-40 MB
  release).
- Updates: bumping r1 forces a full app reinstall — no independent
  upgrade.
- CI must be a matrix — building macOS/Windows/Linux × arch combos.

### External daemon (system-installed `r1 serve`)

User installs `r1` separately (brew, apt, MSI, `curl | sh`); desktop
app connects over `ws://127.0.0.1:<port>`.

**Pros:**
- One Tauri build per OS, not per-arch-pair-with-Go-binary.
- r1 upgrades independently of the GUI — power users on `r1 nightly`
  with stable GUI is the common config.
- The daemon can be reused by `r1` CLI, IDE plugins, CI, `r1 serve`
  remote use cases — desktop is one of N clients.
- Existing scaffolds for `r1 serve` already exist in
  `specs/r1-server.md` + `specs/r1-server-ui-v2.md`.

**Cons:**
- Install-friction story: must onboard "install r1 first." Wizard can
  shell out to download it on first run, but that's bespoke code.
- Version skew: GUI vN may speak protocol vM-1 from old r1.
- Cross-origin / CSP gymnastics for `ws://localhost` (see §4).

### Cross-platform sidecar verdict

Tauri sidecars *do* work on Linux, macOS (Intel + ARM), and Windows
[13]. The friction is in CI matrix, code-signing on macOS, and update
cadence — not in runtime.

### Recommendation

**Hybrid with external daemon as primary, sidecar as fallback / first-run
convenience.** Concretely:

1. Desktop discovers `r1 serve` on `127.0.0.1:<configured-port>` (or via
   a unix socket on macOS/Linux). If found, connects.
2. If not found, the wizard offers: "(a) install r1 system-wide via
   brew/winget/apt, (b) use the bundled copy" — option (b) flips the
   app to sidecar mode and spawns the embedded `r1 serve` via
   `ShellExt::sidecar`.
3. The IPC contract (`desktop/IPC-CONTRACT.md`) stays the same; only
   the transport (stdio vs websocket) differs.

This matches Cursor's relationship to the `cursor-agent` daemon and
Claude Code's relationship to its installed `claude` binary.

---

## 3. Folder picker + per-session workdir persistence

### Picking the folder

`@tauri-apps/plugin-dialog` exposes `open({ directory: true,
multiple: false })` returning the absolute path on desktop (file:// URI
on iOS, content:// on Android — neither matters for r1) [16]. Selected
paths are auto-added to filesystem and asset-protocol scopes by the
plugin, so the WebView can read them without extra capability JSON.

### Persisting per-session workdir → use `tauri-plugin-store`

`localStorage` is **broken for this** in Tauri: when the dev server
moves to a different `localhost` port (or release uses a custom
protocol), the WebView treats it as a different origin and the previous
`localStorage` is invisible [17][18]. `tauri-plugin-store` writes to a
JSON file under the app's data dir and is the documented replacement
for cross-restart prefs [18][19].

API surface:

```ts
import { load } from '@tauri-apps/plugin-store';

const store = await load('sessions.json', { autoSave: true });
await store.set(sessionId, {
  id: sessionId,
  workdir: '/home/eric/repos/foo',
  createdAt: Date.now(),
  archived: false,
});
const all = (await store.entries()) as [string, SessionMeta][];
```

`autoSave: true` with the default 100ms debounce is fine for our update
rate (sessions are created/archived at human cadence, not 10 Hz).

For the *runtime* per-session state (lane buffers, scroll position,
unread counts) keep it in Zustand — no need to persist.

For the *historical ledger* of past sessions (transcripts, tool calls,
verifications) that's already content-addressed in the r1 ledger — the
desktop app should read it via a `ledger.list` IPC verb, not duplicate
it in plugin-store. plugin-store holds only the small index keyed by
session id.

### One specific Tauri 2 API for the workdir-per-session feature

**`tauri-plugin-store`'s `Store.set(sessionId, { workdir, ... })`** —
exactly the API the desktop app should call after the user picks a
folder via `plugin-dialog.open({ directory: true })`. Pairs with
`tauri-plugin-fs` for scope checks before the daemon receives the path.

---

## 4. WebSocket from WebView to local daemon

### The mixed-content trap

Tauri 2 on **Windows** loads the WebView from `https://tauri.localhost`
by default. A `ws://127.0.0.1:<port>` connection from that origin is
flagged as mixed content and blocked by WebView2 — this is reported in
`tauri-apps/tauri#7651` and `#7701` [20][21]. macOS / Linux use a
custom protocol that doesn't trigger the mixed-content check, so the
problem is Windows-specific.

### Three workarounds, ranked

1. **Use `tauri-plugin-websocket`** [22]. The plugin opens the socket
   from the **Rust** side (where mixed-content rules don't apply) and
   exposes a JS handle. JS surface:

   ```ts
   import WebSocket from '@tauri-apps/plugin-websocket';
   const ws = await WebSocket.connect('ws://127.0.0.1:7777');
   ws.addListener((msg) => onFrame(msg));
   await ws.send(JSON.stringify(rpc));
   ```

   This is the recommended path. Same JS contract on every OS, no CSP
   gymnastics, and it inherits Tauri's permission model. Default
   permissions (`allow-connect`, `allow-send`) gate the connect-by-URL.

2. **Tauri Windows http-origin feature** — there's a feature flag that
   makes Windows load over `http://tauri.localhost`, eliminating the
   mixed-content block and letting the native browser `WebSocket`
   constructor reach `ws://127.0.0.1:*` directly [21][23]. Trades CSP
   strictness for simpler JS.

3. **CSP**: regardless of which path you take, `connect-src` must list
   `ws://127.0.0.1:*` (or the daemon's actual port). The current
   `tauri.conf.json` only allows `'self'` and `https://ipc.localhost`
   for connect-src — that needs to change to include
   `ws://127.0.0.1:7777` (or `ws://localhost:7777`) [24]. **Don't** use
   `ws:` as a wildcard; bind it to the configured loopback port.

### Plain `ws://` vs self-signed TLS

For loopback, plain `ws://` is fine and standard. Self-signed TLS for
loopback adds operational pain (cert generation, trust-store
provisioning) without security gain, since the kernel already isolates
loopback traffic from other hosts. The auth model should be
**capability tokens in the WebSocket subprotocol header or initial
handshake frame**, not TLS. r1 daemon already has a token model in
`specs/r1-server.md`.

### Recommendation

`tauri-plugin-websocket` + token auth on the `r1 serve` side. CSP
allows the configured loopback port only. No TLS for loopback.

---

## 5. Code signing + auto-update for cross-platform Tauri (R1D-11)

### What R1D-11 entails (per `desktop/PLAN.md`)

Phase R1D-11 covers polish, signing, notarization, and auto-update for
macOS / Windows / Linux. Targeted 2026-08-07.

### State of tooling, May 2026

- **macOS**: Tauri picks up `APPLE_*` env vars during `tauri build` and
  drives `codesign` + `notarytool` with no extra `tauri.conf.json`
  changes [25][26].
- **Windows**: Tauri 2 documents Azure Trusted Signing as the
  recommended path — replaces older sign-with-pfx flow that doesn't
  meet new SmartScreen reputation requirements [27][14].
- **Linux**: AppImage is unsigned (community-trusted), .deb/.rpm signing
  is optional and trivial.
- **Auto-update**: built-in updater plugin (`tauri-plugin-updater`)
  signs update bundles with an offline keypair and verifies on the
  client. The auto-update bundle itself **must** be signed (this is the
  rule that catches everyone) [28].
- **CI**: dev.to walkthroughs from Feb–Mar 2026 show a single GitHub
  Actions matrix that signs + notarizes + uploads in one run, treated
  as production-ready by indie shops like Fortuna [25][29].
- **Outstanding gotcha**: `externalBin` notarization on macOS — issue
  `#11992` is open at time of writing; the workaround is matching
  entitlements between host and sidecar [15]. Affects us only if we go
  sidecar-mode in §2.

### Verdict

Mature enough in 2026 to ship. Plan one sprint for setup (Apple
Developer ID enrollment + Azure Trusted Signing tenant + GH Actions
matrix), then it's set-and-forget.

---

## 6. Comparable apps to study

| App | Container | Multi-session UX | Notes |
|---|---|---|---|
| **Cursor 3** (Apr 2026) | Electron | Single window, parallel agents, sidebar + tabs | Settings model is per-workspace + global, similar to VSCode |
| **Zed** | Rust + GPUI (native, *not* Tauri) | Editor-tab-style agent chat tabs in existing tab bar | `⌘P` for global session switcher; explicitly: "feels native" because tabs reuse the editor tab widget [8] |
| **Claude Code Desktop** (Apr 2026 redesign) | Electron | Single window, session sidebar, drag-and-drop panes (terminal / preview / diff / chat), three density modes [7] | Best UX analog for what we're building |
| **Claude Desktop** (Anthropic) | Electron | Single window, conversation list left rail, no per-conversation workdir | Less ambitious than Claude Code Desktop |
| **ChatGPT Desktop** | Electron-ish (Tauri-like wrapper varies by OS) | Single window, conversation list left rail, single workspace | No file binding — closest analog to a generic chat client |
| **Bolt.new** | Web-only, no native desktop as of May 2026 | N/A | No desktop binary; runs in browser with WebContainers |
| **OpenDevin / All-Hands GUI** | Web app + Docker daemon backend | Web UI, single workspace, no native multi-session | Architecture reference: web frontend, long-running daemon, agent-per-workspace |
| **Linear Desktop** | Tauri-style wrapper | Single window, team rail + project tabs, in-app chat panel (Mar 2026) [11] | Closest non-AI analog for sidebar + tab UX done well |
| **Slack Desktop** | Electron | Single window, workspace switcher rail; "Open separate window" pop-out per conversation [9][10] | Pop-out pattern is the model for our optional second window |

### Best UX analog

**Claude Code Desktop's Apr 2026 redesign.** It already solved the exact
problem: many concurrent agentic sessions, each bound to a repo, with
live tool-call streams. Match its: sidebar of sessions, drag-and-drop
panes, three density modes (we'd map to Verbose / Normal / Summary),
session auto-archive on completion, `⌘+/` shortcut palette.

---

## 7. WebView IPC perf (10 Hz lane updates)

### What the docs say

- **Events** (`emit` / `listen`): JSON-string payloads, evaluated as JS,
  no strong typing, "not designed for low latency or high throughput"
  per the official docs [30].
- **Channels** (`tauri::ipc::Channel<T>`): designed for streaming;
  ordered delivery; typed; used internally for download progress, child
  process stdout, and the WebSocket plugin [30].
- **Raw payloads**: Tauri 2 added Raw Payload IPC, eliminating the JSON
  serialize/deserialize round-trip that bottlenecked v1 once payloads
  exceeded a few KB [31].

### 10 Hz × N lanes math

Suppose 5 concurrent sessions, each with 4 cognition lanes (model
output, tool calls, verification ladder, cost ticks), each emitting at
10 Hz with ~500 byte payloads:

- 5 × 4 × 10 = 200 messages/sec, ~100 KB/sec.
- Events: 200 JSON-string evaluations per second from JS land. Doable
  but wasteful.
- Channels: 200 typed sends per second over the optimized binary
  transport. Comfortable.

### Recommendation

**One channel per session**, multiplexing all lanes for that session.
Concretely:

```rust
#[tauri::command]
async fn session_subscribe(
    app: tauri::AppHandle,
    session_id: String,
    on_lane_event: tauri::ipc::Channel<LaneEvent>,
) -> Result<(), String> {
    // r1 daemon → forward each lane event into on_lane_event
}
```

```ts
import { Channel, invoke } from '@tauri-apps/api/core';
const ch = new Channel<LaneEvent>();
ch.onmessage = (ev) => sessionStore.appendLane(sessionId, ev);
await invoke('session_subscribe', { sessionId, onLaneEvent: ch });
```

This:
- Avoids the global event bus (no fanout cost when only one session is
  in the foreground).
- Gives each session its own ordered stream — backpressure becomes a
  per-session concern, not global.
- Matches the existing sidecar/streaming pattern Tauri uses internally.

Reserve the global event bus for genuinely cross-session events
(daemon-up, daemon-down, license-state, costtrack-budget-alert) that
every component listens for.

---

## 8. Cross-cutting recommendations

1. **Architecture: external daemon primary, sidecar fallback.** Connect
   to `r1 serve` over `ws://127.0.0.1:<port>` via
   `tauri-plugin-websocket`. If discovery fails, spawn the bundled
   sidecar via `ShellExt::sidecar` and connect to it the same way.
2. **UX: single primary window + sidebar of sessions + tabs/panes
   inside.** Mirror Claude Code Desktop's Apr 2026 redesign. Optional
   pop-out into a `WebviewWindow` for power users.
3. **Persistence: `tauri-plugin-store` for the session index** (id →
   workdir, name, archived flag, last-used timestamp). Zustand for
   live UI state. r1 ledger for transcript history.
4. **Folder picker: `@tauri-apps/plugin-dialog → open({ directory:
   true })`.** Path goes straight into the plugin-store entry.
5. **Streaming: one `tauri::ipc::Channel<LaneEvent>` per session**, not
   global events. CSP must include `ws://127.0.0.1:<port>` in
   `connect-src`.
6. **Signing/update: tauri-plugin-updater + Apple Developer ID + Azure
   Trusted Signing.** Already mature in May 2026. Watch
   `tauri-apps/tauri#11992` if we go sidecar-mode on macOS.

---

## Sources

- [1] [Tauri 2 webviewWindow API namespace](https://v2.tauri.app/reference/javascript/api/namespacewebviewwindow/)
- [2] [DeepWiki: Window and Webview API in tauri-apps/tauri](https://deepwiki.com/tauri-apps/tauri/4.2-window-api)
- [3] [Tauri Discussion #6464 — splittable tab application](https://github.com/orgs/tauri-apps/discussions/6464)
- [4] [Tauri Discussion #9423 — Best practices with multiple windows](https://github.com/tauri-apps/tauri/discussions/9423)
- [5] [Tauri Discussion #11782 — Multi window support with master store](https://github.com/tauri-apps/tauri/discussions/11782)
- [6] [Capabilities for Different Windows and Platforms](https://v2.tauri.app/learn/security/capabilities-for-windows-and-platforms/)
- [7] [Claude Code Desktop Redesign 2026: Parallel Sessions Review (devtoolpicks)](https://devtoolpicks.com/blog/claude-code-desktop-redesign-parallel-sessions-2026)
- [8] [Zed Discussion #42381 — Concurrent Agent Chat Windows (tabbed agent threads)](https://github.com/zed-industries/zed/discussions/42381)
- [9] [How To Manage Multiple Slack Accounts (2026)](https://blog.send.win/how-to-manage-multiple-slack-accounts-step-by-step-setup-guide-2026/)
- [10] [Slack help — Open separate windows in Slack](https://slack.com/help/articles/4403608802963-Open-separate-windows-in-Slack)
- [11] [Linear Changelog — Introducing Linear Agent (Mar 2026)](https://linear.app/changelog/2026-03-24-introducing-linear-agent)
- [12] [Tauri Issue #3912 — How to create multiple separate windows](https://github.com/tauri-apps/tauri/issues/3912)
- [13] [Tauri 2 — Embedding External Binaries (sidecar)](https://v2.tauri.app/develop/sidecar/)
- [14] [Ship Your Tauri v2 App Like a Pro: Code Signing for macOS and Windows (Part 1/2)](https://dev.to/tomtomdu73/ship-your-tauri-v2-app-like-a-pro-code-signing-for-macos-and-windows-part-12-3o9n)
- [15] [Tauri Issue #11992 — macOS codesigning + notarization with externalBin](https://github.com/tauri-apps/tauri/issues/11992)
- [16] [Tauri 2 Dialog Plugin](https://v2.tauri.app/plugin/dialog/)
- [17] [Aptabase — What you need to know about persistent state in Tauri apps](https://aptabase.com/blog/persistent-state-tauri-apps)
- [18] [Tauri 2 Store Plugin](https://v2.tauri.app/plugin/store/)
- [19] [@tauri-apps/plugin-store on npm](https://www.npmjs.com/package/@tauri-apps/plugin-store)
- [20] [Tauri Issue #7701 — insecure WebSocket from HTTPS page](https://github.com/tauri-apps/tauri/issues/7701)
- [21] [Tauri Issue #7651 — duplicate of WebSocket from HTTPS page](https://github.com/tauri-apps/tauri/issues/7651)
- [22] [Tauri 2 WebSocket plugin](https://v2.tauri.app/plugin/websocket/)
- [23] [Tauri Issue #3007 — http vs https tauri.localhost on Windows](https://github.com/tauri-apps/tauri/issues/3007)
- [24] [Tauri 2 Content Security Policy docs](https://v2.tauri.app/security/csp/)
- [25] [Shipping a Production macOS App with Tauri 2.0: Code Signing, Notarization, Homebrew](https://dev.to/0xmassi/shipping-a-production-macos-app-with-tauri-20-code-signing-notarization-and-homebrew-mc3)
- [26] [Tauri 2 macOS Code Signing docs](https://v2.tauri.app/distribute/sign/macos/)
- [27] [Tauri 2 Windows Code Signing docs (Azure Trusted Signing)](https://v2.tauri.app/distribute/sign/windows/)
- [28] [Tauri 2.0 Stable Release announcement (updater + signing notes)](https://v2.tauri.app/blog/tauri-20/)
- [29] [Ship Your Tauri v2 App Like a Pro: GitHub Actions and Release Automation (Part 2/2)](https://dev.to/tomtomdu73/ship-your-tauri-v2-app-like-a-pro-github-actions-and-release-automation-part-22-2ef7)
- [30] [Tauri 2 — Calling the Frontend from Rust (events vs channels)](https://v2.tauri.app/develop/calling-frontend/)
- [31] [Tauri 2 — Inter-Process Communication (raw payloads)](https://v2.tauri.app/concept/inter-process-communication/)
