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

// ApplicationRequiresReview queues unproven skills for review when they are
// applied. It does not pause anything — it simply publishes a review-queued
// event.
type ApplicationRequiresReview struct{}

// NewApplicationRequiresReview returns a new rule instance.
func NewApplicationRequiresReview() *ApplicationRequiresReview {
	return &ApplicationRequiresReview{}
}

func (r *ApplicationRequiresReview) Name() string { return "skill.application_requires_review" }

func (r *ApplicationRequiresReview) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtSkillApplied)}
}

func (r *ApplicationRequiresReview) Priority() int { return 40 }

func (r *ApplicationRequiresReview) Rationale() string {
	return "Skills below 'proven' confidence need post-application review to build trust."
}

// skillAppliedPayload is the expected shape of a skill.applied event.
type skillAppliedPayload struct {
	SkillID    string `json:"skill_id"`
	Confidence string `json:"confidence"` // "tentative", "candidate", "proven"
}

func (r *ApplicationRequiresReview) Evaluate(_ context.Context, evt bus.Event, _ *ledger.Ledger) (bool, error) {
	var sp skillAppliedPayload
	if err := json.Unmarshal(evt.Payload, &sp); err != nil {
		return false, fmt.Errorf("unmarshal skill applied: %w", err)
	}
	// Fire only if confidence is below "proven".
	return sp.Confidence == "tentative" || sp.Confidence == "candidate", nil
}

func (r *ApplicationRequiresReview) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var sp skillAppliedPayload
	if err := json.Unmarshal(evt.Payload, &sp); err != nil {
		return fmt.Errorf("unmarshal skill applied: %w", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"skill_id":   sp.SkillID,
		"confidence": sp.Confidence,
		"trigger_id": evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "skill.review.queued",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}

// PayloadSchema declares the supervisor.spawn.requested shape for
// this rule's primary emitted event (lenient default — most fields
// optional). Closes A3 for this rule.
func (r *ApplicationRequiresReview) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
