# TypeScript / JavaScript Stub & Gap Audit — `web/` and `desktop/`

**Scope:** `/home/eric/repos/r1-agent/web/` and `/home/eric/repos/r1-agent/desktop/`
(skipping `node_modules/`, `dist/`, `build/`, `.next/`, `target/`, `src-tauri/target/`).

**Snapshot summary**

| Metric | Count |
|---|---|
| `throw new Error("not implemented")` (or "unimplemented" / "not yet implemented") | **0** |
| `as any` in production code (excluding `*.test.tsx`, `*.stories.tsx`) | **0** |
| `@ts-ignore` / `@ts-expect-error` / `@ts-nocheck` | **0** |
| `// eslint-disable*` directives | **0** |
| `console.log("TODO")` patterns | **0** |
| Production `console.*` calls | **2** (one in desktop main; one structured TODO log inside `ipc-stub.ts`) |
| `alert(…)` calls (UX-bad blocking dialogs) | **15** (all in desktop panels) |
| `as unknown as <T>` casts | **15** (workdir/IDB shims + per-panel IPC plumbing) |
| `invokeStub(…)` call sites in desktop production code | **75** |

The codebases are unusually clean of TS escape hatches — the real defects are
**unwired surfaces**, **placeholder root entries**, and **load-bearing reliance
on a stub IPC shim** (`desktop/src/ipc-stub.ts`) whose entire dev path returns
`empty` values until Tauri is wired.

---

## Findings

| File:Line | Severity | Category | What's stubbed | Effort |
|---|---|---|---|---|
| `web/src/main.tsx:15-26` | **HIGH** | Stub function bodies / Spec 6 residual | SPA entry renders only a placeholder `<h1>r1</h1>` — no `<App>`, no `<ThemeProvider>`, no daemon-store provider, no router. Spec items 21, 22, 41 (App, ThemeProvider, react-router wiring) never reach production. | L |
| `web/src/` (whole tree) | **HIGH** | Spec 6 residual | **Missing `App.tsx`** — spec §Directory Layout requires `src/App.tsx` with Router + theme provider + daemon-store provider. Only referenced inside `routes/index.test.tsx:27` and `routes/index.stories.tsx:75` as a local helper. No production caller of `buildRouter`. | L |
| `web/src/routes/index.tsx:78-117` | **HIGH** | Unwired components / Spec 6 residual | `buildRoutes` / `buildRouter` are pure factories that demand a `RouteRenderers` argument; **no production code passes one in**. Spec §Routing Map files (`sessions.$id.tsx`, `sessions.$id.lanes.$laneId.tsx`, `settings.tsx`) are not present at the file-tree level — only the renderers map. | M |
| `web/src/hooks/useChat.ts:97-102` | **HIGH** | Stub function body | `clearError` is explicitly a no-op stub: comment says "Hook is a no-op stub so consumers can still call clearError()". `error` field is hardcoded `undefined` (line 107). Per-session error never reaches the UI. | S |
| `web/src/lib/store/daemonStore.ts:107` | **MED** | Spec residual | Comment marks `tileMode` as "(derived from tilePinnedBySession.length > 0 — kept here for tests.)" — no test consumes it; dead state shape annotation. | S |
| `web/src/lib/render/` (missing `highlight.ts`) | **MED** | Spec 6 residual | Spec §Directory Layout calls for `lib/render/highlight.ts` (shiki theme selection light/dark/HC). Folder ships only `markdown.tsx`. Theme is plumbed via `markdown.tsx` defaults instead. | S |
| `web/src/lib/util/` | **MED** | Spec 6 residual | Spec §Directory Layout requires `lib/util/{ids.ts, format.ts, a11y.ts}`. Directory does not exist. Single `lib/utils.ts` (`cn` only) covers it via convention. | S |
| `web/src/lib/store/` (missing slices) | **MED** | Spec 6 residual | Spec §Directory Layout lists `sessionsSlice.ts`, `lanesSlice.ts`, `messagesSlice.ts`. All folded into the monolithic `daemonStore.ts` (works, but spec contract not honored). | M |
| `web/src/test/fixtures/` | **MED** | Spec 6 residual | Spec §Directory Layout requires `test/fixtures/` for canned envelopes. Only `test/testdata/graph-50.json` (graph viewer fixture, unrelated). No envelope fixtures shipped. | S |
| `web/src/components/workdir/WorkdirPicker.tsx:36-102` | **MED** | Duplicated implementation | Two parallel IndexedDB persistence layers exist: `WorkdirPicker.tsx` ships its own `persistWorkdirHandle`/`loadWorkdirHandle`, *and* `useWorkdir.ts:225-270` implements `idbGet/idbPut/idbDel` against a different schema (`r1-web` vs `r1-workdir`, store names differ). Both shipped; neither composed. | M |
| `web/src/components/workdir/WorkdirPicker.tsx:43-49` | **MED** | TS escape | `IdbLike` interface plus `indexedDB as unknown as IdbLike` — DOM types are correct here; cast bypasses for no real reason. | S |
| `web/src/components/workdir/WorkdirPicker.tsx:163,204` | **MED** | TS escape | `(window as unknown as { showDirectoryPicker?: unknown })` — duplicated in `useWorkdir.ts:25-32` declaration; should consolidate the FSA type into one place. | S |
| `web/src/lib/store/daemonStore.ts:159` | **LOW** | TS escape | `clearTimeout(handle as unknown as ReturnType<typeof setTimeout>)` — needed because `unknown` opacity from rAF/timeout dual API. Justified. | — |
| `web/src/components/chat/ReasoningCard.tsx:46-50` | **LOW** | Try/catch as control-flow | `try { themedReducedMotion = useTheme().reducedMotion } catch {…}` swallows the "useTheme outside provider" error. ThemeProvider is required by spec 21; this catches a bug rather than handling a real condition. | S |
| `web/src/components/StatusBar.tsx:95-96` | **LOW** | Subscription side-effect | `useStore(store, (s) => s.lanes.byKey)` and `(s) => s.sessions.byId)` are intentional re-render triggers whose return values are discarded; the comment admits "we don't need the slice values". Works, but smells. | S |
| `desktop/src/main.ts:64-76` | **HIGH** | Unwired components | The PANELS array does not include `daemon-status.ts` (224 lines) — DaemonStatus title-bar pill is never instantiated. Spec 7 §5 + checklist item 26 require it on the title bar. | M |
| `desktop/src/components/discovery-wizard.tsx` (entire file) | **HIGH** | Unwired components / Spec 7 residual | `DiscoveryWizard` (180 lines) is exported but no module imports it. Spec 7 §5 lifecycle step 4 + checklist item 28 require it as the first-launch dialog when `~/.r1/daemon.json` is absent. Currently only `desktop/src/onboarding/onboarding.ts` runs first-launch. | M |
| `desktop/src/panels/session-ipc-test.ts` (296 lines) | **HIGH** | Unwired components | Exported as a panel but never registered in `main.ts` and not referenced anywhere. Likely dev-only; if so, it ships dead code in production bundle. | S |
| `desktop/src/ipc-stub.ts:80-91` | **HIGH** | Mocked-in-prod / Spec 7 residual | The entire desktop dev path falls through to `console.info("TODO …— scaffold stub returning empty"); return empty;`. **Every** panel sees `[]` / empty objects unless `window.__TAURI__` exists. R1D-1.2 (real Tauri wiring) is the work this stub waits on. | M |
| `desktop/src/onboarding/onboarding.ts:415-427` | **HIGH** | Mocked-in-prod | `onboarding_start_demo` and `onboarding_pick_data_dir` go through `invokeStub` with `R1D-11` phase — both return canned `{ ok: true }` / `{ path, valid: true }` defaults. **API key + provider selection is never persisted** beyond local component state; on page reload the onboarding marks the user "onboarded" and the keys are dropped. | M |
| `desktop/src/onboarding/onboarding.ts:44` | **LOW** | Code smell | `const HINT_ATTR = "place" + "holder";` — string concatenation to dodge a linter rule against literal `placeholder`. Either lint config is wrong or this is hiding intent. | S |
| `desktop/src/panels/cost-panel.ts:54-60` | **HIGH** | Mocked-in-prod | `cost_get_current` returns the literal `EMPTY_SNAPSHOT` (`{usd:0, tokens_in:0, tokens_out:0, as_of:""}`) until Tauri is wired. Renders "$0.00" forever in non-Tauri mode. | S |
| `desktop/src/panels/sow-tree.ts:56-62` | **HIGH** | Mocked-in-prod | `session_list` and `session_tree` return `[]` / `{nodes:[]}` empty fixtures via `invokeStub`. Every dev mount shows "No sessions yet." | S |
| `desktop/src/panels/approval-queue.ts:65` | **HIGH** | Mocked-in-prod | `approval_list` returns `[]`. Auto-refreshes a 5 s timer that always renders empty. | S |
| `desktop/src/panels/scheduler.ts` | **HIGH** | Mocked-in-prod | Every IPC verb (`schedule_list`, `schedule_upsert`, `schedule_delete`, `schedule_run_now`) returns canned defaults. CRUD UI is fully wired but the backend round-trip is mocked. | S |
| `desktop/src/panels/scheduler.ts:224,236,245` | **MED** | UX defect | Three `alert(…)` calls on failure. Modal browser alerts in a Tauri WebView are jarring; spec doesn't permit them. Should be toast/inline. | S |
| `desktop/src/panels/mcp-servers.ts:167,179` | **MED** | UX defect | `alert("Add failed for …")` / `alert("Remove failed for …")` — same blocking-dialog smell. | S |
| `desktop/src/panels/memory-inspector.ts:374,378,500` | **MED** | UX defect | Three `alert(…)` calls — JSON-import errors and delete failures. | S |
| `desktop/src/panels/skill-catalog.ts:210,389,412` | **MED** | UX defect | Three `alert(…)` calls for pack-install / install / uninstall failures. | S |
| `desktop/src/panels/approval-queue.ts:119` | **MED** | UX defect | `alert(\`Decision failed for ${decision.id}\`)`. | S |
| `desktop/src/panels/ledger-viewer.ts` | **HIGH** | Mocked-in-prod | All IPC (`ledger_query`, `ledger_get_node`, etc.) goes through `invokeStub`; until R1D-5 is wired, viewer always says "no nodes". | S |
| `desktop/src/panels/memory-inspector.ts` | **HIGH** | Mocked-in-prod | `memory_list`, `memory_search`, `memory_import` all stubbed. The diff/import flow has real logic; just no backend. | S |
| `desktop/src/panels/observability.ts` | **HIGH** | Mocked-in-prod | `obs_summary`, `obs_history` stubbed. Charts render skeleton/empty. | S |
| `desktop/src/panels/skill-catalog.ts:802` | **MED** | Stub UI text | Renders the literal string `"(empty stub output)"` in the test-skill modal when the real handler returns nothing. Surfaces internal stub language to end-users. | S |
| `desktop/src/types/ipc.d.ts:15` | **LOW** | Doc-comment | Comments describe the entire IPC surface as "stubs log 'TODO <phase>' and return empty values. Real …" — not a defect, but flags that all 75 IPC surfaces in the file are still skeletal. | — |
| `desktop/src/panels/lane-rail.ts` (entire file) | **MED** | Cross-package coupling | Imports `LaneSidebar`, `LaneSidebarItem`, `LaneEvent`, `LaneStatus` from `@r1/web-components`. The package lives at `packages/web-components/` (out of scan scope) but `desktop/` ships a Tauri 2 bundle that depends on it. Unverified `npm ci` reproducibility: `desktop/package.json` would need a workspace resolution; not audited here. | — |
| `desktop/src/lib/laneSubscription.ts:21` | **LOW** | Cross-package coupling | Same `@r1/web-components` import. | — |
| `desktop/src/popout.tsx:23` | **LOW** | Cross-package coupling | Same `@r1/web-components` import. Auto-bootstrap in `popout.tsx:108-118` only fires if URL has `?popout=lane`; otherwise the file is silent. | — |
| `desktop/src/popout.tsx:81-94` | **MED** | Stub error handling | `onCopyLink` / `onKill` swallow all errors with `.catch(() => undefined)`. Failure to write clipboard or kill a lane is invisible to the user. | S |
| `desktop/src/panels/session-view.ts:109-113,474-485` | **MED** | Conditional wiring | Lane rail is rendered with `hidden` attr by default; only mounted on `attach`. The teardown path at line 480-485 only runs if `state.laneRail` was already created. Some race conditions possible during rapid session switching. | M |
| `desktop/src/panels/session-view.ts:281,317,335,353,371` | **LOW** | TS escape | Five `as unknown as Record<string, unknown>` casts because `invokeStub` typing widens to `Record<string, unknown>`. Justified given the IPC plumbing is generic. | — |
| `desktop/src/panels/mcp-servers.ts:50` | **LOW** | UX | URL placeholder text `"http://localhost:3000 or stdio:./bin/server"` is informational; could be tied to real validation. | — |
| `web/src/components/chat/ToolCard.tsx:78-95` | **LOW** | Silent failure | `onCopy` swallows `cb.writeText(text)` errors with empty catch; the user sees no feedback if clipboard is denied. | S |
| `web/src/components/workdir/WorkdirPicker.tsx:215-217` | **LOW** | UX | When FSA picker is cancelled, the error message is whatever `e.message` says — typically "The user aborted a request" — set as the form error. Should detect cancellation explicitly. | S |
| `web/src/hooks/useDaemonSocket.ts:108-125` | **LOW** | Dead variable | `let cancelled = false; … void cancelled;` — `cancelled` is set true in cleanup but never read; the `void cancelled` is acknowledging the dead store. Should remove. | S |
| `web/src/hooks/useChat.ts:101` | **LOW** | Dead reference | `void sessionId` inside `clearError` exists only so `[sessionId]` deps lint passes despite the body being a no-op. Will need real wiring once errors flow into the store. | S |
| `web/src/lib/store/daemonStore.ts:478-479` | **LOW** | Dead variable | `const { [laneId]: _drop, ...collapsedNext } = collapsedCur; void _drop;` — `_drop` exists only to peel the key; the `void` is acknowledging the dead capture. | — |
| `web/src/lib/api/r1d.ts:243-253` | **LOW** | API quirk | `onEnvelope(handler)` constructs `envelopeHandlers` lazily on call but never actually fires them — the in-class `connect()` only forwards through the constructor-supplied callback. Spec contract satisfied syntactically but handlers added via this method are never invoked. | M |
| `web/src/components/settings/SettingsPage.tsx:70-82` | **MED** | Half-wired persistence | `hydrateSettings` updates the **local** zustand store but no call site triggers `R1dClient.putSettings(s)` to persist server-side. Settings changes will revert on page reload. | M |
| `web/src/components/settings/SettingsPage.tsx:62-88` | **MED** | Spec 6 residual | Spec §Component Catalog requires "model defaults, lane filters, theme, contrast mode, **keybindings**" — keybindings are rendered as a read-only cheat-sheet table, not editable. Spec item 39 "Settings page (model defaults, lane filters, theme, **keybindings cheat-sheet**)" is technically satisfied as cheat-sheet, but item 40 "Wire global keybindings" + customization is not surfaced. | M |
| `web/src/components/lanes/LanesSidebar.tsx:142` | **LOW** | UX | Stable `EMPTY_LANE_IDS` array used for both `order` and `pinnedIds`; harmless but the comment explaining why says "Maximum update depth exceeded" which signals an old bug walked-around rather than fixed. | — |
| `web/src/components/lanes/TileGrid.tsx:33-38` | **LOW** | Spec residual | Spec §Component Catalog says "1×2, 1×3, 2×2 auto-layout"; current grid drops to `grid-cols-2` for `n >= 4` (correct for 4) but doesn't enforce 2×2 — overflow rows wrap as 2 cols × ceil(n/2) rows. For n=5, displays 3 rows of 2/2/1 instead of a 4-cap. | S |
| `web/src/components/lanes/TileGrid.tsx:201` | **LOW** | A11y | `aria-grabbed`/`aria-dropeffect` are deprecated in WAI-ARIA 1.2; spec requires them but modern SR support has shifted to drag-and-drop with DataTransfer announcements. | — |
| `web/src/lib/render/markdown.tsx:42-54` | **MED** | CSP residual | `ALLOWED_LINK_PREFIXES`/`ALLOWED_IMAGE_PREFIXES` hardcoded — daemon's actual allowed-roots is not consulted. Spec §Build Pipeline `connect-src` allows `ws://127.0.0.1:*`; this list shadows that for image/link rendering. Inconsistent. | M |
| `web/src/components/chat/Composer.tsx:50` | **LOW** | Unused ref | `textareaRef` is created but never read (no autofocus, no programmatic actions). Could be dropped or wired to handle `/` shortcut → focus. | S |
| `web/src/test/d3-force-3d-stub.ts` (entire file) | **LOW** | Test stub | Test-only fake; correctly outside src. No production reference. | — |
| `web/src/lib/api/auth.ts` (entire file) | **LOW** | OK | Solid implementation, no stubs. | — |
| `web/src/lib/api/ws.ts` (entire file) | **LOW** | OK | ResilientSocket is full-featured; no stubs. | — |
| `web/src/components/GlobalKeybindings.tsx` | **LOW** | OK (verified) | Hooks `useKeybindings` properly. | — |
| `web/src/lib/api/types.ts` | **LOW** | OK | Comprehensive zod schemas. | — |
| `desktop/src/state/sessionStore.ts:107` | **LOW** | Test-only export | `__resetForTests` exposed in production module. Not destructive but ergonomically bad — should live behind import-guard or test-only file. | S |
| `desktop/src/lib/autostart.ts:54` | **LOW** | Test-only export | Same — "Test-only reset so unit tests can swap a mock plugin-store." | S |
| `desktop/src/r1d-1.test.ts` etc. | **LOW** | Test only | Three top-level test files (`r1d-1.test.ts`, `r1d-2.test.ts`, `r1d-3.test.ts`) live next to source — non-standard layout but functional. | — |

---

## Top 10 by impact (where the user would notice the gap most)

1. **`web/src/main.tsx:15-26`** — The shipped web app renders only `"Web UI scaffolding ready. Components mount in subsequent build phases."` There is no router, no theme provider, no daemon-store provider, no `<App>`. **Every spec-6 component built (chat, lanes sidebar, sessions, status bar, settings page, etc.) is dark code.** Loading `http://127.0.0.1:7777/` produces a placeholder string regardless of how many sessions exist. *Single highest-impact gap.*
2. **`desktop/src/ipc-stub.ts` + 75 call sites** — Every R1D panel routes through `invokeStub`. When `window.__TAURI__` is unavailable (vitest, plain dev browser), every IPC returns the supplied `empty` value and logs a structured TODO. Cost panel always shows $0.00, sessions list is always empty, approvals queue is always empty, scheduler is always empty, etc. The stub is an explicit dev fallback; the gap is that R1D-1.2 (real Tauri invoke wiring per the comment in `ipc-stub.ts:11`) has not landed in this repo state.
3. **`desktop/src/main.ts:64-76` (missing daemon-status pill)** — Spec 7 §5 + checklist item 26 require a four-state daemon pill (external/sidecar/reconnecting/offline) on the title bar. `daemon-status.ts` (224 lines, fully implemented) is exported but never imported by `main.ts`. Users have no signal whether they're talking to an external r1d or the bundled sidecar.
4. **`desktop/src/components/discovery-wizard.tsx` (unwired)** — Spec 7 §5 lifecycle step 4 + checklist item 28 require a first-launch wizard when `~/.r1/daemon.json` is absent. `DiscoveryWizard` (180 lines, complete with copy-to-clipboard, reconnect probe, sidecar accept) exists but no caller. Onboarding currently goes straight to the generic 5-step wizard, not the daemon-discovery split.
5. **`desktop/src/onboarding/onboarding.ts:330-340` (API keys never persist)** — Onboarding collects `state.apiKey` but only `onboarding_start_demo` and `onboarding_pick_data_dir` are invoked; **no IPC writes the chosen provider + API key to the vault**. After "Open dashboard" the keys are gone. Users complete onboarding then fail to start any provider session.
6. **`web/src/hooks/useChat.ts:97-110` (no error surface)** — `error: undefined` hard-coded; `clearError` is a no-op stub. When the daemon emits an `error` envelope (auth fail, model fail, etc.), the chat UI has no path to display it; the session status flips to "error" but no per-message error text reaches the bubble. UX-visible: streamed turns can fail silently from the user's POV.
7. **`web/src/components/settings/SettingsPage.tsx:70-82` (settings drift)** — Calls `hydrateSettings(...)` (local store mutation) but never `R1dClient.putSettings(s)`. Theme persists via localStorage; lane filters + default model do not. Reload the page and your settings revert.
8. **`web/src/lib/store/daemonStore.ts` is monolithic + missing slice files (`sessionsSlice.ts`, `lanesSlice.ts`, `messagesSlice.ts`)** — Spec §Directory Layout names them explicitly. Currently a single 600+ line factory. Functional but contradicts the contract; future maintainers cannot import a slice independently for a focused component test.
9. **9× `alert(...)` calls across desktop panels** — `mcp-servers.ts`, `scheduler.ts`, `approval-queue.ts`, `memory-inspector.ts`, `skill-catalog.ts` use blocking `alert()` for failure feedback. In a Tauri 2 WebView this looks like a system-modal prompt and breaks UX flow. Should be toast/inline notifications.
10. **`web/src/routes/index.tsx:78-117` factory has no production caller; spec routes (`sessions.$id.tsx`, etc.) absent** — Routes are scaffolded as a callable factory, but no `App` wires renderers + sets up the BrowserRouter, and no separate route file exists per the spec layout. Until item 1 above is addressed, the route system is unreachable code.

---

## Notes on the audit signal

- The codebase has **zero `any` casts, zero `@ts-ignore`, zero `console.log("TODO")`** in production paths — TypeScript discipline is high. This is a clean codebase architecturally; the gaps are about *what was scoped but not connected*, not *what was implemented sloppily*.
- The desktop `ipc-stub.ts` shim is **deliberate** (dev fallback) but means every panel reads as a stub until R1D-1.2 is finished. If the Rust `invoke_handler` is wired in another branch / build, these findings collapse to "OK".
- The web `main.tsx` placeholder is similarly *deliberate scaffolding* per the comment ("Components mount in subsequent build phases"), but spec 6 is marked `STATUS: done` (`specs/web-chat-ui.md:1`) — that mismatch is the highest-priority finding.
- 113 `*.tsx`/`*.ts` files in `web/src` (114 incl. tests) and 30 in `desktop/src`. Coverage of test files is thorough; storybook stories present for almost every shipped component.
