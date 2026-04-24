package trust

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// FixRequiresSecondOpinion pauses a worker that declares a fix complete and
// spawns a Reviewer to verify the fix addresses the prior dissent.
type FixRequiresSecondOpinion struct{}

// NewFixRequiresSecondOpinion returns a new rule instance.
func NewFixRequiresSecondOpinion() *FixRequiresSecondOpinion {
	return &FixRequiresSecondOpinion{}
}

func (r *FixRequiresSecondOpinion) Name() string {
	return "trust.fix_requires_second_opinion"
}

func (r *FixRequiresSecondOpinion) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.fix.completed"}
}

func (r *FixRequiresSecondOpinion) Priority() int { return 100 }

func (r *FixRequiresSecondOpinion) Rationale() string {
	return "A fix for a dissent must be independently verified before acceptance."
}

// fixPayload is the expected structure inside a fix completed event.
type fixPayload struct {
	WorkerID   string `json:"worker_id"`
	TaskID     string `json:"task_id"`
	ArtifactID string `json:"artifact_id"`
	DissentID  string `json:"dissent_id"`
}

func (r *FixRequiresSecondOpinion) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var fp fixPayload
	if err := json.Unmarshal(evt.Payload, &fp); err != nil {
		return false, fmt.Errorf("unmarshal fix payload: %w", err)
	}

	// Check if a reviewer has already verified this fix. On
	// ledger error, be conservative and require review.
	nodes, _ := l.Query(ctx, ledger.QueryFilter{Type: "review.agree"})

	taskID := fp.TaskID
	if taskID == "" {
		taskID = evt.Scope.TaskID
	}

	for _, n := range nodes {
		if n.CreatedBy == evt.EmitterID {
			continue
		}
		var ac agreeContent
		if err := json.Unmarshal(n.Content, &ac); err != nil {
			continue
		}
		if ac.TaskID == taskID {
			return false, nil
		}
	}

	return true, nil
}

func (r *FixRequiresSecondOpinion) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var fp fixPayload
	if err := json.Unmarshal(evt.Payload, &fp); err != nil {
		return fmt.Errorf("unmarshal fix payload: %w", err)
	}

	workerID := fp.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}

	pauseMap := map[string]any{
		"worker_id": workerID,
		"reason":    "awaiting_fix_review",
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

	spawnPayload, _ := json.Marshal(map[string]any{
		"role":        "Reviewer",
		"artifact_id": fp.ArtifactID,
		"task_id":     fp.TaskID,
		"dissent_id":  fp.DissentID,
		"worker_id":   workerID,
	})
	if err := b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   spawnPayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish spawn: %w", err)
	}

	return nil
}

// PayloadSchema declares the worker.paused shape for this rule's
// primary emitted event. Closes A3 for this rule.
func (r *FixRequiresSecondOpinion) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
