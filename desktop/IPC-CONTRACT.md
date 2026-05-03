# R1 Desktop — IPC Contract (JSON-RPC 2.0)

> **Filed:** 2026-04-23. **Status:** SCOPED (R1D-1.4 interface freeze).
> This document is the source of truth for the wire shape spoken between
> **Tier 2 (Rust host)** and **Tier 3 (r1 Go subprocess)**, and the
> method signatures exposed from the Rust host to **Tier 1 (WebView)**
> via Tauri `invoke`. See `docs/architecture.md` §1 for the tier model.
>
> This file is the contract. `src-tauri/src/ipc.rs` has Rust stubs that
> match it. `internal/desktopapi/desktopapi.go` (Go) has a typed
> `Handler` interface with one method per verb and a sentinel
> `ErrNotImplemented` error so CLI and subprocess implementations can
> stub-answer any call while the real bodies land in later phases.
>
> Wire format: **JSON-RPC 2.0** over the subprocess's stdin/stdout,
> NDJSON-framed (one JSON object per line). The Tauri `invoke` layer
> round-trips the same `method` + `params` shape into a Rust command of
> the same name; the Rust host rewrites the envelope to JSON-RPC 2.0
> before writing to the subprocess's stdin.

---

## 1. Envelope

### 1.1 Request

```json
{
  "jsonrpc": "2.0",
  "id": "<string | number, caller-chosen>",
  "method": "<namespace.verb>",
  "params": { ... }
}
```

### 1.2 Response (success)

```json
{
  "jsonrpc": "2.0",
  "id": "<echo>",
  "result": { ... }
}
```

### 1.3 Response (error)

```json
{
  "jsonrpc": "2.0",
  "id": "<echo>",
  "error": {
    "code": <integer>,
    "message": "<human-readable>",
    "data": { "stoke_code": "<taxonomy_code>", ... }
  }
}
```

### 1.4 Server-pushed events

The Rust host also receives unsolicited NDJSON events from the r1
subprocess (chat deltas, tool-use blocks, ledger writes, cost ticks).
These are **not** JSON-RPC responses; they carry an `event` field
instead of `method`/`result`/`error`:

```json
{ "event": "session.delta", "session_id": "...", "payload": { ... } }
```

Events fan out to the WebView via Tauri's typed event bus. Shapes are
defined in §4.

### 1.5 Versioning

Every request carries an implicit `X-R1-RPC-Version: 1` header
(transport-level: the subprocess's handshake announces it on startup,
the Rust host asserts it on every open). The version bumps when a
method's params or result shape changes incompatibly. New methods are
additive and do not bump the version.

**Lanes overlay header**: clients that consume the lane events added in
specs/lanes-protocol.md (BUILD_ORDER 3) ALSO assert an orthogonal
`X-R1-Lanes-Version: 1` header. The two version headers bump on
independent cadences — bumping the RPC version does NOT bump the lanes
version, and vice versa. A client that does not consume lane events can
ignore the header entirely; clients that do MUST refuse to subscribe
when the server's announced version is incompatible with their pinned
version.

---

## 2. Method table

Eleven methods across five categories (3 Session + 2 Ledger + 2 Memory + 2 Cost + 2 Descent — matches §2.6 Total). Each row lists:

- **Method** — the JSON-RPC `method` string (also the Tauri command
  name and the Go `Handler` method name after conversion).
- **Params** — inline JSON Schema sketch. `?` = optional.
- **Result** — inline JSON Schema sketch.
- **Errors** — taxonomy codes emitted beyond the shared set (§3.2).

### 2.1 Session control (3)

| Method | Params | Result | Errors |
|---|---|---|---|
| `session.start` | `{ "prompt": string, "skill_pack"?: string, "provider"?: string, "budget_usd"?: number }` | `{ "session_id": string, "started_at": iso8601 }` | `budget_exceeded`, `validation` |
| `session.pause` | `{ "session_id": string }` | `{ "paused_at": iso8601 }` | `not_found`, `conflict` |
| `session.resume` | `{ "session_id": string }` | `{ "resumed_at": iso8601 }` | `not_found`, `conflict` |

Cancel + send are covered by R1D-1.4 and live in the Tauri-only layer
(see §5); they do not round-trip to the Go `Handler`.

### 2.2 Ledger query (2)

| Method | Params | Result | Errors |
|---|---|---|---|
| `ledger.get_node` | `{ "hash": string }` | `{ "hash": string, "type": string, "payload": object, "edges": [{ "to": string, "kind": string }] }` | `not_found` |
| `ledger.list_events` | `{ "session_id"?: string, "since"?: iso8601, "limit"?: integer (default 100, max 1000) }` | `{ "events": [{ "hash": string, "type": string, "at": iso8601 }], "next_cursor"?: string }` | `validation` |

### 2.3 Memory inspection (2)

| Method | Params | Result | Errors |
|---|---|---|---|
| `memory.list_scopes` | `{}` | `{ "scopes": ["Session", "Worker", "AllSessions", "Global", "Always"] }` | — |
| `memory.query` | `{ "scope": "Session"\|"Worker"\|"AllSessions"\|"Global"\|"Always", "key_prefix"?: string, "limit"?: integer (default 100) }` | `{ "entries": [{ "key": string, "value": string, "updated_at": iso8601 }], "truncated": boolean }` | `validation`, `permission_denied` |

### 2.4 Cost (2)

| Method | Params | Result | Errors |
|---|---|---|---|
| `cost.get_current` | `{ "session_id"?: string }` | `{ "usd": number, "tokens_in": integer, "tokens_out": integer, "as_of": iso8601 }` | `not_found` |
| `cost.get_history` | `{ "session_id"?: string, "since"?: iso8601, "bucket"?: "minute"\|"hour"\|"day" (default "hour") }` | `{ "buckets": [{ "at": iso8601, "usd": number, "tokens": integer }] }` | `validation` |

### 2.5 Descent state (2)

| Method | Params | Result | Errors |
|---|---|---|---|
| `descent.current_tier` | `{ "session_id": string, "ac_id"?: string }` | `{ "ac_id": string, "tier": "T1"\|"T2"\|..."T8", "status": "pending"\|"running"\|"passed"\|"failed", "evidence_ref"?: string }[]` | `not_found` |
| `descent.tier_history` | `{ "session_id": string, "ac_id": string }` | `{ "ac_id": string, "attempts": [{ "tier": string, "status": string, "at": iso8601, "evidence_ref"?: string, "failure_class"?: string }] }` | `not_found` |

### 2.6 Summary

| Category | Count |
|---|---|
| Session control | 3 |
| Ledger query | 2 |
| Memory inspection | 2 |
| Cost | 2 |
| Descent state | 2 |
| **Total** | **11** |

Tauri-only commands (§5) add 4 more verbs that do not round-trip to the
Go subprocess: `session.send`, `session.cancel`, `skill.list`, `skill.get`.
Grand total across the `invoke_handler` surface: **15**.

---

## 3. Error codes

### 3.1 JSON-RPC standard codes

| Code | Meaning |
|---|---|
| -32700 | Parse error (malformed JSON) |
| -32600 | Invalid request (envelope wrong shape) |
| -32601 | Method not found |
| -32602 | Invalid params |
| -32603 | Internal error (unexpected server-side failure) |

### 3.2 R1 taxonomy (mapped from `internal/stokerr`)

Custom application codes live in the reserved `-32000..-32099` range and
always carry a `data.stoke_code` mirror so clients can pattern-match on
the taxonomy string rather than the numeric id:

| JSON-RPC code | `data.stoke_code` | Meaning |
|---|---|---|
| -32001 | `validation` | Input failed structural or semantic validation. |
| -32002 | `not_found` | Well-formed lookup, unsatisfiable. |
| -32003 | `conflict` | Concurrent-mutation collision / stale precondition. |
| -32004 | `append_only_violation` | Caller tried to mutate append-only state. |
| -32005 | `permission_denied` | RBAC or sandbox policy blocked the call. |
| -32006 | `budget_exceeded` | Mission/task hit its cost or token budget. |
| -32007 | `timeout` | Deadline tripped; retry semantics caller-decided. |
| -32008 | `crash_recovery` | State restored from checkpoint; caller may see replays. |
| -32009 | `schema_version` | On-disk artifact version mismatch; needs migration. |
| -32010 | `not_implemented` | Handler acknowledged the method but body is a stub. |
| -32099 | `internal` | Catch-all for unexpected invariants — bug. |

`not_implemented` is the sentinel emitted by the Go-side stub (§6).
It maps to `desktopapi.ErrNotImplemented`. Hitting it is a finding, not
a bug: the method signature is live; the body lands in a later phase.

---

## 4. Server-pushed events

Events follow the shape in §1.4. At scaffold time, five event kinds are
defined; more land with R1D-2+ as the session view demands them.

| `event` | Fields | Emitted when |
|---|---|---|
| `session.started` | `session_id`, `at` | New r1 subprocess live and handshake complete |
| `session.delta` | `session_id`, `payload` (assistant text / tool-use block) | Each NDJSON delta from the subprocess. **Co-emitted with `lane.delta` for the main lane during the lanes-protocol compat window** (see specs/lanes-protocol.md §"Out of scope" item 1). Removal is a follow-up minor release. |
| `session.ended` | `session_id`, `reason` ("ok"\|"cancelled"\|"error"), `at` | Subprocess exits or is SIGTERM'd |
| `ledger.appended` | `session_id`, `hash`, `type` | Ledger node committed |
| `cost.tick` | `session_id`, `usd_delta`, `tokens_delta` | Cost tracker rolls forward |
| `descent.tier_changed` | `session_id`, `ac_id`, `from`, `to`, `status` | A verification tier changes state |
| `lane.created` | `session_id`, `lane_id`, `kind` (`main`\|`lobe`\|`tool`\|`mission_task`\|`router`), `parent_id?`, `label?`, `started_at`, `seq` | Cortex Workspace creates a new lane (NewMainLane / NewLobeLane / NewToolLane). Lanes are the cross-surface representation of Cortex activity; see specs/lanes-protocol.md §3. |
| `lane.status` | `session_id`, `lane_id`, `status` (`pending`\|`running`\|`blocked`\|`done`\|`errored`\|`cancelled`), `reason?`, `reason_code?`, `seq` | Lane FSM transitions. Critical when `status="errored"` (top-level emit per §5.3). |
| `lane.delta` | `session_id`, `lane_id`, `block` (text/tool-use ContentBlock), `seq` | Streaming content within a lane. For the `main` lane, also co-emitted as `session.delta` during compat window. |
| `lane.cost` | `session_id`, `lane_id`, `tokens_in`, `tokens_out`, `usd`, `seq` | Per-lane cost tick (independent of the global `cost.tick`). |
| `lane.note` | `session_id`, `lane_id`, `note_id`, `note_severity` (`info`\|`advice`\|`warning`\|`critical`), `seq` | Lobe published a Note that this lane caused. Critical when `note_severity="critical"`. |
| `lane.killed` | `session_id`, `lane_id`, `reason`, `ended_at`, `seq` | Lane terminated (operator kill, error cascade, or completion). Always critical (top-level emit). |

Tier 2 Rust host subscribes, parses, fans out to the WebView via
`app.emit_to(<session_window>, event, payload)`.

---

## 5. Tauri-only commands

Four verbs live on the `invoke_handler` surface but do **not** round-trip
to the Go subprocess. They execute inside the Rust host or are
implemented via direct stdin writes to an existing r1 process.

| Method | Rationale |
|---|---|
| `session.send` | Write a prompt to the subprocess stdin; no JSON-RPC round-trip needed. |
| `session.cancel` | SIGTERM → grace period → SIGKILL. Pure process control. |
| `skill.list` | Cached in Rust host from first call; avoids subprocess round-trip on every UI refresh. |
| `skill.get` | Same cache as `skill.list`, keyed by name. |

They are still surfaced to the WebView for UI-consistency, and they
still appear in `src-tauri/src/ipc.rs`; they just bypass the Go
`Handler` interface.

---

## 6. Go-side stub contract

The Go side exposes `internal/desktopapi.Handler` (one method per
JSON-RPC verb in §2). The scaffold implementation, `NotImplemented{}`,
returns `ErrNotImplemented` (a sentinel `*stokerr.Error` with code
`not_implemented`) from every method.

Any binary that hosts the Handler — `r1 serve` in the long-lived mode
or `r1 --one-shot` per-session mode — can embed `NotImplemented{}`
today and ship a real implementation method-by-method without
breaking the wire.

See `internal/desktopapi/desktopapi.go` for the Go side and
`src-tauri/src/ipc.rs` for the Rust side.

---

## 7. Cross-references

- Architecture: `docs/architecture.md` §1 (tiers), §4 (RPC surface).
- Phase plan: `PLAN.md` R1D-1.4 (MVP invoke) → R1D-10.5 (multi-session).
- Upstream CLI IPC: `cmd/stoke/ctl_cmd.go` (wire-match target).
- Error taxonomy: `internal/stokerr/errors.go`.
- Work order: `plans/work-orders/work-r1-desktop-app.md` §5.2.
