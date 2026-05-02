<!-- STATUS: ready -->
<!-- CREATED: 2026-05-02 -->
<!-- DEPENDS_ON: cortex-core -->
<!-- BUILD_ORDER: 3 -->

# Lanes Protocol — Cross-Surface Wire Format for Cortex Workspace Activity

## 1. Overview

A "lane" is the per-surface-visible thread of activity inside a single r1d session. Each lane represents one of: the main agent thread (the `agentloop.Loop` driving Sonnet/Opus), one cortex `Lobe` (e.g. `MemoryRecallLobe`, `PlanUpdateLobe`), one in-flight tool call, or one mission-task. Lanes are the rendering abstraction; Lobes are the cognitive abstraction. A Lobe maps 1:1 to a lane, but the main thread is also a lane, and a long-running tool call may be promoted to its own lane so the surface can show streaming progress and a kill control.

This spec freezes the wire format that flows from `cortex-core` (spec 1) outward to every consuming surface — TUI (`internal/tui/`), web UI (spec 6), Tauri desktop (`desktop/`), CLI NDJSON (`internal/streamjson/`), and external agents over MCP. It defines the lane state machine, the six lane event types, the JSON-RPC 2.0 envelope (compatible with the existing `desktop/IPC-CONTRACT.md`), the NDJSON and WebSocket framings, replay semantics with `Last-Event-ID`, the five MCP tool schemas that expose lanes to programmatic consumers, and the bridge points where cortex-core publishes into `internal/hub/Bus` and surfaces subscribe.

The governing principle (from RT-AGENTIC-TEST): every UI action on a lane has an idempotent, schema-validated agent equivalent in MCP. A surface is a view over this protocol; never the reverse.

## 2. Stack & Versions

- Go 1.25.5 (see `go.mod` line 3: `go 1.25.5`). No `go.work` exists in the repo; if added later, keep parity with `go.mod`. Matches `specs/cortex-core.md` §"Stack & Versions".
- Wire envelope: JSON-RPC 2.0 (matches `desktop/IPC-CONTRACT.md` §1).
- Streaming: NDJSON (one JSON object per line, no trailing comma) for stdout/stdin transport; WebSocket for browsers and the Tauri host; HTTP+SSE retained as fallback.
- Replay log: `internal/bus/` durable WAL (`bus.wal`) — already write-through per cortex D-C3 (see `docs/decisions/index.md`).
- MCP: spec `2025-11-25` (`modelcontextprotocol.io`). Tools registered via existing `internal/mcp/types.go` `ToolDefinition`.
- IDs: ULID (`oklog/ulid/v2` v2.1.1, already in `go.mod`) for `lane_id` and `event_id` so every emitted ID is monotonic-by-time and lex-sortable; per-session `seq` is `uint64` monotonic.
- WS subprotocol token: `r1.lanes.v1` (advertised in `Sec-WebSocket-Protocol`).

## 3. Lane State Machine

### 3.1 States

| State | Meaning | Terminal? |
|---|---|---|
| `pending` | Lane created, not yet running (queued behind concurrency cap, or awaiting parent gate) | No |
| `running` | Actively producing deltas (tool executing / Lobe thinking / main thread streaming) | No |
| `blocked` | Paused on external input — `awaiting_user` (clarifying question), `awaiting_review`, `awaiting_dependency` | No |
| `done` | Completed normally; final `lane.status` carries `reason="ok"` | Yes |
| `errored` | Failed; final `lane.status` carries `reason="<error_taxonomy_code>"` (mapped from `internal/stokerr`) | Yes |
| `cancelled` | Killed by operator / parent / budget gate; final `lane.status` carries `reason="cancelled_by_<actor>"` | Yes |

### 3.2 Orthogonal flag

- `pinned` (bool, default `false`). Surfaces show pinned lanes above unpinned. Persists across reconnect. Set/cleared only via `r1.lanes.pin` MCP tool or surface-emitted `pin` action.

### 3.3 Transition diagram

```
                       ┌──── pin ────┐ (orthogonal — does not change state)
                       │             │
                       ▼             ▼

  [created]──►( pending )──►( running )──►( done )       (terminal)
                  │             │  ▲
                  │             │  │
                  │             │  └── unblock ──┐
                  │             │                │
                  │             ▼                │
                  │         ( blocked )──────────┘
                  │             │
                  │             │
                  ├──cancel─────┤
                  │             │
                  ▼             ▼
              ( cancelled ) ( errored )
                                ▲
                                │
                  any state ────┘  (on unrecoverable error)
```

Allowed transitions (validated server-side; an unallowed transition is a `-32099 internal` error):

- `pending → running`, `pending → cancelled`
- `running → blocked`, `running → done`, `running → errored`, `running → cancelled`
- `blocked → running`, `blocked → cancelled`, `blocked → errored`

Terminal states (`done | errored | cancelled`) emit no further `lane.delta` events. A surface receiving a delta after terminal SHOULD discard it and log a `protocol.violation` warning.

## 4. Event Type Catalog

All lane events publish to `internal/hub/Bus` under a new event family `EventLaneXxx` (added in cortex-core spec 1) and write through to `internal/bus/` WAL with a per-session monotonic `seq`. Each event carries:

- `event_id` (ULID, globally unique).
- `session_id` (string).
- `seq` (uint64, monotonic per session).
- `at` (RFC 3339 nanosecond UTC timestamp).
- `lane_id` (string, ULID).

The six event types below are exhaustive. Adding a seventh is a wire-version bump (§5.6).

### 4.1 `lane.created`

Emitted exactly once per lane, before any other event for that `lane_id`.

```json
{
  "event": "lane.created",
  "event_id": "01J0K3M4P5Q6R7S8T9V0W1X2Y3",
  "session_id": "sess_01J0K3M4...",
  "seq": 142,
  "at": "2026-05-02T18:33:21.482917Z",
  "lane_id": "lane_01J0K3M4...",
  "data": {
    "kind": "lobe",
    "lobe_name": "MemoryRecallLobe",
    "parent_lane_id": "lane_01J0K3M3...",
    "label": "Recalling memories matching: 'cortex workspace'",
    "started_at": "2026-05-02T18:33:21.482000Z",
    "labels": {"model": "claude-haiku-4-5", "deterministic": "false"}
  }
}
```

`kind` enum: `main` | `lobe` | `tool` | `mission_task` | `router`. Exactly one lane per session has `kind == "main"`. `parent_lane_id` is empty for the main lane and for top-level mission tasks; otherwise required.

### 4.2 `lane.status`

State machine transition. `reason` is human-readable plus a stable enum tag.

```json
{
  "event": "lane.status",
  "event_id": "01J0K3M4P6...",
  "session_id": "sess_01J0K3M4...",
  "seq": 143,
  "at": "2026-05-02T18:33:21.491000Z",
  "lane_id": "lane_01J0K3M4...",
  "data": {
    "status": "running",
    "prev_status": "pending",
    "reason": "started",
    "reason_code": "started"
  }
}
```

`reason_code` enum: `started` | `tool_dispatch` | `awaiting_user` | `awaiting_review` | `awaiting_dependency` | `unblocked` | `ok` | `cancelled_by_operator` | `cancelled_by_parent` | `cancelled_by_budget` | `errored` (+ any `stokerr` code: `validation`, `not_found`, `conflict`, `budget_exceeded`, `timeout`, etc.).

### 4.3 `lane.delta`

Streaming content. One block per event (text chunk, tool-use start, partial JSON for tool input, etc.). Mirrors the shape of Anthropic's content blocks so the renderer can pass-through.

```json
{
  "event": "lane.delta",
  "event_id": "01J0K3M4P7...",
  "session_id": "sess_01J0K3M4...",
  "seq": 144,
  "at": "2026-05-02T18:33:21.512000Z",
  "lane_id": "lane_01J0K3M4...",
  "data": {
    "delta_seq": 7,
    "content_block": {
      "type": "text_delta",
      "text": "found 3 matching memories: "
    }
  }
}
```

`content_block.type` enum: `text_delta` | `thinking_delta` | `tool_use_start` | `tool_use_input_delta` | `tool_use_end` | `tool_result` | `note_ref` (when the lane mirrors a Note publish — payload is `{note_id}` only, full Note fetched via `r1.cortex.notes`). `delta_seq` is monotonic per `lane_id` and lets the surface detect intra-lane gaps independent of session-wide `seq`.

### 4.4 `lane.cost`

Cost tick. Emitted no more than once per second per lane.

```json
{
  "event": "lane.cost",
  "event_id": "01J0K3M4P8...",
  "session_id": "sess_01J0K3M4...",
  "seq": 158,
  "at": "2026-05-02T18:33:22.500000Z",
  "lane_id": "lane_01J0K3M4...",
  "data": {
    "tokens_in": 12480,
    "tokens_out": 312,
    "cached_tokens": 11200,
    "usd": 0.00184,
    "cumulative_usd": 0.00521
  }
}
```

### 4.5 `lane.note`

Mirror of a cortex `Note` publish. The full Note is fetched via the existing `r1.cortex.notes` MCP tool (cortex-core spec 1); this event is a lightweight pointer so surfaces can badge a lane without round-tripping for every Note.

```json
{
  "event": "lane.note",
  "event_id": "01J0K3M4P9...",
  "session_id": "sess_01J0K3M4...",
  "seq": 161,
  "at": "2026-05-02T18:33:22.611000Z",
  "lane_id": "lane_01J0K3M4...",
  "data": {
    "note_id": "note_01J0K3M4PX...",
    "severity": "info",
    "kind": "memory_recall",
    "summary": "3 prior decisions referenced this Workspace shape"
  }
}
```

`severity` enum: `info` | `warn` | `critical`. `critical` is the level that gates `end_turn` per cortex D-C4.

### 4.6 `lane.killed`

Convenience event emitted when a lane is killed externally (operator pressed `k`, parent cancelled, budget tripped). It is REDUNDANT with a `lane.status` carrying `status="cancelled"` — but surfaces use it as a one-shot signal for kill animations and audit trails. Always followed (in same `seq` window, monotonic) by a `lane.status` to the terminal state.

```json
{
  "event": "lane.killed",
  "event_id": "01J0K3M4PA...",
  "session_id": "sess_01J0K3M4...",
  "seq": 200,
  "at": "2026-05-02T18:33:25.000000Z",
  "lane_id": "lane_01J0K3M4...",
  "data": {
    "reason": "cancelled_by_operator",
    "actor": "operator",
    "actor_id": "user_01J0K..."
  }
}
```

## 5. Wire Envelope

### 5.1 JSON-RPC 2.0 (request/response — matches `desktop/IPC-CONTRACT.md` §1.1-1.3 verbatim)

Every control verb is a JSON-RPC request/response. The shape, error codes, and `data.stoke_code` mirror are unchanged from the existing contract. Lane verbs added (§7) reuse the same shape.

### 5.2 JSON-RPC 2.0 server-pushed events

The existing contract (§1.4) carries server-pushed events via an `event` field:

```json
{ "event": "session.delta", "session_id": "...", "payload": { ... } }
```

Lane events are a SUPERSET: same shape, lane-flavored event names. The Rust host (`src-tauri/src/ipc.rs`) treats `event` strings starting with `lane.` as a routed sub-stream and fans out via a per-session `tauri::ipc::Channel<LaneEvent>` (cortex D-S7).

When delivered as JSON-RPC `$/event` notifications over the WS transport (per `specs/research/synthesized/transport.md` §IPC), the envelope is:

```json
{
  "jsonrpc": "2.0",
  "method": "$/event",
  "params": {
    "sub": 7,
    "seq": 144,
    "event": "lane.delta",
    "session_id": "sess_01J...",
    "lane_id": "lane_01J...",
    "event_id": "01J...",
    "at": "2026-05-02T18:33:21.512000Z",
    "data": { "delta_seq": 7, "content_block": { "type": "text_delta", "text": "..." } }
  }
}
```

`sub` echoes the subscription id from the originating `lane.subscribe`/`session.subscribe` call. `seq` is per-session monotonic. The `event`/`session_id`/`lane_id`/`event_id`/`at`/`data` keys are flattened from §4 verbatim — surfaces that store events directly from the bus can ignore `params.sub` and consume the rest.

### 5.3 NDJSON streaming (stdout — extends existing `internal/streamjson/`)

`internal/streamjson/twolane.go` already routes events into critical (`hitl_required`, `complete`, `error`, `mission.aborted`) and observability lanes. Lane events extend this taxonomy:

| Event | Lane in `streamjson` | Rationale |
|---|---|---|
| `lane.created` | observability | Frequent, derivable from `lane.status` |
| `lane.status` | observability for non-terminal, **critical** for terminal-with-error | Errored-terminal must not drop |
| `lane.delta` | observability | High-volume; back-pressure must not stall the agent |
| `lane.cost` | observability | Coalesced 1 Hz |
| `lane.note` (severity `critical`) | **critical** | Gates `end_turn`; must never drop |
| `lane.note` (other severities) | observability | |
| `lane.killed` | **critical** | Audit-meaningful; never drop |

Add `isCriticalType` cases for `event=="lane.killed"`, `event=="lane.note" && data.severity=="critical"`, and `event=="lane.status" && data.status in {"errored"}`.

NDJSON line shape — one event per line, exactly `§4` body plus a top-level `session_id` (already implicit) and a `uuid` field (pre-existing convention from `streamjson/twolane.go`):

```ndjson
{"type":"lane.delta","uuid":"01J...","session_id":"sess_...","event_id":"01J...","seq":144,"lane_id":"lane_...","at":"2026-05-02T18:33:21.512Z","data":{"delta_seq":7,"content_block":{"type":"text_delta","text":"hello"}}}
```

Note: the existing emitter uses `"type"` (not `"event"`) as the envelope key. Lane emission MUST use `"type"` on the NDJSON wire and `"event"` on the JSON-RPC wire. The MCP server normalizes both to `event` in tool results. A small adapter in `internal/streamjson/lane.go` (new file) renames at emit time.

### 5.4 WebSocket subprotocol

Subprotocol token: `r1.lanes.v1`. Server (r1d) requires it on upgrade; absence → close code 4401 (`unauthorized`). Token-bearing variant: `r1.lanes.v1+token.<token>` per `specs/research/synthesized/transport.md` §IPC (CSWSH defense via subprotocol; see also `docs/decisions/index.md` D-S6).

Frames are JSON text frames carrying §5.2 `$/event` envelopes (server → client) or JSON-RPC requests (client → server). Binary frames are reserved (close 4400 if received).

Heartbeats: server emits `{"jsonrpc":"2.0","method":"$/ping","params":{"at":"..."}}` every 15 s; client SHOULD echo `$/pong`. Idle without ping for 30 s → server closes with code 4408.

### 5.5 Per-session monotonic `seq`

`seq` is allocated by the per-session goroutine that owns the WAL append (single writer, no contention). `seq=0` is reserved for the synthetic `session.bound` event emitted on every fresh subscription so clients have a known floor. `seq` overflow at `2^63` is treated as session-fatal (a session never approaches this; the check is defensive).

### 5.6 Versioning

The protocol version is `1`. The WS subprotocol token (`r1.lanes.v1`) and an `X-R1-Lanes-Version: 1` header (set on HTTP+SSE) carry it. Backwards-compatible changes (new event types, new optional fields) MAY ship without bumping. Breaking changes (renaming a field, changing an enum's meaning, removing an event) MUST bump to `r1.lanes.v2` and run both versions side-by-side for one minor release.

## 6. Replay Semantics

### 6.1 Last-Event-ID over HTTP+SSE

Conforms to the WHATWG HTML Living Standard "Server-sent events" section (https://html.spec.whatwg.org/multipage/server-sent-events.html); `Last-Event-ID` is the standard re-connect cursor, and each emitted line uses `id: <seq>`:

```
GET /v1/sessions/<id>/events HTTP/1.1
Last-Event-ID: 142
Authorization: Bearer <token>
Accept: text/event-stream
X-R1-Lanes-Version: 1
```

Server replays from `internal/bus/` WAL starting at `seq = 142 + 1`. Each emitted event carries `id: <seq>` per SSE convention. If the requested `seq` predates the WAL retention window, server replies `404 Not Found` with `data.stoke_code = "not_found"` and `data.detail = "wal_truncated"`.

### 6.2 `since_seq` over JSON-RPC / WS

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session.subscribe",
  "params": {"session_id": "sess_01J...", "since_seq": 142}
}
```

Identical semantics to §6.1. Server replies with a `result` carrying `{sub, snapshot_seq}` then immediately starts pushing `$/event` notifications from `since_seq + 1`. If `since_seq` is `0` or omitted, the server pushes a `session.bound` synthetic with `seq=0`, then a `lane.created` for every currently-active lane (a one-shot snapshot), then live events.

### 6.3 Gap detection

Clients MUST track `seq` per session. A jump from `seq=N` to `seq=N+k` where `k>1` is a gap. On gap:

1. Client logs the gap (`session_id`, `from`, `to`).
2. Client MAY issue a `r1.bus.tail({session_id, since_seq: N, until_seq: N+k-1})` MCP tool call (existing per `specs/research/synthesized/agentic.md`) to fill the gap.
3. If the server's WAL has truncated past `N`, the client MUST treat the lane state as authoritative-from-snapshot: re-subscribe with `since_seq=0`.

Per-lane `delta_seq` provides a finer gap signal for content streams; missing `delta_seq` triggers an in-lane re-fetch via `r1.lanes.get({session_id, lane_id})`.

### 6.4 WAL retention

Default retention: 24 hours OR 100 MB per session, whichever first. Configurable via `internal/bus/wal.go` already-existing knobs. A daemon restart replays the WAL on session resume (per `specs/research/synthesized/transport.md` §"Hot upgrade") so seq continuity holds across restarts within the retention window.

## 7. MCP Tool Schemas (verbatim, ready-to-paste into `internal/mcp/lanes_server.go`)

The five lane tools live in a new file `internal/mcp/lanes_server.go` registered alongside the existing `internal/mcp/stoke_server.go` and `internal/mcp/codebase_server.go`. Each tool result follows the `{ok, data, error_code, error_message, links}` shape per `specs/research/synthesized/agentic.md` §"Slack-style result envelope". JSON Schemas below are JSON Schema draft 2020-12, ready to splice into Go via `json.RawMessage`.

### 7.1 `r1.lanes.list`

Read-only. Lists all lanes (active + terminal within retention) for one session.

```json
{
  "name": "r1.lanes.list",
  "description": "List all lanes in a session, current state, parent links, and last seq emitted. Read-only.",
  "input_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "additionalProperties": false,
    "required": ["session_id"],
    "properties": {
      "session_id": {"type": "string", "description": "Session ULID."},
      "include_terminal": {"type": "boolean", "default": true, "description": "Include lanes in done/errored/cancelled."},
      "kinds": {
        "type": "array",
        "items": {"type": "string", "enum": ["main", "lobe", "tool", "mission_task", "router"]},
        "description": "Filter by lane kind."
      },
      "limit": {"type": "integer", "minimum": 1, "maximum": 500, "default": 100}
    }
  },
  "output_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ok"],
    "properties": {
      "ok": {"type": "boolean"},
      "data": {
        "type": "object",
        "properties": {
          "lanes": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["lane_id", "kind", "status", "started_at"],
              "properties": {
                "lane_id": {"type": "string"},
                "kind": {"type": "string", "enum": ["main", "lobe", "tool", "mission_task", "router"]},
                "label": {"type": "string"},
                "lobe_name": {"type": "string"},
                "parent_lane_id": {"type": "string"},
                "status": {"type": "string", "enum": ["pending", "running", "blocked", "done", "errored", "cancelled"]},
                "pinned": {"type": "boolean"},
                "started_at": {"type": "string", "format": "date-time"},
                "ended_at": {"type": "string", "format": "date-time"},
                "last_seq": {"type": "integer", "minimum": 0},
                "tokens_in": {"type": "integer"},
                "tokens_out": {"type": "integer"},
                "usd": {"type": "number"}
              }
            }
          }
        }
      },
      "error_code": {"type": "string"},
      "error_message": {"type": "string"}
    }
  }
}
```

### 7.2 `r1.lanes.subscribe`

Streaming. Returns a stream of §4 events. Carries an SSE-style cursor.

```json
{
  "name": "r1.lanes.subscribe",
  "description": "Subscribe to live lane events for one session. Streaming. Replays from since_seq if provided; else starts from a snapshot of currently-active lanes.",
  "input_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "additionalProperties": false,
    "required": ["session_id"],
    "properties": {
      "session_id": {"type": "string"},
      "since_seq": {"type": "integer", "minimum": 0, "description": "Replay from seq+1; 0 emits a snapshot then live."},
      "lane_ids": {"type": "array", "items": {"type": "string"}, "description": "Filter to these lanes only."},
      "kinds": {"type": "array", "items": {"type": "string", "enum": ["main", "lobe", "tool", "mission_task", "router"]}},
      "events": {"type": "array", "items": {"type": "string", "enum": ["lane.created", "lane.status", "lane.delta", "lane.cost", "lane.note", "lane.killed"]}, "description": "Subset of event types."}
    }
  },
  "streaming": true,
  "output_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "description": "One §4 lane event per stream chunk; final chunk is {ok, data:{ended:true,reason}}."
  }
}
```

### 7.3 `r1.lanes.get`

Read-only. Snapshot of one lane plus optional tail of recent deltas.

```json
{
  "name": "r1.lanes.get",
  "description": "Fetch a single lane's current state and an optional bounded tail of its recent events. Read-only.",
  "input_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "additionalProperties": false,
    "required": ["session_id", "lane_id"],
    "properties": {
      "session_id": {"type": "string"},
      "lane_id": {"type": "string"},
      "tail": {"type": "integer", "minimum": 0, "maximum": 500, "default": 0, "description": "Number of trailing events to include."}
    }
  },
  "output_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ok"],
    "properties": {
      "ok": {"type": "boolean"},
      "data": {
        "type": "object",
        "properties": {
          "lane": {"$ref": "#/$defs/Lane"},
          "tail": {"type": "array", "items": {"type": "object", "description": "§4 event"}}
        }
      },
      "error_code": {"type": "string"},
      "error_message": {"type": "string"}
    }
  }
}
```

### 7.4 `r1.lanes.kill`

Mutation. Cancels a lane; cascades to descendants. Idempotent — re-killing a terminal lane returns `ok:true, data.already_terminal:true`.

```json
{
  "name": "r1.lanes.kill",
  "description": "Cancel a running or pending lane. Cascades to descendant lanes. Idempotent.",
  "input_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "additionalProperties": false,
    "required": ["session_id", "lane_id"],
    "properties": {
      "session_id": {"type": "string"},
      "lane_id": {"type": "string"},
      "reason": {"type": "string", "description": "Free-text reason; surfaced in lane.killed.data.reason.", "maxLength": 256},
      "cascade": {"type": "boolean", "default": true, "description": "Also kill all descendants. Set false to kill only this lane."}
    }
  },
  "output_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ok"],
    "properties": {
      "ok": {"type": "boolean"},
      "data": {
        "type": "object",
        "properties": {
          "killed_lane_ids": {"type": "array", "items": {"type": "string"}},
          "already_terminal": {"type": "boolean"}
        }
      },
      "error_code": {"type": "string", "enum": ["not_found", "permission_denied", "internal"]},
      "error_message": {"type": "string"}
    }
  }
}
```

### 7.5 `r1.lanes.pin`

Mutation. Sets/clears the `pinned` flag. Idempotent.

```json
{
  "name": "r1.lanes.pin",
  "description": "Set or clear the pinned flag on a lane. Pinned lanes render above unpinned ones across all surfaces. Idempotent.",
  "input_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "additionalProperties": false,
    "required": ["session_id", "lane_id", "pinned"],
    "properties": {
      "session_id": {"type": "string"},
      "lane_id": {"type": "string"},
      "pinned": {"type": "boolean"}
    }
  },
  "output_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["ok"],
    "properties": {
      "ok": {"type": "boolean"},
      "data": {"type": "object", "properties": {"lane_id": {"type": "string"}, "pinned": {"type": "boolean"}}},
      "error_code": {"type": "string", "enum": ["not_found", "internal"]},
      "error_message": {"type": "string"}
    }
  }
}
```

### 7.6 Tool count

**5 tools.** Surface inventory: list, subscribe, get, kill, pin. The cortex-side `r1.cortex.notes` and `r1.cortex.publish` (already scoped in `specs/research/synthesized/agentic.md` and owned by `specs/cortex-core.md` spec 1) live in `internal/mcp/cortex_server.go` (built by spec 1) and are NOT in this file.

## 8. Bridge Points

### 8.1 How cortex-core publishes lane events

Cortex-core (spec 1) owns the lane lifecycle. It MUST:

1. Add a `Lane` value type in `internal/cortex/lane.go` (struct `{ID, Kind, ParentID, Label, Status, Pinned, StartedAt, EndedAt, LastSeq}`).
2. Add `Workspace.NewLane(ctx, kind, parentID, label) *Lane` — emits `lane.created` synchronously to `hub.Bus` under a new event family `EventLaneCreated`. The Bus subscriber that wires through to surfaces (next bullet) writes to `internal/bus/` WAL with the next per-session `seq`.
3. Add `Lane.Transition(newStatus, reasonCode, reason) error` — validates the transition (§3.3) and emits `lane.status`. Reject illegal transitions with `*stokerr.Error{Code: "internal"}`.
4. Add `Lane.EmitDelta(block ContentBlock)`, `.EmitCost(in,out,usd)`, `.EmitNote(noteID, severity)`, `.Kill(reason)` — each emits the matching §4 event.
5. The main agent thread is born via `Workspace.NewMainLane(ctx)` at `agentloop.Loop` start. Each `Lobe` is born via `Workspace.NewLobeLane(ctx, lobeName, parent)` at Lobe-spawn. Tool calls are born via `Workspace.NewToolLane(ctx, parent, toolName)` only when promoted (long-running tools, > 2 s wall clock).

### 8.2 How surfaces subscribe

Three subscriber kinds, all reading from one source of truth (`internal/bus/` WAL):

- **NDJSON (stdout)** — `internal/streamjson/lane.go` (new file) registers as a hub subscriber for `EventLane*`, formats per §5.3, routes through `TwoLane.EmitTopLevel` with the §5.3 critical-vs-observability rules.
- **HTTP+SSE & WS (browser/desktop)** — `internal/server/server.go` gains a new handler `handleLaneEvents` that wraps the existing `handleEvents`: same SSE plumbing, but filters by `session_id` query param and adds `Last-Event-ID` replay from `internal/bus/` WAL. WS upgrade path lives in `internal/server/ws.go` (new file) — checks `Sec-WebSocket-Protocol: r1.lanes.v1`, validates token, then loops on the same hub subscription.
- **MCP (`internal/mcp/lanes_server.go`)** — registers the five tools §7. `r1.lanes.subscribe` is implemented as a server-streamed tool call (MCP `2025-11-25` streaming response shape). Subscriber pulls from the same hub channel.

All three subscribers consume the same `seq` and the same WAL; gap detection is consistent across surfaces.

### 8.3 Hub event types added (cortex-core spec 1 owns the diff)

```go
// internal/hub/events.go  --- new section ---
const (
    EventLaneCreated EventType = "lane.created"
    EventLaneStatus  EventType = "lane.status"
    EventLaneDelta   EventType = "lane.delta"
    EventLaneCost    EventType = "lane.cost"
    EventLaneNote    EventType = "lane.note"
    EventLaneKilled  EventType = "lane.killed"
)
```

Plus a new `LaneEvent` payload type alongside `ToolEvent`/`FileEvent`/etc., carrying `LaneID, Kind, ParentLaneID, Status, ReasonCode, DeltaSeq, ContentBlock, NoteID, Severity, KillReason`. Cortex-core is responsible for adding this struct; lanes-protocol just freezes the contract.

## 9. Backward-Compat with `desktop/IPC-CONTRACT.md`

The IPC contract is augmented, not broken. Verbatim guarantees:

1. **No existing JSON-RPC method is renamed, removed, or changed in shape.** All 11 methods in §2 of the IPC contract continue to work as documented.
2. **No existing server-pushed event is renamed.** The five events `session.started`, `session.delta`, `session.ended`, `ledger.appended`, `cost.tick`, `descent.tier_changed` remain. Lane events are ADDITIVE; they share the `event` field convention of §1.4.
3. **`session.delta` continues to carry assistant-text deltas for the main lane.** Lane events provide a more granular alternative; clients are free to keep consuming `session.delta` and ignore `lane.delta` entirely (the server emits both for the main lane during the deprecation window).
4. **Tauri-only commands (§5: `session.send`, `session.cancel`, `skill.list`, `skill.get`) are unaffected.** The R1D phase is unchanged.
5. **Error codes are reused.** Lane verbs use the existing `-32001..-32099` taxonomy from §3.2 and the `data.stoke_code` mirror.
6. **Wire envelope is identical.** Same `jsonrpc`, `id`, `method`, `params`/`result`/`error` shape. `$/event` notifications fit in the existing `event`-field server-pushed slot.
7. **Version handshake preserved.** The existing implicit `X-R1-RPC-Version: 1` continues. Lanes adds an orthogonal `X-R1-Lanes-Version: 1` header — independent bump cadence.

A migration window of one minor release runs `session.delta` and `lane.delta` (for the main lane) in parallel. After that, surfaces SHOULD prefer `lane.delta`; `session.delta` remains supported for clients that haven't migrated. Removing `session.delta` is OUT OF SCOPE for this spec.

Risk register:

- **Risk:** A surface that consumed `session.delta` and naively iterates over the new `lane.delta` for the main lane may double-render text. **Mitigation:** Document the dual-emission window prominently in `desktop/IPC-CONTRACT.md` §4 update (this spec edits, doesn't replace, that file). Surfaces should pick one stream per main lane.
- **Risk:** Existing clients that ignore unknown event names handle additions cleanly. Verify in test (§10).
- **Risk:** WAL retention misconfig truncates events the desktop expected to replay across a restart. **Mitigation:** Default 24 h is well above any realistic desktop session.
- **Risk:** ULID monotonicity within the same millisecond requires `ulid.MonotonicEntropy`; using plain `ulid.New(time.Now(), entropy)` can produce out-of-order IDs under bursts. **Mitigation:** §8.1 implementation MUST use `ulid.MonotonicEntropy(rand.Reader, 0)` and serialize ID generation per session.
- **Risk:** Dual emission of `session.delta` + `lane.delta` could double the byte budget for large main-lane outputs. **Mitigation:** Compat window is one minor release; measure in §10.5 backward-compat test.

## 9.5 Out of Scope

Explicitly NOT delivered by this spec:

1. **Removal of `session.delta`** — kept for one minor release post-launch. Removal is a follow-up spec.
2. **Cortex `Lane` value type, `Workspace.NewLane*`, `Lane.Transition`, `Lane.Emit*`, `Lane.Kill`** — owned by `specs/cortex-core.md` (spec 1). This spec only freezes the wire contract those functions must produce.
3. **TUI lane rendering** (Bubble Tea v2 widgets, status icons, lane sidebar) — owned by spec 4 (`tui-lanes`, see `docs/decisions/index.md` D-2026-05-02-01).
4. **Web UI lane rendering** (Cursor "Glass" sidebar, AI SDK 6 `useChat` integration) — owned by spec 6 (`web-chat-ui`).
5. **R1D server endpoints implementation** — endpoint shapes are frozen here; the daemon process, listener wiring, and per-user-singleton lifecycle are owned by `specs/r1d-server.md` / spec 5.
6. **Tauri IPC plumbing** (`tauri::ipc::Channel<LaneEvent>`, `app.emit_to`) — owned by spec 7 (`desktop-cortex-augmentation.md`).
7. **`r1.cortex.notes` and `r1.cortex.publish` MCP tools** — owned by spec 1; this spec only references them.
8. **Multi-session-per-daemon scheduler / `cmd.Dir` audit (D-D2 / D-D4)** — owned by `specs/r1d-server.md`.
9. **Authentication + authorization** beyond the WS subprotocol token check (RBAC, per-tool permission scoping) — covered by existing `internal/rbac/`; lanes inherit, does not extend.
10. **Tracing / OpenTelemetry export of lane events** — future work; the WAL is the source of truth and tracing can be layered atop.

## 10. Test Plan

### 10.1 Golden NDJSON fixtures

Files in `internal/streamjson/testdata/lanes/`:

- `lane_created_lobe.golden.ndjson` — one `lane.created` for a `MemoryRecallLobe`.
- `lane_full_lifecycle.golden.ndjson` — `lane.created → lane.status(running) → lane.delta×3 → lane.cost → lane.status(done)`.
- `lane_blocked_resume.golden.ndjson` — `running → blocked(awaiting_user) → running → done`.
- `lane_killed_cascade.golden.ndjson` — parent `lane.killed` plus three child `lane.status(cancelled_by_parent)` events.
- `lane_critical_note.golden.ndjson` — `lane.note` with `severity:"critical"` routed via critical lane (verifies §5.3 routing).
- `lane_dual_emit_main.golden.ndjson` — main lane emits BOTH `session.delta` and `lane.delta` for the same content (compat window).

Test harness: `internal/streamjson/lane_test.go` reads the goldens, replays them through a `bytes.Buffer`, and asserts byte-for-byte equality after canonicalization (sorted keys, strip `at`/`uuid`/`event_id` for determinism).

### 10.2 Round-trip JSON-RPC test

`internal/server/lane_rpc_test.go`:

- Stand up an in-memory `Server` with a fake `internal/cortex/Workspace`.
- Call `session.subscribe` over the JSON-RPC layer, capture `$/event` notifications.
- Drive the cortex through a synthetic Lobe lifecycle (using a `harness/models` mock).
- Assert: every emitted `EventLane*` shows up in the JSON-RPC stream; `seq` is monotonic and gap-free; the §5.2 envelope is byte-for-byte canonical; the runtime validator (`internal/schemaval/`, custom field-list `Schema`) accepts every event against per-event-type `schemaval.Schema` definitions hand-translated from §4 (one `schemaval.Schema` constant per event type, defined in `internal/hub/lane_schemas.go`).

### 10.3 WS replay correctness test

`internal/server/ws_replay_test.go`:

- Run a session, generate 200 lane events.
- Disconnect at `seq=120`.
- Reconnect with `Sec-WebSocket-Protocol: r1.lanes.v1`, `since_seq: 120`.
- Assert: receives events `121..200` in order; no duplicates; no gaps; `event_id`s match the WAL; total bytes-on-wire matches a control run that never disconnected.
- Negative case: `since_seq=5` after WAL trim → server returns close code 4404 with `data.detail="wal_truncated"`.
- Negative case: missing subprotocol → close code 4401 (`unauthorized`).

### 10.4 MCP tool-call round-trip

`internal/mcp/lanes_server_test.go`:

- For each of the five tools, build a `json.RawMessage` input that the §7 schema admits.
- Invoke through the existing `Client` interface against the in-process MCP server.
- Assert: result envelope matches `{ok, data, error_code?, error_message?, links?}`; for `r1.lanes.subscribe`, validate streaming chunks against the §4 schemas; for `r1.lanes.kill`, second invocation returns `{ok:true, data:{already_terminal:true}}` (idempotency).
- Validate every request/response at runtime with `internal/schemaval/` against hand-translated `schemaval.Schema` constants in `internal/mcp/lanes_schemas.go` (one per tool input + one per tool output). The §7 JSON Schema draft 2020-12 documents are the SPEC contract (paste-ready into the MCP `ToolDefinition.input_schema`/`output_schema` raw-message slots, where the MCP client does draft 2020-12 validation); the `schemaval.Schema` constants are the SERVER-SIDE runtime gate. A unit test asserts the two stay in sync (every required field in §7 is also `Required: true` in the `schemaval.Schema`; every enum matches).

### 10.5 Backward-compat test

`internal/server/compat_test.go`:

- Replay a captured trace from a pre-lanes desktop client (golden file) against the new server.
- Assert: every event the trace expected is still emitted; no unknown-event panics; `session.delta` still arrives for main-lane content.

### 10.6 Manual verification checklist

- TUI shows lanes with correct status icons (per cortex D-S1 vocabulary).
- Web UI sidebar (Cursor Glass per cortex D-S4) renders pinned lanes above unpinned.
- `r1 ctl lanes list <session>` (CLI helper that wraps `r1.lanes.list` over MCP) prints a table.
- A killed lane animates out and produces both `lane.killed` and `lane.status(cancelled_by_operator)`.
- Daemon restart mid-session: surfaces reconnect with `Last-Event-ID` and resume without rendering glitches.

## 11. Implementation Checklist

1. [ ] Add `EventLaneCreated`, `EventLaneStatus`, `EventLaneDelta`, `EventLaneCost`, `EventLaneNote`, `EventLaneKilled` to `internal/hub/events.go` (new section after the `--- Custom (1 event) ---` block). Bumps event-family count.
2. [ ] Add `LaneEvent` payload struct (`internal/hub/events.go`) with all §4 fields. Add `Lane *LaneEvent` pointer field on `Event` (struct defined at `internal/hub/events.go:148`, currently has 11 payload pointers: `Tool`, `File`, `Model`, `Git`, `Cost`, `Prompt`, `Skill`, `Lifecycle`, `Test`, `Security`, plus `Custom map[string]any`). Insert the new pointer after `Security` and before `Custom`, JSON tag `"lane,omitempty"`.
3. [ ] Define `LaneStatus` enum string type with constants (`pending|running|blocked|done|errored|cancelled`) in `internal/hub/lane_status.go`.
4. [ ] Define `LaneKind` enum string type with constants (`main|lobe|tool|mission_task|router`) in `internal/hub/lane_kind.go`.
5. [ ] Cortex-core (spec 1) owns adding `internal/cortex/lane.go` with `Lane`, `Workspace.NewLane*`, `Lane.Transition`, `Lane.Emit*`, `Lane.Kill` — this spec freezes the wire contract those functions must produce.
6. [ ] Validate all transitions in `Lane.Transition` per §3.3 transition table. Reject illegal transitions with `*stokerr.Error{Code: "internal"}`.
7. [ ] Allocate per-session `seq` via a single-writer goroutine in cortex-core's `Workspace`. Reserve `seq=0` for `session.bound`.
8. [ ] Generate `lane_id` and `event_id` as ULIDs via `oklog/ulid/v2` (already in go.mod or add).
9. [ ] Create `internal/streamjson/lane.go` registering a hub subscriber for `EventLane*` and routing through `TwoLane.EmitTopLevel` with the §5.3 critical-vs-observability rules.
10. [ ] Extend `streamjson.isCriticalType` to mark `lane.killed`, `lane.note(severity=critical)`, and `lane.status(status=errored)` as critical.
11. [ ] Create `internal/streamjson/testdata/lanes/` and the six golden fixtures listed in §10.1.
12. [ ] Write `internal/streamjson/lane_test.go` with golden-replay assertions.
13. [ ] Extend `internal/server/server.go` with `handleLaneEvents` (HTTP+SSE) supporting `?session_id=` query, `Last-Event-ID` header, and `X-R1-Lanes-Version` header.
14. [ ] Create `internal/server/ws.go` implementing the WS upgrade with `Sec-WebSocket-Protocol: r1.lanes.v1` requirement, token validation, Origin pinning, and 15-s ping/30-s idle-close.
15. [ ] Implement `session.subscribe` JSON-RPC method (server-side) emitting `$/event` notifications per §5.2.
16. [ ] Implement WAL replay via existing `internal/bus/wal.go` — start at `since_seq+1`, truncate-error on out-of-window.
17. [ ] Implement gap detection: emit `session.bound` synthetic at `seq=0`; surfaces compare on receive.
18. [ ] Create `internal/mcp/lanes_server.go` with the five tools §7. Reuse the existing `ToolDefinition` struct. Embed the §7 JSON Schema draft 2020-12 documents verbatim as `json.RawMessage` in `ToolDefinition.input_schema`/`output_schema`.
18a. [ ] Create `internal/mcp/lanes_schemas.go` with one `schemaval.Schema` constant per tool input + per tool output (10 schemas total), hand-translated from §7. The MCP server validates incoming requests with these BEFORE dispatching. Add `internal/mcp/lanes_schemas_test.go` asserting parity: every required field in §7 is `Required: true` here; every enum matches.
18b. [ ] Create `internal/hub/lane_schemas.go` with one `schemaval.Schema` constant per §4 event type (6 schemas). Used by §10.2 round-trip test to validate emitted events. Add `internal/hub/lane_schemas_test.go` asserting parity with §4 examples.
19. [ ] Implement `r1.lanes.list` against the cortex-core `Workspace.Lanes()` accessor.
20. [ ] Implement `r1.lanes.subscribe` as a streaming tool call. Use the same hub subscription that `streamjson` and `server` use; one connection = one subscription.
21. [ ] Implement `r1.lanes.get` returning current snapshot + optional tail (read from WAL).
22. [ ] Implement `r1.lanes.kill` with cascade semantics. Idempotent — terminal lane returns `data.already_terminal:true`. Emits `lane.killed` + final `lane.status(cancelled_by_operator)`.
23. [ ] Implement `r1.lanes.pin` toggling the `Lane.Pinned` flag. Emit no event (clients re-fetch via `lanes.list`); surfaces poll cheaply.
24. [ ] Wire the five new tools into the MCP server registry next to `stoke_server.go`.
25. [ ] Write `internal/server/lane_rpc_test.go` (round-trip JSON-RPC test §10.2).
26. [ ] Write `internal/server/ws_replay_test.go` (WS replay test §10.3).
27. [ ] Write `internal/mcp/lanes_server_test.go` (MCP round-trip §10.4).
28. [ ] Write `internal/server/compat_test.go` (backward-compat replay §10.5).
29. [ ] Edit `desktop/IPC-CONTRACT.md` §4 to add the six lane events to the server-pushed event table. Mark `session.delta` as "co-emitted with `lane.delta` for main lane (compat window)". Do NOT touch §1, §2, §3, §5, §6, §7.
30. [ ] Edit `desktop/IPC-CONTRACT.md` §1.5 to document the `X-R1-Lanes-Version: 1` orthogonal header.
31. [ ] No-op: `github.com/oklog/ulid/v2` v2.1.1 is already in `go.mod` (line 22). If a future bump is needed, run `go get github.com/oklog/ulid/v2@latest && go mod tidy`.
32. [ ] Add a CI lint at `scripts/lint-lane-events.sh` (matches existing `scripts/pre-push.sh` convention; no `tools/` directory exists in this repo) that greps for direct `streamjson` writes of `lane.*` events bypassing the `internal/streamjson/lane.go` adapter — fails the build on bypass. Wire it into the `go vet ./...` gate per `CLAUDE.md`.
33. [ ] Document the wire format in `docs/AGENTIC-API.md` (created by spec 8 / agentic-test-harness; this spec adds a `## Lanes` section).
34. [ ] Update `docs/decisions/index.md` with this spec's identifier under the 2026-05-02 block once shipped.
35. [ ] Run `go build ./cmd/r1 && go test ./... && go vet ./...` (the CI gate per `CLAUDE.md`).
36. [ ] Verify the goldens in §10.1 pass on Linux + macOS (CI matrix already covers both).
37. [ ] Verify the WS subprotocol negotiation works under Chrome, Firefox, and the Tauri webview.
38. [ ] Manual smoke: spawn a session with two Lobes, watch lanes appear in TUI, kill one with `k`, verify `lane.killed` arrives in NDJSON within 100 ms.
39. [ ] Performance budget: at 100 events/sec sustained for 60 s, MCP `r1.lanes.subscribe` consumer end-to-end p99 latency ≤ 150 ms; CPU overhead of the lane bridge ≤ 3% of one core.
40. [ ] Sign off: cortex-core (spec 1) and desktop tier 2 (R1D) authors review the wire freeze before lock-in.
