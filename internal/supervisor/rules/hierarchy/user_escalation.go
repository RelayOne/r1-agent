package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
	"github.com/RelayOne/r1/internal/supervisor"
)

// OperatingMode controls how user escalation behaves.
type OperatingMode string

// Operating modes switch between human-in-the-loop and fully
// autonomous handling of mission-level escalations.
const (
	// ModeInteractive pauses work and surfaces a message through the
	// PO stance so a human operator decides next.
	ModeInteractive OperatingMode = "interactive"
	// ModeFullAuto spawns a Stakeholder stance to make the decision
	// without human involvement.
	ModeFullAuto OperatingMode = "full_auto"
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

// Name returns the stable rule identifier used by the supervisor
// registry and audit logs.
func (r *UserEscalation) Name() string {
	return "hierarchy.user_escalation"
}

// Pattern subscribes to supervisor.escalation.forwarded — the event
// produced by EscalationForwardsUpward once a branch hands an
// escalation up to the mission level.
func (r *UserEscalation) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "supervisor.escalation.forwarded"}
}

// Priority (100) shares the top slot with CompletionRequiresParentAgreement:
// the user decision is the most urgent action once an escalation reaches
// the mission supervisor.
func (r *UserEscalation) Priority() int { return 100 }

// Rationale is the human-readable justification surfaced in audit.
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

// Evaluate suppresses the rule when a prior mission-level resolution
// for the same task already exists (so Action doesn't fire repeatedly
// across replays); otherwise allows the escalation to proceed.
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

// Action dispatches based on r.Mode. In ModeInteractive it emits a
// PO-routed user message and a worker.paused event so the work halts
// until a human responds. In ModeFullAuto it spawns a Stakeholder
// stance to make the call autonomously. Unknown modes are rejected.
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

		// Pause work. Prefer the worker_id carried in the forwarded
		// escalation payload (set by EscalationForwardsUpward.Action).
		// evt.EmitterID is typically empty on forwarded events, so
		// falling back to it alone broke interactive mode (codex P1).
		workerID := ep.WorkerID
		if workerID == "" {
			workerID = evt.EmitterID
		}
		pauseMap := map[string]any{
			"worker_id": workerID,
			"reason":    "awaiting_user_decision",
		}
		if vErr := supervisor.ValidatePayload(r, pauseMap); vErr != nil {
			return fmt.Errorf("payload schema violation on worker.paused: %w", vErr)
		}
		pausePayload, _ := json.Marshal(pauseMap)
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

// PayloadSchema declares the worker.paused shape. Closes A3.
func (r *UserEscalation) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
