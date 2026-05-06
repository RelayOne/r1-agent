// Package antitrunc implements supervisor rules that fire when the
// LLM exhibits self-truncation / scope-reduction behaviour.
//
// Three rules ship together:
//
//   - TruncationPhraseDetected — fires when the latest assistant turn
//     (extracted from a worker event payload) contains any
//     antitrunc.TruncationPhrases pattern.
//   - ScopeUnderdelivery — fires when a worker declares a task done
//     but the corresponding plan / spec checklist still has unchecked
//     items.
//   - SubagentSummaryTruncation — fires when a subagent's
//     return-summary contains TruncationPhrases AND the parent task
//     is not yet marked complete.
//
// Each rule emits a bus.EvtSupervisorRuleFired event whose payload
// carries category="antitrunc" so the (forthcoming) rulecheck Lobe
// converts it to a SevCritical Workspace Note.
//
// All rules accept a `TextExtractor` so they're decoupled from the
// concrete event schema. The mission/branch wiring constructs a rule
// with an extractor that knows how to pull "summary" / "text" /
// "output" out of the event payload shipped at that tier.
package antitrunc

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/bus"
)

// TextExtractor pulls assistant-output / subagent-summary text from a
// bus event. It returns "" when the event has no relevant text (in
// which case the rule does not fire).
//
// Constructed by the manifest wiring; defaults below cover the
// shapes shipped today.
type TextExtractor func(evt bus.Event) string

// DefaultDeclarationTextExtractor reads `summary` (preferred) then
// `text` from a worker.declaration.* event payload. Returns "" when
// the payload doesn't unmarshal or neither field is present.
func DefaultDeclarationTextExtractor(evt bus.Event) string {
	if len(evt.Payload) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(evt.Payload, &raw); err != nil {
		return ""
	}
	for _, k := range []string{"summary", "text", "output", "message"} {
		if v, ok := raw[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
