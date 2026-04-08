package consensus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

// PartnerTimeout handles a delayed timeout event for a consensus partner.
// If the partner has not responded, it marks them timed-out and spawns a
// replacement. If the replacement also times out, it escalates.
type PartnerTimeout struct{}

// NewPartnerTimeout returns a new rule instance.
func NewPartnerTimeout() *PartnerTimeout {
	return &PartnerTimeout{}
}

func (r *PartnerTimeout) Name() string {
	return "consensus.partner_timeout"
}

func (r *PartnerTimeout) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "consensus.partner.timeout"}
}

func (r *PartnerTimeout) Priority() int { return 80 }

func (r *PartnerTimeout) Rationale() string {
	return "Consensus partners that fail to respond within the timeout must be replaced or escalated."
}

// timeoutPayload is the expected structure inside a partner timeout event.
type timeoutPayload struct {
	PartnerID     string `json:"partner_id"`
	LoopID        string `json:"loop_id"`
	Role          string `json:"role"`
	IsReplacement bool   `json:"is_replacement"`
}

func (r *PartnerTimeout) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var tp timeoutPayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return false, fmt.Errorf("unmarshal timeout payload: %w", err)
	}

	// Check if the partner has already responded.
	nodes, err := l.Query(ctx, ledger.QueryFilter{CreatedBy: tp.PartnerID})
	if err != nil {
		return true, nil // on error, treat as timed out
	}

	for _, n := range nodes {
		// Any agree, dissent, or research node counts as a response.
		switch {
		case n.Type == "review.agree",
			n.Type == "review.dissent",
			n.Type == "research.result":
			return false, nil // partner responded
		}
	}

	return true, nil
}

func (r *PartnerTimeout) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var tp timeoutPayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return fmt.Errorf("unmarshal timeout payload: %w", err)
	}

	// Mark partner as timed-out.
	timedOutPayload, _ := json.Marshal(map[string]string{
		"partner_id": tp.PartnerID,
		"loop_id":    tp.LoopID,
		"status":     "timed_out",
	})
	if err := b.Publish(bus.Event{
		Type:      "consensus.partner.timed_out",
		Scope:     evt.Scope,
		Payload:   timedOutPayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish timed_out: %w", err)
	}

	if tp.IsReplacement {
		// Replacement also timed out -- escalate.
		escalatePayload, _ := json.Marshal(map[string]string{
			"loop_id":    tp.LoopID,
			"partner_id": tp.PartnerID,
			"role":       tp.Role,
			"reason":     "replacement partner also timed out",
		})
		return b.Publish(bus.Event{
			Type:      "supervisor.escalation.forwarded",
			Scope:     evt.Scope,
			Payload:   escalatePayload,
			CausalRef: evt.ID,
		})
	}

	// Spawn replacement.
	spawnPayload, _ := json.Marshal(map[string]any{
		"role":           tp.Role,
		"loop_id":        tp.LoopID,
		"is_replacement": true,
		"replaces":       tp.PartnerID,
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   spawnPayload,
		CausalRef: evt.ID,
	})
}
