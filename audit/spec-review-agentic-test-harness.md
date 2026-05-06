# Spec Review — agentic-test-harness.md

**Spec:** `/home/eric/repos/r1-agent/specs/agentic-test-harness.md`
**Reviewer:** automated 10-check rubric
**Date:** 2026-05-02
**Result:** PASS (after inline fixes)

The spec is in unusually strong shape. It ships a wire surface, a TUI shim
contract with verbatim Go signatures, three full Gherkin fixtures, a CI lint
algorithm with allowlist, and a docs outline. Two critical issues found and
fixed inline; one moderate issue (missing Risks section + auto-snapshot drift
mitigation) found and fixed inline.

## 10-check rubric

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | Frontmatter (STATUS / CREATED / DEPENDS_ON / BUILD_ORDER) | **PASS** | Lines 1-4: `STATUS: ready`, `CREATED: 2026-05-02`, `DEPENDS_ON: cortex-core, cortex-concerns, lanes-protocol, tui-lanes, r1d-server, web-chat-ui, desktop-cortex-augmentation`, `BUILD_ORDER: 8`. All four present. |
| 2 | Self-contained checklist items | **PASS** | §12 has 41 items (one added inline this review). Spot-checked 6 items: each names a concrete file path (`internal/mcp/r1_server.go`, `internal/tui/teatest_shim.go`, `tools/lint-view-without-api/main.go`, `tools/agent-feature-runner/main.go`, `web/.storybook/mcp.config.ts`, `tests/agent/web/chat-send-message.agent.feature.md`), an action verb, and (where relevant) the section it implements. |
| 3 | No vague items / TBD / FIXME / RESEARCH-NEEDED | **PASS** | grep returns 0 markers. Items reference specific tool names (`r1.lanes.kill`, `canonicalStokeServerToolName`), specific deps (`charmbracelet/x/exp/teatest`), specific config keys (`port: 6007`, `transport: 'stdio'`). |
| 4 | Test plan | **PASS** | §10 "Test Plan: Meta-Test Over All `.agent.feature.md` Fixtures" defines the runner, the in-process daemon, the TUI shim, the Playwright child, per-scenario failure capture path (`.agent-failures/<scenario>/`), the per-scenario (5 min) and per-suite (30 min) budgets, and an explicit coverage gate (every §4 category has at least one fixture). |
| 5 | Concrete paths | **PASS** | Required paths all present and named verbatim: `internal/mcp/r1_server.go` (§1.1, §12), `internal/tui/teatest_shim.go` (§5 header, §12), `tools/lint-view-without-api/` (§1.5, §8 header, §12), `tests/agent/` (§6 header, §10, §12). Plus surface-specific server files (`lanes_server.go`, `cortex_server.go`, `tui_server.go`) and storybook config files. Verified `internal/mcp/` already exists with 14 files; the spec correctly says "extend, do NOT create sibling". |
| 6 | Cross-references resolve | **PASS** | All 7 `DEPENDS_ON` specs exist in `specs/`: cortex-core.md, cortex-concerns.md, lanes-protocol.md, tui-lanes.md, r1d-server.md, web-chat-ui.md, desktop-cortex-augmentation.md. Internal-package references (`stokerr/`, `contentid/`, `hub.Bus`, `procutil.ConfigureProcessGroup`, `canonicalStokeServerToolName`) all match real packages or symbols. Verified `canonicalStokeServerToolName` exists at `internal/mcp/stoke_server.go:139`. |
| 7 | Stack & versions pinned | **PASS** | §2 pins MCP wire spec to `2025-11-25`, Playwright MCP to `npx @playwright/mcp@latest` (with risk-mitigation pin in §10a), Storybook MCP to `9.x (March 2026 GA)`, teatest to `charmbracelet/x/exp/teatest@latest`, Bubble Tea to v2 (matching D-S3), lipgloss to v2, Go to 1.26 (post-#119, matches `go.work`). All seven version rows present. |
| 8 | Out of scope | **PASS** | §11 explicitly excludes the implementation of underlying actions in specs 1-7 (cortex Workspace/Lobes/Notes, lane lifecycle, TUI Bubble Tea models, r1d daemon, React UI, Tauri integration). Also defers Computer Use to Q3 2026 and excludes vision-driven test agents (Stagehand/browser-use as clients only) and bespoke DSL replacements. |
| 9 | Existing patterns (extends, not replaces) | **PASS** | §3 has a strong "Existing Patterns to Follow" block that explicitly forbids `internal/mcp2/` or `internal/agentic/` siblings. Lists 7 reuse points: JSON-RPC envelope from `transport_*.go`, `ToolDefinition` struct, `hub.Bus` subscriber pattern, `stokerr/` taxonomy, `contentid/` IDs, `procutil` process-group helpers, Slack-style envelope. Migration of `stoke_server.go` is described as "deprecated shim that re-exports `NewStokeServer` for backward compatibility until v2.0.0". |
| 10 | Risk surfacing | **PASS (after fix)** | INITIALLY ABSENT — no Risks section; "snapshot drift" never named. FIXED inline by adding §10a "Risks & Mitigations" with 7 risk rows, including the called-out **auto-snapshot drift** mitigation: structured a11y trees rather than pixel views, `make agent-features-update` for intentional re-records, ascii-color-profile + fixed term size to remove terminal flake, and a CI guard that fails when golden diff is non-empty but source diff under `web/src/`, `internal/tui/`, `desktop/src-tauri/` is empty. |

## Special-focus checks

| Item | Status | Evidence |
|---|---|---|
| 38 MCP tools across 10 categories with verbatim schemas | **PASS (after fix)** | INITIAL FAILURE: spec said "32 tools" but the math at §4 line 336 totals 38 (6+5+5+4+4+2+3+4+4+1). FIXED inline: §4 footer and §9 outline now both read "38 tools across 10 categories". Every tool has a verbatim `name`, `description`, and `inputSchema` block. |
| TUI shim Go signatures verbatim | **PASS** | §5 contains a complete Go file with package decl, imports, `TUISessionID` type, `Shim` interface (Start, PressKey, Snapshot, GetModel, FocusLane, WaitFor, Stop), `Snapshot`, `A11yNode`, `Predicate`, `FinalOutput` structs with full JSON tags, and the `NewShim(out io.Writer) Shim` constructor. Implementation notes (lipgloss profile, term size, A11yEmitter requirement) follow. |
| 3 example `.agent.feature.md` fixtures present | **PASS** | §6.1 chat-send-message, §6.2 lane-kill-from-sidebar, §6.3 cortex-publish-and-observe. All three fixtures shown in full Gherkin form with Given/When/Then, tag headers, depends-on headers, and per-file Tool-mapping blocks where useful. The negative case (lint scanner asserting view-without-API) appears in §6.2. |
| Storybook MCP setup concrete | **PASS** | §7 specifies stock Storybook 9 config files, `mcp.config.ts` with literal `port: 6007`, `transport: 'stdio'`, `expose: ['stories', 'a11y', 'interactions']`, the CI command (`npx storybook-mcp@latest validate ... --fail-on-missing-a11y`), and a worked example `LaneSidebar.stories.tsx` with `parameters.agentic.actionables` listing the role+name regex+mcp_tool triple per actionable. |
| CI lint script outline AND auto-snapshot mitigation for drift risk | **PASS (after fix)** | §8 has detection-rules table, §8.2 algorithm pseudocode (5 steps), §8.3 allowlist with justification format, §8.4 wiring (`make lint-views`, GitHub Actions matrix, `r1.verify.lint` integration). Auto-snapshot mitigation INITIALLY ABSENT; FIXED inline with §10a row 1 + new checklist items for `make agent-features-update` and the empty-source-diff CI guard. |
| AGENTIC-API.md outline concrete | **PASS** | §9 has a 12-section numbered outline: audience, wire protocol (transports + auth subprotocol), tool catalog (auto-generated), streaming/replay semantics, idempotency rules, error envelope (Slack-style + stokerr taxonomy), capability flags (`--caps=write`), test harness usage, UI-author guide, versioning policy with dual-name aliases, examples (Claude Code, Codex, Stagehand, browser-use), non-goals. |
| Governing principle prominent | **PASS** | §1 lines 10-12: "Governing principle (verbatim, do not paraphrase in code or docs):" followed by a blockquote-style emphasis: **"Every action a human can take through a UI MUST have a documented, idempotent, schema-validated agent equivalent reachable through MCP. The UI is a view over the API; never the reverse."** §9 outline §1 also requires the principle to appear verbatim in the first paragraph of `docs/AGENTIC-API.md`. |

## Inline fixes applied

1. **Tool count math** — corrected "32" to "38" in §4 footer and §9 outline. Ten categories (sessions=6, lanes=5, cortex=5, missions=4, worktrees=4, bus=2, verify=3, tui=4, web=4, cli=1).
2. **Risks & Mitigations section added** — new §10a with 7 risk rows: snapshot drift, tool-vs-UI drift, Playwright/Storybook version churn, stoke-alias removal, reflection-based model leak, lint false negatives, runtime budget blowout. Each row names the surface and a concrete mitigation.
3. **Auto-snapshot mitigation checklist items added** — new items in §12 under "Test DSL + runner" for `make agent-features-update` and the CI check that catches empty-source-diff + non-empty-golden-diff.

## Findings not fixed (deferred to implementation)

None critical. Two minor observations:

- §5 mentions `tidwall/gjson` "if not already a dep, else fallback" — at implementation time this should be resolved to one or the other, not "either".
- §8.2 step 5's WARN-on-unused-tool gate is one-sided; tools added without UI surface would also need a deprecation path — though §10a row 4 covers stoke aliases, the general case isn't addressed.

## Summary (50 words)

Spec is implementation-ready. All 10 rubric checks pass; 7 special-focus checks pass.
Three critical inline fixes applied: corrected tool count (32 → 38), added §10a Risks &
Mitigations section, added auto-snapshot drift mitigation (`make agent-features-update`
plus empty-source-diff CI guard). Governing principle prominent; verbatim TUI shim
signatures, 3 full Gherkin fixtures, Storybook MCP, and AGENTIC-API.md outline all concrete.
