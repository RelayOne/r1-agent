// Package trust implements supervisor rules that enforce second-opinion
// verification before accepting worker declarations.
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

// CompletionRequiresSecondOpinion pauses a worker that declares "done" and
// spawns a fresh-context Reviewer to independently verify the work product
// before the declaration is accepted.
type CompletionRequiresSecondOpinion struct{}

// NewCompletionRequiresSecondOpinion returns a new rule instance.
func NewCompletionRequiresSecondOpinion() *CompletionRequiresSecondOpinion {
	return &CompletionRequiresSecondOpinion{}
}

func (r *CompletionRequiresSecondOpinion) Name() string {
	return "trust.completion_requires_second_opinion"
}

func (r *CompletionRequiresSecondOpinion) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtWorkerDeclarationDone)}
}

func (r *CompletionRequiresSecondOpinion) Priority() int { return 100 }

func (r *CompletionRequiresSecondOpinion) Rationale() string {
	return "No worker may unilaterally declare completion; an independent Reviewer must agree."
}

// declarationPayload is the expected structure inside a declaration event.
type declarationPayload struct {
	WorkerID   string `json:"worker_id"`
	TaskID     string `json:"task_id"`
	ArtifactID string `json:"artifact_id"`
}

// agreeContent is the expected structure inside a reviewer agree node.
type agreeContent struct {
	TaskID string `json:"task_id"`
}

func (r *CompletionRequiresSecondOpinion) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var dp declarationPayload
	if err := json.Unmarshal(evt.Payload, &dp); err != nil {
		return false, fmt.Errorf("unmarshal declaration payload: %w", err)
	}

	taskID := dp.TaskID
	if taskID == "" {
		taskID = evt.Scope.TaskID
	}
	if taskID == "" {
		return true, nil // no task info, require review to be safe
	}

	// Look for agree nodes from a different worker.
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "review.agree"})
	if err != nil {
		return true, nil // on error, be conservative and require review
	}

	for _, n := range nodes {
		if n.CreatedBy == evt.EmitterID {
			continue // same worker, doesn't count
		}
		var ac agreeContent
		if err := json.Unmarshal(n.Content, &ac); err != nil {
			continue
		}
		if ac.TaskID == taskID {
			return false, nil // already have a second opinion
		}
	}

	return true, nil
}

func (r *CompletionRequiresSecondOpinion) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var dp declarationPayload
	if err := json.Unmarshal(evt.Payload, &dp); err != nil {
		return fmt.Errorf("unmarshal declaration payload: %w", err)
	}

	workerID := dp.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}

	// Pause the declaring worker.
	pauseMap := map[string]any{
		"worker_id": workerID,
		"reason":    "awaiting_review",
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

	// Spawn a Reviewer.
	spawnPayload, _ := json.Marshal(map[string]any{
		"role":        "Reviewer",
		"artifact_id": dp.ArtifactID,
		"task_id":     dp.TaskID,
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

// PayloadSchema declares the schema for this rule's primary emitted
// event (worker.paused). Closes anti-deception matrix row A3 for
// this rule — the payload is validated at dispatch against a
// known-good shape instead of silently failing at replay if a
// required field goes missing.
func (r *CompletionRequiresSecondOpinion) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
