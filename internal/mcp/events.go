// Package mcp — events.go — MCP-10 lifecycle publishers.
//
// This file is the single entry point for MCP-specific lifecycle events.
// Every call emits on BOTH the in-process event bus AND the streamjson
// emitter; both sinks are optional (nil-safe) and best-effort — a nil or
// failed write is logged at debug level but NEVER fails the calling tool
// dispatch.
//
// Hard rule from specs/mcp-client.md §Event Emission: payloads MUST NOT
// carry raw tool-call args, raw response bodies, or env-var / auth-token
// values. `bytes_in`/`bytes_out` are counts, not contents. Stoke surfaces
// only: server name, tool name, call id, durations, sizes, error-kind,
// redacted error message, and circuit state transitions.
//
// Scope: this file only emits events. It does NOT wire into the circuit
// breaker (MCP-8) or the registry dispatch path (MCP-7); those packages
// import this one and call the Publish* methods directly. MCP-5's SSE
// transport will call PublishConfigDeprecated once it's upgraded to stop
// using log.Printf.
package mcp

import (
	"encoding/json"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/logging"
	"github.com/ericmacdougall/stoke/internal/streamjson"
)

// Bus event type constants — dotted-namespace per bus.EventType convention.
// The streamjson mirror prepends "stoke." so subscribers can distinguish
// Stoke-family events from the base Claude Code envelope.
const (
	EvtMCPCallStart          bus.EventType = "mcp.call.start"
	EvtMCPCallComplete       bus.EventType = "mcp.call.complete"
	EvtMCPCallError          bus.EventType = "mcp.call.error"
	EvtMCPCircuitStateChange bus.EventType = "mcp.circuit.state_change"
	EvtMCPConfigDeprecated   bus.EventType = "mcp.config.deprecated"
)

// Canonical err_kind values for PublishError. Keep in sync with
// specs/mcp-client.md §Event Emission.
const (
	ErrKindCircuitOpen    = "circuit_open"
	ErrKindAuthMissing    = "auth_missing"
	ErrKindPolicyDenied   = "policy_denied"
	ErrKindSchemaInvalid  = "schema_invalid"
	ErrKindSizeCap        = "size_cap"
	ErrKindTimeout        = "timeout"
	ErrKindTransportError = "transport_error"
	ErrKindOther          = "other"
)

// Emitter is the MCP-10 lifecycle publisher. Both the bus and stream
// dependencies are optional — pass nil for either when the corresponding
// sink is not available (e.g. headless unit tests with no bus).
type Emitter struct {
	bus    *bus.Bus
	stream *streamjson.Emitter
}

// NewEmitter constructs a nil-safe Emitter. Either argument may be nil;
// the resulting Emitter degrades gracefully by skipping the absent sink.
func NewEmitter(b *bus.Bus, s *streamjson.Emitter) *Emitter {
	return &Emitter{bus: b, stream: s}
}

// PublishStart emits a call-start lifecycle event. Payload contains ONLY
// {server, tool, call_id} — no args, no schema, no token.
func (e *Emitter) PublishStart(server, tool, callID string) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"server":  server,
		"tool":    tool,
		"call_id": callID,
	}
	e.emit(EvtMCPCallStart, payload)
}

// PublishComplete emits a call-complete lifecycle event. `sizeBytes` is
// the byte COUNT of the response — the body itself is never included.
func (e *Emitter) PublishComplete(server, tool, callID string, durationMs int64, sizeBytes int) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"server":      server,
		"tool":        tool,
		"call_id":     callID,
		"duration_ms": durationMs,
		"size_bytes":  sizeBytes,
	}
	e.emit(EvtMCPCallComplete, payload)
}

// PublishError emits a call-error lifecycle event. `errKind` must be one
// of the ErrKind* constants; `errMsg` is the redacted human-readable
// message (callers are responsible for routing through the redactor
// before handing to this function — the emitter is not the redaction
// boundary).
func (e *Emitter) PublishError(server, tool, callID, errKind, errMsg string) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"server":   server,
		"tool":     tool,
		"call_id":  callID,
		"err_kind": errKind,
		"err_msg":  errMsg,
	}
	e.emit(EvtMCPCallError, payload)
}

// PublishCircuitStateChange emits a circuit-state transition event.
// Called by the MCP-8 Circuit's OnStateChange hook once it's wired in.
// `info` is a free-form map (e.g. fail_count, cooldown_ms) — callers
// MUST NOT put args / bodies / tokens in it.
func (e *Emitter) PublishCircuitStateChange(server, from, to string, info map[string]any) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"server": server,
		"from":   from,
		"to":     to,
	}
	// Always emit the key so consumers can rely on its presence; copy into
	// a fresh map so downstream mutation doesn't leak into the caller's.
	infoCopy := map[string]any{}
	for k, v := range info {
		infoCopy[k] = v
	}
	payload["info"] = infoCopy
	e.emit(EvtMCPCircuitStateChange, payload)
}

// PublishConfigDeprecated emits a config-deprecated event. Used by the
// SSE transport (MCP-5) once it's upgraded to call this instead of
// log.Printf so operators get bus-visible notice of deprecated config.
func (e *Emitter) PublishConfigDeprecated(server, reason string) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"server": server,
		"reason": reason,
	}
	e.emit(EvtMCPConfigDeprecated, payload)
}

// emit is the shared best-effort dispatch path: marshal once, fan out to
// bus + stream. Failures on either sink are swallowed at debug level so
// tool dispatch never blocks on telemetry.
func (e *Emitter) emit(evtType bus.EventType, payload map[string]any) {
	if e.bus != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			logging.Global().Debug("mcp.emit: marshal failed",
				"event_type", string(evtType),
				"error", err.Error())
		} else {
			if pubErr := e.bus.Publish(bus.Event{
				Type:      evtType,
				Timestamp: time.Now(),
				EmitterID: "mcp.emitter",
				Payload:   raw,
			}); pubErr != nil {
				logging.Global().Debug("mcp.emit: bus publish failed",
					"event_type", string(evtType),
					"error", pubErr.Error())
			}
		}
	}

	if e.stream != nil {
		// streamjson mirror: prefix "stoke." per spec §Event Emission.
		e.stream.EmitStoke("stoke."+string(evtType), payload)
	}
}
