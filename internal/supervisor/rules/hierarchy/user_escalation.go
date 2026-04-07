package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

// OperatingMode controls how user escalation behaves.
type OperatingMode string

const (
	ModeInteractive OperatingMode = "interactive"
	ModeFullAuto    OperatingMode = "full_auto"
)

// UserEscalation handles escalations that reach the mission level.
// In interactive mode it produces a user-facing message via PO and pauses.
// In full-auto mode it spawns a Stakeholder stance.
type UserEscalation struct {
	Mode OperatingMode
}

// NewUserEscalation returns a new rule with interactive mode as default.
func NewUserEscalation() *UserEscalation {
	return &UserEscalation{Mode: ModeInteractive}
}

func (r *UserEscalation) Name() string {
	return "hierarchy.user_escalation"
}

func (r *UserEscalation) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "supervisor.escalation.forwarded"}
}

func (r *UserEscalation) Priority() int { return 100 }

func (r *UserEscalation) Rationale() string {
	return "Mission-level escalations that cannot be auto-resolved must reach the user or a Stakeholder."
}

// forwardedEscalationPayload is the expected structure inside a forwarded escalation event.
type forwardedEscalationPayload struct {
	WorkerID       string `json:"worker_id"`
	TaskID         string `json:"task_id"`
	EscalationType string `json:"escalation_type"`
	Reason         string `json:"reason"`
	SourceBranch   string `json:"source_branch"`
	Level          string `json:"level"`
}

func (r *UserEscalation) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ep forwardedEscalationPayload
	if err := json.Unmarshal(evt.Payload, &ep); err != nil {
		return false, fmt.Errorf("unmarshal forwarded escalation payload: %w", err)
	}

	// Check if mission-level resolution already exists.
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "escalation.mission_resolution"})
	if err != nil {
		return true, nil
	}

	taskID := ep.TaskID
	if taskID == "" {
		taskID = evt.Scope.TaskID
	}

	for _, n := range nodes {
		var mr struct {
			TaskID   string `json:"task_id"`
			Resolved bool   `json:"resolved"`
		}
		if err := json.Unmarshal(n.Content, &mr); err != nil {
			continue
		}
		if mr.TaskID == taskID && mr.Resolved {
			return false, nil
		}
	}

	return true, nil
}

func (r *UserEscalation) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var ep forwardedEscalationPayload
	if err := json.Unmarshal(evt.Payload, &ep); err != nil {
		return fmt.Errorf("unmarshal forwarded escalation payload: %w", err)
	}

	switch r.Mode {
	case ModeInteractive:
		// Produce a user-facing message via PO and pause all work.
		messagePayload, _ := json.Marshal(map[string]any{
			"role":            "PO",
			"task_id":         ep.TaskID,
			"escalation_type": ep.EscalationType,
			"reason":          ep.Reason,
			"source_branch":   ep.SourceBranch,
			"action":          "user_message",
			"message":         fmt.Sprintf("Escalation requires human decision: %s - %s", ep.EscalationType, ep.Reason),
		})
		if err := b.Publish(bus.Event{
			Type:      "supervisor.user.message",
			Scope:     evt.Scope,
			Payload:   messagePayload,
			CausalRef: evt.ID,
		}); err != nil {
			return fmt.Errorf("publish user message: %w", err)
		}

		// Pause work.
		pausePayload, _ := json.Marshal(map[string]string{
			"reason": "awaiting_user_decision",
		})
		return b.Publish(bus.Event{
			Type:      bus.EvtWorkerPaused,
			Scope:     evt.Scope,
			Payload:   pausePayload,
			CausalRef: evt.ID,
		})

	case ModeFullAuto:
		// Spawn a Stakeholder stance to make the decision.
		spawnPayload, _ := json.Marshal(map[string]any{
			"role":            "Stakeholder",
			"task_id":         ep.TaskID,
			"escalation_type": ep.EscalationType,
			"reason":          ep.Reason,
			"source_branch":   ep.SourceBranch,
		})
		return b.Publish(bus.Event{
			Type:      "supervisor.spawn.requested",
			Scope:     evt.Scope,
			Payload:   spawnPayload,
			CausalRef: evt.ID,
		})

	default:
		return fmt.Errorf("unknown operating mode: %s", r.Mode)
	}
}
