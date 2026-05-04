<!-- STATUS: ready -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: cortex-core, cortex-concerns, lanes-protocol, tui-lanes, r1d-server, web-chat-ui, desktop-cortex-augmentation (cross-cutting) -->
<!-- BUILD_ORDER: 8 -->

# agentic-test-harness — Every UI Action Reachable Through MCP

## 1. Overview

**Governing principle (verbatim, do not paraphrase in code or docs):**

> **Every action a human can take through a UI MUST have a documented, idempotent, schema-validated agent equivalent reachable through MCP. The UI is a view over the API; never the reverse.**

This spec is cross-cutting. Specs 1–7 ship the underlying capabilities (cortex, lanes, TUI, r1d daemon, web chat, desktop). Spec 8 ships the **harness** that makes every one of those surfaces driveable, observable, and testable by an external agent (Claude, Codex, browser-use, Stagehand, custom MCP clients) — over a single wire protocol.

Concretely, this spec ships:

1. A consolidated `internal/mcp/r1_server.go` that publishes the full r1 tool catalog (sessions, lanes, cortex, missions, worktrees, bus, verify, TUI, web, CLI). The existing `internal/mcp/stoke_server.go` is migrated into it (dual-name aliases preserved per `canonicalStokeServerToolName` until v2.0.0).
2. Surface-specific server files that delegate from `r1_server.go`: `lanes_server.go`, `cortex_server.go`, `tui_server.go`. Web is delegated to upstream Playwright MCP via a thin `r1.web.*` adapter.
3. A new `internal/tui/teatest_shim.go` that wraps `charmbracelet/x/exp/teatest` and exposes `tui_press_key` / `tui_snapshot` / `tui_get_model` over MCP without requiring a real terminal emulator.
4. A Gherkin-flavored markdown DSL (`*.agent.feature.md`) for agent-readable scenarios, plus a Go runner at `tools/agent-feature-runner/` that parses the DSL and dispatches each step through MCP.
5. A CI lint at `tools/lint-view-without-api/main.go` that scans React, Bubble Tea, and Tauri sources for interactive components and fails the build when no MCP tool exists for the corresponding action.
6. Storybook MCP wired into `web/` for component contracts; CI runs `storybook-mcp` to validate that every component story has the required role + accessible-name + state metadata.
7. `docs/AGENTIC-API.md` — the contract for external agents.

**Not in scope:** the implementation of the underlying actions themselves. Those live in specs 1–7. Spec 8 only ships the wire surface, the test harness, the lint, and the docs.

## 2. Stack & Versions

| Component | Version | Source |
|---|---|---|
| MCP wire spec | 2025-11-25 | `modelcontextprotocol.io/specification/2025-11-25` |
| Playwright MCP | latest (`npx @playwright/mcp@latest`) | `microsoft/playwright-mcp` |
| Storybook MCP | 9.x (March 2026 GA) | `storybook/mcp` |
| teatest | `charmbracelet/x/exp/teatest@latest` | `charmbracelet/x/exp` |
| Bubble Tea | v2 (matches D-S3) | `charmbracelet/bubbletea` |
| lipgloss | v2 (`SetColorProfile(termenv.Ascii)` for deterministic CI output) | `charmbracelet/lipgloss` |
| Go | 1.26 (per `go.work`, post-#119) | toolchain |
| Test DSL parser | custom Go (in `tools/agent-feature-runner/`) | n/a |
| Lint scanner | custom Go (in `tools/lint-view-without-api/`), uses `go/ast` + `regexp` for JSX/Tauri | n/a |

## 3. Existing Patterns to Follow

- **Extend `internal/mcp/`** — do **NOT** create a sibling `internal/mcp2/` or `internal/agentic/`. The package already has 14 files (transport_stdio, transport_sse, transport_http, registry, discovery, codebase_server, stoke_server, etc.). New files land alongside.
- **Reuse the JSON-RPC 2.0 envelope** in `transport_stdio.go` / `transport_http.go` / `transport_sse.go`. All three transports MUST advertise the same `tools/list` payload; the only difference is framing.
- **Reuse the `ToolDefinition` struct** from `internal/mcp/types.go` (already used by `StokeServer.ToolDefinitions()` and `CodebaseServer.ToolDefinitions()`).
- **Reuse the `hub.Bus` subscriber pattern** (`hub/builtin/`) for any tool that streams (lanes, bus.tail, lanes.subscribe). Subscribers register on session start, drop on cancel.
- **Reuse `stokerr/` taxonomy** for every error response. Map every internal error to one of the 10 codes; never return a raw Go error string.
- **Reuse `contentid/` for stable IDs** in tool responses (never random suffixes — see `aria-apg-impl` citation in research; same anti-pattern in agent harnesses).
- **Reuse `procutil.ConfigureProcessGroup` / `procutil.Terminate`** for any subprocess the MCP server spawns (Playwright, Storybook, agent-feature-runner). Matches `stoke_server.go` realSpawn pattern.
- **Reuse the Slack-style envelope** for every tool response: `{ok: bool, data?: any, error_code?: string, error_message?: string, links?: {self, related}}`. Even when wrapping legacy tools, normalize to this envelope at the `r1_server.go` boundary.

## 4. MCP Tool Catalog (Verbatim Schemas)

All tools live under the `r1.` namespace. The legacy `stoke_*` aliases (build_from_sow, get_mission_status, get_mission_logs, cancel_mission, list_missions) are preserved verbatim per `canonicalStokeServerToolName` and dispatch to the same handlers.

### 4.1 Sessions

```json
{
  "name": "r1.session.start",
  "description": "Start a new r1 session bound to a workdir. Returns session_id used by every other r1.* tool.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "workdir": {"type": "string", "description": "Absolute path. Becomes SessionRoot per D-D2."},
      "model":   {"type": "string", "description": "Model alias (e.g. 'claude-sonnet-4-6'). Defaults to config.", "default": ""},
      "resume_session_id": {"type": "string", "description": "If set, resume the prior session instead of creating a new one."}
    },
    "required": ["workdir"]
  }
}
```

```json
{
  "name": "r1.session.send",
  "description": "Send a user message to a session. Returns message_id. Idempotent on (session_id, client_message_id).",
  "inputSchema": {
    "type": "object",
    "properties": {
      "session_id": {"type": "string"},
      "message":    {"type": "string"},
      "client_message_id": {"type": "string", "description": "Caller-chosen idempotency key."}
    },
    "required": ["session_id", "message"]
  }
}
```

```json
{ "name": "r1.session.cancel",
  "description": "Cancel the in-flight turn for a session. Per D-C4, drops partial assistant message; drains SSE; never persists partial state.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]} }
```

```json
{ "name": "r1.session.list",
  "description": "List sessions on this daemon. Read-only.",
  "inputSchema": {"type":"object","properties":{"include_finished":{"type":"boolean","default":false}}} }
```

```json
{ "name": "r1.session.get",
  "description": "Get full session state: workdir, model, status, last seq, lane summary.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]} }
```

```json
{ "name": "r1.session.resume",
  "description": "Resume a paused or disconnected session. Replays bus events since last_seq.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"since_seq":{"type":"integer","default":0}},"required":["session_id"]} }
```

### 4.2 Lanes

```json
{ "name": "r1.lanes.list",
  "description": "List lanes for a session with status (pending|running|blocked|done|errored|cancelled — D-S1).",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]} }
```

```json
{ "name": "r1.lanes.subscribe",
  "description": "Stream lane events (SSE/WS). Server emits LaneEvent at 5–10 Hz coalesced (D-S2). Client must consume or back-pressure.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"since_seq":{"type":"integer","default":0}},"required":["session_id"]} }
```

```json
{ "name": "r1.lanes.get",
  "description": "Fetch one lane: status, model, started_at, message tail, latest Note refs.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"lane_id":{"type":"string"}},"required":["session_id","lane_id"]} }
```

```json
{ "name": "r1.lanes.kill",
  "description": "Terminate a lane (SIGTERM → SIGKILL via procutil). Idempotent: no-op if lane already finished.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"lane_id":{"type":"string"}},"required":["session_id","lane_id"]} }
```

```json
{ "name": "r1.lanes.pin",
  "description": "Pin a lane to the spotlight (TUI focus + web sidebar). Per D-C1 GWT spotlight semantics.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"lane_id":{"type":"string"},"pinned":{"type":"boolean","default":true}},"required":["session_id","lane_id"]} }
```

### 4.3 Cortex

```json
{ "name": "r1.cortex.notes",
  "description": "Read the Workspace: list of Notes published by Lobes this turn (and prior turns within the cache window).",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"since_seq":{"type":"integer","default":0}},"required":["session_id"]} }
```

```json
{ "name": "r1.cortex.publish",
  "description": "Publish a Note to the Workspace from an external agent. Useful for tests and human-in-the-loop hints.",
  "inputSchema": {"type":"object","properties":{
    "session_id":{"type":"string"},
    "note":{"type":"object","properties":{"text":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"critical":{"type":"boolean","default":false}},"required":["text"]}
  },"required":["session_id","note"]} }
```

```json
{ "name": "r1.cortex.lobes_list",
  "description": "List Lobes (memory-recall, rule-check, plan-update, clarifying-Q, memory-curator, WAL-keeper). Status + last activity.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]} }
```

```json
{ "name": "r1.cortex.lobe_pause",
  "description": "Pause a specific Lobe for a session (e.g. silence the rule-check Lobe during a known-good test).",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"lobe":{"type":"string"}},"required":["session_id","lobe"]} }
```

```json
{ "name": "r1.cortex.lobe_resume",
  "description": "Resume a previously-paused Lobe.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"lobe":{"type":"string"}},"required":["session_id","lobe"]} }
```

### 4.4 Missions

```json
{ "name": "r1.mission.create",
  "description": "Create a mission (multi-turn, multi-lane unit of work). Returns mission_id.",
  "inputSchema": {"type":"object","properties":{
    "session_id":{"type":"string"},
    "spec":{"type":"object","description":"Mission spec — plan-shaped, see plan/ package."}
  },"required":["session_id","spec"]} }
```

```json
{ "name": "r1.mission.list",
  "description": "List missions for a session (or all sessions if session_id omitted).",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"}}} }
```

```json
{ "name": "r1.mission.cancel",
  "description": "Cancel a mission. Idempotent.",
  "inputSchema": {"type":"object","properties":{"mission_id":{"type":"string"}},"required":["mission_id"]} }
```

```json
{ "name": "r1.mission.get",
  "description": "Get mission state: phase, tasks, attempts, lanes, artifacts, cost.",
  "inputSchema": {"type":"object","properties":{"mission_id":{"type":"string"}},"required":["mission_id"]} }
```

### 4.5 Worktrees

```json
{ "name": "r1.worktree.list",
  "description": "List worktrees for a session: path, base_commit, status, lane_id owner.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]} }
```

```json
{ "name": "r1.worktree.diff",
  "description": "Return git diff BaseCommit..HEAD for a worktree. Token-budget caps at 200KiB unless 'full': true.",
  "inputSchema": {"type":"object","properties":{"worktree_id":{"type":"string"},"full":{"type":"boolean","default":false}},"required":["worktree_id"]} }
```

```json
{ "name": "r1.worktree.merge",
  "description": "Merge a worktree to main via 'git merge-tree --write-tree' (zero-side-effect validation) then 'git merge'. Serialized by mergeMu.",
  "inputSchema": {"type":"object","properties":{"worktree_id":{"type":"string"},"strategy":{"type":"string","enum":["ff-only","squash","ours"],"default":"ff-only"}},"required":["worktree_id"]} }
```

```json
{ "name": "r1.worktree.clean",
  "description": "Destroy a worktree (--force + os.RemoveAll fallback + worktree prune). Idempotent.",
  "inputSchema": {"type":"object","properties":{"worktree_id":{"type":"string"}},"required":["worktree_id"]} }
```

### 4.6 Bus

```json
{ "name": "r1.bus.tail",
  "description": "Stream bus events from since_seq (SSE/WS framing). Causality-ordered per WAL.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"since_seq":{"type":"integer","default":0}},"required":["session_id"]} }
```

```json
{ "name": "r1.bus.replay",
  "description": "Replay bus events for [from_seq, to_seq] inclusive. Read-only; deterministic.",
  "inputSchema": {"type":"object","properties":{"session_id":{"type":"string"},"from_seq":{"type":"integer"},"to_seq":{"type":"integer"}},"required":["session_id","from_seq","to_seq"]} }
```

### 4.7 Verify

```json
{ "name": "r1.verify.build",
  "description": "Run 'go build ./...' (or detected toolchain) in the worktree. Returns exit_code, log tail.",
  "inputSchema": {"type":"object","properties":{"worktree_id":{"type":"string"}},"required":["worktree_id"]} }
```

```json
{ "name": "r1.verify.test",
  "description": "Run scoped tests (testselect package decides scope). Returns pass/fail counts + failing test names.",
  "inputSchema": {"type":"object","properties":{"worktree_id":{"type":"string"},"packages":{"type":"array","items":{"type":"string"}}},"required":["worktree_id"]} }
```

```json
{ "name": "r1.verify.lint",
  "description": "Run 'go vet' + project linters. Includes the lint-view-without-api scanner.",
  "inputSchema": {"type":"object","properties":{"worktree_id":{"type":"string"}},"required":["worktree_id"]} }
```

### 4.8 TUI

```json
{ "name": "r1.tui.press_key",
  "description": "Inject a tea.Msg into a teatest harness. Keys: 'k', 'j', 'enter', 'esc', 'ctrl+c', 'tab'. Char keys: '<x>'.",
  "inputSchema": {"type":"object","properties":{"tui_session_id":{"type":"string"},"key":{"type":"string"}},"required":["tui_session_id","key"]} }
```

```json
{ "name": "r1.tui.snapshot",
  "description": "Capture {view: string (deterministic ANSI-stripped), tree: {role,name,state}[], focus: stable_id}. No screenshots, no OCR.",
  "inputSchema": {"type":"object","properties":{"tui_session_id":{"type":"string"}},"required":["tui_session_id"]} }
```

```json
{ "name": "r1.tui.get_model",
  "description": "Introspect the live tea.Model via reflection (Noteleaf pattern). Returns JSON projection at jsonpath.",
  "inputSchema": {"type":"object","properties":{"tui_session_id":{"type":"string"},"jsonpath":{"type":"string","default":"$"}},"required":["tui_session_id"]} }
```

```json
{ "name": "r1.tui.focus_lane",
  "description": "Focus a specific lane in the TUI (equivalent to pressing 'j'/'k' until lane_id is the spotlight).",
  "inputSchema": {"type":"object","properties":{"tui_session_id":{"type":"string"},"lane_id":{"type":"string"}},"required":["tui_session_id","lane_id"]} }
```

### 4.9 Web (delegated to Playwright MCP)

```json
{ "name": "r1.web.navigate",
  "description": "Navigate the r1d web UI. Thin wrapper over Playwright MCP 'browser_navigate'.",
  "inputSchema": {"type":"object","properties":{"url":{"type":"string"}},"required":["url"]} }
```

```json
{ "name": "r1.web.click",
  "description": "Click by accessibility selector (role + name preferred; data-testid fallback). Wraps Playwright MCP 'browser_click'.",
  "inputSchema": {"type":"object","properties":{"selector":{"type":"string"}},"required":["selector"]} }
```

```json
{ "name": "r1.web.fill",
  "description": "Fill a form field by accessibility selector. Wraps Playwright MCP 'browser_fill'.",
  "inputSchema": {"type":"object","properties":{"selector":{"type":"string"},"value":{"type":"string"}},"required":["selector","value"]} }
```

```json
{ "name": "r1.web.snapshot",
  "description": "Return the structured a11y tree (NOT a screenshot). Wraps Playwright MCP 'browser_snapshot'.",
  "inputSchema": {"type":"object","properties":{}} }
```

### 4.10 CLI

```json
{ "name": "r1.cli.invoke",
  "description": "One-shot wrapper around 'r1 <args>' for headless ops. Returns {exit_code, stdout, stderr, duration_ms}. Process-group isolated.",
  "inputSchema": {"type":"object","properties":{
    "args":{"type":"array","items":{"type":"string"}},
    "workdir":{"type":"string"},
    "stdin":{"type":"string","default":""},
    "timeout_sec":{"type":"integer","default":120}
  },"required":["args","workdir"]} }
```

**Tool count: 38** (6 sessions + 5 lanes + 5 cortex + 4 missions + 4 worktrees + 2 bus + 3 verify + 4 TUI + 4 web + 1 CLI). The catalog spans 10 categories; the lint at §8 enforces at least one fixture per category.

## 5. TUI Shim Contract (Verbatim Go Signatures)

File: `internal/tui/teatest_shim.go`. New file.

```go
package tui

import (
    "context"
    "encoding/json"
    "io"
    "time"

    tea "github.com/charmbracelet/bubbletea/v2"
    "github.com/charmbracelet/x/exp/teatest"
)

// TUISessionID uniquely identifies a teatest harness session opened over MCP.
// Format: "tui-<contentid prefix>" so it is stable across reconnects.
type TUISessionID string

// Shim is the MCP-facing surface for the teatest harness. One Shim instance
// per r1d daemon; it owns all live TUISession entries.
type Shim interface {
    // Start launches a new teatest.Program-backed session for the given
    // root-model factory. Returns immediately once the program is ready
    // to receive Send().
    Start(ctx context.Context, modelFactory func() tea.Model, opts ...teatest.ProgramOption) (TUISessionID, error)

    // PressKey injects a key event. Mapping: "enter" -> tea.KeyEnter,
    // "esc" -> tea.KeyEsc, "ctrl+c" -> tea.KeyCtrlC, "tab" -> tea.KeyTab,
    // single chars -> tea.KeyRunes. Errors on unknown keys.
    PressKey(id TUISessionID, key string) error

    // Snapshot returns the deterministic view (lipgloss profile = Ascii),
    // the synthetic accessibility tree, and the currently-focused stable ID.
    // Idempotent; safe to call concurrently.
    Snapshot(id TUISessionID) (Snapshot, error)

    // GetModel returns a JSON projection of the live tea.Model at the
    // given JSONPath. Empty path returns the whole model.
    GetModel(id TUISessionID, jsonPath string) (json.RawMessage, error)

    // FocusLane drives "j"/"k" key presses until the given lane_id is
    // focused. No-op if already focused. Errors if lane not present after
    // a full cycle.
    FocusLane(id TUISessionID, laneID string) error

    // WaitFor blocks up to timeout for the predicate (regex on view OR
    // jsonpath match on model) to be satisfied. Used by the feature runner.
    WaitFor(id TUISessionID, predicate Predicate, timeout time.Duration) error

    // Stop terminates the session, drains stdout, calls FinalModel, and
    // returns the captured output for golden-file diffing.
    Stop(id TUISessionID) (FinalOutput, error)
}

type Snapshot struct {
    View  string         `json:"view"`           // ANSI-stripped, deterministic
    Tree  []A11yNode     `json:"tree"`           // role+name+state for every actionable
    Focus string         `json:"focus"`          // stable_id of the focused element
    Seq   int64          `json:"seq"`            // monotonic per-session
}

type A11yNode struct {
    StableID string            `json:"stable_id"`
    Role     string            `json:"role"`     // "button", "list", "listitem", "textbox"
    Name     string            `json:"name"`     // accessible name (verb + noun)
    State    map[string]string `json:"state"`    // pressed, expanded, busy, selected
    Children []A11yNode        `json:"children,omitempty"`
}

type Predicate struct {
    Regex    string `json:"regex,omitempty"`
    JSONPath string `json:"jsonpath,omitempty"`
    Equals   string `json:"equals,omitempty"`
}

type FinalOutput struct {
    StdoutTail string          `json:"stdout_tail"`
    Model      json.RawMessage `json:"model"`
    DurationMs int64           `json:"duration_ms"`
}

// NewShim constructs the singleton (one per process). out is typically
// io.Discard in production; tests can pass a *bytes.Buffer to capture.
func NewShim(out io.Writer) Shim
```

Implementation notes:

- `lipgloss.SetColorProfile(termenv.Ascii)` MUST be called in `NewShim` before any program starts so snapshots are byte-deterministic across CI runners.
- `teatest.WithInitialTermSize(120, 40)` is the default; agents can override per session.
- The synthetic A11y tree is built by traversing the `tea.Model` for any field implementing an `A11yEmitter` interface (defined in `internal/tui/a11y.go`, ships with spec 4 tui-lanes). If a model does not implement it, the lint at §8 fails.

## 6. Web Test Recipes (3 Example .agent.feature.md Files in Full)

All web feature files live at `tests/agent/web/*.agent.feature.md`. The runner at §10 parses them; each `When` step maps to one MCP tool call.

### 6.1 `tests/agent/web/chat-send-message.agent.feature.md`

```markdown
# tests/agent/web/chat-send-message.agent.feature.md

<!-- TAGS: smoke, web, chat -->
<!-- DEPENDS: r1d-server, web-chat-ui -->

## Scenario: User sends a message and sees a streamed response

- Given a fresh r1d daemon at "http://127.0.0.1:3948"
- And the web UI is loaded at "/"
- And a session is started with workdir "/tmp/agentic-test-1"
- When I fill the textbox with name "Message" with "ping"
- And I click the button with name "Send"
- Then within 5 seconds the chat log contains an assistant message matching "pong|ping"
- And the cortex Workspace contains at least one Note tagged "memory-recall"
- And no lane has status "errored"

## Tool mapping (informative, runner derives automatically)
- "loaded at" → r1.web.navigate
- "fill the textbox" → r1.web.fill
- "click the button" → r1.web.click
- "chat log contains" → r1.web.snapshot + assertion
- "cortex Workspace" → r1.cortex.notes
- "no lane has status" → r1.lanes.list
```

### 6.2 `tests/agent/web/lane-kill-from-sidebar.agent.feature.md`

```markdown
# tests/agent/web/lane-kill-from-sidebar.agent.feature.md

<!-- TAGS: web, lanes, idempotency -->
<!-- DEPENDS: lanes-protocol, web-chat-ui -->

## Scenario: Killing a lane from the sidebar produces an idempotent state

- Given a session with id "${SESSION_ID}" running at least 2 lanes
- And the web UI is focused on the agents sidebar
- When I click the button with name "Kill lane memory-curator"
- Then within 2 seconds r1.lanes.list reports lane "memory-curator" with status "cancelled"
- And the cortex Workspace contains a Note with tag "lane_cancelled" and lobe "memory-curator"
- When I click the button with name "Kill lane memory-curator" again
- Then r1.lanes.list still reports lane "memory-curator" with status "cancelled"
- And no error toast is visible in the UI snapshot

## Negative case: missing API counterpart
- Given the lint scanner ran on this PR
- Then no React component in web/src/ has an onClick handler without a matching r1.* MCP tool
```

### 6.3 `tests/agent/web/cortex-publish-and-observe.agent.feature.md`

```markdown
# tests/agent/web/cortex-publish-and-observe.agent.feature.md

<!-- TAGS: web, cortex, agentic-loopback -->
<!-- DEPENDS: cortex-core, cortex-concerns, web-chat-ui -->

## Scenario: External agent publishes a Note and the UI reflects it

- Given an external agent connected over MCP
- And a session "${SESSION_ID}" with the web UI open
- When the external agent calls r1.cortex.publish with note { text: "remember: prefer SQLite", tags: ["preference"], critical: true }
- Then within 1 second the Workspace pane in the UI shows a Note with text "remember: prefer SQLite"
- And the Note is rendered with role "listitem" and aria-label containing "preference"
- And the Note has aria-current="true" because it is critical
- When the user clicks the button with name "Pin Note"
- Then r1.cortex.notes reports the Note with field "pinned": true
- And re-clicking "Pin Note" leaves "pinned": true (idempotency)
```

## 7. Storybook MCP Setup

Goal: every `web/src/components/*.tsx` ships with a `*.stories.tsx` declaring `role`, `name`, `state` for every actionable element. Storybook MCP exposes those as fixtures and CI runs the contract checker.

- Add `web/.storybook/main.ts` and `web/.storybook/preview.ts` (stock Storybook 9 config).
- Add `web/.storybook/mcp.config.ts` with `port: 6007`, `transport: 'stdio'`, `expose: ['stories', 'a11y', 'interactions']`.
- CI step: `npx storybook-mcp@latest validate web/.storybook/mcp.config.ts --fail-on-missing-a11y` runs after the unit-test job.
- Every story file declares the contract:

```ts
// LaneSidebar.stories.tsx
export const Default: Story = {
  render: () => <LaneSidebar lanes={fixture} />,
  parameters: {
    a11y: { role: 'complementary', name: 'Agents sidebar' },
    agentic: {
      // Required: every interactive descendant declared here.
      // Lint at §8 checks this list against the rendered DOM.
      actionables: [
        { role: 'button', name: /^Kill lane /, mcp_tool: 'r1.lanes.kill' },
        { role: 'button', name: /^Pin lane /,  mcp_tool: 'r1.lanes.pin' },
        { role: 'listitem', name: /^Lane /,    mcp_tool: 'r1.lanes.get' },
      ],
    },
  },
}
```

## 8. CI Lint Design — `tools/lint-view-without-api/main.go`

**Rule:** every interactive component must (a) carry a stable `data-testid` (or equivalent TUI A11yNode stable_id, or Tauri command name) AND (b) reference an MCP tool name that exists in the `r1_server.go` catalog.

### 8.1 Detection rules

| Surface | What counts as "interactive" | Required metadata |
|---|---|---|
| React (`web/src/**/*.tsx`) | Any element with `onClick`, `onChange`, `onSubmit`, `onKeyDown`, `role="button"`, or in a `*.stories.tsx` `actionables` array | `data-testid` AND `agentic.mcp_tool` reference |
| Bubble Tea (`internal/tui/**/*.go`) | Any model that consumes `tea.KeyMsg` and dispatches a state change | `A11yEmitter` impl with `StableID` AND `mcp_tool` tag in the case branch |
| Tauri (`desktop/src-tauri/**/*.rs`) | Any `#[tauri::command]` function | `mcp_tool` doc-comment annotation |

### 8.2 Algorithm (pseudocode)

```
1. Walk the source trees with go/ast (Go), regexp+JSX-tokenizer (TSX), syn-AST shell-out for Rust.
2. For each interactive element, extract { surface, location, mcp_tool_ref, stable_id }.
3. Load the MCP tool catalog by spawning `r1 mcp serve --print-tools` and parsing JSON.
4. For each interactive element:
     - if mcp_tool_ref is empty → FAIL with message "view-without-API at <loc>: declare an MCP tool"
     - if mcp_tool_ref is not in catalog → FAIL with "unknown MCP tool '<ref>' at <loc>"
     - if stable_id is empty → FAIL with "missing stable_id at <loc>"
5. For each MCP tool in catalog with category in {sessions,lanes,cortex,missions,worktrees}:
     - if no UI surface references it AND the tool is not flagged headless-only → WARN.
6. Exit non-zero on any FAIL.
```

### 8.3 Allowlist

A `tools/lint-view-without-api/allowlist.yaml` permits flagging headless-only tools (`r1.cli.invoke`, `r1.bus.replay`) so they don't trigger the unused-tool warning. Allowlist entries require a justification string and an issue link.

### 8.4 Wiring

- New `make lint-views` target.
- Added to `r1.verify.lint` MCP tool's pipeline.
- Added to GitHub Actions matrix: `name: lint-views` after `go vet`.

## 9. Documentation: `docs/AGENTIC-API.md` Outline

The contract for external agents. Audience: agent authors integrating with r1.

```
1. Audience and promise
   - Single MCP wire, governing-principle quoted verbatim.
2. Wire protocol
   - MCP 2025-11-25, transports: stdio, SSE, HTTP (loopback only by default).
   - Auth: token via Sec-WebSocket-Protocol subprotocol (D-S6).
3. Tool catalog (38 tools across 10 categories, generated from r1_server.go via `r1 mcp serve --print-tools --markdown`)
4. Streaming and replay
   - r1.lanes.subscribe, r1.bus.tail, since_seq semantics, replay-on-reconnect (D-D3).
5. Idempotency rules
   - client_message_id on r1.session.send, idempotent kill/cancel, no double-effect on retry.
6. Error envelope
   - Slack-style {ok, error_code, error_message}; full taxonomy from stokerr/.
7. Capability flags
   - --caps=write opt-in for mutations; read-only by default for untrusted agents.
8. Test harness
   - How to write *.agent.feature.md, how to run the agent-feature-runner, CI integration.
9. UI-author guide
   - data-testid + role + accessible name; Storybook actionables; lint behavior.
10. Versioning and deprecation
    - SemVer on tool names, dual-name aliases until v2.0.0 (per stoke_* → r1.* rule), CHANGELOG entries.
11. Examples
    - Claude Code MCP config, Codex CLI, Stagehand, browser-use snippet.
12. Non-goals
    - Computer Use as primary driver (revisit Q3 2026 per RT-AGENTIC-TEST §2).
    - Generic CLI scraping; use r1.cli.invoke instead.
```

## 10. Test Plan: Meta-Test Over All `.agent.feature.md` Fixtures

A meta-test (`tools/agent-feature-runner/runner_test.go`) walks `tests/agent/**/*.agent.feature.md` and executes every scenario through the MCP catalog. It runs against:

1. An ephemeral r1d daemon spun up in-process (`r1d.NewTestServer(t)`).
2. The TUI shim from §5 (no terminal emulator).
3. A Playwright MCP child process (`npx @playwright/mcp@latest` in headless mode) for web fixtures.

For each scenario:

- Parse Given/When/Then steps. Map each step to a tool call via the heuristic table in §6 plus per-file `Tool mapping` blocks.
- Execute steps in order. Each `Then` step fails fast on assertion miss.
- On failure, capture: snapshot (TUI or web a11y tree), bus tail since scenario start, last 20 lanes events. Write to `.agent-failures/<scenario>/`.
- Total runtime budget: 5 minutes per scenario, 30 minutes per suite.

Coverage gate: every category in §4 has at least one fixture (sessions, lanes, cortex, missions, worktrees, bus, verify, tui, web, cli). The lint also checks this.

## 10a. Risks & Mitigations

| Risk | Surface | Mitigation |
|---|---|---|
| **Snapshot drift** — TUI/web golden snapshots churn on every cosmetic change, making CI a noise machine and pushing reviewers to rubber-stamp updates. | `r1.tui.snapshot`, `r1.web.snapshot`, `tests/agent/**/*.agent.feature.md` | (a) Snapshots assert on the **structured a11y tree** (`role`+`name`+`state`), NOT pixel views — strings only used for human debugging. (b) `make agent-features-update` re-records all expected trees in one shot when a UI redesign is intentional, and the resulting diff is reviewed alongside the UI diff in the same PR. (c) `lipgloss.SetColorProfile(termenv.Ascii)` + fixed `WithInitialTermSize(120,40)` removes terminal-driven flake. (d) The lint at §8 fails when a snapshot is auto-updated without an accompanying source-code change in `web/src/` or `internal/tui/` (signature: empty source diff + non-empty golden diff). |
| **Tool catalog vs UI drift** — UI ships a button before the MCP tool exists, or vice-versa. | `web/src/`, `internal/tui/`, `desktop/src-tauri/` | §8 lint runs in `r1.verify.lint` AND CI; PRs cannot merge with view-without-API. |
| **Playwright/Storybook MCP version churn** breaks CI overnight when `@latest` resolves to a new major. | `tools/agent-feature-runner`, `web/.storybook/mcp.config.ts` | Pin major versions: `@playwright/mcp@^0` and `storybook-mcp@^9` in CI; `@latest` only in local docs. Renovate bot opens PRs for upgrades; humans review. |
| **Stoke alias removal** silently breaks external clients still calling `stoke_*`. | `internal/mcp/r1_server.go`, `canonicalStokeServerToolName` | Keep dual names until v2.0.0; emit a one-time deprecation warning per session via `r1.session.start` response `links.deprecations[]`. Removal scheduled in CHANGELOG. |
| **Reflection-based `r1.tui.get_model`** leaks unintended internal state to external agents. | `internal/tui/teatest_shim.go` | `A11yEmitter`-derived projection only; raw `tea.Model` access guarded behind `--caps=debug` flag (off by default), audited via `bus.tail`. |
| **CI lint false negatives** — TSX scanner misses dynamic `onClick={fn}` bindings created by hooks. | `tools/lint-view-without-api/main.go` | Two-pass scan: (1) static AST/regex pass; (2) runtime pass using Storybook MCP `interactions` channel which lists every actually-rendered handler. Both must agree; mismatch fails the build. |
| **Test runtime budget blows out** when many `.agent.feature.md` fixtures land. | `tools/agent-feature-runner` | Per-scenario cap (5 min) + per-suite cap (30 min); scenarios tagged `@slow` run nightly only. |

## 11. Out of Scope

The following are explicitly NOT shipped by this spec and live in specs 1–7:

- Implementation of the cortex Workspace, Lobes, Notes (specs 1, 2: cortex-core, cortex-concerns).
- Lane lifecycle, status vocabulary, render coalescing (spec 3: lanes-protocol).
- TUI Bubble Tea models that emit A11y trees (spec 4: tui-lanes).
- r1d daemon, transport, session routing (spec 5: r1d-server).
- React web UI components (spec 6: web-chat-ui).
- Tauri desktop integration (spec 7: desktop-cortex-augmentation).
- Computer Use as a driver (deferred until Q3 2026 per research).
- Vision-driven test agents (Stagehand, browser-use) — supported as MCP clients but not vendored.
- A bespoke DSL replacing Gherkin markdown.

## 12. Checklist (40 items — self-contained)

### MCP server consolidation
- [ ] Create `internal/mcp/r1_server.go` registering the full §4 catalog under JSON-RPC `tools/list`.
- [ ] Migrate `internal/mcp/stoke_server.go` content into `r1_server.go`; keep `stoke_server.go` as a deprecated shim that re-exports `NewStokeServer` for backward compatibility until v2.0.0.
- [ ] Preserve every legacy `stoke_*` tool name via `canonicalStokeServerToolName` aliasing; add tests asserting both names dispatch to the same handler.
- [ ] Create `internal/mcp/lanes_server.go` implementing the 5 lane tools; signatures must match spec 3 (lanes-protocol) — confirm with that spec before merge.
- [ ] Create `internal/mcp/cortex_server.go` implementing the 5 cortex tools.
- [ ] Create `internal/mcp/tui_server.go` delegating to the teatest shim from §5.
- [ ] Add a Slack-style envelope wrapper at the `r1_server.go` boundary so every tool returns `{ok, data?, error_code?, error_message?, links?}`.
- [ ] Map every internal error to a `stokerr/` taxonomy code; never return a raw Go error string from a tool handler.
- [ ] Add `r1 mcp serve --print-tools` flag that emits the full catalog as JSON (used by the lint).
- [ ] Add `r1 mcp serve --print-tools --markdown` for the AGENTIC-API.md generator.

### TUI shim
- [ ] Create `internal/tui/teatest_shim.go` with the verbatim signatures from §5.
- [ ] Add `internal/tui/a11y.go` defining `A11yEmitter` interface (StableID() string, A11y() A11yNode).
- [ ] Wire `lipgloss.SetColorProfile(termenv.Ascii)` in `NewShim` for deterministic snapshots.
- [ ] Add `internal/tui/teatest_shim_test.go` covering Start/PressKey/Snapshot/GetModel/FocusLane/Stop.
- [ ] Reflect-based JSONPath model introspection (use `tidwall/gjson` if not already a dep, else fallback to `encoding/json` round-trip).

### Test DSL + runner
- [ ] Create `tools/agent-feature-runner/main.go` that parses `*.agent.feature.md` and executes via MCP.
- [ ] Define the Given/When/Then-to-tool mapping heuristics; allow per-file `Tool mapping` block override.
- [ ] Support `${SESSION_ID}` and `${MISSION_ID}` variable interpolation with prior-step output.
- [ ] Capture failure context (snapshot, bus tail, lanes list) to `.agent-failures/<scenario>/`.
- [ ] Add `make agent-features` target that runs the meta-test from §10.
- [ ] Add `make agent-features-update` target that re-records expected a11y trees + view strings (snapshot-drift mitigation per §10a).
- [ ] Add a CI check that fails when the golden snapshot diff is non-empty AND the source diff in `web/src/`, `internal/tui/`, `desktop/src-tauri/` is empty (catches accidental auto-updates).

### Seed fixtures
- [ ] `tests/agent/tui/lanes-kill.agent.feature.md`.
- [ ] `tests/agent/tui/cortex-pin-note.agent.feature.md`.
- [ ] `tests/agent/web/chat-send-message.agent.feature.md` (verbatim §6.1).
- [ ] `tests/agent/web/lane-kill-from-sidebar.agent.feature.md` (verbatim §6.2).
- [ ] `tests/agent/web/cortex-publish-and-observe.agent.feature.md` (verbatim §6.3).
- [ ] `tests/agent/cli/session-start-stop.agent.feature.md` exercising r1.cli.invoke.
- [ ] `tests/agent/mission/mission-create-cancel.agent.feature.md`.
- [ ] `tests/agent/worktree/diff-and-merge.agent.feature.md`.

### Storybook MCP
- [ ] Add `web/.storybook/main.ts`, `preview.ts`, `mcp.config.ts`.
- [ ] Add Storybook 9 + `storybook-mcp` to `web/package.json` devDependencies.
- [ ] Author `*.stories.tsx` for every component in `web/src/components/`, each with the `parameters.agentic.actionables` contract from §7.
- [ ] CI step: `npx storybook-mcp@latest validate web/.storybook/mcp.config.ts --fail-on-missing-a11y`.

### CI lint
- [ ] Create `tools/lint-view-without-api/main.go` implementing the §8.2 algorithm.
- [ ] Add `tools/lint-view-without-api/allowlist.yaml` with justification format.
- [ ] Wire `make lint-views` and add to GitHub Actions matrix as job `lint-views`.
- [ ] Add `tools/lint-view-without-api/main_test.go` with positive and negative fixtures.
- [ ] Wire the lint into `r1.verify.lint` so the MCP tool reports the same failures the CI does.

### Documentation
- [ ] Create `docs/AGENTIC-API.md` per the §9 outline; include the verbatim governing principle in the first paragraph.
- [ ] Generate the tool catalog section from `r1 mcp serve --print-tools --markdown` (Make target: `make docs-agentic`).
- [ ] Add a row to `docs/decisions/index.md` recording D-A1..D-A5 acceptance and link to this spec.
- [ ] Update `docs/FEATURE-MAP.md` Status: Done section once shipped.
