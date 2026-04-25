// Package sdm implements Stance Detection Manager rules. All SDM rules are
// detection-only: they emit advisory events but NEVER pause workers, spawn
// stances, or transition state.
package sdm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
	"github.com/RelayOne/r1-agent/internal/schemaval"
)

// CollisionFileModification detects when a proposed file modification targets
// a file that was recently modified in another active branch.
type CollisionFileModification struct{}

// NewCollisionFileModification returns a new rule instance.
func NewCollisionFileModification() *CollisionFileModification {
	return &CollisionFileModification{}
}

func (r *CollisionFileModification) Name() string { return "sdm.collision_file_modification" }

func (r *CollisionFileModification) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.action.proposed"}
}

func (r *CollisionFileModification) Priority() int { return 50 }

func (r *CollisionFileModification) Rationale() string {
	return "File-level collisions across branches risk merge conflicts; early detection allows proactive coordination."
}

// actionProposedPayload is the expected shape of worker.action.proposed events.
type actionProposedPayload struct {
	WorkerID  string   `json:"worker_id"`
	BranchID  string   `json:"branch_id"`
	FilePaths []string `json:"file_paths"`
	Action    string   `json:"action"` // "modify", "create", "delete"
}

func (r *CollisionFileModification) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ap actionProposedPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return false, fmt.Errorf("unmarshal action proposed: %w", err)
	}

	if len(ap.FilePaths) == 0 {
		return false, nil
	}

	branchID := ap.BranchID
	if branchID == "" {
		branchID = evt.Scope.BranchID
	}

	// Query for recent file modification records. Ledger read
	// errors here mean "no known modifications"; we skip rather
	// than fire a false-positive collision.
	nodes, _ := l.Query(ctx, ledger.QueryFilter{
		Type:      "file.modification",
		MissionID: evt.Scope.MissionID,
		Limit:     100,
	})

	targetFiles := make(map[string]bool, len(ap.FilePaths))
	for _, fp := range ap.FilePaths {
		targetFiles[fp] = true
	}

	for _, n := range nodes {
		var fm struct {
			BranchID string   `json:"branch_id"`
			Files    []string `json:"files"`
		}
		if err := json.Unmarshal(n.Content, &fm); err != nil {
			continue
		}
		if fm.BranchID == branchID {
			continue // same branch, not a collision
		}
		for _, f := range fm.Files {
			if targetFiles[f] {
				return true, nil
			}
		}
	}

	return false, nil
}

func (r *CollisionFileModification) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var ap actionProposedPayload
	_ = json.Unmarshal(evt.Payload, &ap)

	payload, _ := json.Marshal(map[string]any{
		"advisory":   true,
		"type":       "file_collision",
		"branch_id":  ap.BranchID,
		"file_paths": ap.FilePaths,
		"trigger_id": evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "sdm.collision.detected",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits sdm.collision.detected — SDM-specific shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *CollisionFileModification) PayloadSchema() *schemaval.Schema {
	return nil
}
