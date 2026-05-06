# STOKE Protocol — v1.0

**STOKE** — **S**trong **T**raceable **O**bservable **K**nowledge **E**xecutor.

STOKE is the event envelope that every R1-emitted reasoning event
carries when the NDJSON stream is enabled. It is additive on top of the
existing Claude-Code-compatible shape (`type`, `uuid`, `session_id`,
`ts`) and extends it with fields that let r1-server, CloudSwarm, and
any other consumer correlate a single event back to:

- the **protocol version** (forward-compat gate)
- the **instance** that produced it (one R1 process per repo)
- the **trace** it participates in (W3C Trace Context)
- the **ledger node** (content-addressed SHA-256) that authored it

The protocol is intentionally tiny. The STOKE "glass box" thesis is
that R1 already produces a content-addressed Merkle-chained
reasoning ledger; this envelope just makes it addressable by external
consumers without parsing the event body.

## Envelope

```json
{
  "type":           "stoke.descent.tier",
  "ts":             "2026-04-20T21:05:32.123456Z",
  "uuid":           "8c36b0e2-df4d-4e08-b37f-f0a18c4bda2f",
  "session_id":     "c3b2e1a9-...",
  "stoke_version":  "1.0",
  "instance_id":    "r1-a3f7b2c4",
  "trace_parent":   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
  "ledger_node_id": "node-sha256-...",
  "data":           { ... event body ... }
}
```

| Field | Source | Required? |
|-------|--------|-----------|
| `type` | call-site string, e.g. `stoke.descent.tier` | yes |
| `ts` | UTC RFC3339Nano, stamped by emitter | yes |
| `uuid` | fresh v4, stamped by emitter | yes |
| `session_id` | emitter's session ID (Claude-Code-compat) | yes |
| `stoke_version` | `streamjson.StokeProtocolVersion` constant | when `SetStokeMeta` ran |
| `instance_id` | `r1-<8hex>` from `internal/session.NewInstanceID` | when `SetStokeMeta` ran |
| `trace_parent` | W3C Trace Context header string | when `SetStokeMeta` ran |
| `ledger_node_id` | content-addressed hash of the emitting ledger node | when known by caller |
| remainder | call-site-provided body (kept under the event's top level) | optional |

**Backward compatibility rule:** envelope fields are additive. A
consumer that speaks only the Claude-Code-compatible subset (`type`,
`uuid`, `session_id`, `ts`) sees every STOKE event as a superset of
the old shape — never a breaking change. This is why empty envelope
values are *omitted*, not emitted as `null`.

## Event types

Today's `stoke.*` namespace — grows as more R1 subsystems route
events through the envelope.

| Event type | Emitted when | Notable body fields |
|-----------|--------------|---------------------|
| `stoke.plan.ready` | SOW parsed + session DAG + cost estimate | `sow_title`, `total_sessions`, `estimated_cost`, `session_ids` |
| `stoke.session.start` | session goroutine begins | `session_id`, `title`, `task_count` |
| `stoke.session.end` | session acceptance loop exits | `outcome`, `cost_usd`, `duration_s` |
| `stoke.task.start` | task worker dispatched | `task_id`, `title` |
| `stoke.task.end` | task worker returns | `outcome`, `duration_s`, `tool_calls` |
| `stoke.ac.result` | acceptance criterion evaluated | `ac_id`, `passed`, `output` (≤2KB) |
| `stoke.descent.start` | verification-descent engine entered | `ac_id`, `criterion_description` |
| `stoke.descent.tier` | tier transition inside descent | `ac_id`, `from_tier`, `to_tier`, `reason` |
| `stoke.descent.resolve` | descent exits | `ac_id`, `outcome`, `tier_reached`, `category`, `reason` |
| `stoke.cost` | provider cost update | `session_id`, `cost_usd`, `input_tokens`, `output_tokens` |
| `stoke.delegation.verify` | hired-agent deliverable passes descent | `contract_id`, `ac_id`, `outcome`, `tier_reached` |
| `stoke.delegation.settle` | TrustPlane settlement completes | `contract_id` |

## Trace context

`trace_parent` follows the W3C Trace Context spec
(<https://www.w3.org/TR/trace-context-1/#traceparent-header>):

```
<version>-<trace-id>-<parent-id>-<trace-flags>
00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
```

- `version`: `00`
- `trace-id`: 16 random bytes (hex-encoded, 32 chars) — stable for
  the lifetime of one R1 process / session
- `parent-id`: 8 random bytes (hex-encoded, 16 chars) — stable per
  R1 process; becomes the parent when cross-system propagation
  lands
- `trace-flags`: `01` (sampled)

`streamjson.NewTraceParent()` generates one. R1 stamps it once at
startup via `SetStokeMeta` and keeps it immutable for the process
lifetime. A future release may extend this to propagate an incoming
`traceparent` from RelayGate / CloudSwarm headers — the envelope
field is already in place for that.

## Ledger node IDs

`ledger_node_id` is a SHA-256-derived content-addressed identifier
allocated when R1 writes a node under `<repo>/.stoke/ledger/nodes/`.
The emit call site passes it in via the `data` map:

```go
emitter.EmitStoke("stoke.descent.tier", map[string]any{
    "ledger_node_id": node.ID,
    "ac_id":          ac.ID,
    "from_tier":      "T3",
    "to_tier":        "T4",
})
```

r1-server uses this to join event rows against `ledger_nodes` without
pattern-matching on the event body.

## Compatibility notes

- The three legacy single-lane emitters (`EmitSystem`, `EmitAssistant`,
  `EmitUser`, `EmitResult`) keep their existing shapes verbatim. STOKE
  does not retrofit them.
- `EmitTopLevel` remains the CloudSwarm-facing emitter for the
  compact events catalog (`hitl_required`, `error`, `complete`,
  `mission.aborted`). Those events will gain the STOKE envelope in a
  follow-up if CloudSwarm wants early adoption; until then they stay
  at their current shape.
- `EmitStoke` is additive — it adds the STOKE-namespace events
  without altering any pre-existing emit behavior.

## Versioning

`stoke_version` is a semver-ish short string on the envelope. `1.0`
covers the field set documented above. Breaking changes bump the
major (e.g. removing a field) and are announced in release notes;
additive changes bump the minor.

Consumers should gate on the version when reading newly-added fields:

```go
if evt.StokeVersion == "" || semver.Compare("v"+evt.StokeVersion, "v1.0") < 0 {
    // old emitter; fall back to legacy parse
}
```
