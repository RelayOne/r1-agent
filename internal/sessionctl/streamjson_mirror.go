package sessionctl

import "strings"

// StreamjsonSink is the narrow interface the mirror needs from the
// streamjson emitter. Keeping this package decoupled from streamjson
// internals means tests can provide a fake sink without dragging the
// writer / uuid machinery in.
//
// The concrete implementation lives on *streamjson.Emitter as
// EmitOperator(verb, payload, eventID). Callers wire it in via
// NewStreamjsonEmit at main.go seam time.
type StreamjsonSink interface {
	EmitOperator(verb string, payload any, eventID string)
}

// NewStreamjsonEmit returns a Deps.Emit function that fans out every
// operator.* event emitted by handlers.go to two sinks:
//
//  1. delegate -- the authoritative event bus. The returned eventID
//     is threaded through so the streamjson record can cross-reference.
//  2. stream -- the Claude-Code-shape NDJSON emitter. The bus kind
//     ("operator.approve") is rewritten to its verb suffix ("approve")
//     before the sink is called, so the streamjson side produces
//     "stoke.operator.approve" (not "stoke.operator.operator.approve").
//
// Either sink may be nil and the mirror silently skips that path,
// which keeps the wiring trivial in tests and in early boot phases
// where streamjson isn't plumbed yet.
//
// Only kinds with the "operator." prefix are mirrored to streamjson;
// any other kind passes through to the delegate unmodified so the
// helper can be used unconditionally at the Deps.Emit seam.
func NewStreamjsonEmit(
	stream StreamjsonSink,
	delegate func(kind string, payload any) (eventID string),
) func(kind string, payload any) (eventID string) {
	return func(kind string, payload any) string {
		var eventID string
		if delegate != nil {
			eventID = delegate(kind, payload)
		}
		if stream != nil && strings.HasPrefix(kind, "operator.") {
			verb := strings.TrimPrefix(kind, "operator.")
			stream.EmitOperator(verb, payload, eventID)
		}
		return eventID
	}
}
