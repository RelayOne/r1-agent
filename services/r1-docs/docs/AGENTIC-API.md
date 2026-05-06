# r1 Agentic API — The Wire Surface for External Agents

> **Every action a human can take through a UI MUST have a documented, idempotent, schema-validated agent equivalent reachable through MCP. The UI is a view over the API; never the reverse.**

This document is the contract between the r1 daemon and external agents (Claude Code, Codex CLI, Stagehand, browser-use, custom MCP clients). If a UI button cannot be reached through this API, the UI button is broken — not the API.

## 1. Audience and promise

Audience: agent authors integrating with r1 over MCP, including but not limited to:

- Anthropic's Claude Code (stdio MCP)
- OpenAI Codex CLI (stdio MCP)
- Stagehand (browser automation that wraps Playwright MCP)
- browser-use (LLM-driven browser automation)
- Custom integrations via raw MCP JSON-RPC

Promise: the catalog described here is the only surface r1 exposes for programmatic control. Each tool is schema-validated, idempotent on its declared idempotency key, and never returns a raw Go error string — every error carries one of the 10 codes from `internal/stokerr/`.

## 2. Wire protocol

- **MCP version:** [2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25).
- **Transports:** stdio (default), SSE, HTTP (loopback only by default).
- **Auth:** token via `Sec-WebSocket-Protocol` subprotocol per D-S6 (spec 5 r1d-server).
- **Framing:** JSON-RPC 2.0; the `tools/list` payload is identical across transports — only the framing differs.

### Connecting from Claude Code

```json
{
  "mcpServers": {
    "r1": {
      "command": "r1",
      "args": ["mcp", "serve"]
    }
  }
}
```

### Connecting from Codex CLI

```toml
[mcp_servers.r1]
command = "r1"
args = ["mcp", "serve"]
```

## 3. Tool catalog (38 tools across 10 categories)

The full catalog is generated from `internal/mcp/r1_server_catalog.go` via:

```
r1 mcp serve --print-tools --markdown > docs/AGENTIC-API-CATALOG.md
```

Sections (counts):

| Category | Tools | Section |
|---|---|---|
| Sessions | 6 | r1.session.{start,send,cancel,list,get,resume} |
| Lanes | 5 | r1.lanes.{list,subscribe,get,kill,pin} |
| Cortex | 5 | r1.cortex.{notes,publish,lobes_list,lobe_pause,lobe_resume} |
| Missions | 4 | r1.mission.{create,list,cancel,get} |
| Worktrees | 4 | r1.worktree.{list,diff,merge,clean} |
| Bus | 2 | r1.bus.{tail,replay} |
| Verify | 3 | r1.verify.{build,test,lint} |
| TUI | 4 | r1.tui.{press_key,snapshot,get_model,focus_lane} |
| Web | 4 | r1.web.{navigate,click,fill,snapshot} (Playwright MCP wrappers) |
| CLI | 1 | r1.cli.invoke |

The full per-tool JSON schemas are in `specs/agentic-test-harness.md` §4. Run `r1 mcp serve --print-tools` for the live machine-readable form.

## 4. Streaming and replay

Two tools stream:

- **`r1.lanes.subscribe`** emits `LaneEvent` at 5–10 Hz coalesced (D-S2). Clients must consume or back-pressure; the server drops the slowest subscriber after a grace period.
- **`r1.bus.tail`** streams every event in the WAL since `since_seq`. Causality-ordered.

`since_seq` semantics: a 0 value replays from the start of the session; any positive integer replays from that exact sequence number. Reconnect-resume:

1. Client records the highest `seq` seen.
2. On disconnect (network blip, daemon restart), client reconnects with `since_seq = lastSeen + 1`.
3. Server replays missed events from the WAL, then resumes live tailing.

This is the D-D3 (durable replay) contract; clients that ignore it will see gaps under restart.

## 5. Idempotency rules

Every mutation tool is idempotent on the documented key:

| Tool | Idempotency key | Behavior |
|---|---|---|
| `r1.session.send` | `(session_id, client_message_id)` | Re-sending the same message_id is a no-op; returns the original message_id. |
| `r1.lanes.kill` | `(session_id, lane_id)` | Killing a lane that is already cancelled returns ok=true with status=cancelled; no error. |
| `r1.lanes.pin` | `(session_id, lane_id, pinned)` | Re-pinning an already-pinned lane is a no-op. |
| `r1.mission.cancel` | `mission_id` | Re-cancelling is a no-op. |
| `r1.worktree.clean` | `worktree_id` | Cleaning an already-cleaned worktree returns ok=true. |
| `r1.cortex.lobe_pause/_resume` | `(session_id, lobe)` | Idempotent on the target state. |

Tools NOT in this table (e.g. `r1.session.cancel`, `r1.mission.create`) are NOT idempotent — calling them twice has user-visible side effects (a new mission_id, etc.).

## 6. Error envelope

Every tool response wraps in the Slack-style envelope from `internal/mcp/envelope.go`:

```json
{
  "ok": false,
  "error_code": "not_found",
  "error_message": "session s-9 not found",
  "links": {
    "self": "r1.session.cancel",
    "related": ["r1.session.list"],
    "deprecations": []
  }
}
```

The `error_code` is one of the 10 stokerr/ taxonomy values:

| Code | Meaning |
|---|---|
| `validation` | Malformed input (missing field, bad shape) |
| `not_found` | Resource ID does not exist |
| `conflict` | Concurrent-mutation collision or state precondition |
| `append_only_violation` | Attempt to mutate immutable storage |
| `permission_denied` | RBAC or sandbox policy blocked the call |
| `budget_exceeded` | Cost/token cap reached |
| `timeout` | Deadline tripped |
| `crash_recovery` | State restored from checkpoint; partial replay possible |
| `schema_version` | Data-format mismatch; migration required |
| `internal` | Unexpected invariant — bug, file an issue |

See `internal/mcp/stokerr_map.go` for the mapping rules from arbitrary Go errors to taxonomy codes.

## 7. Capability flags

Untrusted agents are read-only by default:

- `--caps=read` (default for new MCP clients): only `r1.*` tools that do not mutate state.
- `--caps=write` (opt-in): mutations enabled; the daemon logs a one-time consent prompt on first mutating call.
- `--caps=debug`: enables `r1.tui.get_model` reflection-based introspection (off by default per spec 8 §10a).

## 8. Test harness (this spec)

External agents write `*.agent.feature.md` files under `tests/agent/` to assert behavior. The format is documented in `specs/agentic-test-harness.md` §6; in short:

```markdown
## Scenario: User sends a message and sees a streamed response

- Given a fresh r1d daemon at "http://127.0.0.1:3948"
- When I fill the textbox with name "Message" with "ping"
- And I click the button with name "Send"
- Then within 5 seconds the chat log contains an assistant message matching "pong|ping"
```

Run the suite:

```
make agent-features              # execute every scenario
make agent-features-update       # re-record golden a11y snapshots
make agent-features-drift-check  # CI guard against accidental updates
```

The runner at `tools/agent-feature-runner` walks `tests/agent/**/*.agent.feature.md`, dispatches each step against the r1.* catalog (heuristics in `dispatcher/heuristics.go`, per-file overrides via `## Tool mapping`), and writes failure context to `.agent-failures/<scenario>/` per spec 8 §10.

## 9. UI-author guide

If you are adding a UI button (React, Bubble Tea, or Tauri), the lint at `tools/lint-view-without-api/` will fail your PR unless:

- Your component carries a stable identifier:
  - React: `data-testid="..."`.
  - Bubble Tea: implements `tui.A11yEmitter` with a non-empty `StableID()`.
  - Tauri: `#[tauri::command]` with a doc-comment `mcp_tool` annotation.
- AND your component references an MCP tool that exists in the r1.* catalog:
  - React: `data-mcp_tool="r1.lanes.kill"` (or via the Storybook `parameters.agentic.actionables` block).
  - Bubble Tea: emit the tool name in the case branch's a11y state (e.g. `state["mcp_tool"] = "r1.lanes.kill"`).
  - Tauri: `/// mcp_tool: r1.lanes.kill` in the function's doc comment.

Adding a new UI surface without a corresponding MCP tool is a build break.

Adding a new MCP tool without a UI surface is a WARN (catalog tools should be reachable from a human-visible surface unless explicitly headless-only in `tools/lint-view-without-api/allowlist.yaml`).

## 10. Versioning and deprecation

- **SemVer on tool names.** The `r1.*` namespace is stable from v1.0.0; renames go through dual-name aliasing (see `canonicalStokeServerToolName` for the legacy `stoke_*` -> `r1_*` precedent).
- **Dual-name aliases until v2.0.0.** The 5 legacy `stoke_*` tools (`build_from_sow`, `get_mission_status`, `get_mission_logs`, `cancel_mission`, `list_missions`) remain reachable under both names. Removal is scheduled for v2.0.0; until then `r1.session.start` returns a deprecation warning in `links.deprecations[]` for sessions that invoked legacy names.
- **CHANGELOG entries.** Every breaking schema change to a tool's `inputSchema` lands as a CHANGELOG entry under `## [Unreleased]` with a `BREAKING:` prefix.

## 11. Examples

### Claude Code MCP config

```json
{
  "mcpServers": {
    "r1": { "command": "r1", "args": ["mcp", "serve"] }
  }
}
```

### Codex CLI MCP config

```toml
[mcp_servers.r1]
command = "r1"
args = ["mcp", "serve"]
```

### Stagehand snippet

```ts
import { Stagehand } from "@browserbasehq/stagehand";
const sh = new Stagehand({
  mcpClient: { command: "r1", args: ["mcp", "serve"] },
});
await sh.act({ tool: "r1.session.start", args: { workdir: "/tmp/demo" } });
```

### browser-use snippet

```python
from browser_use import Agent
agent = Agent(
    mcp_servers={"r1": {"command": "r1", "args": ["mcp", "serve"]}},
)
agent.run("send 'ping' to a fresh r1 session")
```

## 12. Non-goals

- **Computer Use as primary driver** — vision-driven control is deferred to Q3 2026 per `RT-AGENTIC-TEST §2`. The harness exercises a11y trees, not pixels.
- **Generic CLI scraping** — agents that want CLI access call `r1.cli.invoke` rather than re-implementing argv parsing.
- **A bespoke DSL replacing Gherkin markdown** — the `*.agent.feature.md` format is intentionally Gherkin-shaped so the cognitive load on humans reading the file is zero.

---

For the per-tool input schemas in machine-readable form, run:

```
r1 mcp serve --print-tools           # JSON
r1 mcp serve --print-tools --markdown # this document's tool catalog section
```

For the source of truth, see `specs/agentic-test-harness.md`.
