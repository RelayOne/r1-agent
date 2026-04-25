package drift

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
	"github.com/RelayOne/r1-agent/internal/schemaval"
	"github.com/RelayOne/r1-agent/internal/supervisor"
)

// IntentAlignmentCheck spawns a fresh-context Judge at every task milestone to
// verify the work still aligns with the user's original intent.
type IntentAlignmentCheck struct{}

// NewIntentAlignmentCheck returns a new rule instance.
func NewIntentAlignmentCheck() *IntentAlignmentCheck {
	return &IntentAlignmentCheck{}
}

func (r *IntentAlignmentCheck) Name() string { return "drift.intent_alignment_check" }

func (r *IntentAlignmentCheck) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "task.milestone.reached"}
}

func (r *IntentAlignmentCheck) Priority() int { return 65 }

func (r *IntentAlignmentCheck) Rationale() string {
	return "Milestones are natural checkpoints to verify the team is still building what the user asked for."
}

func (r *IntentAlignmentCheck) Evaluate(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) {
	// Always fires on milestone events.
	return true, nil
}

func (r *IntentAlignmentCheck) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"role":       "Judge",
		"reason":     "intent_alignment_check",
		"trigger_id": evt.ID,
		"task_id":    evt.Scope.TaskID,
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: supervisor.spawn.requested — alignment check.
func (r *IntentAlignmentCheck) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
