// Package server — lanes-protocol backward-compat dual-emit bridge (TASK-28).
//
// During the compat window (one minor release per
// specs/lanes-protocol.md §"Out of scope" item 1 and §10.5), every
// `lane.delta` emitted by the MAIN lane is re-emitted as a legacy
// `session.delta` event so pre-lanes desktop clients (which subscribe via
// the older `/api/events` SSE endpoint and the JSON `event` field) keep
// working without code changes.
//
// Behaviour:
//
//   - Subscribes to `EventLaneDelta` on the hub. Filters in the handler
//     so only Kind == "main" lanes generate the dual-emit. Other lane
//     kinds (lobe, tool, mission_task, router) are NOT re-emitted —
//     legacy clients never saw their content and would not know how to
//     route it anyway.
//   - For each qualifying delta, emits a new `EventSessionDelta` hub
//     event AND directly publishes a JSON envelope to the legacy
//     EventBus so the existing `/api/events` SSE handler picks it up.
//   - The `lane.delta` itself is left untouched on the bus; new clients
//     consuming `lane.delta` see the original event with the same
//     content.
//
// Removal: when the compat window closes, delete this file plus the
// EventSessionDelta constant in internal/hub/events.go. Tests in
// internal/server/compat_test.go pin the contract and will break loudly
// at that point — that is intentional.
package server

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/hub"
)

// LegacySessionDeltaSubID is the hub subscriber ID used by the dual-emit
// bridge. Exposed so tests can assert registration.
const LegacySessionDeltaSubID = "server.lanes.compat.session_delta"

// BridgeMainLaneToSessionDelta wires the dual-emit subscriber on the
// supplied hub Bus. When eventBus is non-nil, qualifying deltas are also
// pushed to it so legacy `/api/events` SSE clients receive the
// `session.delta` JSON envelope without going back through the hub
// fan-out a second time.
//
// The wiring is idempotent: calling twice is allowed but the second call
// is a no-op (Register replaces by ID in the hub). Returns the subscriber
// ID so callers can Unregister at shutdown.
func BridgeMainLaneToSessionDelta(bus *hub.Bus, eventBus *EventBus) string {
	if bus == nil {
		return ""
	}
	bus.Register(hub.Subscriber{
		ID:       LegacySessionDeltaSubID,
		Events:   []hub.EventType{hub.EventLaneDelta},
		Mode:     hub.ModeObserve,
		Priority: 9100, // below the lanes SSE/WS subscribers (9200/9300)
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			if !shouldDualEmit(ev) {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			emitLegacySessionDelta(bus, eventBus, ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
	return LegacySessionDeltaSubID
}

// shouldDualEmit reports whether ev qualifies for legacy session.delta
// dual-emission. Per spec §"Out of scope" item 1 the dual-emit covers
// the MAIN lane's assistant-text deltas only — every other lane and
// content-block type passes through untouched.
func shouldDualEmit(ev *hub.Event) bool {
	if ev == nil || ev.Lane == nil {
		return false
	}
	if ev.Type != hub.EventLaneDelta {
		return false
	}
	if ev.Lane.Kind != hub.LaneKindMain {
		return false
	}
	if ev.Lane.Block == nil {
		return false
	}
	// We re-emit text_delta and thinking_delta; tool_use_* and
	// tool_result blocks were never carried by the legacy session.delta
	// payload (legacy desktop only rendered streamed text). Re-emitting
	// them would break clients that expected payload.text to be a string.
	switch ev.Lane.Block.Type {
	case "text_delta", "thinking_delta":
		return true
	default:
		return false
	}
}

// emitLegacySessionDelta produces the legacy event in two places:
//
//  1. on the hub Bus as EventSessionDelta — picks up any subscriber
//     that wants the typed event (e.g. test harnesses, the Tauri host
//     stub via desktop_rpc_cmd.go);
//  2. on the EventBus directly as the JSON envelope every legacy
//     /api/events SSE consumer expects.
//
// The two paths carry the same content; (2) skips the hub fan-out so we
// don't double-publish on the EventBus through BridgeHubToEventBus.
//
// dualEmitCounter is incremented once per call; tests use
// LegacyDualEmitCount to assert wiring is firing.
var dualEmitCounter atomic.Uint64

func emitLegacySessionDelta(bus *hub.Bus, eventBus *EventBus, ev *hub.Event) {
	dualEmitCounter.Add(1)

	// Path 1: hub re-emit. Build a minimal Event with Type=session.delta
	// and a payload that mirrors what pre-lanes clients consumed. We
	// reuse the original Lane payload's text and timestamp so the
	// content matches lane.delta byte-for-byte.
	legacy := &hub.Event{
		Type:      hub.EventSessionDelta,
		Timestamp: ev.Timestamp,
		Custom: map[string]any{
			"session_id": ev.Lane.SessionID,
			"payload": map[string]any{
				"text":     ev.Lane.Block.Text,
				"thinking": ev.Lane.Block.Thinking,
				"type":     ev.Lane.Block.Type,
			},
		},
	}
	bus.EmitAsync(legacy)

	// Path 2: legacy SSE envelope. The /api/events bridge in bridge.go
	// publishes JSON-encoded *hub.Event records; the legacy desktop
	// client looks for a top-level `event` field carrying the type and
	// `session_id` / `payload` fields underneath. We construct the
	// envelope inline because hub.Event JSON encoding does not surface
	// session.delta in the shape pre-lanes clients expect (the legacy
	// shape predates the typed Custom map).
	if eventBus != nil {
		body, err := json.Marshal(map[string]any{
			"event":      string(hub.EventSessionDelta),
			"session_id": ev.Lane.SessionID,
			"event_id":   ev.ID,
			"at":         ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
			"payload": map[string]any{
				"text":     ev.Lane.Block.Text,
				"thinking": ev.Lane.Block.Thinking,
				"type":     ev.Lane.Block.Type,
			},
		})
		if err == nil {
			eventBus.Publish(string(body))
		}
	}
}

// LegacyDualEmitCount returns the running tally of legacy session.delta
// emissions. Test-only accessor; production code should not branch on
// this number.
func LegacyDualEmitCount() uint64 {
	return dualEmitCounter.Load()
}
