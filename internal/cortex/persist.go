// Package cortex -- persistent write-through for Workspace Notes.
//
// writeNote is the single point where the in-memory Workspace fans a freshly
// validated Note out to the durable, WAL-backed bus. The Workspace.Publish
// critical section calls this function while still holding the write mutex
// (per spec item 4) so that crash-recovery replay observes Notes in the
// same total order they appear in Workspace.notes.
//
// Two operating modes:
//
//  1. Durable mode (durable != nil): marshal the Note to JSON and emit a
//     bus.Event of Type "cortex.note.published". Errors propagate to the
//     caller, which surfaces them through Workspace.Publish.
//
//  2. In-memory mode (durable == nil): no-op. The Workspace constructor
//     accepts a nil bus to support tests, the prewarm harness, and
//     ephemeral runs that intentionally skip persistence.
package cortex

import (
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
)

// EventTypeCortexNotePublished is the bus.EventType emitted for every Note
// that lands in a durable Workspace. Subscribers and crash-recovery
// replayers filter on this string; keeping it as a named constant prevents
// drift between writeNote and any future reader.
const EventTypeCortexNotePublished bus.EventType = "cortex.note.published"

// writeNote performs the durable write-through for a single Note.
//
// The function is internal to package cortex on purpose: callers go
// through Workspace.Publish, which already validates the Note, assigns
// its ID/Round/EmittedAt, and serializes Publishes via the workspace
// mutex. writeNote therefore assumes its Note argument is fully populated
// and structurally valid.
//
// Error contract:
//
//   - durable == nil          -> nil (in-memory mode, intentional no-op).
//   - json.Marshal failure    -> wrapped error with the "cortex/persist"
//     namespace so failure-class fingerprinting can group it.
//   - durable.Publish failure -> bubbled verbatim; bus.Bus.Publish already
//     wraps WAL append errors with its own "bus:" prefix.
func writeNote(durable *bus.Bus, n Note) error {
	if durable == nil {
		return nil
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("cortex/persist: marshal note: %w", err)
	}
	return durable.Publish(bus.Event{
		Type:    EventTypeCortexNotePublished,
		Payload: json.RawMessage(payload),
	})
}
