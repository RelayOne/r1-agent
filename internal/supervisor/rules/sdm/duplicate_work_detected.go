package sdm

import (
	"context"
	"encoding/json"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
)

// DuplicateWorkDetected checks for overlapping work across branches by
// comparing file paths, function names, and acceptance criteria.
type DuplicateWorkDetected struct{}

// NewDuplicateWorkDetected returns a new rule instance.
func NewDuplicateWorkDetected() *DuplicateWorkDetected {
	return &DuplicateWorkDetected{}
}

func (r *DuplicateWorkDetected) Name() string { return "sdm.duplicate_work_detected" }

func (r *DuplicateWorkDetected) Pattern() bus.Pattern {
	// Match both worker.action.proposed and ledger.node.added.
	return bus.Pattern{}
}

func (r *DuplicateWorkDetected) Priority() int { return 50 }

func (r *DuplicateWorkDetected) Rationale() string {
	return "Duplicate work across branches wastes budget and creates merge conflicts."
}

// workDescriptor captures fields used for overlap detection.
type workDescriptor struct {
	BranchID      string   `json:"branch_id"`
	FilePaths     []string `json:"file_paths"`
	FunctionNames []string `json:"function_names"`
	Criteria      []string `json:"acceptance_criteria"`
}

func (r *DuplicateWorkDetected) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	switch bus.EventType(evt.Type) {
	case "worker.action.proposed":
		return r.evaluateAction(ctx, evt, l)
	case bus.EvtLedgerNodeAdded:
		return r.evaluateNode(ctx, evt, l)
	default:
		return false, nil
	}
}

func (r *DuplicateWorkDetected) evaluateAction(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
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

	// Query active work records.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "active_work",
		MissionID: evt.Scope.MissionID,
		Limit:     100,
	})
	if err != nil {
		return false, nil
	}

	targetFiles := make(map[string]bool, len(ap.FilePaths))
	for _, f := range ap.FilePaths {
		targetFiles[f] = true
	}

	for _, n := range nodes {
		var wd workDescriptor
		if err := json.Unmarshal(n.Content, &wd); err != nil {
			continue
		}
		if wd.BranchID == branchID {
			continue
		}
		for _, f := range wd.FilePaths {
			if targetFiles[f] {
				return true, nil
			}
		}
	}

	return false, nil
}

func (r *DuplicateWorkDetected) evaluateNode(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np struct {
		NodeID   string `json:"node_id"`
		NodeType string `json:"node_type"`
	}
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, nil
	}
	if np.NodeType != "task" {
		return false, nil
	}
	if np.NodeID == "" {
		return false, nil
	}

	node, err := l.Get(ctx, np.NodeID)
	if err != nil {
		return false, nil
	}

	var wd workDescriptor
	if err := json.Unmarshal(node.Content, &wd); err != nil {
		return false, nil
	}

	branchID := wd.BranchID
	if branchID == "" {
		branchID = evt.Scope.BranchID
	}
	if branchID == "" {
		return false, nil
	}

	// Check for overlap with other tasks.
	tasks, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "task",
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, nil
	}

	for _, t := range tasks {
		if t.ID == np.NodeID {
			continue
		}
		var other workDescriptor
		if err := json.Unmarshal(t.Content, &other); err != nil {
			continue
		}
		if other.BranchID == branchID {
			continue
		}
		if hasOverlap(wd.FilePaths, other.FilePaths) || hasOverlap(wd.FunctionNames, other.FunctionNames) {
			return true, nil
		}
	}

	return false, nil
}

// hasOverlap returns true if any element appears in both slices.
func hasOverlap(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		if set[v] {
			return true
		}
	}
	return false
}

func (r *DuplicateWorkDetected) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"advisory":   true,
		"type":       "duplicate_work",
		"trigger_id": evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "sdm.duplicate_work.detected",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits sdm.duplicate_work.detected — SDM-specific shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *DuplicateWorkDetected) PayloadSchema() *schemaval.Schema {
	return nil
}
