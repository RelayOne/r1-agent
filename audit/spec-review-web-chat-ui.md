# Spec Review — `specs/web-chat-ui.md`

**Date:** 2026-05-02
**Reviewer:** review-spec (10-check rubric)
**Spec path:** `/home/eric/repos/r1-agent/specs/web-chat-ui.md`
**Status before review:** `ready` (BUILD_ORDER 6, depends on lanes-protocol + r1d-server)

## Summary verdict

10/10 checks PASS after inline fixes. 3 critical issues found and fixed inline:
1. Streamdown not pinned to 1.2.x (was `^1.x`) — FIXED.
2. `@ai-sdk/elements` not pinned (was `latest ^0.x`) — FIXED.
3. Tile-mode collapse + reorder behavior under-specified — FIXED.

Plus one PARTIAL upgraded to PASS by adding concrete component file paths
to the catalog (per-component `<Name>.tsx` + `<Name>.test.tsx` + `<Name>.stories.tsx`).

---

## Check-by-check

### 1. Frontmatter — PASS

Evidence: lines 1-4
```
<!-- STATUS: ready -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: lanes-protocol, r1d-server -->
<!-- BUILD_ORDER: 6 -->
```
Matches sibling specs (lanes-protocol BUILD_ORDER 3, r1d-server 5,
agentic-test-harness 8). Dep ordering coherent.

### 2. Self-contained items — PASS

55-item Implementation Checklist; each item is independently actionable
(creates a file, installs a dep, implements a component). Cross-section
references (e.g. "as in §Build Pipeline step 9") are resolved within the
same document — no external lookups needed. Test plan is also fully
embedded.

### 3. No vague items — PASS (after fix)

**Pre-fix (FAIL):** §Stack & Versions said `streamdown: latest (^1.x)`
and the recommendation read "Pin its exact minor (e.g. 1.2.x) once
initial integration lands" — leaving the pin to be decided later. User
explicitly required "streamdown 1.2.x pinned" up-front. Same issue for
`@ai-sdk/elements: latest (^0.x)`.

**Fix applied (lines 23-24, 38):**
- `streamdown` → `1.2.x (pinned exact minor; ~1.2.0 in package.json)`
- `@ai-sdk/elements` → `0.0.x (pinned exact minor; ~0.0.0 in package.json)`
- Recommended-pin paragraph rewritten as **Pinned:** statement.

All other versions are concrete (`^18.3`, `^6.0`, `3.4.x`, `^7.0`, `^5.0`,
`^3.x`, `^2.1.x`, `^1.49.x`, `^4.10.x`, `>=20`).

### 4. Test plan — PASS

§Test Plan covers three tiers:
- **Unit (Vitest+jsdom+MSW):** 10 explicit test surfaces with
  per-component cases (e.g. ResilientSocket backoff curve, jitter bounds,
  state transitions; `<MessageLog>` collapses tool cards on
  output-available; coalescing-under-firehose with fake timers).
- **E2E (Playwright + Playwright MCP):** 9 `*.agent.feature.md` flows
  enumerated by name (matches user-spec'd "9 Playwright fixtures").
- **A11y (axe-core/playwright):** every route, fail on
  serious/critical.
- **Storybook MCP (spec 8):** every catalog component has
  `*.stories.tsx`.

Acceptance criteria (8 WHEN/SHALL clauses) are testable and tied to
specific code paths.

### 5. Concrete paths — PASS

§Directory Layout (lines 60-121) gives a complete `web/...` tree with
per-file roles. Build pipeline says
`build.outDir = '../internal/server/static/dist'` (verified against
existing `internal/server/embed.go` `//go:embed static`). All routes,
components, hooks, lib modules are at concrete paths under
`web/src/...`.

### 6. Cross-refs — PASS

All referenced specs exist:
- `specs/lanes-protocol.md` ✓
- `specs/r1d-server.md` ✓
- `specs/desktop-cortex-augmentation.md` ✓ (referenced as spec 7)
- `specs/agentic-test-harness.md` ✓ (referenced as spec 8)

All referenced decision IDs exist in `docs/decisions/index.md`:
- D-2026-05-02-02 (React+Vite+Tailwind) ✓
- D-S1 (status vocabulary, glyph+color) ✓
- D-S2 (5–10 Hz coalescing) ✓
- D-S4 (Cursor 3 Glass) ✓
- D-S5 (streamdown + ai-sdk/elements + useChat) ✓
- D-S6 (subprotocol auth) ✓
- D-C4 (drop-partial interrupt + 30s ping watchdog) ✓
- D-A4 (Gherkin-flavored markdown) ✓

`internal/concern/`, `internal/bus/`, `internal/server/embed.go`
references all map to real packages on disk.

### 7. Stack & Versions — PASS (after fix)

User checklist:
- React 18.3 ✓ (`18.3.x`)
- Vite 6 ✓ (`6.0.x`, with explicit note that desktop is on 5)
- Tailwind 3.4 ✓ (`3.4.x`, with explicit "v4 NOT used" rationale)
- shadcn/ui ✓ (`latest`, CLI-generated, copied into
  `web/src/components/ui/`)
- streamdown 1.2.x pinned ✓ **(fixed inline — was `^1.x`)**
- `@ai-sdk/elements` pinned ✓ **(fixed inline — was `latest (^0.x)`)**

Bonus: `@ai-sdk/react ^6.0.0`, `react-router ^7.0.0`, `zustand ^5.0.0`,
`zod ^3.23.x`, `react-hook-form ^7.53.x`, `date-fns ^4.x`, `vitest
^2.1.x`, `@playwright/test ^1.49.x`, `@axe-core/playwright ^4.10.x`,
`node >=20`. All versions concrete.

### 8. Out of scope — PASS

§Boundaries — What NOT To Do (10 explicit prohibitions: no native shell,
no MCP tool surface, no embed.go mods beyond spec 5, no second router /
state lib / markdown renderer, no service worker yet, no localStorage
for FSA handles, no SSO/OAuth, no SSR/RSC, no react-markdown, no
auto-scroll-when-scrolled-up).

§Out of Scope (6 items: native shell, MCP, cloud daemons, multi-tenant
auth, mobile <768 px, i18n).

### 9. Existing patterns — PASS

§Existing Patterns to Follow explicitly anchors on:
- `desktop/` toolchain (vite.config, tsconfig, vitest.config,
  package.json — verified: desktop is private, type=module, node>=20,
  vitest ^2.1.9). Spec deliberately bumps Vite 5 → 6 with rationale.
- `internal/server/embed.go` — verified the `//go:embed static`
  directive exists and `RegisterDashboardUI` already serves from there;
  spec correctly states no Go change needed beyond what spec 5
  prescribes.
- `internal/concern/` and `internal/bus/` — referenced for server-side
  projection and WAL replay. Both are real packages.

### 10. Risk surfacing — PASS

§Risks / Gotchas covers 7 concrete risks with mitigations:
- Vite 6 + Tailwind 3 (don't mix v4 yet)
- Streamdown partial-markdown minor regressions
- WS subprotocol echo (browser will reject otherwise)
- Origin/Host strictness in dev (`:5173` cross-origin to `:7777`)
- FSA permission revocation (Chrome can revoke between sessions)
- 200 Hz lane re-render storm (5–10 Hz coalescing)
- Lane order churn (stable creation-time ordering)

---

## Special-focus checks

### 11-component catalog complete with paths, signatures, test files — PASS (after fix)

Pre-fix: catalog had 21 components but per-component test/story file
**paths** were stated as a generic rule ("colocated `<Name>.test.tsx`")
rather than enumerated.

Fix applied (line 137): added a concrete enumeration of
`group/Name.tsx` paths for all 22 components, plus an explicit statement
that `<Name>.test.tsx` and `<Name>.stories.tsx` sit next to each `.tsx`.
Component count exceeds the user-stated "11" minimum (catalog has 22 —
that is fine; `>= 11` was the floor).

Each row has props (`high-level`) and responsibilities. `data-testid`
contract reiterated.

### Routing map: `/`, `/sessions/:id`, `/sessions/:id/lanes/:lane_id`, `/settings` — PASS

§Routing Map has exactly these four routes plus `*` 404. Each has
component path and purpose. Nested loader chain documented
(`r1d.getSession(id)` failure → redirect to `/`).

### WS auth via subprotocol token in concrete TS code — PASS

Line 197 (concrete TS):
```ts
new WebSocket(wsUrl, ["r1.bearer", token])
```
Server-side contract spelled out (echoes `r1.bearer`, validates
`Sec-WebSocket-Protocol`, strict `Origin`+`Host` allowlist). Token
minted via `POST /auth/ws-ticket` (`mintWsTicket()`); 4401-on-expiry +
`auth.expiring_soon` pre-emptive refresh both specified.

### Tile mode (the hardest UX flow) — PASS (after fix)

Pre-fix: drag/reorder/unpin partially specified; **collapse** absent.

Fix applied (lines 155 + 373):
- Drag/reorder: HTML5 DnD on tile headers with `aria-grabbed` /
  `aria-dropeffect`; `Cmd+Shift+←/→` keyboard alternative.
- Collapse: per-tile chevron in tile header; collapsed tile = 32 px
  header strip; CSS `grid-auto-rows: minmax(min-content, 1fr)` reflow;
  collapsed-id list persisted in zustand `ui` slice (per-session).
- Unpin: existing `unpin removes pane` retained.
- Pop-out: double-click header → `/sessions/:id/lanes/:lane_id`.

Layouts (1×2, 1×3, 2×2) and acceptance criterion ("WHEN a user pins 2,
3, or 4 lanes THE SYSTEM SHALL render … 1×2 / 1×3 / 2×2") are intact.

### 9 Playwright `.agent.feature.md` fixtures listed — PASS

Counted in §Test Plan → End-to-end:
1. happy-path-chat
2. multi-instance-switch
3. lane-pin-tile-mode
4. interrupt-mid-stream
5. reconnect-replay
6. workdir-picker-fsa
7. deep-link-lane
8. a11y-keyboard-only
9. csp-no-violations

= 9 exactly.

### A11y checklist (ARIA, keyboard, contrast) specific — PASS

§Accessibility Checklist has 12 specific items:
- aria-label on every interactive control
- data-testid on every interactive element (spec 8 lint)
- Tab order matches visual order; no `tabindex > 0`
- Dialogs trap focus + restore on close (axe-verified)
- Lane status = glyph + color (D-S1 — never color-only)
- shadcn Button + Tooltip keyboard activation (Enter / Space)
- Skip-to-main-content link
- HC theme ≥ 7:1 contrast, toggleable, persisted
- `prefers-reduced-motion` disables shimmer + stream animations
- `aria-live="polite"` on streaming bubble; `assertive` on errors
- Keybindings cheat-sheet on `?`
- Color tokens via Tailwind theme; no hard-coded hex outside
  `globals.css`

### Build pipeline → embed.FS path concrete — PASS

§Build Pipeline step 3-5:
- Vite `build.outDir = '../internal/server/static/dist'`
  (`emptyOutDir: true`)
- `internal/server/embed.go` `//go:embed static` picks up
  `static/dist/**` automatically (verified — embed.go does have this
  exact directive)
- `RegisterDashboardUI` updated in spec 5 (cross-spec hand-off
  documented; legacy `/legacy-dashboard` path retained during
  transition)
- CSP enforced via `<meta http-equiv>` in `index.html` with full policy
  string

---

## Inline fixes applied

| # | Location | Change |
|---|---|---|
| 1 | Line 23 | `streamdown` pin: `latest (^1.x)` → `1.2.x (pinned exact minor; ~1.2.0)` |
| 2 | Line 24 | `@ai-sdk/elements` pin: `latest (^0.x)` → `0.0.x (pinned exact minor; ~0.0.0)` |
| 3 | Line 38 | "Recommended pin" paragraph rewritten as **Pinned:** statement covering both load-bearing libs |
| 4 | Line 137 | Component catalog preamble: enumerated all 22 component file paths and stated colocated `<Name>.test.tsx` + `<Name>.stories.tsx` |
| 5 | Line 155 | `<TileGrid>` row: added explicit collapse spec + DnD a11y attrs + keyboard alt + pop-out |
| 6 | Line 373 | Implementation checklist item 36: matched expanded TileGrid behavior |

No content removed. All edits are additive specificity.

---

## Items left as-is (deliberate)

- 22-component catalog vs user "11 components" floor: kept full catalog;
  user said floor of 11 and this exceeds it.
- Vite 6 (vs desktop's Vite 5): kept the bump with explicit rationale
  (SWC + Lightning CSS); spec acknowledges the desktop divergence.
- Tailwind 3.4 (vs Tailwind 4 GA): kept v3 with rationale (shadcn
  generators emit v3 tokens in May 2026).
- 55-item implementation checklist: not trimmed; each item is small +
  self-contained.

---

## 50-word summary

Spec passes 10/10 checks after three inline fixes: streamdown pinned to
1.2.x (was floating ^1.x), `@ai-sdk/elements` pinned to 0.0.x, and tile
mode collapse + DnD-a11y + keyboard-reorder + pop-out behavior fully
specified. Component catalog now lists concrete file paths for all 22
components plus colocated test and story files.
