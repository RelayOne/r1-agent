package sdm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
)

// DependencyCrossed detects when task dependencies span branches, which may
// indicate coordination risk.
type DependencyCrossed struct{}

// NewDependencyCrossed returns a new rule instance.
func NewDependencyCrossed() *DependencyCrossed {
	return &DependencyCrossed{}
}

func (r *DependencyCrossed) Name() string { return "sdm.dependency_crossed" }

func (r *DependencyCrossed) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

func (r *DependencyCrossed) Priority() int { return 50 }

func (r *DependencyCrossed) Rationale() string {
	return "Cross-branch task dependencies create implicit coupling that may cause integration failures."
}

// taskDAGPayload is the expected payload of a task DAG node addition event.
type taskDAGPayload struct {
	NodeID   string `json:"node_id"`
	NodeType string `json:"node_type"`
}

// taskDAGContent is the expected content of a task_dag node.
type taskDAGContent struct {
	TaskID       string   `json:"task_id"`
	BranchID     string   `json:"branch_id"`
	DependsOn    []string `json:"depends_on"` // task IDs
}

func (r *DependencyCrossed) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np taskDAGPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, nil
	}
	if np.NodeType != "task_dag" {
		return false, nil
	}
	if np.NodeID == "" {
		return false, nil
	}

	node, err := l.Get(ctx, np.NodeID)
	if err != nil {
		return false, nil
	}

	var tc taskDAGContent
	if err = json.Unmarshal(node.Content, &tc); err != nil {
		return false, nil
	}

	branchID := tc.BranchID
	if branchID == "" {
		branchID = evt.Scope.BranchID
	}
	if branchID == "" || len(tc.DependsOn) == 0 {
		return false, nil
	}

	// Check if any dependency belongs to a different branch.
	dagNodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "task_dag",
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, fmt.Errorf("query task_dag nodes: %w", err)
	}

	taskBranch := make(map[string]string)
	for _, n := range dagNodes {
		var dc taskDAGContent
		if err := json.Unmarshal(n.Content, &dc); err != nil {
			continue
		}
		if dc.TaskID != "" && dc.BranchID != "" {
			taskBranch[dc.TaskID] = dc.BranchID
		}
	}

	for _, dep := range tc.DependsOn {
		if depBranch, ok := taskBranch[dep]; ok && depBranch != branchID {
			return true, nil
		}
	}

	return false, nil
}

func (r *DependencyCrossed) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"advisory":   true,
		"type":       "dependency_crossed",
		"trigger_id": evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "sdm.dependency.crossed",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits sdm.dependency.crossed — SDM-specific shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *DependencyCrossed) PayloadSchema() *schemaval.Schema {
	return nil
}
