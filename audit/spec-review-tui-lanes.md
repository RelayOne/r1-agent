# Spec Review — `specs/tui-lanes.md`

Reviewer: review-spec (10-check rubric)
Date: 2026-05-02
Spec: `/home/eric/repos/r1-agent/specs/tui-lanes.md` (611 → 624 lines after fixes; 38 checklist items)
Depends on: `lanes-protocol` (BUILD_ORDER 3) → `tui-lanes` (BUILD_ORDER 4)

## Verdict summary

| # | Check | Result | Notes |
|---|---|---|---|
| 1 | Frontmatter | PASS | STATUS/CREATED/DEPENDS_ON/BUILD_ORDER all present (lines 1–4). |
| 2 | Self-contained items | PASS | Spot-check items 8, 17, 19 each name file path + responsibility + downstream. Item 8 was already expanded with envelope/RPC ownership in a prior pass. |
| 3 | No vague items | PASS | Zero `TBD`, `FIXME`, `RESEARCH-NEEDED` markers; checklist verbs concrete (`Define`, `Implement`, `Wire`, `Snapshot`). |
| 4 | Test plan | PASS | Dedicated §Testing with unit / `teatest` snapshot / behavioral / manual sub-sections; 24 explicit test cases. |
| 5 | Concrete paths | PASS | Every new file under `internal/tui/lanes/*.go` enumerated in §New Files; `internal/tui/interactive.go`, `internal/tui/runner.go`, `internal/tui/renderer/` cited with line numbers. All paths verified to exist (or, for new files, to live under an existing directory). |
| 6 | Cross-refs | PARTIAL → PASS (after fix) | Spec referenced `decisions/index.md`; canonical path is `docs/decisions/index.md` (matches `lanes-protocol.md:711` and `web-chat-ui.md:45`). Fixed inline. Decision IDs (D-D3, D-D5, D-S2, D-S6) and research IDs (RT-TUI-LANES, RT-R1D-DAEMON, RT-AGENTIC-TEST) all resolve. |
| 7 | Stack & versions | PASS | §Stack & Versions table pins Bubble Tea v2 `v2.0.0-beta.4`, Bubbles v2 `v2.0.0-beta.3`, lipgloss v2 `v2.0.0-beta.5` (under new `charm.land/...` module path), `winder/bubblelayout v1.4.0`. Co-existence with v1 renderer/interactive explicitly called out. |
| 8 | Out of scope | PASS | §Boundaries — What NOT To Do (10 explicit prohibitions, including no v1↔v2 cross-import, no `tea.ClearScreen`, no per-token repaint, no goroutine-per-lane). |
| 9 | Existing patterns | PASS | Cites `interactive.go:105-107` (tickCmd), `:113-141` (mode keys), `:163-165` (WindowSizeMsg), `:241-249` (lipgloss styles), `:427-449` (Send* helpers). Verified accurate against current source. |
| 10 | Risk surfacing | PARTIAL → PASS (after fix) | Boundaries section listed prohibited actions but no explicit risk register. Added §Risks & Mitigations with 8 risks + likelihood/impact/mitigation. |

## Special-focus checks

- **Keybinding completeness (1–9, tab, j/k, enter, esc, k+y, K, ?)** — PASS. §Keybinding map is a 16-row table covering every required key plus `shift+tab`, `x` (kill alias for accessibility / focus-mode collision), `q`/`ctrl+c`, `r` (force re-render debug), `L` (panel toggle for composition under `interactive.go`). Mode-disambiguation explicit: `k` is cursor-up in overview, viewport-scroll in focus; `K`/`x` are kill in overview/focus respectively.
- **Adaptive layout algorithm (column vs vertical)** — PASS. §Layout Algorithm provides `decideMode(width, height, n, mode)` pseudo-code with `LANE_MIN_WIDTH=32`, `COLS_MAX=4`, focus override, and `bubblelayout.Wrap` declaration sample for `modeColumns`; `modeFocus` documented as hand-rolled 65/35 horizontal join.
- **Render-cache invalidation rules** — PASS. §Render-Cache Contract enumerates 6 invalidation triggers (Dirty flag, width change, status transition, focus on/off, spinner-tick + Running, full drop on resize). Anti-pattern of per-token repaint explicitly forbidden (D-S2).
- **`teatest` snapshot tests planned** — PASS. 10 named snapshot tests (Empty, StackMode, ColumnsMode_2, ColumnsMode_4, FocusMode, KillConfirm, KillAllConfirm, HelpOverlay, NoColor, StatusGlyphs) plus 4 behavioral key-event tests + 2 producer tests + 1 reconnect test, all wired to `testdata/*.golden`.
- **Accessibility paired-glyph rule** — PASS. Item 37 (package doc accessibility note), Acceptance Criterion `WHEN NO_COLOR=1 ... status SHALL still be unambiguous via glyph`, status enum item 3 pairs each color with a paired glyph (`·`, `▸`, `⏸`, `✓`, `✗`, `⊘`).

## Critical issues fixed inline

1. **Cross-ref path bug** — `decisions/index.md` → `docs/decisions/index.md`
   (line 403). Other specs in the cycle (`lanes-protocol.md:711`,
   `web-chat-ui.md:45`) use the prefixed form; without the fix `/build` would
   produce a broken intra-repo link in the rendered package doc and a real
   `os.Open` in any tool that tries to follow the path. Fixed by Edit.

2. **Risks section absent** — added §Risks & Mitigations with 8 entries
   covering beta-pinned upstream churn, `winder/bubblelayout` upstream risk,
   coalescer hiding real latency, keybinding collisions, render-cache stale
   string, IPC/WS code-path drift, `NO_COLOR` regression, and producer-
   goroutine leak on quit. Each risk has likelihood, impact, and mitigation
   tied to a specific spec section or test case.

## Non-critical observations (no fix applied)

- §Component Model uses Go-style code blocks (not pseudocode) — fine for an
  implementation spec but means downstream `/build` agents will be tempted to
  copy struct definitions verbatim; that is intended here.
- Item 8 is now a long paragraph (envelope decode, dual transports, both RPCs);
  consider splitting into 8a/8b/8c during /build for finer-grained progress
  tracking. Not blocking.
- Item 11 hedges with "or before `tea.NewProgram` runs, depending on
  lifecycle" — acceptable because `tea.Cmd` lifecycle is a known Bubble Tea
  v2 ergonomics question; concrete decision can land at implementation time.
- Acceptance criterion `width >= cols * 32` is satisfied by the `decideMode`
  formula but not literally restated in pseudo-code; treat as soft contract.

## Files modified by this review

- `/home/eric/repos/r1-agent/specs/tui-lanes.md` (Edit: `decisions/index.md` →
  `docs/decisions/index.md`; Edit: appended §Risks & Mitigations between
  §Acceptance Criteria and §Implementation Checklist).

## Final verdict

**APPROVED for /build at BUILD_ORDER 4.** Two PARTIAL checks (cross-refs,
risk surfacing) lifted to PASS via inline fixes. Spec is self-contained,
versions pinned, file/line citations verified against current source,
keybinding/layout/cache/test/accessibility focus areas all met.
