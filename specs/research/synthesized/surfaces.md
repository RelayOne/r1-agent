# Synthesized — UI Surfaces (TUI, Web, Desktop)

Combines: RT-TUI-LANES, RT-WEB-UX, RT-DESKTOP-TAURI.

## Cross-surface decisions

### Visual model
Lanes (cognition + tool-use threads) render as **first-class UI primitives** on every surface:
- **TUI**: bubblelayout grid, columns when width ≥ N×32, vertical list otherwise.
- **Web**: right-sidebar "Agents Window" + tile-into-main-pane mode (Cursor 3 Glass pattern).
- **Desktop**: same web UI inside Tauri shell with native folder picker + multi-window pop-out for power users.

### Status vocabulary (shared across surfaces)
| State | Glyph | Color |
|-------|-------|-------|
| Pending | · | gray |
| Running | ▸ | blue |
| Blocked | ⏸ | yellow |
| Done | ✓ | green |
| Errored | ✗ | red |
| Cancelled | ⊘ | dim gray |

Always pair color with glyph (accessibility — RT-TUI-LANES).

### Live-update tempo
- Upstream events (model deltas, tool ticks): ≤200 Hz.
- Surface render: coalesce to 5–10 Hz (200–300 ms windows).
- Per-lane render strings cached in surface state; only re-render the lanes whose data changed (RT-TUI-LANES anti-pattern).

### TUI specifics (spec 4)
- Bubble Tea v2 + Bubbles v2 (`spinner`, `progress`, `viewport`).
- `winder/bubblelayout` for declarative responsive grid.
- lipgloss v2 AdaptiveColor.
- Single fan-in `chan laneTickMsg` + `waitForLaneTick` cmd re-armed each receive (canonical realtime example).
- Keybindings: `1`–`9` jump-to-lane, `tab`/`shift-tab` cycle, `j`/`k` move, `enter` focus mode (65/35 main+peers), `esc` exit, `k` kill (with `y` confirm), `K` kill all, `?` help.
- Reuse existing `internal/tui/` (interactive.go, runner.go, renderer/).

### Web specifics (spec 6)
- Stack: React 18 + Vite + Tailwind + shadcn/ui (matches `desktop/`).
- Markdown: `vercel/streamdown` (graceful partial-Markdown handling, Shiki, KaTeX, Mermaid, rehype-harden).
- AI cards: `@ai-sdk/elements` for tool/reasoning/plan cards, drives via AI SDK 6 `useChat`.
- Layout: 3-column — left=session list, center=chat, right=lanes sidebar.
- Tile mode: 2–4 lanes pinnable into the center pane (Cursor 3 Glass).
- Routing: `/sessions/:id`, `/sessions/:id/lanes/:lane`, deep links work.
- WS auth: `new WebSocket(url, ["r1.bearer", token])` (subprotocol header — only browser-accessible "real header"). Origin/Host strict allowlist.
- CSP: connect-src `ws://127.0.0.1:*`, default-src 'self'.

### Desktop specifics (spec 7)
- Existing Tauri 2 app at `desktop/` (12-phase R1D plan exists; do NOT redo phases — augment).
- Discovery flow:
  1. Try connect to `ws://127.0.0.1:<port>` from `~/.r1/daemon.json`.
  2. If fail: Tauri sidecar via `ShellExt::sidecar` spawning bundled `r1 serve`.
  3. Wizard offers `r1 serve --install` for always-on (kardianos/service).
- Workdir per session: `tauri-plugin-store` (`Store.set(sessionId, { workdir, … })`). NOT localStorage.
- Folder picker: `@tauri-apps/plugin-dialog` `open({ directory: true })`.
- Lane streaming: one `tauri::ipc::Channel<LaneEvent>` per session at 10 Hz (events API "not designed for low latency").
- Pop-out: `WebviewWindow.new()` per pinned lane.

### Surface ↔ Cortex integration
All three surfaces consume the same lane stream from r1d via the lanes-protocol (spec 3):
- Stream-JSON over WS (web/desktop) or stdout NDJSON (TUI when r1 chat-interactive runs locally without daemon).
- Common envelope: `{type: "lane.delta", lane_id, seq, data}`.
- Reconnect replays from `Last-Event-ID` (RT-R1D-DAEMON).

## Risks / gotchas

- **TUI repaint on every event**: explicitly forbidden. Per-lane render cache + diff-only update.
- **Web mixed-content** (Tauri only): `tauri-plugin-websocket` opens from Rust, not webview, sidesteps `https://tauri.localhost` → `ws://` block on Windows.
- **Lane ordering churn**: lanes render in stable order (creation timestamp + lane_id tiebreak). Re-ranking by activity is opt-in; default is stable.
