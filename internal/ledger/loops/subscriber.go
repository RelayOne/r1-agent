// subscriber.go — bus-to-ledger persistence for consensus loop state
// transitions (audit A066).
//
// Supervisor consensus rules (internal/supervisor/rules/consensus)
// announce loop transitions as "consensus.loop.state.changed" bus events
// but cannot write the ledger from their Action hook (it only receives
// the bus). Tracker.SubscribeStateChanges is the missing consumer: it
// persists each announced transition via Tracker.TransitionState so loop
// nodes actually change state. Registered at governance boot
// (internal/governance.New).
package loops

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/bus"
)

// StateChangedEventType is the bus event type announcing a consensus
// loop state transition. Emitted by the consensus supervisor rules
// (ConvergenceDetected, DissentRequiresAddress) and consumed by
// Tracker.SubscribeStateChanges.
const StateChangedEventType = "consensus.loop.state.changed"

// knownStates guards the subscriber against persisting states outside
// the seven-state machine.
var knownStates = map[LoopState]bool{
	StateProposing:         true,
	StateDrafted:           true,
	StateConvening:         true,
	StateReviewing:         true,
	StateResolvingDissents: true,
	StateConverged:         true,
	StateEscalated:         true,
}

// stateChangedPayload is the JSON payload of a
// consensus.loop.state.changed event (see
// supervisor.ConsensusLoopStateSchema).
type stateChangedPayload struct {
	LoopID string `json:"loop_id"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// SubscribeStateChanges registers a bus subscription that persists every
// consensus.loop.state.changed event to the ledger via TransitionState.
// Malformed payloads, empty loop IDs, and unknown states are ignored;
// persistence errors are fire-and-forget (the subscription is an
// observer — same convention as the governance hub bridge). The returned
// subscription lets callers unsubscribe; it also dies with the bus.
func (t *Tracker) SubscribeStateChanges(b *bus.Bus) *bus.Subscription {
	return b.Subscribe(bus.Pattern{TypePrefix: StateChangedEventType}, func(evt bus.Event) {
		var p stateChangedPayload
		if err := json.Unmarshal(evt.Payload, &p); err != nil {
			return
		}
		state := LoopState(p.State)
		if p.LoopID == "" || !knownStates[state] {
			return
		}
		_ = t.TransitionState(context.Background(), p.LoopID, state, p.Reason)
	})
}
