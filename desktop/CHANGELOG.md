# R1 Desktop Changelog

This file tracks user-visible changes to the R1 Desktop app (the
Tauri 2 binary built from `desktop/`). The R1D-* phase changelog
lives in `desktop/PLAN.md`; this file captures incremental shipped
features grouped by release.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Unreleased

### Cortex Augmentation (spec `desktop-cortex-augmentation`)

The desktop now augments the R1D-1..R1D-12 phases with cortex-aware
features. Every R1D-* surface remains intact; this release purely
adds new capabilities.

#### Added

- **Lanes sidebar.** Right-rail aside in the session view rendering
  cognition lanes streamed from the daemon via
  `tauri::ipc::Channel<LaneEvent>`. Components live in the new
  `@r1/web-components` workspace package and are shared with the
  upcoming web surface (spec 6).
- **Lane focus view + pop-out windows.** Cmd+\\ pops the active lane
  into a 480×640 `WebviewWindow` that survives primary-window close.
  Window labels follow `lane:<session>:<lane>` for stable cross-launch
  identification.
- **Per-session workdir.** Each session can pick its own workspace
  folder via `Open Folder…` (Cmd+O); persisted via `tauri-plugin-store`
  in `sessions.json`. The daemon binds the workdir to that session's
  `cmd.Dir` for any subprocess it spawns.
- **Daemon discovery.** New `discovery.rs` probes `~/.r1/daemon.json`
  and the daemon's loopback port; falls back to spawning the bundled
  `r1` sidecar via `tauri-plugin-shell::ShellExt::sidecar`. First-run
  wizard (`<DiscoveryWizard>`) offers `r1 serve --install` per OS.
- **Auto-reconnect transport.** Exponential backoff (250 ms → 16 s
  cap) with ±20 % jitter; replays missed events via `Last-Event-ID`.
- **DaemonStatus title-bar pill.** Four-state status (Connected
  external / Bundled daemon / Reconnecting / Offline) with Reconnect
  click-to-retry on red.
- **Native menu bar.** Full Claude Code Desktop / Linear-style menu
  with platform-conditional layout (macOS app menu vs Linux/Windows
  Help / Edit). 6 verbs, 18 stable id strings, all accelerators wired
  to `menu://<id>` events.
- **Auto-start at login.** Settings → Auto-start checkbox routed
  through `tauri-plugin-autostart`; persisted to `prefs.json` and
  reconciled with OS-side state at every launch.
- **Lane density preference.** Settings → Lanes radio group
  (Verbose / Normal / Summary) controls how much detail each lane
  card renders.
- **9 new IPC verbs.** `session.lanes.list / .subscribe / .unsubscribe
  / .kill`, `session.set_workdir`, `daemon.status / .shutdown`,
  `app.popout_lane`, `app.open_folder_picker`. Per spec mandate the
  X-R1-RPC-Version header stays at 1 — every change is purely additive.
- **6 new server-pushed events.** `daemon.up / .down`, `lane.delta /
  .status_changed / .spawned / .killed`. `lane.delta` arrives via the
  per-session Channel; the other 5 use the global event bus (R7
  ordering: deltas always flush before status_changed for the same
  lane).
- **Sidecar bundling.** `tauri.conf.json` `bundle.externalBin` now
  ships the `r1` daemon binary for 5 target triples
  (linux-x86_64, macos-arm64, macos-x86_64, windows-x86_64,
  windows-arm64). `desktop/scripts/copy-r1-binaries.sh` symlinks
  per-triple builds before `cargo tauri build`.

#### Changed

- **CSP.** `connect-src` gains `ws://127.0.0.1:*` so
  `tauri-plugin-websocket` can reach the daemon on a runtime-chosen
  loopback port. Loopback only — explicitly NOT a wildcard.
- **Settings overlay.** Three new sub-sections (Daemon, Auto-start,
  Lanes) appended to the existing R1D-7 left-nav. Existing General /
  Providers / Vault / Ledger / Memory / Governance / Advanced tabs
  unchanged.
- **`session-view` panel.** Grew a right-rail mount slot for the
  shared `<LaneSidebar>` component. The chat / SOW / sidebar layout
  is otherwise unchanged.
- **Cargo workspace.** Six new Tauri 2 plugins added:
  `tauri-plugin-websocket`, `tauri-plugin-store`, `tauri-plugin-dialog`,
  `tauri-plugin-shell`, `tauri-plugin-fs`, `tauri-plugin-autostart`.
- **npm workspace.** Repo root now declares `workspaces:
  [desktop, web, packages/*]`. Root `node_modules` only carries the
  shared dev tooling; per-package installs honour the workspace
  resolution path.

#### Fixed

- **Build script regressions from earlier scaffold checkpoints.**
  `tauri-plugin-dialog` now enables the `gtk3` rfd backend by default
  (previously stripped via `default-features = false` which caused
  the rfd build script to panic on Linux). The `_comment_csp` /
  `_comment_externalBin` keys were dropped from `tauri.conf.json`
  because Tauri 2's strict schema rejects unknown fields.

#### Tests

- 12 new Rust unit + integration tests covering discovery,
  transport, lanes, popout. `cargo test` now runs 110 tests across
  4 binaries.
- 20+ new Vitest tests in `@r1/web-components` covering LaneCard
  (6 statuses + interactions) and LaneSidebar (stable ordering +
  diff-only repaint).
- 12 new Playwright e2e specs covering daemon discovery, multi-session
  workdir persistence, lane streaming under sustained load, and
  pop-out lifecycle. CI runs them on macOS-latest, ubuntu-22.04,
  windows-latest.
