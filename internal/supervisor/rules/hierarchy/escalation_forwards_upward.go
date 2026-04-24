package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
	"github.com/RelayOne/r1/internal/supervisor"
)

// EscalationForwardsUpward forwards escalations from branch to mission
// supervisor when branch-level resolution has failed.
type EscalationForwardsUpward struct{}

// NewEscalationForwardsUpward returns a new rule instance.
func NewEscalationForwardsUpward() *EscalationForwardsUpward {
	return &EscalationForwardsUpward{}
}

// Name returns the stable rule identifier used by the supervisor
// registry and audit logs.
func (r *EscalationForwardsUpward) Name() string {
	return "hierarchy.escalation_forwards_upward"
}

// Pattern subscribes to worker-originated escalation requests so the
// rule can decide whether to punt them upward.
func (r *EscalationForwardsUpward) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.escalation.requested"}
}

// Priority (95) runs this rule slightly below the parent-agreement
// rule (100) but above generic hierarchy bookkeeping.
func (r *EscalationForwardsUpward) Priority() int { return 95 }

// Rationale is the human-readable justification surfaced in audit.
func (r *EscalationForwardsUpward) Rationale() string {
	return "Unresolved branch-level escalations must be forwarded to the mission supervisor."
}

// escalationForwardPayload is the expected structure inside an escalation event.
type escalationForwardPayload struct {
	WorkerID       string `json:"worker_id"`
	TaskID         string `json:"task_id"`
	EscalationType string `json:"escalation_type"`
	Reason         string `json:"reason"`
	BranchResolved bool   `json:"branch_resolved"`
}

// Evaluate fires only when branch-level resolution has been attempted
// and failed. Short-circuits on BranchResolved=true; otherwise looks
// for a matching escalation.resolution_attempt ledger node with
// success=false.
func (r *EscalationForwardsUpward) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ep escalationForwardPayload
	if err := json.Unmarshal(evt.Payload, &ep); err != nil {
		return false, fmt.Errorf("unmarshal escalation payload: %w", err)
	}

	// Only fire when branch-level resolution has already been attempted and failed.
	// Check for resolution attempt nodes in the ledger.
	if ep.BranchResolved {
		return false, nil
	}

	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "escalation.resolution_attempt"})
	if err != nil {
		return false, fmt.Errorf("query resolution attempts: %w", err)
	}

	taskID := ep.TaskID
	if taskID == "" {
		taskID = evt.Scope.TaskID
	}

	// If we find a failed resolution attempt for this task, forward upward.
	for _, n := range nodes {
		var ra struct {
			TaskID  string `json:"task_id"`
			Success bool   `json:"success"`
		}
		if err := json.Unmarshal(n.Content, &ra); err != nil {
			continue
		}
		if ra.TaskID == taskID && !ra.Success {
			return true, nil
		}
	}

	return false, nil
}

// Action emits a supervisor.escalation.forwarded event scoped to the
// parent mission (branch stripped from Scope) carrying the original
// escalation metadata plus the source branch ID for provenance.
func (r *EscalationForwardsUpward) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var ep escalationForwardPayload
	if err := json.Unmarshal(evt.Payload, &ep); err != nil {
		return fmt.Errorf("unmarshal escalation payload: %w", err)
	}

	forwardPayload, _ := json.Marshal(map[string]any{
		"worker_id":       ep.WorkerID,
		"task_id":         ep.TaskID,
		"escalation_type": ep.EscalationType,
		"reason":          ep.Reason,
		"source_branch":   evt.Scope.BranchID,
		"level":           "mission",
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.escalation.forwarded",
		Scope:     bus.Scope{MissionID: evt.Scope.MissionID},
		Payload:   forwardPayload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: supervisor.escalation.forwarded.
func (r *EscalationForwardsUpward) PayloadSchema() *schemaval.Schema {
	return supervisor.EscalationForwardedSchema()
}
