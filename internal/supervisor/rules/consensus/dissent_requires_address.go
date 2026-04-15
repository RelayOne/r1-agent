package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// DissentRequiresAddress transitions the loop to "resolving_dissents" when a
// dissent node is added, and notifies the proposing worker to address it.
type DissentRequiresAddress struct{}

// NewDissentRequiresAddress returns a new rule instance.
func NewDissentRequiresAddress() *DissentRequiresAddress {
	return &DissentRequiresAddress{}
}

func (r *DissentRequiresAddress) Name() string {
	return "consensus.dissent_requires_address"
}

func (r *DissentRequiresAddress) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

func (r *DissentRequiresAddress) Priority() int { return 90 }

func (r *DissentRequiresAddress) Rationale() string {
	return "Dissent must be resolved before a loop can converge; the proposing worker must address it."
}

func (r *DissentRequiresAddress) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, fmt.Errorf("unmarshal node added payload: %w", err)
	}

	return strings.Contains(np.NodeType, "dissent"), nil
}

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

// PayloadSchema declares the supervisor.spawn.requested shape for
// this rule's primary emitted event (lenient default — most fields
// optional). Closes A3 for this rule.
func (r *DissentRequiresAddress) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
