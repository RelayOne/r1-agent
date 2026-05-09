package sessionctl

import (
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/google/uuid"
)

// NewBusEmitter returns an Emit callback (matching the signature
// of Deps.Emit) that publishes operator-originated events to a
// durable bus.Bus instead of the audit-only no-op default. The
// returned eventID is the bus event ID so audit consumers can
// correlate Slack-envelope responses with bus rows.
//
// Closes audit/scan-go-stubs.md item "internal/sessionctl
// handlers.go Emit pass-through". The pre-existing wiring in
// cmd/r1/ctl_bootstrap.go installs a no-op emitter; that's still
// the safe default for entry points that don't run a bus, but
// any caller that has a *bus.Bus can swap in NewBusEmitter(b,
// sessionID) and get durable, replayable operator events for
// free.
//
// Event shape:
//   - Type: kind (e.g. "operator.approve", "operator.override")
//   - Timestamp: time.Now()
//   - EmitterID: "sessionctl/" + sessionID  (uniquely identifies
//     the originating session — bus.Scope's first-class fields
//     are mission/branch/loop/task/stance, not session, so we
//     pin the session via EmitterID)
//   - Payload: JSON-marshaled `payload` (any). On marshal error
//     the event is dropped and the empty string is returned —
//     consistent with the existing handlers.go marshal helper
//     contract (operator events are best-effort, not durable
//     primary state).
func NewBusEmitter(b *bus.Bus, sessionID string) func(kind string, payload any) string {
	if b == nil {
		// Nil bus → no-op emitter. Callers can still wire this
		// without a nil-check at every site.
		return func(string, any) string { return "" }
	}
	emitter := "sessionctl/" + sessionID
	return func(kind string, payload any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		evt := bus.Event{
			ID:        uuid.New().String(),
			Type:      bus.EventType(kind),
			Timestamp: time.Now(),
			EmitterID: emitter,
			Payload:   raw,
		}
		if err := b.Publish(evt); err != nil {
			return ""
		}
		return evt.ID
	}
}
