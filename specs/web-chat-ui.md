<!-- STATUS: ready -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: lanes-protocol, r1d-server -->
<!-- BUILD_ORDER: 6 -->

# web-chat-ui — Cross-Platform Web UI for `r1 serve`

## Overview

A cross-platform web app served by `r1 serve` (spec 5: `r1d-server`) that provides a Claude-chat-style interface to one or more local r1d daemons. Layout follows the **Cursor 3 "Glass" pattern**: three columns with a left **session list** (multi-instance switcher), a center **chat pane** (streaming markdown + tool / reasoning / plan cards), and a right **lanes sidebar** of all active cognition + tool-use lanes. A **tile mode** lets the user pin 2–4 lanes into the center pane for parallel watching. Connects to the daemon at `ws://127.0.0.1:<port>` over the lanes-protocol (spec 3) using subprotocol-token auth. Sources live in a new `web/` directory at repo root (mirroring `desktop/`); built artifacts emit to `internal/server/static/dist/` and ship via the existing `embed.FS` in `internal/server/embed.go`.

This is the **default** UI for r1; the Tauri shell (spec 7) wraps the same React bundle.

## Stack & Versions

| Concern | Library | Pinned version | Notes |
|---|---|---|---|
| Framework | React | `18.3.x` | Matches `desktop/`. |
| Build | Vite | `6.0.x` | Matches `desktop/` toolchain (desktop is on Vite 5; this spec bumps to 6 for SWC + Lightning CSS). |
| Lang | TypeScript | `5.4.x` | `tsc --noEmit` runs in `npm run build`. |
| CSS | Tailwind | `3.4.x` | shadcn baseline. Tailwind v4 is intentionally NOT used (shadcn/ui generators still target v3 in May 2026). |
| Component primitives | shadcn/ui | `latest` (CLI-generated, copied into `web/src/components/ui/`) | Generated, not depended-on. |
| Streaming markdown | `streamdown` (`vercel/streamdown`) | `1.2.x` (pinned exact minor; `~1.2.0` in `package.json`) — **anchor library** | Graceful partial-Markdown, Shiki, KaTeX, `@streamdown/mermaid`, rehype-harden. Partial-markdown handling has subtle regressions across minors — pin exact minor. |
| AI primitives | `@ai-sdk/elements` | `0.0.x` (pinned exact minor; `~0.0.0` in `package.json`) | `Message`, `Tool`, `Reasoning`, `Plan`, `CodeBlock`, `Conversation`, `PromptInput`. Pre-1.0; pinned to avoid breaking minor bumps. |
| Streaming hook | `@ai-sdk/react` (AI SDK 6) | `^6.0.0` | `useChat` with `message.parts` model — maps directly to lanes-protocol envelopes. |
| Routing | `react-router` | `^7.0.0` | Nested routes `daemon → session → lane`. TanStack Router considered; rejected for 5x larger bundle and weaker SSR alignment. |
| State | `zustand` | `^5.0.0` | One store **instance per daemon connection**; daemons cannot share state. |
| WS client | hand-rolled wrapper around native `WebSocket` | n/a | Subprotocol auth + reconnect + Last-Event-ID replay. No socket.io. |
| Diff view | `react-diff-view` | `^3.x` | For lane diff cards. |
| Code highlighting | `shiki` | (transitive via streamdown) | No client-side Prism/highlight.js. |
| Math | `katex` | (transitive via streamdown) | |
| Diagrams | `mermaid` + `@streamdown/mermaid` | latest | |
| Testing — unit | `vitest` | `^2.1.x` | Matches `desktop/`. |
| Testing — e2e | Playwright via Playwright MCP | `^1.49.x` | spec 8 enforces `data-testid` on every interactive element. |
| A11y testing | `@axe-core/playwright` | `^4.10.x` | Run during e2e suite. |
| Node | `>=20` | engines | Matches `desktop/`. |

**Pinned:** `streamdown@~1.2.0` is the load-bearing dependency — every chat surface flows through it. The exact minor is pinned in `package.json` at `~1.2.0` (allows patches, blocks 1.3.x); partial-markdown handling has subtle regressions across minors. `@ai-sdk/elements@~0.0.0` is similarly pinned (pre-1.0; minor bumps may break).

## Existing Patterns to Follow

- **`desktop/`** — mirror the toolchain: `vite.config.ts`, `tsconfig.json`, `vitest.config.ts`, `package.json` (private, type=module, engines node>=20).
- **`internal/server/embed.go`** — `//go:embed static` already wired; this spec adds `static/dist/` as the Vite output target so the existing `RegisterDashboardUI` serves the SPA without code changes.
- **`internal/server/`** — HTTP+WS server (spec 5). API client wraps these endpoints.
- **`docs/decisions/index.md`** D-2026-05-02-02 (React+Vite+Tailwind), D-S4 (Cursor 3 Glass), D-S5 (streamdown + ai-sdk/elements + useChat), D-S6 (subprotocol auth).
- **`internal/concern/`** — concern field projection lives **server-side**; UI just renders the projected payloads.
- **`internal/bus/`** — WAL-backed event bus replays from `Last-Event-ID`; reconnect logic must use this.

## Library Preferences

- **Validation**: `zod` (`^3.23.x`) for all WS payload + form validation.
- **HTTP**: native `fetch` (no axios).
- **Forms**: `react-hook-form` (`^7.53.x`) + `zod` resolver.
- **Icons**: `lucide-react` (shadcn default).
- **Date**: `date-fns` (`^4.x`).
- **DO NOT** add: socket.io, react-markdown, highlight.js, redux, MUI, Chakra, Ant.

## Directory Layout

```
web/
├── package.json                 # name=r1-web, private, type=module
├── vite.config.ts               # build.outDir = '../internal/server/static/dist'
├── tsconfig.json                # strict, paths { "@/*": ["./src/*"] }
├── tailwind.config.ts
├── postcss.config.cjs
├── index.html                   # CSP meta + #root
├── components.json              # shadcn config
├── vitest.config.ts
├── playwright.config.ts
├── public/                      # static assets, favicon
└── src/
    ├── main.tsx                 # ReactDOM.createRoot → <App/>
    ├── App.tsx                  # Router + theme provider + daemon-store provider
    ├── routes/
    │   ├── index.tsx            # /  → daemon picker / new-session landing
    │   ├── sessions.$id.tsx     # /sessions/:id  → 3-column chat
    │   ├── sessions.$id.lanes.$laneId.tsx  # /sessions/:id/lanes/:lane_id  → focused-lane view
    │   └── settings.tsx         # /settings
    ├── components/
    │   ├── layout/              # ThreeColumnShell, LeftRail, RightRail, Header
    │   ├── session/             # SessionList, SessionItem, NewSessionDialog
    │   ├── chat/                # ChatPane, MessageLog, MessageBubble, Composer, StopButton
    │   ├── cards/               # ToolCard, ReasoningCard, PlanCard, DiffCard
    │   ├── lanes/               # LanesSidebar, LaneRow, LaneTile, TileGrid
    │   ├── workdir/             # WorkdirBadge, WorkdirPickerDialog
    │   ├── status/              # StatusBar, StatusDot
    │   └── ui/                  # shadcn-generated primitives (button, dialog, input, …)
    ├── hooks/
    │   ├── useDaemonSocket.ts   # WS lifecycle + reconnect + replay
    │   ├── useChat.ts           # wraps @ai-sdk/react useChat with r1d transport
    │   ├── useLanes.ts          # selector over zustand store
    │   ├── useSession.ts
    │   ├── useWorkdir.ts        # FSA API + IndexedDB persistence + manual fallback
    │   └── useKeybindings.ts    # global hotkey map
    ├── lib/
    │   ├── api/
    │   │   ├── r1d.ts           # public API surface (HTTP + WS) ← see §7
    │   │   ├── http.ts          # fetch wrapper, throws typed R1dError
    │   │   ├── ws.ts            # ResilientSocket class, exponential backoff, Last-Event-ID
    │   │   ├── auth.ts          # subprotocol-token mint + ticket refresh
    │   │   └── types.ts         # zod schemas + inferred TS types for envelopes
    │   ├── store/
    │   │   ├── daemonStore.ts   # zustand store factory (one per daemon)
    │   │   ├── sessionsSlice.ts
    │   │   ├── lanesSlice.ts
    │   │   └── messagesSlice.ts
    │   ├── render/
    │   │   ├── markdown.tsx     # Streamdown wrapper with shared config
    │   │   └── highlight.ts     # shiki theme selection (light/dark/HC)
    │   └── util/
    │       ├── ids.ts
    │       ├── format.ts
    │       └── a11y.ts          # ariaLabel helpers, contrast utils
    ├── styles/
    │   └── globals.css          # tailwind base + shadcn vars + HC theme tokens
    └── test/
        ├── setup.ts             # vitest setup; jsdom + msw
        ├── fixtures/            # canned envelopes
        └── e2e/                 # *.spec.ts Playwright + *.agent.feature.md (spec 8)
```

## Routing Map

| Path | Component | Purpose |
|---|---|---|
| `/` | `routes/index.tsx` | Empty state. Lists known daemons (from `~/.r1/daemons.json` mirror via HTTP). "Open Folder" CTA. New-session dialog. |
| `/sessions/:id` | `routes/sessions.$id.tsx` | 3-column chat for a session. Default view. |
| `/sessions/:id/lanes/:lane_id` | `routes/sessions.$id.lanes.$laneId.tsx` | Focused-lane view (single lane fills center pane; chat collapses to right rail). Deep-linkable from sidebar pin or external share. |
| `/settings` | `routes/settings.tsx` | Model defaults, lane filters, theme, contrast mode, keybindings. |
| `*` | 404 page with link back to `/` | |

Nested routing: route loaders fetch session metadata via `r1d.getSession(id)`; failure → redirect to `/`.

## Component Catalog

Each component lives at `web/src/components/<group>/<Name>.tsx`, exports a single default component, has a colocated `<Name>.test.tsx` (Vitest) and `<Name>.stories.tsx` (Storybook MCP — spec 8) in the same directory, and **carries `data-testid` on every interactive sub-element** (spec 8 lint requires this). Concrete paths (group → file): `layout/ThreeColumnShell.tsx`, `session/SessionList.tsx`, `session/SessionItem.tsx`, `session/NewSessionDialog.tsx`, `chat/ChatPane.tsx`, `chat/MessageLog.tsx`, `chat/MessageBubble.tsx`, `chat/Composer.tsx`, `chat/StopButton.tsx`, `cards/ToolCard.tsx`, `cards/ReasoningCard.tsx`, `cards/PlanCard.tsx`, `cards/DiffCard.tsx`, `lanes/LanesSidebar.tsx`, `lanes/LaneRow.tsx`, `lanes/LaneTile.tsx`, `lanes/TileGrid.tsx`, `workdir/WorkdirBadge.tsx`, `workdir/WorkdirPickerDialog.tsx`, `status/StatusBar.tsx`, `status/HighContrastToggle.tsx`, `layout/ThemeProvider.tsx`. Each `<Name>.test.tsx` and `<Name>.stories.tsx` sits next to its `.tsx`.

| Component | Props (high-level) | Responsibilities |
|---|---|---|
| `<SessionList>` | `daemonId` | Subscribes to `sessions` slice; renders `SessionItem` rows; new-session button at top; collapsible. `data-testid="session-list"`. |
| `<SessionItem>` | `session, active` | Status dot + title + last-activity time; click → router.navigate. |
| `<ChatPane>` | `sessionId` | Hosts `MessageLog` + `Composer`. Switches to `TileGrid` when `tileMode` is on. |
| `<MessageLog>` | `messages` | Virtualised list (react-virtual). Renders one `MessageBubble` per message; renders cards for tool / reasoning / plan parts. |
| `<MessageBubble>` | `message` | Role-styled container. Wraps `<Streamdown>` from `lib/render/markdown.tsx` for streamed content. |
| `<ToolCard>` | `part` | Collapsible (default collapsed when `state==="output-available"`). Syntax-highlighted input + output. Copy button. From `@ai-sdk/elements` `Tool`. |
| `<ReasoningCard>` | `part` | Collapsible, dim. From `@ai-sdk/elements` `Reasoning`. Shimmer while streaming. |
| `<PlanCard>` | `part` | Live-updates from `PlanUpdateLobe` events. Checklist with status icons. From `@ai-sdk/elements` `Plan`. |
| `<DiffCard>` | `lane` | `react-diff-view` rendering of consolidated lane diff. |
| `<Composer>` | `onSend, sending` | shadcn `Textarea` + send button. Cmd/Ctrl+Enter sends. Disabled during streaming. |
| `<StopButton>` | `onStop` | Replaces send during streaming. Sends `{type:"interrupt"}` over WS. |
| `<LanesSidebar>` | `sessionId` | Right rail. Lists active lanes; status-dot + label + progress glyph; Pin button per lane; collapsible. |
| `<LaneRow>` | `lane, pinned, active` | Click → focus lane. Pin → adds to `tileGrid`. Kill → confirmation dialog. |
| `<LaneTile>` | `laneId, paneIndex` | One pinned lane in the center pane. Renders live tool-use output via cached render-string. Header has unpin + pop-out. |
| `<TileGrid>` | `tileIds[]` | 2/3/4-pane CSS grid; auto-layout (1×2, 1×3, 2×2). Drag-handles to reorder (HTML5 DnD; `aria-grabbed`/`aria-dropeffect` on tile headers; keyboard alternative `Cmd+Shift+←/→`). Per-tile collapse toggle in tile header (collapsed tile shrinks to 32px header strip; CSS grid auto-rows reflow; collapsed state persisted per-session in zustand `ui` slice). Unpin removes from grid; double-click tile header to pop out to focused-lane route `/sessions/:id/lanes/:lane_id`. |
| `<WorkdirBadge>` | `sessionId` | Header chip showing current workdir basename + tooltip with full path. Click → opens picker. |
| `<WorkdirPickerDialog>` | `onPick` | FSA `showDirectoryPicker()` if available; else manual path entry with autocomplete from `r1d.listAllowedRoots()`. |
| `<NewSessionDialog>` | `daemonId` | Pick model + workdir + system-prompt preset. zod-validated. |
| `<StatusBar>` | — | Bottom-of-screen strip: connection state, latency, current cost, lane counts. Live-updating. |
| `<HighContrastToggle>` | — | Settings toggle; persists in localStorage; CSS class on `<html>`. |
| `<ThemeProvider>` | — | light / dark / HC; honors `prefers-color-scheme`; passes to Streamdown config. |

All components use shadcn primitives (`Button`, `Dialog`, `DropdownMenu`, `Tooltip`, `ScrollArea`, `Tabs`, `Badge`) — generated into `web/src/components/ui/` via shadcn CLI.

## API Client Wrapper — `web/src/lib/api/r1d.ts`

Single public surface for all daemon I/O. All payloads validated with zod schemas in `lib/api/types.ts`. All methods reject with a typed `R1dError` (taxonomy mirrors `internal/stokerr/`).

### Construction

```ts
const client = new R1dClient({
  baseUrl: 'http://127.0.0.1:7777',
  wsUrl:   'ws://127.0.0.1:7777/ws',
  token:   await mintToken(),     // from /auth/ws-ticket
});
```

### HTTP Methods

| Method | HTTP | Purpose |
|---|---|---|
| `listDaemons()` | `GET /api/daemons` | All known r1d daemons reachable from this origin. |
| `listSessions()` | `GET /api/sessions` | All sessions on the connected daemon. |
| `getSession(id)` | `GET /api/sessions/:id` | Session metadata (workdir, model, status). |
| `createSession(req)` | `POST /api/sessions` | New session. Body: `{model, workdir, systemPromptPreset}`. |
| `setSessionWorkdir(id, path)` | `PATCH /api/sessions/:id` | Change workdir. Server validates against `--allowed-roots`. |
| `listLanes(sessionId)` | `GET /api/sessions/:id/lanes` | Snapshot of lanes (also streamed via WS). |
| `killLane(sessionId, laneId)` | `POST /api/sessions/:id/lanes/:lane_id/kill` | Cancel a lane. |
| `getSettings()` | `GET /api/settings` | User settings (server-persisted). |
| `putSettings(s)` | `PUT /api/settings` | Persist settings. |
| `mintWsTicket()` | `POST /auth/ws-ticket` | Short-lived ticket (~30 s) for WS subprotocol. |
| `listAllowedRoots()` | `GET /api/allowed-roots` | For workdir picker autocomplete fallback. |

### WebSocket Methods

WS auth (D-S6): `new WebSocket(wsUrl, ["r1.bearer", token])`. Server validates `Sec-WebSocket-Protocol`, echoes `r1.bearer`, and **strictly checks `Origin` and `Host`** against the configured allowlist (loopback + dev-server ports + served-from-self origin).

| Method | Direction | Purpose |
|---|---|---|
| `connect()` | client → server upgrade | Opens WS with subprotocol token. Returns `Promise<void>` resolved on `open`. |
| `subscribe(sessionId)` | client → server | `{type:"subscribe", sessionId, lastEventId?}` — server replays missed events. |
| `unsubscribe(sessionId)` | client → server | |
| `sendMessage(sessionId, text)` | client → server | `{type:"chat", sessionId, content}` |
| `interrupt(sessionId)` | client → server | `{type:"interrupt", sessionId}` — cancels current turn (D-C4 drop-partial). |
| `onEnvelope(handler)` | server → client | Called for every `{type, …}` envelope. |
| `close()` | both | Clean close with code 1000. |

Envelope types (zod-validated, mirrored from lanes-protocol spec 3):

- `lane.delta` — `{lane_id, seq, data}` — incremental render-string update.
- `lane.status` — `{lane_id, state}` — status transition.
- `lane.created` / `lane.killed` — lifecycle.
- `message.part` — `{messageId, part}` — streamed message part (text / tool / reasoning / plan).
- `message.complete` — terminal.
- `session.updated` — workdir / model / cost changes.
- `auth.expiring_soon` — pre-emptive ticket refresh trigger (~60 s before expiry).
- `error` — `{code, message, retryable}`.

Each envelope carries a monotonic `seq` (per session) used by Last-Event-ID replay.

## WebSocket Reconnect Strategy

Implemented in `lib/api/ws.ts` `ResilientSocket`.

1. **Backoff**: exponential with jitter — `min(8000, 250 * 2^n) ± rand(0, 250) ms`. Reset on successful `open` + first envelope received.
2. **State machine**: `idle → connecting → open → reconnecting → closed`. Exposed via `socket.state` + `onStateChange`.
3. **Last-Event-ID replay**: store last-seen `seq` per session in zustand. On reconnect, send `{type:"subscribe", sessionId, lastEventId: seq}`. Server replays from bus WAL.
4. **Token refresh**: on close code `4401` (auth expired), call `mintWsTicket()`, then reconnect. Also pre-emptive on `auth.expiring_soon` envelope.
5. **Heartbeat / watchdog**: 30 s no-traffic → send `{type:"ping"}`; 30 s no `pong` → force close + reconnect (matches D-C4 30 s ping watchdog).
6. **Drop-partial on interrupt**: on `interrupt`, drain any in-flight `message.part` events; never persist partial assistant message (D-C4).
7. **Hard cap**: 10 reconnect attempts before surfacing a `<ConnectionLostBanner>` with manual retry.
8. **Multi-daemon**: one `ResilientSocket` per daemon; instances tracked in a `Map<daemonId, ResilientSocket>` inside the `daemonStore` factory.

Anti-patterns explicitly forbidden:
- No long-lived token in URL query.
- No silent retry loop without surfacing state to UI.
- No re-render on every envelope — coalesce to 5–10 Hz with `requestAnimationFrame` batching (mirrors RT-TUI-LANES anti-pattern).

## Build Pipeline

1. `cd web && npm install`.
2. `npm run build` → runs `tsc --noEmit && vite build`.
3. Vite `build.outDir` = `../internal/server/static/dist` (relative to `web/`). `emptyOutDir: true`.
4. Outputs `index.html` + hashed `assets/*.{js,css}`.
5. The existing `internal/server/embed.go` (`//go:embed static`) picks up `static/dist/**` automatically. **`RegisterDashboardUI` is updated in spec 5 (r1d-server) to serve `/dashboard/*` from `static/dist/`** while keeping the legacy single-file dashboard at `/legacy-dashboard` during the transition.
6. `go build ./cmd/r1` produces a binary that includes the SPA.
7. CI gate adds `cd web && npm run build && cd .. && go build ./cmd/r1` to the existing build/test/vet matrix.
8. Dev workflow: `cd web && npm run dev` runs Vite on `:5173`; the SPA connects to the daemon at `ws://127.0.0.1:7777/ws` using the subprotocol token (cross-origin during dev — daemon allowlist must include `:5173`).
9. CSP enforced via `<meta http-equiv="Content-Security-Policy" …>` in `index.html` — `default-src 'self'; connect-src 'self' ws://127.0.0.1:* http://127.0.0.1:*; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; script-src 'self'; worker-src 'self' blob:; frame-ancestors 'none';`.
10. `npm run typecheck` separately runs `tsc --noEmit` (matches `desktop/`).

## Test Plan

### Unit (Vitest + jsdom + MSW)

- `useDaemonSocket` — open / close / reconnect / replay / token-refresh / 4401 path.
- `ResilientSocket` — backoff curve, jitter bounds, state transitions, heartbeat watchdog.
- `r1d.ts` — every method validates request + response with zod; throws `R1dError` on schema mismatch.
- `<MessageLog>` — renders text, tool, reasoning, plan parts; collapses tool cards by default once `output-available`.
- `<LanesSidebar>` — renders all status states; pin button calls store action; coalesces re-renders to ≤10 Hz under a 200 Hz event firehose (jest fake timers).
- `<TileGrid>` — 1/2/3/4-pane layouts; reorder; unpin removes pane.
- `<WorkdirPickerDialog>` — FSA path + manual fallback path; persists handle to IndexedDB; gracefully handles permission revocation.
- `<NewSessionDialog>` — zod validation surfaces field errors; submit calls `r1d.createSession` with correct body.
- `<StopButton>` — sends `interrupt` envelope; UI returns to idle; partial assistant message dropped.
- Theme + HC mode — class toggling; Streamdown reads correct shiki theme.

### End-to-end (Playwright + Playwright MCP — spec 8)

`*.agent.feature.md` files in `web/src/test/e2e/` using Gherkin-flavored markdown (D-A4):

- **happy-path-chat.agent.feature.md** — open `/`, create session, send message, see streamed response, verify tool card renders.
- **multi-instance-switch.agent.feature.md** — start 2 daemons, switch via left rail Cmd+1/Cmd+2, verify isolation (no shared state, separate cost counters).
- **lane-pin-tile-mode.agent.feature.md** — pin 2 lanes, verify TileGrid 1×2; pin a 3rd → 1×3; pin a 4th → 2×2; unpin one → reflows.
- **interrupt-mid-stream.agent.feature.md** — send long message, click Stop, verify partial dropped + composer re-enabled.
- **reconnect-replay.agent.feature.md** — kill daemon mid-stream, restart, verify Last-Event-ID replay catches up the message log without duplicates.
- **workdir-picker-fsa.agent.feature.md** — FSA path on Chromium; manual path on Firefox.
- **deep-link-lane.agent.feature.md** — direct nav to `/sessions/:id/lanes/:lane_id` works without going through `/`.
- **a11y-keyboard-only.agent.feature.md** — full session creation + send + lane pin using only keyboard.
- **csp-no-violations.agent.feature.md** — load every route; assert zero CSP violations in `console.error`.

`@axe-core/playwright` runs on every route in CI; zero serious / critical violations allowed.

### Storybook MCP (spec 8)

Each component in the catalog has a `*.stories.tsx` file; Storybook MCP exercises every story prop variation.

## Accessibility Checklist

- [ ] Every interactive control has an `aria-label` or visible text label.
- [ ] Every interactive element has a `data-testid` (spec 8 lint).
- [ ] Tab order matches visual order; no `tabindex > 0`.
- [ ] All dialogs trap focus and restore on close (shadcn defaults; verify with axe).
- [ ] Lane status indicated by **glyph + color** (D-S1 — never color-only).
- [ ] All shadcn `Button` and `Tooltip` keyboard activation works (Enter / Space).
- [ ] Skip-to-main-content link at top of layout.
- [ ] High-contrast theme available (≥7:1 contrast for text); toggleable in Settings; persists.
- [ ] Reduced motion: respect `prefers-reduced-motion` — disable Composer shimmer + Streamdown stream animations.
- [ ] Screen-reader announcements for streaming via `aria-live="polite"` on the active assistant bubble; `aria-live="assertive"` for errors.
- [ ] Keyboard shortcuts documented in `/settings`; `?` opens the cheat-sheet dialog.
- [ ] Color tokens routed through Tailwind theme; no hard-coded hex outside `globals.css`.

## Boundaries — What NOT To Do

- Do **not** build a native desktop shell. That is spec 7 (`desktop-cortex-augmentation`).
- Do **not** add an MCP tool surface here. That is spec 8 (`agentic-test-harness`). This UI must, however, **respect the `data-testid` lint** that spec 8 enforces.
- Do **not** modify `internal/server/embed.go` other than what spec 5 already prescribes.
- Do **not** introduce a second routing library, second state library, or second markdown renderer.
- Do **not** ship a service worker yet (offline mode is out of scope; would interfere with dev WS).
- Do **not** ship localStorage persistence of workdir handles — use IndexedDB (FSA handles cannot serialize to localStorage).
- Do **not** wire authentication beyond loopback subprotocol-token (no SSO, no OAuth).
- Do **not** add SSR / RSC. SPA only; the daemon is local.
- Do **not** depend on `react-markdown` — Streamdown replaces it.
- Do **not** auto-scroll the message log when the user has scrolled up (sticky-bottom only).

## Out of Scope

- Native desktop shell (spec 7).
- MCP tool surface for the UI (spec 8).
- Cloud daemon support (future spec).
- Multi-tenant auth.
- Mobile responsive layouts below 768 px (chat is hidden, sidebar collapses; lanes view degrades gracefully but no specific design).
- Internationalization (English only in v1).

## Acceptance Criteria

- WHEN a user runs `r1 serve` and opens `http://127.0.0.1:<port>/` THE SYSTEM SHALL render the SPA from the embedded `static/dist/` bundle without network calls beyond `127.0.0.1`.
- WHEN a user clicks "New Session" THE SYSTEM SHALL show a dialog with model + workdir + system-prompt-preset fields and create the session via `POST /api/sessions`.
- WHEN a session is active and the daemon emits `lane.delta` events at 200 Hz THE SYSTEM SHALL coalesce UI repaints to ≤10 Hz with no dropped state (last-event-id wins).
- WHEN a user pins 2, 3, or 4 lanes THE SYSTEM SHALL render the center pane as a 1×2, 1×3, or 2×2 tile grid respectively with each tile streaming its lane independently.
- WHEN the user clicks Stop during a streaming turn THE SYSTEM SHALL send `{type:"interrupt"}`, drop the partial assistant message, and re-enable the composer within 200 ms.
- WHEN the WS closes with code 4401 THE SYSTEM SHALL mint a fresh ticket and reconnect with `Last-Event-ID` replay, with no message duplication.
- WHEN a route fails to validate against its zod schema THE SYSTEM SHALL log a typed `R1dError` and surface a recoverable error toast — never a white screen.
- WHEN axe is run against any route THE SYSTEM SHALL have zero serious/critical accessibility violations.

## Implementation Checklist

1. [ ] Create `web/` directory at repo root with `package.json` (`name=r1-web`, `private`, `type=module`, `engines.node>=20`).
2. [ ] Add `vite.config.ts` with `build.outDir='../internal/server/static/dist'`, `base='/'`, React SWC plugin, Tailwind plugin.
3. [ ] Add `tsconfig.json` (strict, `paths: { "@/*": ["./src/*"] }`).
4. [ ] Add `tailwind.config.ts` + `postcss.config.cjs` + `src/styles/globals.css` (Tailwind base + shadcn vars + HC tokens).
5. [ ] Add `vitest.config.ts` (jsdom env, `setupFiles: ['./src/test/setup.ts']`).
6. [ ] Add `playwright.config.ts` (chromium + firefox + webkit; baseURL `http://127.0.0.1:7777`).
7. [ ] Add `index.html` with CSP meta tag exactly as in §Build Pipeline step 9.
8. [ ] Initialise shadcn/ui via `npx shadcn@latest init`; generate components: `button`, `dialog`, `dropdown-menu`, `input`, `textarea`, `scroll-area`, `tabs`, `tooltip`, `badge`, `toast`, `command`, `select`, `separator`, `skeleton`. Commit generated `components/ui/`.
9. [ ] Install runtime deps with the exact pins in §Stack & Versions (React 18.3, Vite 6, Tailwind 3.4, react-router 7, zustand 5, streamdown ^1, @ai-sdk/elements latest, @ai-sdk/react ^6, react-diff-view 3, lucide-react, date-fns 4, zod 3, react-hook-form 7).
10. [ ] Install dev deps: `vitest@^2.1`, `@vitest/coverage-v8`, `jsdom`, `@testing-library/react`, `@testing-library/user-event`, `msw`, `@playwright/test`, `@axe-core/playwright`, `typescript@^5.4`, `@types/react`, `@types/react-dom`.
11. [ ] Create `src/lib/api/types.ts` with zod schemas for every envelope listed in §API Client Wrapper and exported TS types.
12. [ ] Create `src/lib/api/http.ts` (fetch wrapper, throws typed `R1dError` on non-2xx + zod failure).
13. [ ] Create `src/lib/api/auth.ts` with `mintWsTicket()` POSTing to `/auth/ws-ticket`; cache result; refresh on `auth.expiring_soon`.
14. [ ] Create `src/lib/api/ws.ts` `ResilientSocket` with state machine (idle/connecting/open/reconnecting/closed), exponential backoff + jitter (250 ms → 8 s cap), heartbeat (30 s ping / 30 s pong watchdog), Last-Event-ID replay, 4401 → re-mint ticket → reconnect, 10-attempt hard cap.
15. [ ] Create `src/lib/api/r1d.ts` `R1dClient` exposing every method in §API Client Wrapper.
16. [ ] Create `src/lib/store/daemonStore.ts` zustand factory; one store instance per daemon connection; slices: sessions, lanes, messages, settings, ui (tile-pinned ids, sidebar collapsed flags, theme).
17. [ ] Create `src/hooks/useDaemonSocket.ts` — owns the `ResilientSocket` lifecycle; routes envelopes into store slices.
18. [ ] Create `src/hooks/useChat.ts` wrapping `@ai-sdk/react` `useChat` with the custom transport that sends/receives via `useDaemonSocket`.
19. [ ] Create `src/hooks/useLanes.ts`, `useSession.ts`, `useWorkdir.ts`, `useKeybindings.ts`.
20. [ ] Create `src/lib/render/markdown.tsx` Streamdown wrapper (shared shiki theme, GFM, KaTeX, Mermaid, rehype-harden).
21. [ ] Implement `<ThemeProvider>` honoring `prefers-color-scheme`, light/dark/HC, persisted in localStorage; passes shiki theme into Streamdown wrapper.
22. [ ] Implement `<ThreeColumnShell>` layout with collapsible left + right rails (shadcn `Resizable`-style); persists collapse state per-daemon.
23. [ ] Implement `<SessionList>` + `<SessionItem>` with status dot, last-activity, click-to-route; `data-testid="session-list-item-<id>"`.
24. [ ] Implement `<NewSessionDialog>` (model + workdir + system-prompt preset; zod + react-hook-form).
25. [ ] Implement `<ChatPane>` switching between `<MessageLog>+<Composer>` and `<TileGrid>` based on `tileIds.length > 0`.
26. [ ] Implement `<MessageLog>` with `react-virtual`; sticky-bottom scroll; aria-live polite on active bubble.
27. [ ] Implement `<MessageBubble>` rendering text via Streamdown; renders cards for tool / reasoning / plan parts.
28. [ ] Implement `<ToolCard>` (collapsible, default-collapsed once `output-available`; copy button; syntax-highlighted input + output via Streamdown).
29. [ ] Implement `<ReasoningCard>` (collapsible, dim, shimmer while streaming; respects reduced-motion).
30. [ ] Implement `<PlanCard>` live-updating from `PlanUpdateLobe` parts.
31. [ ] Implement `<DiffCard>` using `react-diff-view`; consolidated per-lane diff.
32. [ ] Implement `<Composer>` (Textarea + Send; Cmd/Ctrl+Enter sends; disabled during streaming; `aria-label="Compose message"`).
33. [ ] Implement `<StopButton>` swap with Send during streaming; sends `interrupt` envelope; drops partial.
34. [ ] Implement `<LanesSidebar>` + `<LaneRow>` (status dot + label + progress glyph + Pin + Kill); ≤10 Hz coalesced rerender.
35. [ ] Implement `<LaneTile>` rendering live tool-use output via cached render-string (diff-only update).
36. [ ] Implement `<TileGrid>` with 1×2 / 1×3 / 2×2 auto-layout; HTML5 drag-and-drop reorder on tile headers (with `aria-grabbed`/`aria-dropeffect` + `Cmd+Shift+←/→` keyboard alternative); per-tile collapse toggle (chevron in header; collapsed tile = 32 px header strip, CSS `grid-auto-rows: minmax(min-content, 1fr)` reflow; collapsed-ids persisted in zustand `ui` slice); unpin button per tile; double-click header pops out to `/sessions/:id/lanes/:lane_id`.
37. [ ] Implement `<WorkdirBadge>` + `<WorkdirPickerDialog>` (FSA `showDirectoryPicker()`; IndexedDB persistence; manual path entry fallback with `r1d.listAllowedRoots()` autocomplete).
38. [ ] Implement `<StatusBar>` (connection state + latency + cost + lane counts).
39. [ ] Implement `<HighContrastToggle>` and Settings page (model defaults, lane filters, theme, keybindings cheat-sheet).
40. [ ] Wire global keybindings: Cmd+1..9 daemon switch, Cmd+Shift+S toggle daemon rail, `?` cheat-sheet, `/` focus composer, `Esc` exit focused-lane view, `Cmd+Enter` send.
41. [ ] Add route files in `src/routes/` and wire `react-router` v7 with nested `daemon → session → lane` structure.
42. [ ] Add 404 route + `<ConnectionLostBanner>` for hard-cap reconnect failures.
43. [ ] Add zero-CSP-violation enforcement: load every route in Playwright, assert no `console.error` matching `Content Security Policy`.
44. [ ] Add `@axe-core/playwright` to e2e config; fail on serious/critical findings on every route.
45. [ ] Author Vitest unit tests for every component listed in §Test Plan; achieve ≥80% statements coverage on `src/components/` and `src/lib/`.
46. [ ] Author the nine `*.agent.feature.md` Playwright e2e flows listed in §Test Plan.
47. [ ] Author Storybook stories for every component (spec 8 dependency).
48. [ ] Add `npm run build` script (`tsc --noEmit && vite build`); verify outputs land in `internal/server/static/dist/`.
49. [ ] Add `npm run dev` (vite), `npm run test` (vitest run), `npm run test:e2e` (playwright), `npm run typecheck` (tsc --noEmit), `npm run lint` (eslint with shadcn config).
50. [ ] Update root CI workflow to run `cd web && npm ci && npm run build && npm run test` before the existing `go build ./cmd/r1 && go test ./... && go vet ./...` gate.
51. [ ] Verify `internal/server/embed.go` picks up the new `static/dist/` (no Go code change required); add a Go test in `internal/server/embed_test.go` asserting `index.html` is served at `/` and at `/dashboard`.
52. [ ] Add a smoke test `cmd/r1/serve_smoke_test.go` that boots `r1 serve` on a random loopback port, fetches `/`, asserts status 200 + `text/html`, and asserts CSP header echoes the meta CSP.
53. [ ] Add `data-testid` lint rule (custom eslint rule under `web/eslint-rules/`) — every interactive JSX element must have one; CI fails otherwise. (Spec 8 will consume this.)
54. [ ] Run full local QA pass: build, all tests, all e2e flows on chromium + firefox + webkit, axe clean, manual keyboard-only walkthrough, manual high-contrast walkthrough.
55. [ ] Update `docs/decisions/index.md` with any decisions discovered during build (e.g. resolution on TanStack Router rejection, exact streamdown minor pin chosen).

## Risks / Gotchas

- **Vite 6 + Tailwind 3 compatibility**: Tailwind 4 is GA but shadcn generators in May 2026 still emit v3 tokens. Stay on v3 until shadcn migrates; do not mix.
- **Streamdown partial-markdown regressions** between minor versions — pin exact minor after integration.
- **WS subprotocol auth**: server (spec 5) MUST echo exactly one accepted protocol or browsers will reject; integration test must verify.
- **Origin/Host strictness during dev**: Vite dev server on `:5173` is cross-origin to daemon `:7777`. Daemon `--allowed-origins` must include `http://127.0.0.1:5173` or dev fails silently.
- **FSA permission revocation**: Chrome can revoke directory handles between sessions; the picker must detect and re-prompt rather than crash.
- **Re-render storm**: 200 Hz lane updates without coalescing will pin a CPU core. Enforce 5–10 Hz coalescing in the store middleware (D-S2).
- **Lane order churn**: render lanes in stable order (creation timestamp + lane_id tiebreak); re-rank-by-activity is opt-in only (cross-surface decision in surfaces.md).
