package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/loops"
	"github.com/RelayOne/r1/internal/schemaval"
	"github.com/RelayOne/r1/internal/supervisor"
)

// ConvergenceDetected checks whether all consensus partners have agreed
// and no outstanding dissents remain, transitioning the loop to "converged".
type ConvergenceDetected struct{}

// NewConvergenceDetected returns a new rule instance.
func NewConvergenceDetected() *ConvergenceDetected {
	return &ConvergenceDetected{}
}

// Name returns the stable rule identifier used by the supervisor
// registry and audit logs.
func (r *ConvergenceDetected) Name() string {
	return "consensus.convergence_detected"
}

// Pattern tells the supervisor which bus events trigger this rule:
// any ledger-node-added event, since new agree/dissent nodes may flip
// the convergence state.
func (r *ConvergenceDetected) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtLedgerNodeAdded)}
}

// Priority places this rule near the top of the queue (95 of 100) so
// convergence is detected before lower-priority follow-ups fire.
func (r *ConvergenceDetected) Priority() int { return 95 }

// Rationale is the human-readable explanation included in supervisor
// decisions for audit.
func (r *ConvergenceDetected) Rationale() string {
	return "A loop converges when all partners agree and no dissents are outstanding."
}

// Evaluate reports whether the loop referenced by evt structurally
// converged: all convened partners have agree nodes referencing the
// loop's current draft and no dissents are outstanding. Convergence is
// read through loops.Tracker.IsConverged — the single loop-state query
// mechanism (edge-walk over EdgeReferences from the loop's artifact) —
// instead of the former mission-wide content scan with its divergent
// {loop_id, resolved} schema (audit A066). Returns false (with no
// error) for irrelevant node types, missing loop scope, or loop IDs
// that do not resolve to a loop node.
func (r *ConvergenceDetected) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return false, fmt.Errorf("unmarshal node added payload: %w", err)
	}

	// Only trigger on agree, dissent, or draft nodes.
	isRelevant := strings.Contains(np.NodeType, "agree") ||
		strings.Contains(np.NodeType, "dissent") ||
		strings.Contains(np.NodeType, "draft")
	if !isRelevant {
		return false, nil
	}

	loopID := np.LoopID
	if loopID == "" {
		loopID = evt.Scope.LoopID
	}
	if loopID == "" {
		return false, nil
	}

	converged, err := loops.NewTracker(l).IsConverged(ctx, loopID)
	if err != nil {
		// The referenced loop node does not exist or its content is not
		// loop-shaped — nothing to converge. Not a rule error: the event
		// may reference a foreign or not-yet-created loop.
		return false, nil
	}
	return converged, nil
}

// Action emits a consensus.loop.state.changed event transitioning the
// loop to the converged state. CausalRef links back to the triggering
// node-added event.
func (r *ConvergenceDetected) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var np nodeAddedPayload
	if err := json.Unmarshal(evt.Payload, &np); err != nil {
		return fmt.Errorf("unmarshal node added payload: %w", err)
	}

	loopID := np.LoopID
	if loopID == "" {
		loopID = evt.Scope.LoopID
	}

	transitionPayload, _ := json.Marshal(map[string]string{
		"loop_id": loopID,
		"state":   "converged",
		"reason":  "all partners agreed, no outstanding dissents",
	})
	return b.Publish(bus.Event{
		Type:      loops.StateChangedEventType,
		Scope:     evt.Scope,
		Payload:   transitionPayload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema declares the shape for this rule's primary emitted
// event: consensus.loop.state.changed — convergence reached.
func (r *ConvergenceDetected) PayloadSchema() *schemaval.Schema {
	return supervisor.ConsensusLoopStateSchema()
}
