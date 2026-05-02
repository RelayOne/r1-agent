<!-- STATUS: ready -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: lanes-protocol, r1d-server, web-chat-ui (for shared components) -->
<!-- BUILD_ORDER: 7 -->

# Desktop Cortex Augmentation — Implementation Spec

## 1. Overview

This spec **augments** the existing Tauri 2 desktop app at `desktop/` with cortex-aware
features (lanes sidebar, multi-session sidebar with per-session workdir, daemon
discovery + sidecar fallback, lane streaming via channels, native menu, auto-start)
**without** redoing any of the 12 R1D phases already scoped in `desktop/PLAN.md`.

The existing R1D-1..R1D-12 work continues unchanged: scaffold, session view, SOW tree,
verification descent, skill catalog, ledger browser, memory bus viewer, settings,
MCP servers panel, observability dashboard, multi-session parallelism, signing &
auto-update, store submissions. **This spec adds new files and extends the IPC
contract additively** — it does not delete or rewrite existing R1D files.

The principal shift this spec encodes: the desktop now treats `r1 serve` (the
long-running daemon scoped under `specs/r1-server.md`) as the **primary** process
rather than spawning a per-session `r1 --one-shot`. The bundled binary becomes
a **sidecar fallback** when no daemon is discovered. Cognition lanes (cortex
output streams) become first-class UI primitives, mirroring the TUI lanes spec
(`specs/tui-lanes.md`) and the web sidebar (`specs/web-chat-ui.md`).

## 2. Stack & Versions

Existing (unchanged — pinned in `desktop/Cargo.toml` + `desktop/package.json`):

- Tauri **2.x** (workspace `tauri = { version = "2" }`, `tauri-build = "2"`).
- Rust 1.85, edition 2021.
- React + TypeScript 5.4 + Vite 5.2 + Tailwind + shadcn/ui.
- Node ≥ 20.
- `@tauri-apps/api` ^2.0.0, `@tauri-apps/cli` ^2.0.0.

New plugins (this spec adds):

| Plugin | Version (pinned) | Used in |
|---|---|---|
| `tauri-plugin-websocket` | `2.0` (Rust crate `tauri-plugin-websocket = "2"` and `@tauri-apps/plugin-websocket` `^2.0.0`) | `src-tauri/src/transport.rs` (Rust), `src/lib/ws.ts` (TS) — connects to r1 daemon. |
| `tauri-plugin-store` | `2.0` (Rust `tauri-plugin-store = "2"`, `@tauri-apps/plugin-store` `^2.0.0`) | `src/state/sessionStore.ts` — per-session workdir + index. |
| `tauri-plugin-dialog` | `2.0` (Rust `tauri-plugin-dialog = "2"`, `@tauri-apps/plugin-dialog` `^2.0.0`) | Folder picker for session workdir. |
| `tauri-plugin-shell` | `2.0` (Rust `tauri-plugin-shell = "2"`) | `ShellExt::sidecar` for fallback daemon spawn. |
| `tauri-plugin-autostart` | `2.0` (Rust `tauri-plugin-autostart = "2"`, `@tauri-apps/plugin-autostart` `^2.0.0`) | Settings → "Start at login" toggle. |
| `tauri-plugin-fs` | `2.0` | Scope-checks before passing workdir to daemon. |

Bundle config delta (`tauri.conf.json` — `bundle.externalBin`):
- `r1-x86_64-unknown-linux-gnu`
- `r1-aarch64-apple-darwin`
- `r1-x86_64-apple-darwin`
- `r1-x86_64-pc-windows-msvc.exe`
- `r1-aarch64-pc-windows-msvc.exe`

CSP delta (`tauri.conf.json` → `app.security.csp`): add `connect-src` entry
`ws://127.0.0.1:*` (limited to loopback; explicitly NOT a `ws:` wildcard).

## 3. R1D Phase Reconciliation

| R1D phase | Touched? | How |
|---|---|---|
| R1D-1 (scaffold + r1 subprocess IPC) | **Touched additively** — keeps subprocess path; adds daemon-WS path. `src-tauri/src/ipc.rs` gains a Transport enum (Subprocess vs Daemon). |
| R1D-2 (session view + chat) | **Untouched** structurally; this spec adds a lanes sidebar to its right rail (new file, no edits to existing panels). |
| R1D-3 (SOW + descent) | **Untouched.** |
| R1D-4 (skill catalog) | **Untouched.** |
| R1D-5 (ledger) | **Untouched.** |
| R1D-6 (memory bus viewer) | **Untouched.** |
| R1D-7 (settings) | **Touched additively** — adds "Daemon", "Auto-start", "Lanes" settings sub-sections. |
| R1D-8 (MCP servers) | **Untouched.** |
| R1D-9 (observability) | **Untouched.** |
| R1D-10 (multi-session) | **Touched additively** — augmented with workdir-per-session via plugin-store; the headless schedule remains intact. |
| R1D-11 (polish + signing + updater) | **Touched additively** — sidecar binaries added to signing matrix; entitlements adjusted for `externalBin` (tracking [tauri-apps/tauri#11992](https://github.com/tauri-apps/tauri/issues/11992)). |
| R1D-12 (store submissions) | **Untouched.** |

Existing files under `desktop/src/panels/` (`session-view.ts`, `sow-tree.ts`,
`descent-ladder.ts`, `descent-evidence.ts`, `ledger-viewer.ts`, `memory-inspector.ts`,
`cost-panel.ts`) **must not** be deleted or rewritten by this spec.

## 4. Component Sharing Strategy with `web-chat-ui` (spec 6)

**Decision: monorepo with a shared package `@r1/web-components` consumed via
workspace protocol.** Justification:

1. **Atomic refactors.** Lane components (`<LaneCard>`, `<LaneSidebar>`,
   `<LaneDetail>`) will evolve fast in v1; both surfaces must update together.
   A monorepo means one PR touches both. Vendoring (copy in) means desktop
   silently drifts; symlinks break Windows CI and `npm ci` reproducibility.
2. **Existing ergonomics.** Both surfaces already use Vite + Tailwind + shadcn/ui
   per `docs/decisions/index.md` D-2026-05-02-02. Tailwind config + shadcn primitives
   are naturally shared.
3. **Build-time hoisting.** Vite + npm workspaces dedupes React/Tailwind to a
   single copy; sidecar bundle stays small.
4. **Tooling reuse.** Shared Vitest suite runs once for both consumers.

**Layout:**

```
/                       (repo root)
  package.json          (npm workspaces root: ["web", "desktop", "packages/*"])
  packages/
    web-components/     ← new package this spec creates
      package.json      (name: "@r1/web-components", main: "src/index.ts")
      src/
        index.ts        (barrel export)
        lanes/
          LaneCard.tsx
          LaneSidebar.tsx
          LaneDetail.tsx
          PoppedLaneApp.tsx
        chat/           (later — populated by spec 6)
        primitives/     (shadcn re-exports)
        types/
          LaneEvent.ts  (TS mirror of lanes-protocol envelope)
      tailwind.preset.js (shared Tailwind config preset both surfaces extend)
      vitest.config.ts
  desktop/
    package.json        (deps: "@r1/web-components": "workspace:*")
  web/                  (created by spec 6)
    package.json        (deps: "@r1/web-components": "workspace:*")
```

**Rejected alternatives:**

- **Vendor (copy-in).** Easy bootstrap, but desktop drift is inevitable in a
  6-week roadmap with two surfaces under active dev.
- **Symlink (`file:../web/src/components`).** Breaks on Windows CI without
  Developer Mode; `npm ci` resolves the symlink at install time and produces
  non-reproducible installs.
- **Separate published npm package.** Premature; both consumers live in this
  repo and need versioning faster than npm publish cadence allows.

**Migration:** packages/web-components is created in this spec's checklist
item 1; spec 6 (web-chat-ui) populates the `chat/` subdirectory; this spec
populates the `lanes/` subdirectory.

## 5. Daemon Discovery Flow + Sidecar Fallback

New file: `desktop/src-tauri/src/discovery.rs`. Public API:

```rust
pub struct DaemonHandle {
    pub url: String,                 // e.g. "ws://127.0.0.1:7777"
    pub token: String,               // bearer token from daemon.json
    pub mode: TransportMode,         // External | Sidecar
    pub child: Option<CommandChild>, // Some when mode==Sidecar
}

pub enum TransportMode { External, Sidecar }

#[derive(thiserror::Error)]
pub enum DiscoveryError { NotFound, Refused, BadHandshake, SidecarSpawn(String) }

/// Reads ~/.r1/daemon.json, returns (url, token) if file exists & is fresh.
pub fn read_daemon_json() -> Result<DaemonInfo, DiscoveryError>;

/// Tries TCP connect to ws://127.0.0.1:<port>. 1s timeout.
pub async fn probe_external() -> Result<DaemonInfo, DiscoveryError>;

/// Spawns the bundled `r1 serve --port=0 --emit-port-stdout` via ShellExt::sidecar.
/// Reads the chosen port from the child's stdout (first NDJSON event with
/// `event: "daemon.listening", port: <int>, token: <str>`). Returns once
/// the WS handshake completes.
pub async fn spawn_sidecar(app: &AppHandle) -> Result<DaemonHandle, DiscoveryError>;

/// Top-level orchestrator. Tries probe_external() first; falls through to
/// spawn_sidecar() on NotFound | Refused. Caller invokes once at app startup
/// and on every Settings → "Reconnect daemon" click.
pub async fn discover_or_spawn(app: &AppHandle) -> Result<DaemonHandle, DiscoveryError>;

/// Wizard helper — emits the install command for the active OS.
/// macOS: `r1 serve --install --launchd`
/// Linux: `r1 serve --install --systemd-user`
/// Windows: `r1 serve --install --task-scheduler`
pub fn install_command_for_host_os() -> String;
```

Lifecycle:

1. `tauri::Builder::setup` calls `discover_or_spawn(app)` and stores the
   resulting `DaemonHandle` in `tauri::State<Mutex<Option<DaemonHandle>>>`.
2. On window close / `app.exit`: if `mode == Sidecar`, send `daemon.shutdown`
   over WS, wait 5s, then `child.kill()`.
3. The discovery banner in the UI (new `<DaemonStatus>` component) shows:
   - Green dot + "Connected (external)" if external.
   - Blue dot + "Bundled daemon" if sidecar.
   - Yellow dot + "Reconnecting…" during retry.
   - Red dot + "Offline" + retry button on hard fail.
4. The Wizard offers `r1 serve --install` (via `install_command_for_host_os()`)
   the first time a sidecar is spawned, with explanation: "Run as a system
   service so the app starts faster next time."

## 6. IPC Contract Additions

The wire envelope from `desktop/IPC-CONTRACT.md` is preserved verbatim. This
section is **purely additive** — no existing verb's params or result shape
changes, so `X-R1-RPC-Version` stays at **1**.

### 6.1 New methods (`session.lanes.*` + `daemon.*`)

| Method | Params | Result |
|---|---|---|
| `session.lanes.list` | `{ "session_id": string }` | `{ "lanes": [{ "lane_id": string, "title": string, "status": "pending"\|"running"\|"blocked"\|"done"\|"errored"\|"cancelled", "created_at": iso8601 }] }` |
| `session.lanes.subscribe` | `{ "session_id": string, "channel": tauri::ipc::Channel<LaneEvent> }` | `{ "subscription_id": string }` |
| `session.lanes.unsubscribe` | `{ "subscription_id": string }` | `{ "ok": true }` |
| `session.lanes.kill` | `{ "session_id": string, "lane_id": string }` | `{ "killed_at": iso8601 }` |
| `session.set_workdir` | `{ "session_id": string, "workdir": string }` | `{ "ok": true, "workdir": string }` |
| `daemon.status` | `{}` | `{ "url": string, "mode": "external"\|"sidecar", "version": string, "uptime_s": integer }` |
| `daemon.shutdown` | `{ "graceful": boolean (default true) }` | `{ "shutdown_at": iso8601 }` |
| `app.popout_lane` | `{ "session_id": string, "lane_id": string }` | `{ "window_label": string }` (Tauri-only — see §5 of IPC-CONTRACT.md) |
| `app.open_folder_picker` | `{ "title"?: string }` | `{ "path": string \| null }` (Tauri-only) |

### 6.2 New event types (server-pushed)

| `event` | Fields | Emitted when |
|---|---|---|
| `daemon.up` | `url`, `mode`, `at`, `replayed_from?` (last_event_id served on reconnect, omitted on first connect) | Daemon connected after probe or sidecar spawn. |
| `daemon.down` | `reason`, `at`, `will_retry` | WS closed unexpectedly. |
| `lane.delta` | `session_id`, `lane_id`, `seq`, `payload` | Token / tool-call increment for a lane. |
| `lane.status_changed` | `session_id`, `lane_id`, `from`, `to`, `at` | Lane state transition. |
| `lane.spawned` | `session_id`, `lane_id`, `title`, `at` | New cognition lane created mid-session. |
| `lane.killed` | `session_id`, `lane_id`, `reason`, `at` | Lane terminated (operator kill or natural end). |

`lane.delta` arrives via the per-session `tauri::ipc::Channel<LaneEvent>` (§8),
**not** the global event bus. Status / spawn / kill events are global because
they affect sidebar rendering across surfaces. Per RT-DESKTOP-TAURI §7.

### 6.3 Verbatim JSON examples

**Subscribe + receive lane delta:**
```json
{"jsonrpc":"2.0","id":"r-1","method":"session.lanes.subscribe","params":{"session_id":"S01","channel":"<channel-handle>"}}
{"jsonrpc":"2.0","id":"r-1","result":{"subscription_id":"sub-9f2c"}}
```
Channel-borne event:
```json
{"event":"lane.delta","session_id":"S01","lane_id":"L02","seq":42,"payload":{"kind":"tool_use","tool":"r1.fs.read","args":{"path":"src/main.rs"}}}
```

**Set workdir:**
```json
{"jsonrpc":"2.0","id":"r-2","method":"session.set_workdir","params":{"session_id":"S01","workdir":"/home/eric/repos/foo"}}
{"jsonrpc":"2.0","id":"r-2","result":{"ok":true,"workdir":"/home/eric/repos/foo"}}
```

**Daemon status:**
```json
{"jsonrpc":"2.0","id":"r-3","method":"daemon.status","params":{}}
{"jsonrpc":"2.0","id":"r-3","result":{"url":"ws://127.0.0.1:7777","mode":"external","version":"0.5.2","uptime_s":18234}}
```

## 7. Workdir-per-Session

Schema in `tauri-plugin-store` file `sessions.json` under the app data dir
(`~/Library/Application Support/dev.r1.desktop/sessions.json` on macOS, etc.):

```ts
// desktop/src/state/sessionStore.ts — new file
type SessionMeta = {
  id: string;                 // ULID
  name: string;               // user-edited, defaults to "Session N"
  workdir: string | null;     // absolute path picked by user; null = ephemeral
  workdir_set_at?: string;    // iso8601
  archived: boolean;
  created_at: string;         // iso8601
  last_used_at: string;
  pinned_lane_ids: string[];  // for tile-mode persistence
};

// File shape:
// { "<sessionId>": SessionMeta, ... }
```

Folder picker integration:

```ts
import { open } from '@tauri-apps/plugin-dialog';
import { load } from '@tauri-apps/plugin-store';

export async function pickWorkdir(sessionId: string): Promise<string | null> {
  const path = await open({ directory: true, multiple: false, title: 'Pick session workspace' });
  if (typeof path !== 'string') return null;
  const store = await load('sessions.json', { autoSave: true });
  const meta = ((await store.get(sessionId)) as SessionMeta) ?? defaultMeta(sessionId);
  meta.workdir = path;
  meta.workdir_set_at = new Date().toISOString();
  await store.set(sessionId, meta);
  // Push to daemon: cmd.Dir-equivalent on the Go side.
  await invoke('session.set_workdir', { session_id: sessionId, workdir: path });
  return path;
}
```

The Go-side `session.set_workdir` handler MUST refuse if the session has any
in-flight tool calls — return `conflict` error per §3.2 of `IPC-CONTRACT.md`.
Workdir is bound to `cmd.Dir` for any subprocess the daemon spawns on behalf
of that session (matching `docs/decisions/index.md` D-D2: `SessionRoot` threaded
via `cmd.Dir`).

## 8. Lane Streaming via `tauri::ipc::Channel<LaneEvent>`

One channel per session, multiplexing all that session's lanes. Per the math
in RT-DESKTOP-TAURI §7: 5 sessions × 4 lanes × 10 Hz × ~500 B = 200 msg/s,
~100 KB/s — well within channel throughput.

**Rust side** (`desktop/src-tauri/src/lanes.rs` — new file):

```rust
#[derive(Clone, serde::Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum LaneEvent {
    Delta   { session_id: String, lane_id: String, seq: u64, payload: serde_json::Value },
    Status  { session_id: String, lane_id: String, from: String, to: String, at: String },
    Spawned { session_id: String, lane_id: String, title: String, at: String },
    Killed  { session_id: String, lane_id: String, reason: String, at: String },
}

#[tauri::command]
pub async fn session_lanes_subscribe(
    state: tauri::State<'_, AppState>,
    session_id: String,
    on_event: tauri::ipc::Channel<LaneEvent>,
) -> Result<SubscribeResult, IpcError> {
    let sub_id = state.lanes.subscribe(session_id, on_event).await?;
    Ok(SubscribeResult { subscription_id: sub_id })
}
```

The daemon-side WS reader pushes parsed `lane.*` frames into a tokio mpsc;
a per-session forwarder task `select!`s the mpsc and calls
`channel.send(event)` with backpressure handling: if `send` errs (channel
closed because the WebView dropped it), the subscription is torn down.

**TS side** (`desktop/src/lib/laneSubscription.ts` — new file):

```ts
import { Channel, invoke } from '@tauri-apps/api/core';

export async function subscribeLanes(
  sessionId: string,
  onEvent: (ev: LaneEvent) => void,
): Promise<() => Promise<void>> {
  const ch = new Channel<LaneEvent>();
  ch.onmessage = onEvent;
  const { subscription_id } = await invoke<{ subscription_id: string }>(
    'session_lanes_subscribe',
    { session_id: sessionId, on_event: ch },
  );
  return async () => { await invoke('session.lanes.unsubscribe', { subscription_id }); };
}
```

Coalescing per `docs/decisions/index.md` D-S2: render at 5–10 Hz, diff-only repaint.
The component (`<LaneSidebar>` from `@r1/web-components`) holds a per-lane
buffer; a `requestAnimationFrame`-based flush every 100 ms applies the
buffered ops. No re-render fires for lanes whose buffer is empty.

## 9. Native Menu Bar

Implemented in `desktop/src-tauri/src/menu.rs` (new file) using Tauri 2's
menu API. Mirrors the Claude Code Desktop / Linear convention.

```
R1 Desktop                                                   (macOS app menu)
  About R1 Desktop
  Settings…                              ⌘,
  ───
  Hide R1 Desktop                        ⌘H
  Quit R1 Desktop                        ⌘Q

File
  New Session                            ⌘N      → invoke('session.start', ...)
  Open Folder…                           ⌘O      → app.open_folder_picker
  Switch Session                         ⌘P      → opens shadcn Command palette
  Close Session                          ⌘W
  ───
  Import Session…
  Export Session…

Edit
  Undo / Redo / Cut / Copy / Paste / Select All  (default roles)

View
  Lanes Sidebar                          ⌘1
  Toggle Tile Mode                       ⌘2
  Pop Out Lane                           ⌘\      → app.popout_lane (active lane)
  Density: Verbose                       ⌘⇧V
  Density: Normal                        ⌘⇧N
  Density: Summary                       ⌘⇧S

Session
  Pause                                  ⌘.
  Resume                                 ⌘⇧.
  Cancel                                 ⌘⌫
  Kill Active Lane                       k
  Kill All Lanes                         ⇧K

Tools
  MCP Servers…
  Skills…
  Memory Bus…

Window
  Minimize / Zoom (default roles)
  Bring All to Front
  Lane Pop-Outs                          (submenu listing labels of WebviewWindows opened via app.popout_lane)

Help
  Documentation                                  → opens https://r1.dev/docs/
  Release Notes
  Report an Issue
  About                                          (Linux/Windows only; on macOS it's in the app menu)
```

Windows / Linux place the `About` and `Settings…` items under `Help` and
`Edit` respectively per platform convention. Auto-start toggle lives in
Settings, NOT in the menu bar.

## 10. Auto-Start (Per OS)

Use `tauri-plugin-autostart` for the cross-platform shim; it abstracts the
three native paths.

| OS | Mechanism | Location |
|---|---|---|
| macOS | Login Items (LSSharedFileList → ServiceManagement.framework) | System Settings → General → Login Items. Plugin writes `~/Library/LaunchAgents/dev.r1.desktop.plist` with `RunAtLoad=true` when toggled on. |
| Windows | Startup registry key | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value `R1Desktop = "C:\Program Files\R1 Desktop\R1 Desktop.exe"`. |
| Linux | XDG autostart | `~/.config/autostart/r1-desktop.desktop` with `X-GNOME-Autostart-enabled=true`. |

UI: Settings → "Startup" → "Start R1 Desktop at login" checkbox. The setting
persists in `tauri-plugin-store` under `prefs.json` key `autostart_enabled`.
On toggle, `await enable()` / `await disable()` from `@tauri-apps/plugin-autostart`.

**Daemon vs UI auto-start are independent**:
- Auto-start the **UI** uses tauri-plugin-autostart (this section).
- Auto-start the **daemon** uses `r1 serve --install` from §5; on macOS this
  installs a launchd LaunchAgent at `~/Library/LaunchAgents/dev.r1.daemon.plist`,
  on Linux a systemd-user unit at `~/.config/systemd/user/r1.service`, on
  Windows a Task Scheduler task `R1 Daemon`. `kardianos/service` (Go) handles
  this; the desktop merely shells out and reports success.

## 11. Test Plan

### 11.1 Unit / component (Vitest)

Lives in `packages/web-components/src/lanes/__tests__/`. Shared with `web/`
via the workspace import — both surfaces consume the same passing tests.

- `LaneCard.test.tsx`: renders all 6 statuses with correct glyph+color pair
  per `docs/decisions/index.md` D-S1.
- `LaneSidebar.test.tsx`: sorts lanes in stable creation-time order (RT-SURFACES
  gotcha "lane ordering churn"); diff-only repaint when only one lane's buffer
  changes (uses spy on `useMemo` recomputation).
- `PoppedLaneApp.test.tsx`: receives Channel events and renders.

### 11.2 Rust unit (`cargo test` in `desktop/src-tauri/`)

- `discovery::probe_external` — mock TCP listener; assert connect succeeds; kill
  listener mid-handshake → assert `BadHandshake` returned.
- `discovery::spawn_sidecar` — point `ShellExt::sidecar` at a fake binary that
  prints `{"event":"daemon.listening","port":12345,"token":"t"}` on stdout;
  assert returned handle has `mode: Sidecar` and parsed port.
- `lanes::session_lanes_subscribe` — feed an mpsc 100 fake `LaneEvent`s and
  assert the test-side channel receiver got them in order.
- `menu::build_menu` — assert all expected items present per OS.

### 11.3 End-to-end (tauri-driver + Playwright MCP)

`desktop/tests/e2e/`:

- `e2e/daemon-discovery.spec.ts`: launch app with no daemon — assert sidecar
  spawned, status banner blue. Kill child PID — assert banner red, retry
  succeeds.
- `e2e/multi-session.spec.ts`: create 5 sessions, each with distinct workdir
  via the folder picker; assert `tauri-plugin-store` has 5 entries; switch
  between sessions — assert workdir persists across app restart.
- `e2e/lanes-streaming.spec.ts`: drive a fake daemon emitting lane events at
  10 Hz for 30 seconds across 4 lanes; assert all events rendered, no dropped
  seq numbers, render frame-rate ≥ 5 Hz under load (matching D-S2).
- `e2e/popout-lane.spec.ts`: pop a lane via menu → assert new `WebviewWindow`
  with label `lane:<session>:<lane>`; close primary window — assert pop-outs
  remain.
- `e2e/menu.spec.ts`: drive every menu item with keyboard accelerator; assert
  the corresponding `invoke`/event fires.
- `e2e/autostart.spec.ts` (linux + macOS only — Windows runs in CI image
  without registry write perms): toggle auto-start → assert LaunchAgent /
  .desktop file exists; toggle off → assert removed.

### 11.4 Agentic tests (Playwright MCP)

Per `docs/decisions/index.md` D-A3: web testing via Playwright MCP. Component
contracts via Storybook MCP — `@r1/web-components` ships a Storybook with
stories for each lane component; the Storybook MCP server lets the agentic
test harness (spec 8) drive every story and screenshot-diff.

### 11.5 CI gate

`.github/workflows/desktop.yml`:
- `cargo build` + `cargo test` in `desktop/src-tauri/`.
- `npm test` in `desktop/`.
- `npm test` in `packages/web-components/`.
- `cargo tauri build` (artifact upload only on tag push).
- E2E suite runs on macOS-latest, ubuntu-22.04, windows-latest.

## 12. Risks & Mitigations

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | **macOS sidecar notarization.** Tauri's `bundle.externalBin` ships the bundled `r1` binary inside the `.app`. Apple gatekeeper requires every executable inside a notarized bundle to itself be signed with the same Developer ID **and** be referenced in the entitlements manifest. Tauri 2 had a bug ([tauri-apps/tauri#11992](https://github.com/tauri-apps/tauri/issues/11992)) where externalBin entries were stripped from the deep-signing pass; if we ship before that lands the app will fail notarization or, worse, pass notarization but be killed by gatekeeper at runtime ("damaged" prompt). | Medium | Ship-blocking on macOS | (a) Pin Tauri to the first 2.x release that includes the #11992 fix; CI gate the `tauri-apps/tauri` git tag in `desktop/Cargo.toml`. (b) Add a `desktop/scripts/verify-notarization.sh` step to R1D-11.2 that runs `spctl --assess --verbose=4` on the built `.app` and fails the build if the sidecar binary's signature is rejected. (c) Add `<key>com.apple.security.cs.allow-unsigned-executable-memory</key>` only as a last-resort fallback (documented in §11.2 of R1D-11). |
| R2 | **Daemon discovery race.** If the user installs `r1 serve` mid-session, the desktop's `discover_or_spawn` returns the sidecar handle and never re-tries the external probe. | Low | Annoying — user runs two daemons | Settings → "Reconnect daemon" button (already in §5 lifecycle step 1) re-runs the orchestrator. Also re-probe on window focus if sidecar mode has been active >30 min. |
| R3 | **`tauri::ipc::Channel` backpressure.** A frozen WebView (DevTools paused, tab inactive) lets the per-session channel buffer grow without bound — OOM risk under 4-lane × 10 Hz load. | Low | Process crash | The forwarder task in `lanes.rs` keeps a per-channel ring of 1024 events; on overflow it drops `lane.delta` (status/spawn/kill never drop) and emits a single `lane.delta.gap` marker so the UI can re-fetch via `session.lanes.list`. Documented in §8. |
| R4 | **Workdir picker on Wayland.** GTK file chooser on Wayland is sandboxed via xdg-desktop-portal; some compositors still don't expose a portal, so `tauri-plugin-dialog` returns null. | Low | Linux-only UX paper-cut | `pickWorkdir` falls back to a manual path entry input when the dialog returns null twice in a row, with copy "Your desktop environment doesn't expose a folder picker; paste a path." |
| R5 | **Per-session workdir conflicts with running tools.** Changing `workdir` mid-session would orphan in-flight `cmd.Dir` subprocesses on the Go side. | Medium | Data loss / wrong-repo writes | The Go-side handler already returns `conflict` (§7); the desktop UI surfaces this with a modal "Wait for current step to finish, or cancel it first." |
| R6 | **Monorepo workspace pulls in node_modules churn.** Adding a workspace root introduces a `node_modules/` at repo root that some R1D-* CI jobs may not expect. | Low | CI flake | `desktop-augmentation.yml` uses `npm ci --workspace=desktop --workspace=packages/web-components` to scope installs; root `package.json` declares `"private": true` to block accidental publishes. Existing `desktop.yml` is untouched. |
| R7 | **Channel + global event bus split.** Mixing `lane.delta` (per-session channel) with `lane.status_changed` (global event bus, §6.2) means the UI must reconcile two sources. A status flip arriving before its preceding deltas would render a "done" lane that's actually still streaming. | Medium | Visible UI glitch | The forwarder task emits `lane.status_changed` **after** flushing pending deltas for that lane. Documented in §8. Test asserts in `lanes-streaming.spec.ts`. |
| R8 | **Tauri 2.x plugin version skew.** Six new plugins all pinned to `^2.0`; if any plugin's 2.x line ships a breaking change before app v1, the lockfile lets it through on `npm install`. | Medium | Build break | Pin all six to exact 2.0.x in `Cargo.toml` AND `package.json` once those versions are published; checklist item 10 says "Pin to exact 2.0.x once published". Add a `.github/dependabot.yml` entry (new checklist item 41) with `package-ecosystem: cargo` + `package-ecosystem: npm` for `desktop/` and `packages/web-components/` directories, `schedule.interval: weekly`, `open-pull-requests-limit: 5`, and `allow: [{dependency-type: "direct"}]` so plugin minors land as PRs for review. |
| R9 | **Last-Event-ID replay loss.** D-S6 says reconnect replays from `Last-Event-ID`, but the daemon's WAL retention isn't specified in this spec — if daemon-side WAL has rotated past the disconnected client's last id, replay returns empty. | Low | Lost lane events on long disconnect | The daemon's `daemon.up` event payload (added to §6.2) carries `replayed_from: <last_event_id_served>`. The desktop's `transport.rs` reconnect handler compares `replayed_from` against the client's `requested_last_id`; on shortfall it emits a `lane.delta.gap` marker and triggers a fresh `session.lanes.list` fetch to rehydrate state. WAL retention policy itself is the daemon's responsibility (`specs/r1d-server.md`), but desktop-side gap handling is fully specified here. |

## 13. Out of Scope

Anything assigned to R1D-1..R1D-12 in `desktop/PLAN.md` that is not
explicitly mentioned in §3 above remains **out of scope** for this spec.
Non-exhaustive list of out-of-scope items:

- **R1D-3**: react-flow dependency-graph visualization, failure-classification
  UI wiring (R1D-3.2, R1D-3.5).
- **R1D-4.4**: Actium Studio pack bundled install.
- **R1D-5.4**: crypto-shred double-confirm modal.
- **R1D-7.4**: `.stoke/` → `.r1/` migration tool.
- **R1D-8**: full MCP servers panel (this spec only adds an MCP entry to the
  Tools menu — it does not implement the panel).
- **R1D-9**: observability dashboard — KPI cards, recharts/visx, CSV export.
- **R1D-10.4**: macOS launchd / Windows Task Scheduler / systemd unit for
  *headless schedules* (different from daemon auto-start in §10 — the
  schedule feature is a separate worker that runs scheduled missions even
  when the GUI is closed).
- **R1D-11.2 / 11.3 / 11.4**: signing pipelines themselves. This spec only
  adds the externalBin entries to the matrix; the actual signing infra is
  R1D-11's job.
- **R1D-12**: Homebrew cask, Scoop manifest, Flathub submission.
- **Web UI (spec 6)**: chat composer, transcript, streamdown markdown rendering,
  AI SDK 6 `useChat` integration. This spec only sets up the shared package
  and lane components consumed by both surfaces.
- **TUI (spec 4)**: Bubble Tea v2 lanes rendering — entirely separate surface.
- **Cortex daemon internals (specs 2, 3, 5)**: the Lobe runtime, lanes-protocol
  envelope, r1d-server WS auth/token logic.

## Implementation Checklist

Each item names a concrete path under `desktop/`, `packages/`, or repo root.

1. [ ] Create monorepo workspace root `package.json` at `/home/eric/repos/r1-agent/package.json` declaring `"workspaces": ["desktop", "web", "packages/*"]`. Add a no-op `"private": true`. Do not delete existing `desktop/package.json`.
2. [ ] Scaffold `packages/web-components/package.json` with name `@r1/web-components`, `main: "src/index.ts"`, peerDependencies on `react` `^18` and `tailwindcss` `^3`. Devdeps: `vitest`, `@testing-library/react`, `tsup` for build.
3. [ ] Create `packages/web-components/src/index.ts` barrel + `packages/web-components/src/types/LaneEvent.ts` (TS mirror of lanes-protocol envelope; matches `docs/decisions/index.md` D-S1 status enum).
4. [ ] Create `packages/web-components/src/lanes/LaneCard.tsx` rendering one lane (status glyph + color + title + last-event preview), styled via shadcn primitives.
5. [ ] Create `packages/web-components/src/lanes/LaneSidebar.tsx` rendering an ordered list of LaneCards with stable creation-time ordering (RT-SURFACES anti-churn).
6. [ ] Create `packages/web-components/src/lanes/LaneDetail.tsx` rendering the focus view (full event timeline, kill button, copy-link button).
7. [ ] Create `packages/web-components/src/lanes/PoppedLaneApp.tsx` — root component used inside the pop-out `WebviewWindow`; subscribes to a single lane via channel and renders `<LaneDetail>` full-window.
8. [ ] Add `packages/web-components/tailwind.preset.js` with shared color tokens (status palette per D-S1) and shadcn defaults; both `desktop/tailwind.config.js` and `web/tailwind.config.js` extend this preset.
9. [ ] Wire `desktop/package.json` to import `@r1/web-components: "workspace:*"`. Update `desktop/vite.config.ts` to resolve the workspace package without bundling React twice (use `resolve.dedupe`).
10. [ ] Add Rust deps to `desktop/Cargo.toml` workspace.dependencies: `tauri-plugin-websocket = "2"`, `tauri-plugin-store = "2"`, `tauri-plugin-dialog = "2"`, `tauri-plugin-shell = "2"`, `tauri-plugin-fs = "2"`, `tauri-plugin-autostart = "2"`. Pin to exact 2.0.x once published; track `Cargo.lock`.
11. [ ] Add JS deps to `desktop/package.json`: `@tauri-apps/plugin-websocket`, `@tauri-apps/plugin-store`, `@tauri-apps/plugin-dialog`, `@tauri-apps/plugin-autostart` — all `^2.0.0`.
12. [ ] Register all six Tauri plugins in `desktop/src-tauri/src/main.rs` (`.plugin(tauri_plugin_websocket::init())` etc.).
13. [ ] Update `desktop/src-tauri/tauri.conf.json` `app.security.csp.connect-src` to include `ws://127.0.0.1:*` (loopback only). Document rationale inline as a JSON `_comment` key per existing convention.
14. [ ] Update `desktop/src-tauri/tauri.conf.json` `bundle.externalBin` array with the 5 r1 binary triples listed in §2. Add a build script `desktop/scripts/copy-r1-binaries.sh` invoked by `cargo tauri build` to symlink the latest `r1` build outputs into `desktop/src-tauri/binaries/`.
15. [ ] Create `desktop/src-tauri/src/discovery.rs` implementing `read_daemon_json`, `probe_external`, `spawn_sidecar`, `discover_or_spawn`, `install_command_for_host_os` per §5.
16. [ ] Create `desktop/src-tauri/src/transport.rs` wrapping `tauri-plugin-websocket` with connect/auto-reconnect/exponential-backoff (250 ms → 16 s cap) and the `Last-Event-ID` replay handshake from `docs/decisions/index.md` D-S6 / RT-R1D-DAEMON.
17. [ ] Create `desktop/src-tauri/src/lanes.rs` with `LaneEvent` enum, `session_lanes_subscribe`/`session_lanes_unsubscribe` Tauri commands, and a per-session forwarder task per §8.
18. [ ] Extend `desktop/src-tauri/src/ipc.rs` with the 9 new verbs from §6.1. Existing 15 verbs stay untouched. Each new verb routes through either WS-to-daemon (most) or Tauri-host-only (`app.popout_lane`, `app.open_folder_picker`).
19. [ ] Update `desktop/IPC-CONTRACT.md`: append a new §2.7 "Lane control" subsection with the 4 `session.lanes.*` verbs, a new §2.8 "Daemon control" subsection with `daemon.status`/`daemon.shutdown`, append the 6 new event types to §4, and add 2 entries to §5 (`app.popout_lane`, `app.open_folder_picker`). Do NOT bump `X-R1-RPC-Version` — purely additive.
20. [ ] Create `desktop/src/state/sessionStore.ts` per §7 schema, with `pickWorkdir`, `getMeta`, `setMeta`, `archive`, `listAll`, `clearWorkdir`. Migration: if a session previously stored `workdir` in `localStorage`, copy it on first read then delete the localStorage key.
21. [ ] Create `desktop/src/lib/laneSubscription.ts` per §8 TS snippet with auto-cleanup on component unmount.
22. [ ] Add `<LaneSidebar>` from `@r1/web-components` to the right rail of `desktop/src/panels/session-view.ts`. Subscribe on mount, unsubscribe on unmount or session switch.
23. [ ] Implement `app.popout_lane` Tauri command in `desktop/src-tauri/src/popout.rs`: builds a `WebviewWindowBuilder` with label `"lane:<session>:<lane>"`, URL `index.html?popout=lane&session=<>&lane=<>`, size 480×640, on-close removes from a tracking `HashMap`.
24. [ ] Implement `desktop/src/popout.tsx` entry that reads URL params, mounts `<PoppedLaneApp>` from `@r1/web-components`, subscribes to its single lane via Channel.
25. [ ] Create `desktop/src-tauri/src/menu.rs` building the full menu structure from §9 with platform-conditional layout (macOS app menu vs Linux/Windows Help menu). Wire each accelerator to the corresponding command.
26. [ ] Add a `<DaemonStatus>` component in `desktop/src/panels/daemon-status.ts` to the app's title bar. Listens for `daemon.up` / `daemon.down` events. Click → opens Settings → Daemon section.
27. [ ] Extend `desktop/src/panels/settings.ts` (created under R1D-7) with three new sub-sections: "Daemon" (URL, mode read-only, "Reconnect" button, "Install as service" button), "Auto-start" (checkbox bound to tauri-plugin-autostart), "Lanes" (density toggle Verbose/Normal/Summary).
28. [ ] Create `desktop/src/components/discovery-wizard.tsx` shown on first launch when no `~/.r1/daemon.json` exists. Offers (a) Install r1 system-wide (shows `install_command_for_host_os()` in copy-paste box); (b) Use bundled copy.
29. [ ] Wire auto-start toggle: import `enable`, `disable`, `isEnabled` from `@tauri-apps/plugin-autostart` in settings sub-section. Persist desired state in `prefs.json` via plugin-store; on app start reconcile actual vs desired.
30. [ ] Create `packages/web-components/src/lanes/__tests__/LaneCard.test.tsx` — 6 statuses × glyph+color match.
31. [ ] Create `packages/web-components/src/lanes/__tests__/LaneSidebar.test.tsx` — stable ordering + diff-only render assertion via spy.
32. [ ] Create `desktop/src-tauri/tests/discovery_test.rs` with `probe_external`, `spawn_sidecar`, error-path tests per §11.2.
33. [ ] Create `desktop/src-tauri/tests/lanes_test.rs` feeding 100 LaneEvents and asserting in-order channel delivery.
34. [ ] Create `desktop/tests/e2e/daemon-discovery.spec.ts` (tauri-driver + Playwright MCP) per §11.3.
35. [ ] Create `desktop/tests/e2e/multi-session.spec.ts` covering folder picker + plugin-store persistence across restart.
36. [ ] Create `desktop/tests/e2e/lanes-streaming.spec.ts` driving a fake daemon at 10 Hz over 30 seconds; assert no dropped seq.
37. [ ] Create `desktop/tests/e2e/popout-lane.spec.ts` opening pop-out via menu and verifying its WebviewWindow label + lifecycle.
38. [ ] Create `.github/workflows/desktop-augmentation.yml` running cargo + npm tests in `desktop/`, `packages/web-components/`, plus the e2e suite on macos-latest, ubuntu-22.04, windows-latest. Block merge on red.
39. [ ] Add a `desktop/CHANGELOG.md` entry under "Unreleased" listing this spec's additive changes (so users know lane sidebar + workdir picker shipped without breaking existing R1D-* phases).
40. [ ] Update `desktop/README.md` "Architecture" section with one paragraph + a link to this spec; **do not** modify any sentence describing R1D-1..R1D-12 except to add a new "Cortex augmentation" bullet.
41. [ ] Create `/home/eric/repos/r1-agent/.github/dependabot.yml` (or extend if it exists) with two ecosystems: `cargo` rooted at `desktop/src-tauri/` and `npm` rooted at both `desktop/` and `packages/web-components/`. Each entry: `schedule.interval: weekly`, `open-pull-requests-limit: 5`, `allow: [{dependency-type: "direct"}]`, `groups.tauri-plugins.patterns: ["tauri-plugin-*", "@tauri-apps/plugin-*"]` so the six new plugins update as one PR per ecosystem. Mitigates R8.

## Acceptance Criteria

- WHEN the user launches the desktop app and a `r1 serve` daemon is running, THE SYSTEM SHALL connect via `tauri-plugin-websocket` within 1 second and display the green "Connected (external)" banner.
- WHEN no daemon is running on first launch, THE SYSTEM SHALL spawn the bundled `r1` sidecar within 3 seconds and display the blue "Bundled daemon" banner.
- WHEN the user picks a folder via `Open Folder…` (⌘O), THE SYSTEM SHALL persist `{workdir}` in `tauri-plugin-store` keyed by `session_id` AND push it to the daemon via `session.set_workdir`, and the daemon SHALL bind it to that session's `cmd.Dir`.
- WHEN a session has 4 active cognition lanes streaming at 10 Hz, THE SYSTEM SHALL render at ≥ 5 Hz with diff-only repaints (no whole-sidebar re-render per event).
- WHEN the user invokes `Pop Out Lane` (⌘\), THE SYSTEM SHALL open a new `WebviewWindow` rendering only that lane, surviving close of the primary window.
- WHEN the user toggles "Start at login" in Settings, THE SYSTEM SHALL register the appropriate per-OS auto-start hook (Login Items / Run key / .desktop autostart) and reconcile state on next launch.
- WHEN the daemon disconnects mid-session, THE SYSTEM SHALL emit `daemon.down`, attempt reconnect with exponential backoff, and replay missed events via `Last-Event-ID` once reconnected — without losing any user-visible lane state.
- WHEN this spec ships, THE SYSTEM SHALL pass `cargo test`, `npm test` in both `desktop/` and `packages/web-components/`, and the full e2e suite on macOS, Linux, and Windows; AND no R1D-1..R1D-12 acceptance criterion shall regress.
