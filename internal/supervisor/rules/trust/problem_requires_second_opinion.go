package trust

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
	"github.com/RelayOne/r1/internal/supervisor"
)

// ProblemRequiresSecondOpinion handles escalation requests of type
// "task.infeasible" or "task.blocked" by pausing the worker and spawning
// a Reviewer to independently evaluate whether the escalation is justified.
type ProblemRequiresSecondOpinion struct{}

// NewProblemRequiresSecondOpinion returns a new rule instance.
func NewProblemRequiresSecondOpinion() *ProblemRequiresSecondOpinion {
	return &ProblemRequiresSecondOpinion{}
}

func (r *ProblemRequiresSecondOpinion) Name() string {
	return "trust.problem_requires_second_opinion"
}

func (r *ProblemRequiresSecondOpinion) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.escalation.requested"}
}

func (r *ProblemRequiresSecondOpinion) Priority() int { return 100 }

func (r *ProblemRequiresSecondOpinion) Rationale() string {
	return "Escalations claiming a task is infeasible or blocked must be independently verified."
}

// escalationPayload is the expected structure inside an escalation event.
type escalationPayload struct {
	WorkerID       string `json:"worker_id"`
	TaskID         string `json:"task_id"`
	EscalationType string `json:"escalation_type"`
	Reason         string `json:"reason"`
}

func (r *ProblemRequiresSecondOpinion) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ep escalationPayload
	if err := json.Unmarshal(evt.Payload, &ep); err != nil {
		return false, fmt.Errorf("unmarshal escalation payload: %w", err)
	}

	// Only handle task.infeasible and task.blocked escalation types.
	if ep.EscalationType != "task.infeasible" && ep.EscalationType != "task.blocked" {
		return false, nil
	}

	taskID := ep.TaskID
	if taskID == "" {
		taskID = evt.Scope.TaskID
	}

	// Check if a reviewer has already agreed the escalation is
	// justified. On ledger error, be conservative and require review.
	nodes, _ := l.Query(ctx, ledger.QueryFilter{Type: "escalation.agree"})

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

func (r *ProblemRequiresSecondOpinion) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var ep escalationPayload
	if err := json.Unmarshal(evt.Payload, &ep); err != nil {
		return fmt.Errorf("unmarshal escalation payload: %w", err)
	}

	workerID := ep.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}

	pauseMap := map[string]any{
		"worker_id": workerID,
		"reason":    "awaiting_escalation_review",
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
		"role":            "Reviewer",
		"task_id":         ep.TaskID,
		"escalation_type": ep.EscalationType,
		"reason":          ep.Reason,
		"worker_id":       workerID,
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

// PayloadSchema declares the worker.paused shape. Closes A3.
func (r *ProblemRequiresSecondOpinion) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
