package antitrunc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// SubagentSummaryTruncation fires when a subagent's
// worker.action.completed summary contains a truncation phrase AND
// the parent task has not yet been marked complete in the ledger.
//
// This pairs with TruncationPhraseDetected: that rule catches the
// phrase wherever it appears; this rule narrows the focus to the
// specific case where the parent task's incomplete status confirms
// the subagent is bailing out prematurely (rather than legitimately
// reporting "stopped due to missing input" or similar).
type SubagentSummaryTruncation struct {
	Extractor TextExtractor
}

// NewSubagentSummaryTruncation returns a rule wired to the default
// extractor.
func NewSubagentSummaryTruncation() *SubagentSummaryTruncation {
	return &SubagentSummaryTruncation{Extractor: DefaultDeclarationTextExtractor}
}

func (r *SubagentSummaryTruncation) Name() string {
	return "antitrunc.subagent_summary_truncation"
}

// Pattern matches worker.action.completed events.
func (r *SubagentSummaryTruncation) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtWorkerActionCompleted)}
}

func (r *SubagentSummaryTruncation) Priority() int { return 180 }

func (r *SubagentSummaryTruncation) Rationale() string {
	return "A subagent that returns with a truncation phrase while its parent task is unfinished is self-truncating; refuse the return."
}

// taskCompleteContent is the expected shape of a task.complete
// ledger node, used to confirm the parent task is finished. A
// missing record means "incomplete" (firing condition).
type taskCompleteContent struct {
	TaskID string `json:"task_id"`
}

func (r *SubagentSummaryTruncation) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	if r.Extractor == nil {
		return false, nil
	}
	text := r.Extractor(evt)
	if text == "" {
		return false, nil
	}
	if len(antitrunc.MatchTruncation(text)) == 0 {
		return false, nil
	}

	// Parent task identifier: prefer the event scope, fall back to
	// nothing. Without a TaskID we cannot prove incompletion, so be
	// conservative and DO fire — better a false positive than a
	// silent self-truncation.
	taskID := evt.Scope.TaskID
	if taskID == "" {
		return true, nil
	}

	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "task.complete"})
	if err != nil {
		// On query error, fire to be safe — we cannot prove the
		// task is complete.
		return true, nil
	}
	for _, n := range nodes {
		var c taskCompleteContent
		if err := json.Unmarshal(n.Content, &c); err != nil {
			continue
		}
		if c.TaskID == taskID {
			return false, nil // task is complete, no truncation concern
		}
	}
	return true, nil
}

func (r *SubagentSummaryTruncation) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	text := ""
	if r.Extractor != nil {
		text = r.Extractor(evt)
	}
	matches := antitrunc.MatchTruncation(text)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.PhraseID)
	}
	payload := map[string]any{
		"category":   "antitrunc",
		"rule":       r.Name(),
		"severity":   "critical",
		"task_id":    evt.Scope.TaskID,
		"phrase_ids": ids,
		"detail":     fmt.Sprintf("subagent returned with truncation phrases %v while parent task incomplete", ids),
	}
	body, _ := json.Marshal(payload)
	return b.Publish(bus.Event{
		Type:      bus.EvtSupervisorRuleFired,
		Scope:     evt.Scope,
		Payload:   body,
		CausalRef: evt.ID,
	})
}
