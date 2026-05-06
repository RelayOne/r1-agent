// Package mcp — JSON Schema draft 2020-12 documents for the lanes
// MCP tools (specs/lanes-protocol.md §7).
//
// Each schema is the verbatim §7 contract, embedded as a Go string
// constant so json.RawMessage can wrap it without parse cost. The
// constants are split into one input/output pair per tool so they
// can be reviewed against the spec one tool at a time.
//
// IMPORTANT: edits to these schemas MUST be mirrored in
// specs/lanes-protocol.md §7. The TASK-25 round-trip test asserts
// the wire shape of every emitted result against §4 / §7; a drift
// here without a spec update will fail that test.
package mcp

// 7.1 r1.lanes.list — read-only.
const lanesListInputSchema = `{
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
}`

const lanesListOutputSchema = `{
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
}`

// 7.2 r1.lanes.subscribe — streaming.
const lanesSubscribeInputSchema = `{
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
}`

const lanesSubscribeOutputSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "description": "One §4 lane event per stream chunk; final chunk is {ok, data:{ended:true,reason}}."
}`

// 7.3 r1.lanes.get — read-only.
const lanesGetInputSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["session_id", "lane_id"],
  "properties": {
    "session_id": {"type": "string"},
    "lane_id": {"type": "string"},
    "tail": {"type": "integer", "minimum": 0, "maximum": 500, "default": 0, "description": "Number of trailing events to include."}
  }
}`

const lanesGetOutputSchema = `{
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
}`

// 7.4 r1.lanes.kill — mutation, idempotent, cascade.
const lanesKillInputSchema = `{
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
}`

const lanesKillOutputSchema = `{
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
}`

// 7.5 r1.lanes.pin — mutation, idempotent.
const lanesPinInputSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["session_id", "lane_id", "pinned"],
  "properties": {
    "session_id": {"type": "string"},
    "lane_id": {"type": "string"},
    "pinned": {"type": "boolean"}
  }
}`

const lanesPinOutputSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["ok"],
  "properties": {
    "ok": {"type": "boolean"},
    "data": {"type": "object", "properties": {"lane_id": {"type": "string"}, "pinned": {"type": "boolean"}}},
    "error_code": {"type": "string", "enum": ["not_found", "internal"]},
    "error_message": {"type": "string"}
  }
}`
