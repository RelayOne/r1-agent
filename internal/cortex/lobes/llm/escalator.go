package llm

import "context"

// Escalator decides whether a Lobe upgrades to Sonnet for a particular call.
// Called by LLM Lobes when a deterministic check (rule failure, criticality
// signal) suggests Haiku output may not be sufficient. Returning "" keeps
// the Lobe on its default model (Haiku 4.5).
//
// Spec: specs/cortex-concerns.md item 2 + decisions D-2026-05-02-06.
type Escalator func(ctx context.Context, reason string) (model string)

// NewEscalator returns a default escalator. When allowed is true, any
// non-empty reason triggers Sonnet. When allowed is false, escalation
// is permanently off.
func NewEscalator(allowed bool) Escalator {
	if !allowed {
		return func(ctx context.Context, reason string) string { return "" }
	}
	return func(ctx context.Context, reason string) string {
		if reason == "" {
			return ""
		}
		return "claude-sonnet-4-6"
	}
}
