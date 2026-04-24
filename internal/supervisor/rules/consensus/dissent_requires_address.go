package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
	"github.com/RelayOne/r1/internal/supervisor"
)

// DissentRequiresAddress transitions the loop to "resolving_dissents" when a
// dissent node is added, and notifies the proposing worker to address it.
type DissentRequiresAddress struct{}

// NewDissentRequiresAddress returns a new rule instance.
func NewDissentRequiresAddress() *DissentRequiresAddress {
	return &DissentRequiresAddress{}
}

// Name returns the stable rule identifier used by the supervisor
// registry and audit logs.
func (r *DissentRequiresAddress) Name() string {
	return "consensus.dissent_requires_address"
}

// Pattern subscribes the rule to ledger-node-added events so each new
// node can be screened for the dissent type.
func (r *DissentRequiresAddress) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

// Priority (90) runs this rule before ConvergenceDetected (95) so a
// dissent flips the loop state before any convergence check fires on
// the same event.
func (r *DissentRequiresAddress) Priority() int { return 90 }

// Rationale is the human-readable justification surfaced in audit.
func (r *DissentRequiresAddress) Rationale() string {
	return "Dissent must be resolved before a loop can converge; the proposing worker must address it."
}

// Evaluate reports true iff the added node is a dissent node.
func (r *DissentRequiresAddress) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, fmt.Errorf("unmarshal node added payload: %w", err)
	}

	return strings.Contains(np.NodeType, "dissent"), nil
}

// Action emits two bus events: a loop state transition to
// resolving_dissents and a dissent notification addressed to the
// proposing worker with instructions to address the dissent.
func (r *DissentRequiresAddress) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return fmt.Errorf("unmarshal node added payload: %w", err)
	}

	// Transition loop to resolving_dissents.
	transitionPayload, _ := json.Marshal(map[string]string{
		"loop_id": np.LoopID,
		"state":   "resolving_dissents",
		"reason":  "dissent received",
	})
	if err := b.Publish(bus.Event{
		Type:      "consensus.loop.state.changed",
		Scope:     evt.Scope,
		Payload:   transitionPayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish state change: %w", err)
	}

	// Notify the proposing worker.
	notifyPayload, _ := json.Marshal(map[string]string{
		"node_id":     np.NodeID,
		"dissent_by":  evt.EmitterID,
		"action":      "address_dissent",
	})
	if err := b.Publish(bus.Event{
		Type:      "consensus.dissent.notification",
		Scope:     evt.Scope,
		Payload:   notifyPayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish notification: %w", err)
	}

	return nil
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: consensus.loop.state.changed — primary; also emits consensus.dissent.notification.
func (r *DissentRequiresAddress) PayloadSchema() *schemaval.Schema {
	return supervisor.ConsensusLoopStateSchema()
}
