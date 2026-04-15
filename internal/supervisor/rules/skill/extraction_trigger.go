// Package skill implements supervisor rules for skill extraction, governance,
// import consensus, and quality auditing.
package skill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// ExtractionTrigger fires when a loop converges or escalates to request skill
// extraction. For escalated loops, it only fires when the user indicated a
// change of approach or abandonment.
type ExtractionTrigger struct{}

// NewExtractionTrigger returns a new rule instance.
func NewExtractionTrigger() *ExtractionTrigger {
	return &ExtractionTrigger{}
}

func (r *ExtractionTrigger) Name() string { return "skill.extraction_trigger" }

func (r *ExtractionTrigger) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "loop."}
}

func (r *ExtractionTrigger) Priority() int { return 50 }

func (r *ExtractionTrigger) Rationale() string {
	return "Every completed loop is a potential source of reusable skill; extraction captures learnings."
}

// escalationPayload is the expected shape of a loop.escalated event.
type escalationPayload struct {
	Outcome string `json:"outcome"` // "try_different_approach", "abandon", etc.
}

func (r *ExtractionTrigger) Evaluate(_ context.Context, evt bus.Event, _ *ledger.Ledger) (bool, error) {
	switch bus.EventType(evt.Type) {
	case "loop.converged":
		return true, nil
	case "loop.escalated":
		var ep escalationPayload
		if err := json.Unmarshal(evt.Payload, &ep); err != nil {
			return false, fmt.Errorf("unmarshal escalation payload: %w", err)
		}
		return ep.Outcome == "try_different_approach" || ep.Outcome == "abandon", nil
	default:
		return false, nil
	}
}

func (r *ExtractionTrigger) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"trigger_id":   evt.ID,
		"trigger_type": string(evt.Type),
		"mission_id":   evt.Scope.MissionID,
	})
	return b.Publish(bus.Event{
		Type:      bus.EvtSkillExtraction,
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}

// PayloadSchema declares the supervisor.spawn.requested shape for
// this rule's primary emitted event (lenient default — most fields
// optional). Closes A3 for this rule.
func (r *ExtractionTrigger) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
