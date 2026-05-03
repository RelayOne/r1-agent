# Agentic API

> **Status:** Stub created by `specs/lanes-protocol.md` (BUILD_ORDER 3) for the
> `## Lanes` section. The full document is owned by
> `specs/agentic-test-harness.md` (BUILD_ORDER 8) and will expand to cover
> every programmatic surface (CLI, TUI, web, desktop) under one MCP-primary
> contract.

## Governing principle

Every UI action a human can take MUST have an idempotent, schema-validated
agent equivalent reachable through MCP. The UI is a view over the API; never
the reverse. CI lint enforces this — see
`scripts/lint-view-without-api/` (created by spec 8).

---

## Lanes

The lanes protocol exposes Cortex Workspace activity to other agents through a
stable wire format with five MCP tools, one HTTP+SSE endpoint, and one
WebSocket endpoint.

### Cross-surface model

A **lane** is the per-Lobe (or per-tool-call, or per-mission-task) UI-visible
thread of activity. Lane != Lobe — a lane can also represent the main agent
thread, or a single in-flight tool. Lobes are the cognitive abstraction; lanes
are the rendering and remote-observation abstraction.

### Lane state machine

```
pending → running → blocked → done
           ↓          ↓
         errored    cancelled
```

Plus an orthogonal `pinned` flag toggleable via `r1.lanes.pin` (no event
emitted — clients re-fetch via `r1.lanes.list`).

### Event types

Six event types fan out from `internal/streamjson/lane.go` to all surfaces:

| Event | Critical? | Purpose |
|-------|-----------|---------|
| `lane.created` | no | Lane registered (kind, parent, label, started_at). |
| `lane.status` | only when `status="errored"` | FSM transition (validated by spec §3.3 table). |
| `lane.delta` | no | Streaming content within a lane (text or tool-use ContentBlock). |
| `lane.cost` | no | Per-lane token + USD cost tick. |
| `lane.note` | only when `note_severity="critical"` | Lobe published a Note that this lane caused. |
| `lane.killed` | yes | Lane terminated (operator kill, error cascade, or completion). |

Critical events are top-level emitted on the streamjson NDJSON pipe; observability
events drop-oldest under backpressure.

### MCP tools

Five goal-shaped tools, registered in `internal/mcp/lanes_server.go`:

#### `r1.lanes.list`
Returns all current lanes for a session.
```json
{ "session_id": "string" }
→ { "lanes": [{ "lane_id": "...", "kind": "main|lobe|tool|mission_task|router", "status": "...", "label": "...", "started_at": "iso8601", "ended_at?": "iso8601", "pinned": false, "last_seq": 42 }, ...] }
```

#### `r1.lanes.subscribe`
Streaming tool — opens a subscription for the session's lane events.
```json
{ "session_id": "string", "since_seq?": 0 }
→ stream of { "type": "lane.*", "data": {...}, "seq": N }
```
Cancel via the streaming-call cancellation handle.

#### `r1.lanes.get`
Snapshot of a single lane plus optional bounded tail of its recent events.
```json
{ "session_id": "string", "lane_id": "string", "tail?": 100 }
→ { "lane": {...}, "events": [...] }
```

#### `r1.lanes.kill`
Terminates a lane; cascades to children (per spec §"cascade semantics").
Idempotent — if already terminal, returns `data.already_terminal:true`.
```json
{ "session_id": "string", "lane_id": "string", "reason?": "string" }
→ { "killed": true, "data": { "already_terminal?": false } }
```
Emits `lane.killed` plus a final `lane.status(cancelled_by_operator)`.

#### `r1.lanes.pin`
Toggles the lane's `pinned` flag. Pin is UI-only (clients tile pinned lanes
into the main pane); no event emitted. Surfaces re-fetch via `r1.lanes.list`.
```json
{ "session_id": "string", "lane_id": "string", "pinned": true|false }
→ { "lane_id": "...", "pinned": true|false }
```

### HTTP / WebSocket endpoints

- `GET /v1/lanes/events?session_id=...` — Server-Sent Events stream. Honors
  `Last-Event-ID` header for resume. Sets `X-R1-Lanes-Version: 1` response
  header.
- `GET /v1/lanes/ws?session_id=...` — WebSocket upgrade. Requires
  `Sec-WebSocket-Protocol: r1.lanes.v1, <bearer-token>` and a permitted
  `Origin`. JSON-RPC 2.0 envelope; the server pushes `$/event` notifications.

### Reconnect semantics

Clients that disconnect resume by passing `Last-Event-ID: <seq>` (SSE) or
`since_seq: N` (WS). The server replays from `internal/bus/` WAL starting at
`since_seq+1`. If the WAL has truncated past `N`, the server returns a
`wal_truncated` error and the client MUST re-subscribe with `since_seq=0`,
treating the lane state as authoritative-from-snapshot.

A synthetic `session.bound{seq:0, session_id:...}` is the FIRST event every
new subscription receives, so the client can detect session-mismatch and tear
down stale UI state.

### Backward compatibility

The pre-lanes JSON-RPC IPC contract is fully preserved. See
`desktop/IPC-CONTRACT.md` §1.5 (orthogonal `X-R1-Lanes-Version: 1` header) and
§4 (lane events appended to server-pushed event table; `session.delta`
co-emitted with `lane.delta` for the main lane during the compat window).

### Versioning

The lanes protocol version is independent of the RPC version. Bumping
`X-R1-Lanes-Version` does NOT bump `X-R1-RPC-Version`. The current versions
are:

| Header | Version | First shipped |
|--------|---------|---------------|
| `X-R1-RPC-Version` | 1 | R1D-1 |
| `X-R1-Lanes-Version` | 1 | lanes-protocol (BUILD_ORDER 3) |

---

## Other surfaces (forward-references)

The remaining sections of this document — sessions, missions, worktrees, bus
tail, verify, TUI control, web navigation, CLI invoke — are owned by
`specs/agentic-test-harness.md` (BUILD_ORDER 8) and land when that spec
builds.
