package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// EscalationForwardsUpward forwards escalations from branch to mission
// supervisor when branch-level resolution has failed.
type EscalationForwardsUpward struct{}

// NewEscalationForwardsUpward returns a new rule instance.
func NewEscalationForwardsUpward() *EscalationForwardsUpward {
	return &EscalationForwardsUpward{}
}

func (r *EscalationForwardsUpward) Name() string {
	return "hierarchy.escalation_forwards_upward"
}

func (r *EscalationForwardsUpward) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.escalation.requested"}
}

func (r *EscalationForwardsUpward) Priority() int { return 95 }

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

// PayloadSchema declares the supervisor.spawn.requested shape for
// this rule's primary emitted event (lenient default — most fields
// optional). Closes A3 for this rule.
func (r *EscalationForwardsUpward) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
