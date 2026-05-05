package antitrunc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// TruncationPhraseDetected fires when an event payload contains a
// TruncationPhrase, so the supervisor can publish a critical Note
// and (eventually) refuse end_turn.
type TruncationPhraseDetected struct {
	Extractor TextExtractor
}

// NewTruncationPhraseDetected constructs the rule with the default
// extractor (reads `summary`/`text`/`output`/`message` from the
// payload).
func NewTruncationPhraseDetected() *TruncationPhraseDetected {
	return &TruncationPhraseDetected{Extractor: DefaultDeclarationTextExtractor}
}

func (r *TruncationPhraseDetected) Name() string { return "antitrunc.truncation_phrase_detected" }

// Pattern matches every worker.declaration.* event. The mission
// wiring ALSO subscribes the rule to worker.action.completed in
// branch-level manifests; the prefix string covers both because
// supervisor.Pattern matches by TypePrefix.
//
// We use the broad prefix "worker." so subagent return events,
// declaration events, and worker action events all flow through —
// the extractor is the filter.
func (r *TruncationPhraseDetected) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker."}
}

// Priority high so this rule evaluates before completion-second-
// opinion (which would otherwise let the truncation slip through
// while the reviewer spawns).
func (r *TruncationPhraseDetected) Priority() int { return 200 }

func (r *TruncationPhraseDetected) Rationale() string {
	return "Self-truncation phrases must be detected before downstream rules accept the worker's claim."
}

func (r *TruncationPhraseDetected) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	if r.Extractor == nil {
		return false, nil
	}
	text := r.Extractor(evt)
	if text == "" {
		return false, nil
	}
	return len(antitrunc.MatchTruncation(text)) > 0, nil
}

func (r *TruncationPhraseDetected) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	text := ""
	if r.Extractor != nil {
		text = r.Extractor(evt)
	}
	matches := antitrunc.MatchTruncation(text)

	ids := make([]string, 0, len(matches))
	snippets := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.PhraseID)
		snippets = append(snippets, m.Snippet)
	}

	payload := map[string]any{
		"category":   "antitrunc",
		"rule":       r.Name(),
		"severity":   "critical",
		"phrase_ids": ids,
		"snippets":   snippets,
		"detail":     fmt.Sprintf("self-truncation phrases detected: %v", ids),
	}
	body, _ := json.Marshal(payload)
	return b.Publish(bus.Event{
		Type:      bus.EvtSupervisorRuleFired,
		Scope:     evt.Scope,
		Payload:   body,
		CausalRef: evt.ID,
	})
}
