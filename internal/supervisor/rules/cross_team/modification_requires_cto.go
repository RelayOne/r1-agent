// Package crossteam implements supervisor rules that enforce cross-branch
// coordination when workers propose changes to shared files.
package crossteam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// ModificationRequiresCTO pauses workers that propose changes to files
// flagged as cross-branch by the SDM (Software Development Manager) and
// convenes a consensus loop with the CTO, affected Lead Engineer, and
// the proposing worker.
type ModificationRequiresCTO struct{}

// NewModificationRequiresCTO returns a new rule instance.
func NewModificationRequiresCTO() *ModificationRequiresCTO {
	return &ModificationRequiresCTO{}
}

func (r *ModificationRequiresCTO) Name() string {
	return "cross_team.modification_requires_cto"
}

func (r *ModificationRequiresCTO) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.action.proposed"}
}

func (r *ModificationRequiresCTO) Priority() int { return 85 }

func (r *ModificationRequiresCTO) Rationale() string {
	return "Cross-branch file modifications require CTO and affected Lead Engineer consensus to prevent conflicts."
}

// crossTeamActionPayload is the expected structure inside a worker action proposed event.
type crossTeamActionPayload struct {
	WorkerID   string   `json:"worker_id"`
	ActionType string   `json:"action_type"`
	FilePaths  []string `json:"file_paths"`
}

// sdmFlagContent represents an SDM cross-branch flag in the ledger.
type sdmFlagContent struct {
	FilePath       string `json:"file_path"`
	IsCrossBranch  bool   `json:"is_cross_branch"`
	AffectedBranch string `json:"affected_branch"`
	LeadEngineer   string `json:"lead_engineer"`
}

func (r *ModificationRequiresCTO) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ap crossTeamActionPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return false, fmt.Errorf("unmarshal action payload: %w", err)
	}

	// Check if any target file has been flagged cross-branch by SDM.
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "sdm.cross_branch_flag"})
	if err != nil {
		return false, fmt.Errorf("query SDM flags: %w", err)
	}

	pathSet := make(map[string]bool, len(ap.FilePaths))
	for _, p := range ap.FilePaths {
		pathSet[p] = true
	}

	for _, n := range nodes {
		var sf sdmFlagContent
		if err := json.Unmarshal(n.Content, &sf); err != nil {
			continue
		}
		if sf.IsCrossBranch && pathSet[sf.FilePath] {
			return true, nil
		}
	}

	return false, nil
}

func (r *ModificationRequiresCTO) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var ap crossTeamActionPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return fmt.Errorf("unmarshal action payload: %w", err)
	}

	workerID := ap.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}

	// Pause the worker.
	pauseMap := map[string]any{
		"worker_id": workerID,
		"reason":    "awaiting_cross_team_consensus",
	}
	if vErr := supervisor.ValidatePayload(r, pauseMap); vErr != nil {
		return fmt.Errorf("payload schema violation on worker.paused: %w", vErr)
	}
	pausePayload, _ := json.Marshal(pauseMap)
	if err := b.Publish(bus.Event{
		Type:      bus.EvtWorkerPaused,
		Scope:     evt.Scope,
		Payload:   pausePayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish pause: %w", err)
	}

	// Convene consensus loop with CTO and affected Lead Engineer.
	consensusPayload, _ := json.Marshal(map[string]any{
		"roles":      []string{"CTO", "LeadEngineer"},
		"file_paths": ap.FilePaths,
		"worker_id":  workerID,
		"reason":     "cross-branch file modification",
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   consensusPayload,
		CausalRef: evt.ID,
	})
}

// PayloadSchema declares the worker.paused shape. Closes A3.
func (r *ModificationRequiresCTO) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
