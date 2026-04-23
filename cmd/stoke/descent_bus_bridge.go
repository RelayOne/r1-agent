package main

// descent_bus_bridge.go translates descent-hardening bus events (spec-1
// item 8) into streamjson "stoke.descent.*" subtypes so CloudSwarm and
// other NDJSON consumers can observe descent-side escalations without
// subscribing to the bus directly. This keeps decision C1 intact (no
// new package; extend streamjson) and matches spec-2's event schema
// table.
//
// Wiring: call InstallDescentBusBridge(bus, emitter) once per stoke
// process before dispatching SOW runs. Subsequent Publish() calls for
// descent/worker events fire the subscriber, which emits one streamjson
// system event per bus event.

import (
	"encoding/json"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/streamjson"
)

// descentSubtypeFor maps a bus event type to its streamjson subtype.
// Returns ("", false) for non-descent events.
func descentSubtypeFor(k bus.EventType) (string, bool) {
	switch k {
	case bus.EvtDescentFileCapExceeded:
		return "stoke.descent.file_cap_exceeded", true
	case bus.EvtDescentGhostWriteDetected:
		return "stoke.descent.ghost_write_detected", true
	case bus.EvtDescentBootstrapReinstalled:
		return "stoke.descent.bootstrap_reinstalled", true
	case bus.EvtDescentPreCompletionGateFailed:
		return "stoke.descent.pre_completion_gate_failed", true
	case bus.EvtWorkerEnvBlocked:
		return "stoke.worker.env_blocked", true
	}
	return "", false
}

// InstallDescentBusBridge subscribes to every descent event kind and
// mirrors the payload into streamjson as a "stoke.descent.*" subtype.
// The subscriber captures the bus event's Payload (JSON) and merges
// it under _stoke.dev/payload so consumers can inspect the original
// shape without parser gymnastics.
//
// No-op when either the bus or emitter is nil.
func InstallDescentBusBridge(b *bus.Bus, em *streamjson.Emitter) {
	if b == nil || em == nil {
		return
	}
	handler := func(evt bus.Event) {
		sub, ok := descentSubtypeFor(evt.Type)
		if !ok {
			return
		}
		extra := map[string]any{
			"_stoke.dev/bus_event_id": evt.ID,
			"_stoke.dev/bus_sequence": evt.Sequence,
			"_stoke.dev/emitter":      evt.EmitterID,
		}
		if len(evt.Payload) > 0 {
			var decoded any
			if err := json.Unmarshal(evt.Payload, &decoded); err == nil {
				extra["_stoke.dev/payload"] = decoded
			} else {
				extra["_stoke.dev/payload_raw"] = string(evt.Payload)
			}
		}
		em.EmitSystem(sub, extra)

		// AL-3 / SEAM-20: also emit the canonical 9-field SharedAuditEvent
		// shape so cross-product collectors (Multica, CloudSwarm audit
		// pipeline) can join stoke + cloudswarm + multica streams without
		// per-product parsers. Emitted alongside the legacy EmitSystem
		// call, not instead of it, so existing consumers keep working.
		severity := "info"
		switch evt.Type {
		case bus.EvtDescentFileCapExceeded,
			bus.EvtDescentGhostWriteDetected,
			bus.EvtDescentPreCompletionGateFailed,
			bus.EvtWorkerEnvBlocked:
			severity = "warn"
		}
		em.EmitSharedAudit(streamjson.SharedAuditEvent{
			Type: sub,
			// bus.Scope has no explicit SessionID; LoopID is used as
			// the session identifier per internal/eventlog/log.go:202
			// ("resume_cmd.go treats LoopID as the session identifier").
			SessionID: evt.Scope.LoopID,
			AgentID:   evt.EmitterID,
			TaskID:    evt.Scope.TaskID,
			Payload:   evt.Payload,
			Severity:  severity,
		})
	}
	// Subscribe once per event kind — Bus.Subscribe expects a pattern.
	for _, k := range bus.DescentEventKinds {
		_ = b.Subscribe(bus.Pattern{TypePrefix: string(k)}, handler)
	}
}
