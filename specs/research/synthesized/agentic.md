# Synthesized — Agentic Test Harness

Source: RT-AGENTIC-TEST.md.

## Governing principle

**Every UI action has an idempotent, schema-validated agent equivalent reachable through MCP.** UI is a view over the API; never the reverse. Enforce with a CI lint rule that scans for UI events without MCP-tool counterparts.

## Standardize on MCP

- Single wire protocol for every programmatic surface — CLI, TUI, web, desktop.
- One r1 MCP server: `internal/mcp/r1_server.go` (extend the existing one).
- Tool surface (goal-shaped, not RPC-shaped):
  - `r1.session.start({ workdir, model? })` → `session_id`
  - `r1.session.send({ session_id, message })` → `message_id`
  - `r1.session.cancel({ session_id })`
  - `r1.lanes.list({ session_id })`
  - `r1.lanes.subscribe({ session_id })` (streaming)
  - `r1.lanes.kill({ session_id, lane_id })`
  - `r1.cortex.notes({ session_id })` — read Workspace
  - `r1.cortex.publish({ session_id, note })` — let agents post Notes
  - `r1.mission.create / .list / .cancel`
  - `r1.worktree.list / .diff`
  - `r1.bus.tail({ session_id, since_seq })`
  - `r1.verify.build / .test / .lint`
  - `r1.tui.press_key / .snapshot / .get_model` (TUI test surface)

## Surface-specific test harnesses

### Web UI
- `npx @playwright/mcp@latest` as the agent driver (DOM/a11y-snapshot, 12–17pp more reliable than vision-driven Computer Use).
- Lint: every interactive component has `data-testid` and ARIA label.
- Storybook MCP for component contracts.

### TUI
- Wrap `charmbracelet/x/exp/teatest` in a thin RPC shim → exposed via MCP as `r1.tui.*`.
- No terminal emulator required. Agent posts key events; TUI returns snapshot strings.

### Desktop (Tauri)
- Same Playwright MCP — Tauri webview is Chromium/WebKit. Use `tauri-driver` for native window control.
- Tauri channels are auditable from Rust side; the JSON-RPC envelope is itself the test surface.

### CLI
- Already JSON-RPC 2.0 (NDJSON). Tests dispatch over MCP `r1.cli.invoke({ args: [...] })`.

## Test DSL

- Gherkin-flavored markdown: `*.agent.feature.md` files in `tests/agent/`.
- Each scenario: Given/When/Then with MCP tool calls implied. Agent runs them as tasks.
- Example:

```markdown
# tests/agent/lanes-kill.agent.feature.md
## Scenario: Kill a runaway lane
- Given a session is running
- And a lane "memory-curator" is in state "running"
- When the user presses 'k' on the focused lane
- Then the lane state is "cancelled"
- And the cortex publishes a "lane_cancelled" Note
```

## CI integration

- Per-PR: smoke matrix runs the .agent.feature.md tests via Playwright MCP + teatest MCP shim.
- Failure mode: missing MCP tool for a UI action → CI lint fails with "view-without-API" violation pointing to the offending component.

## What this spec ships (spec 8)

1. New MCP tool surface in `internal/mcp/r1_server.go`.
2. `internal/tui/teatest_shim.go` exposing TUI under MCP.
3. CI lint: `tools/lint-view-without-api.go` (new).
4. `tests/agent/` directory with seed `.agent.feature.md` files for cortex/lanes/sessions/missions.
5. `docs/AGENTIC-API.md` — the contract for external agents.
