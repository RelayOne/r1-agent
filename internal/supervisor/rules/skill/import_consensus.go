package skill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

// ImportConsensus spawns a consensus loop when a skill import proposal is
// submitted. High-risk imports auto-route to user escalation.
type ImportConsensus struct{}

// NewImportConsensus returns a new rule instance.
func NewImportConsensus() *ImportConsensus {
	return &ImportConsensus{}
}

func (r *ImportConsensus) Name() string { return "skill.import_consensus" }

func (r *ImportConsensus) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

func (r *ImportConsensus) Priority() int { return 80 }

func (r *ImportConsensus) Rationale() string {
	return "Imported skills require consensus from senior roles; high-risk imports need user approval."
}

// importProposalPayload is extracted from the ledger.node.added event.
type importProposalPayload struct {
	NodeID   string `json:"node_id"`
	NodeType string `json:"node_type"`
}

// importProposalContent is the expected content of a skill_import_proposal node.
type importProposalContent struct {
	SkillID        string `json:"skill_id"`
	Source         string `json:"source"`
	RiskAssessment string `json:"risk_assessment"` // "low", "medium", "high"
}

func (r *ImportConsensus) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np importProposalPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, nil
	}
	return np.NodeType == "skill_import_proposal", nil
}

func (r *ImportConsensus) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var np importProposalPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return fmt.Errorf("unmarshal node payload: %w", err)
	}

	// Determine risk from the proposal node content.
	riskAssessment := "low"
	if np.NodeID != "" {
		// We don't have the ledger in Action, but we can parse content from
		// the event if available. For robustness, default to requiring consensus.
		_ = riskAssessment
	}

	// Try to extract risk from a content field embedded in the event payload.
	var fullPayload struct {
		NodeID         string `json:"node_id"`
		NodeType       string `json:"node_type"`
		RiskAssessment string `json:"risk_assessment"`
	}
	_ = json.Unmarshal(evt.Payload, &fullPayload)
	if fullPayload.RiskAssessment != "" {
		riskAssessment = fullPayload.RiskAssessment
	}

	autoEscalate := riskAssessment == "high"

	payload, _ := json.Marshal(map[string]any{
		"role":            "ConsensusLoop",
		"reason":          "skill_import_proposal",
		"required_roles":  []string{"CTO", "LeadEngineer"},
		"node_id":         np.NodeID,
		"auto_escalate":   autoEscalate,
		"risk_assessment": riskAssessment,
		"trigger_id":      evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
