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

// DefaultThresholds maps artifact types to their maximum iteration counts.
var DefaultThresholds = map[string]int{
	"prd":      5,
	"pr":       3,
	"refactor": 2,
}

// IterationThreshold fires when a draft has been superseded too many times,
// spawning a Judge stance to break the deadlock.
type IterationThreshold struct {
	Thresholds map[string]int
}

// NewIterationThreshold returns a new rule with default thresholds.
func NewIterationThreshold() *IterationThreshold {
	thresholds := make(map[string]int, len(DefaultThresholds))
	for k, v := range DefaultThresholds {
		thresholds[k] = v
	}
	return &IterationThreshold{Thresholds: thresholds}
}

// Name returns the stable rule identifier used by the supervisor
// registry and audit logs.
func (r *IterationThreshold) Name() string {
	return "consensus.iteration_threshold"
}

// Pattern subscribes to ledger-node-added events; each draft is
// walked backward through supersedes edges to count revisions.
func (r *IterationThreshold) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

// Priority (85) runs this rule after per-event rules like
// DraftRequiresReview so Judge escalation only fires once reviewers
// exist to deadlock.
func (r *IterationThreshold) Priority() int { return 85 }

// Rationale is the human-readable justification surfaced in audit.
func (r *IterationThreshold) Rationale() string {
	return "Too many draft revisions indicate a deadlocked loop; a Judge must intervene."
}

// Evaluate returns true when the added draft's supersedes chain is at
// or above the per-artifact-type threshold (see DefaultThresholds).
// Non-draft nodes and unidentifiable nodes are skipped.
func (r *IterationThreshold) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, fmt.Errorf("unmarshal node added payload: %w", err)
	}

	if !strings.Contains(np.NodeType, "draft") && np.Status != "draft" {
		return false, nil
	}

	if np.NodeID == "" {
		return false, nil
	}

	// Walk supersedes edges backward to count predecessors.
	predecessors, err := l.Walk(ctx, np.NodeID, ledger.Backward, []ledger.EdgeType{ledger.EdgeSupersedes})
	if err != nil {
		return false, fmt.Errorf("walk supersedes: %w", err)
	}

	// Subtract 1 for the node itself.
	count := len(predecessors)
	if count > 0 {
		count--
	}

	// Find the applicable threshold.
	threshold := 3 // default
	for prefix, t := range r.Thresholds {
		if strings.Contains(np.NodeType, prefix) {
			threshold = t
			break
		}
	}

	return count >= threshold, nil
}

// Action publishes a supervisor.spawn.requested event for a Judge
// role with the triggering loop and node IDs; Judge resolution breaks
// the deadlock.
func (r *IterationThreshold) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return fmt.Errorf("unmarshal node added payload: %w", err)
	}

	loopID := np.LoopID
	if loopID == "" {
		loopID = evt.Scope.LoopID
	}

	spawnPayload, _ := json.Marshal(map[string]any{
		"role":    "Judge",
		"loop_id": loopID,
		"node_id": np.NodeID,
		"reason":  "iteration threshold exceeded",
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   spawnPayload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: supervisor.spawn.requested — iteration trigger.
func (r *IterationThreshold) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
