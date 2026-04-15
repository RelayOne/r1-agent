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

// ContradictsOutcome detects when a loop with applied skills reaches a negative
// terminal state, indicating the skill may have contributed to failure.
type ContradictsOutcome struct{}

// NewContradictsOutcome returns a new rule instance.
func NewContradictsOutcome() *ContradictsOutcome {
	return &ContradictsOutcome{}
}

func (r *ContradictsOutcome) Name() string { return "skill.contradicts_outcome" }

func (r *ContradictsOutcome) Pattern() bus.Pattern {
	// Match both loop.escalated and judge verdict events.
	return bus.Pattern{TypePrefix: "loop."}
}

func (r *ContradictsOutcome) Priority() int { return 60 }

func (r *ContradictsOutcome) Rationale() string {
	return "When a loop fails and skills were applied, those skills need urgent review to prevent future harm."
}

// loopTerminalPayload is the expected payload for loop terminal events.
type loopTerminalPayload struct {
	Outcome     string   `json:"outcome"`
	SkillsUsed  []string `json:"skills_used"`
	IsNegative  bool     `json:"is_negative"`
}

func (r *ContradictsOutcome) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	switch bus.EventType(evt.Type) {
	case "loop.escalated":
		// Check if outcome is negative.
		var ltp loopTerminalPayload
		if err := json.Unmarshal(evt.Payload, &ltp); err != nil {
			return false, fmt.Errorf("unmarshal loop terminal: %w", err)
		}
		if !ltp.IsNegative {
			return false, nil
		}
		// Check if any skills were applied in this loop.
		if len(ltp.SkillsUsed) > 0 {
			return true, nil
		}
		// Fall back to querying the ledger for skill applications in this loop.
		return r.hasSkillsInLoop(ctx, evt, l)
	default:
		return false, nil
	}
}

func (r *ContradictsOutcome) hasSkillsInLoop(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "skill.application",
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, nil
	}

	for _, n := range nodes {
		var sa struct {
			LoopID string `json:"loop_id"`
		}
		if err := json.Unmarshal(n.Content, &sa); err != nil {
			continue
		}
		if sa.LoopID == evt.Scope.LoopID {
			return true, nil
		}
	}
	return false, nil
}

func (r *ContradictsOutcome) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var ltp loopTerminalPayload
	_ = json.Unmarshal(evt.Payload, &ltp)

	payload, _ := json.Marshal(map[string]any{
		"role":        "SkillReviewer",
		"reason":      "skill_contradicts_outcome",
		"urgent":      true,
		"skills_used": ltp.SkillsUsed,
		"loop_id":     evt.Scope.LoopID,
		"trigger_id":  evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: supervisor.spawn.requested — contradiction spawn.
func (r *ContradictsOutcome) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
