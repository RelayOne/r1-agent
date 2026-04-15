package sdm

import (
	"context"
	"encoding/json"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
)

// DriftCrossBranch detects when interface, schema, or contract definitions
// diverge across branches by examining shared boundary files and contract nodes.
type DriftCrossBranch struct{}

// NewDriftCrossBranch returns a new rule instance.
func NewDriftCrossBranch() *DriftCrossBranch {
	return &DriftCrossBranch{}
}

func (r *DriftCrossBranch) Name() string { return "sdm.drift_cross_branch" }

func (r *DriftCrossBranch) Pattern() bus.Pattern {
	// Match both ledger.node.added and worker.action.proposed.
	return bus.Pattern{}
}

func (r *DriftCrossBranch) Priority() int { return 50 }

func (r *DriftCrossBranch) Rationale() string {
	return "Diverging interfaces or contracts across branches cause integration failures at merge time."
}

// boundaryNodeTypes are the node types that represent shared boundaries.
var boundaryNodeTypes = map[string]bool{
	"interface":  true,
	"schema":     true,
	"contract":   true,
	"api_spec":   true,
	"type_def":   true,
}

func (r *DriftCrossBranch) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	switch bus.EventType(evt.Type) {
	case bus.EvtLedgerNodeAdded:
		return r.evaluateNodeAdded(ctx, evt, l)
	case "worker.action.proposed":
		return r.evaluateActionProposed(ctx, evt, l)
	default:
		return false, nil
	}
}

func (r *DriftCrossBranch) evaluateNodeAdded(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np struct {
		NodeID   string `json:"node_id"`
		NodeType string `json:"node_type"`
	}
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, nil
	}
	if !boundaryNodeTypes[np.NodeType] {
		return false, nil
	}

	branchID := evt.Scope.BranchID
	if branchID == "" {
		return false, nil
	}

	// Check if same type of boundary node exists in another branch.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      np.NodeType,
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, nil
	}

	for _, n := range nodes {
		if n.ID == np.NodeID {
			continue
		}
		var nc struct {
			BranchID string `json:"branch_id"`
			Name     string `json:"name"`
		}
		if err := json.Unmarshal(n.Content, &nc); err != nil {
			continue
		}
		if nc.BranchID != "" && nc.BranchID != branchID {
			// Get the new node to compare names.
			if np.NodeID != "" {
				newNode, err := l.Get(ctx, np.NodeID)
				if err != nil {
					continue
				}
				var newNC struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(newNode.Content, &newNC); err != nil {
					continue
				}
				if newNC.Name == nc.Name {
					return true, nil // same interface name, different branch
				}
			}
		}
	}
	return false, nil
}

func (r *DriftCrossBranch) evaluateActionProposed(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ap actionProposedPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return false, nil
	}
	if len(ap.FilePaths) == 0 {
		return false, nil
	}

	branchID := ap.BranchID
	if branchID == "" {
		branchID = evt.Scope.BranchID
	}
	if branchID == "" {
		return false, nil
	}

	// Check if any target file is a known boundary file modified in another branch.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "boundary_file",
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, nil
	}

	targetFiles := make(map[string]bool, len(ap.FilePaths))
	for _, f := range ap.FilePaths {
		targetFiles[f] = true
	}

	for _, n := range nodes {
		var bf struct {
			BranchID string `json:"branch_id"`
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(n.Content, &bf); err != nil {
			continue
		}
		if bf.BranchID != branchID && targetFiles[bf.FilePath] {
			return true, nil
		}
	}

	return false, nil
}

func (r *DriftCrossBranch) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"advisory":   true,
		"type":       "cross_branch_drift",
		"trigger_id": evt.ID,
		"branch_id":  evt.Scope.BranchID,
	})
	return b.Publish(bus.Event{
		Type:      "sdm.cross_branch_drift.detected",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits sdm.cross_branch_drift.detected — SDM-specific shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *DriftCrossBranch) PayloadSchema() *schemaval.Schema {
	return nil
}
