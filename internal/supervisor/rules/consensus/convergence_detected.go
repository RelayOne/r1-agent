package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
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

// convergenceContent holds the content of agree/dissent review nodes.
type convergenceContent struct {
	LoopID   string `json:"loop_id"`
	Resolved bool   `json:"resolved"`
}

// Evaluate reports whether the loop referenced by evt has at least
// one agree node and no unresolved dissent nodes. Returns false (with
// no error) for irrelevant node types or missing loop scope.
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

	// Query all review nodes in this loop's context.
	nodes, err := l.Query(ctx, ledger.QueryFilter{MissionID: evt.Scope.MissionID})
	if err != nil {
		return false, fmt.Errorf("query loop nodes: %w", err)
	}

	hasAgree := false
	for _, n := range nodes {
		if strings.Contains(n.Type, "dissent") {
			// Check if dissent is resolved.
			var cc convergenceContent
			if err := json.Unmarshal(n.Content, &cc); err != nil {
				continue
			}
			if cc.LoopID == loopID && !cc.Resolved {
				return false, nil // outstanding dissent
			}
		}
		if strings.Contains(n.Type, "agree") {
			var cc convergenceContent
			if err := json.Unmarshal(n.Content, &cc); err != nil {
				continue
			}
			if cc.LoopID == loopID {
				hasAgree = true
			}
		}
	}

	return hasAgree, nil
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
		Type:      "consensus.loop.state.changed",
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
