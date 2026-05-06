// Package streamjson — lane.go
//
// TASK-9 of specs/lanes-protocol.md §11: register a hub subscriber for
// every EventLane* type and route through TwoLane.EmitTopLevel with the
// §5.3 critical-vs-observability rules.
//
// Cortex-core publishes lane events through the in-process hub.Bus
// (see internal/cortex/lane_lifecycle.go). This file is the bridge
// between hub events and the NDJSON wire format consumed by stdout
// readers (CloudSwarm, Multica, OpenACP).
//
// ## Critical-vs-observability split (spec §5.3)
//
//   | Event                                    | Lane          |
//   |------------------------------------------|---------------|
//   | lane.created                             | observability |
//   | lane.status (non-terminal)               | observability |
//   | lane.status (status=errored)             | critical      |
//   | lane.delta                               | observability |
//   | lane.cost                                | observability |
//   | lane.note (severity=critical)            | critical      |
//   | lane.note (other severities)             | observability |
//   | lane.killed                              | critical      |
//
// The TwoLane emitter routes by event type alone via isCriticalType.
// Lane events that are CONDITIONALLY critical (lane.note, lane.status)
// get routed via the new isCriticalEvent helper introduced in TASK-10
// — this file simply hands the full event to TwoLane.EmitTopLevel with
// the right routing decision pre-computed.
//
// ## Naming convention (spec §5.3)
//
// The NDJSON wire envelope uses `"type"` (not `"event"`) per the
// existing emitter convention. The JSON-RPC wire envelope uses
// `"event"` per JSON-RPC convention. The MCP server normalizes both
// to `event`. This file emits the NDJSON shape: lane events are
// emitted as top-level events with `"type": "lane.<kind>"`.
package streamjson

import (
	"context"

	"github.com/RelayOne/r1/internal/hub"
)

// laneEventTypes lists every EventLane* type the subscriber attaches to.
// Listed exhaustively (instead of subscribing via the "*" wildcard) so
// adding a new lane event family later is a deliberate code change here
// — keeps drift between hub and streamjson visible in review.
var laneEventTypes = []hub.EventType{
	hub.EventLaneCreated,
	hub.EventLaneStatus,
	hub.EventLaneDelta,
	hub.EventLaneCost,
	hub.EventLaneNote,
	hub.EventLaneKilled,
}

// RegisterLaneEvents subscribes a hub.Bus subscriber that bridges every
// EventLane* event to the supplied TwoLane emitter. The subscriber is
// registered under id "streamjson.lanes" with Mode=Observe so it does
// not gate other handlers and never blocks the publishing goroutine.
//
// Routing decisions are made per spec §5.3:
//
//   - lane.killed                       → critical lane
//   - lane.status (status=errored)      → critical lane
//   - lane.note (severity=critical)     → critical lane
//   - everything else                   → observability lane
//
// The actual routing is performed by TwoLane.EmitTopLevel which inspects
// the event type via isCriticalType. Conditional cases (note severity,
// status value) are dispatched through sendCritical/sendObserv directly
// here so the routing logic stays co-located with the spec table.
//
// Returns the subscriber ID so callers can deregister or trace.
func RegisterLaneEvents(bus *hub.Bus, emitter *TwoLane) string {
	if bus == nil || emitter == nil {
		return ""
	}
	const subID = "streamjson.lanes"
	bus.Register(hub.Subscriber{
		ID:       subID,
		Events:   laneEventTypes,
		Mode:     hub.ModeObserve,
		Priority: 9100,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			emitLaneEvent(emitter, ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
	return subID
}

// emitLaneEvent formats one hub.Event carrying a LaneEvent payload into
// the NDJSON wire shape (spec §5.3) and routes it through the supplied
// TwoLane. The routing decision (critical vs observability) is made by
// isCriticalLaneEvent so conditional cases (lane.status with
// status=errored, lane.note with severity=critical) land on the right
// lane.
//
// Defensive: if ev is nil, ev.Lane is nil, or the emitter is disabled,
// returns without emitting. The bus dispatches asynchronously so a
// stale event arriving after Drain falls through TwoLane's stopped-lane
// fallback to a synchronous direct write.
func emitLaneEvent(emitter *TwoLane, ev *hub.Event) {
	if emitter == nil || !emitter.Enabled() || ev == nil || ev.Lane == nil {
		return
	}

	extra := buildLaneNDJSON(ev)
	eventType := string(ev.Type)

	if isCriticalLaneEvent(ev) {
		// Build a full envelope and route through the critical lane
		// directly so the EmitTopLevel critical/observability decision
		// (which only looks at event type) doesn't downgrade lane.note
		// or lane.status to observability.
		evt := buildEnvelope(emitter, eventType, extra)
		emitter.sendCritical(evt)
		return
	}
	emitter.EmitTopLevel(eventType, extra)
}

// isCriticalLaneEvent reports whether the lane event must route via the
// critical (blocking, never-drop) lane per spec §5.3:
//
//   - lane.killed is always critical
//   - lane.note with data.severity=="critical" is critical
//   - lane.status with data.status=="errored" is critical
//
// All other lane events (including lane.created, lane.delta, lane.cost,
// non-critical lane.note, non-errored lane.status) route via the
// observability lane.
//
// This helper looks at the full event (type + Lane payload) and is the
// counterpart to isCriticalType(eventType, subtype) which only sees the
// type string. TASK-10 extends isCriticalType to mark lane.killed
// unconditionally critical for callers that don't have access to the
// LaneEvent payload.
func isCriticalLaneEvent(ev *hub.Event) bool {
	if ev == nil {
		return false
	}
	switch ev.Type {
	case hub.EventLaneKilled:
		return true
	case hub.EventLaneNote:
		return ev.Lane != nil && ev.Lane.NoteSeverity == "critical"
	case hub.EventLaneStatus:
		return ev.Lane != nil && ev.Lane.Status == hub.LaneStatusErrored
	}
	return false
}

// buildEnvelope composes the canonical {type,uuid,session_id,...extra}
// shape used by TwoLane.EmitTopLevel. Extracted here so the conditional
// critical-routing path in emitLaneEvent can compose the same shape
// without duplicating EmitTopLevel's logic.
func buildEnvelope(emitter *TwoLane, eventType string, extra map[string]any) map[string]any {
	evt := map[string]any{
		"type":       eventType,
		"uuid":       generateEnvelopeUUID(),
		"session_id": emitter.SessionID(),
	}
	for k, v := range extra {
		evt[k] = v
	}
	return evt
}

// generateEnvelopeUUID returns a fresh UUID for the NDJSON envelope's
// `uuid` field. The `event_id` already carries the wire-level identifier
// (a ULID per TASK-8); `uuid` is kept for backwards-compat with the
// pre-existing TwoLane convention.
func generateEnvelopeUUID() string {
	// Reuse the existing buildEvent helper's UUID source via a synthetic
	// build to keep the format consistent with EmitTopLevel.
	return buildEvent("", "", "", nil)["uuid"].(string)
}

// buildLaneNDJSON converts a hub.Event carrying a LaneEvent payload into
// the per-event-type fields documented in spec §4. The returned map is
// MERGED into the canonical {type,uuid,session_id} envelope by the
// caller (TwoLane.EmitTopLevel adds those three keys). The returned map
// must therefore not include those three keys; doing so would shadow
// the envelope's session_id with a stale value.
func buildLaneNDJSON(ev *hub.Event) map[string]any {
	out := map[string]any{
		"event_id": ev.ID,
		"lane_id":  ev.Lane.LaneID,
	}
	if ev.Lane.Seq != 0 {
		out["seq"] = ev.Lane.Seq
	}
	if ev.Lane.SessionID != "" {
		// session_id flows through the envelope; we still include it
		// here so consumers that consume the body without the envelope
		// see it. TwoLane.EmitTopLevel adds session_id at the top
		// level — both paths produce identical session_id.
		out["session_id"] = ev.Lane.SessionID
	}
	if !ev.Timestamp.IsZero() {
		out["at"] = ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}

	// Per-type data subobject.
	data := map[string]any{}
	switch ev.Type {
	case hub.EventLaneCreated:
		if ev.Lane.Kind != "" {
			data["kind"] = string(ev.Lane.Kind)
		}
		if ev.Lane.LobeName != "" {
			data["lobe_name"] = ev.Lane.LobeName
		}
		if ev.Lane.ParentID != "" {
			data["parent_lane_id"] = ev.Lane.ParentID
		}
		if ev.Lane.Label != "" {
			data["label"] = ev.Lane.Label
		}
		if ev.Lane.StartedAt != nil {
			data["started_at"] = ev.Lane.StartedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		}
		if ev.Lane.Labels != nil {
			data["labels"] = ev.Lane.Labels
		}

	case hub.EventLaneStatus:
		if ev.Lane.Status != "" {
			data["status"] = string(ev.Lane.Status)
		}
		if ev.Lane.PrevStatus != "" {
			data["prev_status"] = string(ev.Lane.PrevStatus)
		}
		if ev.Lane.Reason != "" {
			data["reason"] = ev.Lane.Reason
		}
		if ev.Lane.ReasonCode != "" {
			data["reason_code"] = ev.Lane.ReasonCode
		}
		if ev.Lane.EndedAt != nil {
			data["ended_at"] = ev.Lane.EndedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		}

	case hub.EventLaneDelta:
		if ev.Lane.DeltaSeq != 0 {
			data["delta_seq"] = ev.Lane.DeltaSeq
		}
		if ev.Lane.Block != nil {
			data["content_block"] = ev.Lane.Block
		}

	case hub.EventLaneCost:
		data["tokens_in"] = ev.Lane.TokensIn
		data["tokens_out"] = ev.Lane.TokensOut
		if ev.Lane.CachedTokens != 0 {
			data["cached_tokens"] = ev.Lane.CachedTokens
		}
		data["usd"] = ev.Lane.USD
		if ev.Lane.CumulativeUSD != 0 {
			data["cumulative_usd"] = ev.Lane.CumulativeUSD
		}

	case hub.EventLaneNote:
		if ev.Lane.NoteID != "" {
			data["note_id"] = ev.Lane.NoteID
		}
		if ev.Lane.NoteSeverity != "" {
			data["severity"] = ev.Lane.NoteSeverity
		}
		if ev.Lane.NoteKind != "" {
			data["kind"] = ev.Lane.NoteKind
		}
		if ev.Lane.NoteSummary != "" {
			data["summary"] = ev.Lane.NoteSummary
		}

	case hub.EventLaneKilled:
		if ev.Lane.Reason != "" {
			data["reason"] = ev.Lane.Reason
		}
		if ev.Lane.Actor != "" {
			data["actor"] = ev.Lane.Actor
		}
		if ev.Lane.ActorID != "" {
			data["actor_id"] = ev.Lane.ActorID
		}
	}
	out["data"] = data
	return out
}
