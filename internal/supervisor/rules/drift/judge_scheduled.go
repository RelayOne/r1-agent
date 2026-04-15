// Package drift implements supervisor rules that detect and respond to
// plan divergence, budget overruns, and intent misalignment.
package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// JudgeScheduled fires when a scheduled drift-judge timeout arrives or when a
// dissent is lodged that looks similar to a prior one. It spawns a Judge stance
// to re-evaluate the current direction.
type JudgeScheduled struct {
	// SlowDriftTimeout is the timeout duration for draft supersession checks.
	SlowDriftTimeout time.Duration
	// SimilarityThreshold is the minimum similarity for dissent dedup (0-1).
	SimilarityThreshold float64
}

// NewJudgeScheduled returns a rule with default configuration.
func NewJudgeScheduled() *JudgeScheduled {
	return &JudgeScheduled{
		SlowDriftTimeout:    30 * time.Minute,
		SimilarityThreshold: 0.7,
	}
}

func (r *JudgeScheduled) Name() string { return "drift.judge_scheduled" }

func (r *JudgeScheduled) Pattern() bus.Pattern {
	// We match both drift.judge.timeout and ledger.node.added; the broader
	// prefix lets the pattern match both. Evaluate narrows further.
	return bus.Pattern{}
}

func (r *JudgeScheduled) Priority() int { return 70 }

func (r *JudgeScheduled) Rationale() string {
	return "Recurring dissents or stale drafts indicate the team may be drifting; a Judge provides an independent check."
}

// judgeTimeoutPayload is the payload for drift.judge.timeout events.
type judgeTimeoutPayload struct {
	DraftNodeID string `json:"draft_node_id"`
}

// dissentContent is the expected content shape of a dissent node.
type dissentContent struct {
	Concern string `json:"concern"`
	LoopID  string `json:"loop_id"`
}

func (r *JudgeScheduled) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	switch bus.EventType(evt.Type) {
	case "drift.judge.timeout":
		return r.evaluateTimeout(ctx, evt, l)
	case bus.EvtLedgerNodeAdded:
		return r.evaluateDissent(ctx, evt, l)
	default:
		return false, nil
	}
}

func (r *JudgeScheduled) evaluateTimeout(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var tp judgeTimeoutPayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return false, fmt.Errorf("unmarshal timeout payload: %w", err)
	}
	if tp.DraftNodeID == "" {
		return false, nil
	}

	// Check if the draft was superseded before the timer fired.
	resolved, err := l.Resolve(ctx, tp.DraftNodeID)
	if err != nil {
		return true, nil // can't resolve — fire to be safe
	}
	// If resolved node differs from original, draft was superseded.
	if resolved.ID != tp.DraftNodeID {
		return false, nil
	}
	return true, nil
}

func (r *JudgeScheduled) evaluateDissent(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	// Extract the added node info.
	var nodeRef struct {
		NodeID   string `json:"node_id"`
		NodeType string `json:"node_type"`
	}
	if err := json.Unmarshal(evt.Payload, &nodeRef); err != nil {
		return false, nil
	}
	if nodeRef.NodeType != "dissent" {
		return false, nil
	}

	node, err := l.Get(ctx, nodeRef.NodeID)
	if err != nil {
		return false, nil
	}

	var dc dissentContent
	if err := json.Unmarshal(node.Content, &dc); err != nil {
		return false, nil
	}

	// Walk dissent history in this loop.
	loopID := dc.LoopID
	if loopID == "" {
		loopID = evt.Scope.LoopID
	}
	if loopID == "" {
		return false, nil
	}

	prior, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "dissent",
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, nil
	}

	for _, p := range prior {
		if p.ID == nodeRef.NodeID {
			continue
		}
		var pc dissentContent
		if err := json.Unmarshal(p.Content, &pc); err != nil {
			continue
		}
		if simpleSimilarity(dc.Concern, pc.Concern) >= r.SimilarityThreshold {
			return true, nil
		}
	}
	return false, nil
}

// simpleSimilarity computes a basic Jaccard similarity between two strings.
func simpleSimilarity(a, b string) float64 {
	wordsA := tokenize(a)
	wordsB := tokenize(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0
	}

	setA := make(map[string]bool, len(wordsA))
	for _, w := range wordsA {
		setA[w] = true
	}
	setB := make(map[string]bool, len(wordsB))
	for _, w := range wordsB {
		setB[w] = true
	}

	intersection := 0
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func tokenize(s string) []string {
	return strings.Fields(strings.ToLower(s))
}

func (r *JudgeScheduled) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"role":       "Judge",
		"reason":     "drift_detected",
		"trigger_id": evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}

// PayloadSchema declares the supervisor.spawn.requested shape for
// this rule's primary emitted event (lenient default — most fields
// optional). Closes A3 for this rule.
func (r *JudgeScheduled) PayloadSchema() *schemaval.Schema {
	return supervisor.SpawnRequestedSchema()
}
