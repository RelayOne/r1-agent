// Package consensus implements supervisor rules that enforce multi-party
// agreement before accepting proposals and drafts.
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

// DraftRequiresReview spawns consensus partner stances when a draft node
// is added to the ledger, kicking off the review loop.
type DraftRequiresReview struct{}

// NewDraftRequiresReview returns a new rule instance.
func NewDraftRequiresReview() *DraftRequiresReview {
	return &DraftRequiresReview{}
}

func (r *DraftRequiresReview) Name() string {
	return "consensus.draft_requires_review"
}

func (r *DraftRequiresReview) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

func (r *DraftRequiresReview) Priority() int { return 90 }

func (r *DraftRequiresReview) Rationale() string {
	return "Every draft must be reviewed by consensus partners before it can be accepted."
}

// nodeAddedPayload is the expected structure inside a ledger.node.added event.
type nodeAddedPayload struct {
	NodeID   string `json:"node_id"`
	NodeType string `json:"node_type"`
	Status   string `json:"status"`
	LoopID   string `json:"loop_id"`
	Concern  string `json:"concern"`
}

func (r *DraftRequiresReview) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, fmt.Errorf("unmarshal node added payload: %w", err)
	}

	// Check if the node is a draft.
	isDraft := strings.Contains(np.NodeType, "draft") || np.Status == "draft"
	if !isDraft {
		return false, nil
	}

	return true, nil
}

func (r *DraftRequiresReview) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return fmt.Errorf("unmarshal node added payload: %w", err)
	}

	roles := []string{"Reviewer", "LeadEngineer"}

	for _, role := range roles {
		spawnPayload, _ := json.Marshal(map[string]any{
			"role":    role,
			"node_id": np.NodeID,
			"loop_id": np.LoopID,
			"concern": np.Concern,
		})
		if err := b.Publish(bus.Event{
			Type:      "supervisor.spawn.requested",
			Scope:     evt.Scope,
			Payload:   spawnPayload,
			CausalRef: evt.ID,
		}); err != nil {
			return fmt.Errorf("publish spawn %s: %w", role, err)
		}
	}

	return nil
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: supervisor.spawn.requested — reviewer dispatch.
func (r *DraftRequiresReview) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
