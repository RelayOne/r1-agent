// Package ctxpack — engine.go
//
// ContextEngine is the threshold-driven policy that decides when to
// compact the model's context window. Stoke already has three
// complementary primitives: microcompact (tiered Verbatim / Summarized
// / Dropped compression), ctxpack (relevance-weighted bin-packing),
// and context.MaskObservations (JetBrains-style old-tool-output
// masking). They've always been callable directly; what was missing
// was the dual-threshold policy that decides when to invoke each.
//
// Dual-threshold design (matches Hermes' published pattern):
//
//   - Primary in-loop trigger at PrimaryPct of the budget (default 50%).
//     Measured with real API token counts when available; this is the
//     preferred, cache-aligned trigger that compacts before the cache
//     cliff would kick in.
//
//   - Safety-net between-turn trigger at SafetyPct of the budget
//     (default 85%). Measured with the cheap token estimator; this is
//     the last-resort check that catches runs where the in-loop
//     trigger didn't fire (e.g. tool output exploded mid-turn).
//
// The engine is deliberately simple: callers compute the current token
// count, call ShouldCompact(used, budget, phase), and if it returns
// true call Compact(sections). Nothing fancy happens without an
// explicit caller action — this keeps the policy testable in isolation
// and avoids hidden state in the hot path.
package ctxpack

import (
	"github.com/RelayOne/r1-agent/internal/microcompact"
)

// ContextEngine is the threshold-policy contract. The default
// implementation in this file orchestrates microcompact; richer engines
// (e.g. a future Lossless Context Management plugin) can implement the
// same interface and be swapped in via config.
type ContextEngine interface {
	// ShouldCompact returns true when the current token usage (`used`)
	// against the budget exceeds one of the configured thresholds,
	// plus a short string naming which threshold fired (for
	// observability). phase is "in_loop" (primary) or "between_turn"
	// (safety net); engines may apply different rules per phase.
	ShouldCompact(used, budget int, phase string) (bool, string)

	// Compact runs the active compaction primitive against the given
	// sections and returns the compacted result. A nil engine or a
	// zero-section input returns an empty CompactResult — callers
	// don't need to null-check.
	Compact(sections []microcompact.Section) microcompact.CompactResult
}

// DefaultEngine wraps microcompact with a threshold policy. Safe for
// concurrent use (no internal state mutates after construction).
type DefaultEngine struct {
	// PrimaryPct triggers an in-loop compaction. Default 50.
	PrimaryPct int
	// SafetyPct triggers a between-turn compaction. Default 85.
	SafetyPct int
	// Compactor is the microcompact instance to delegate to. When nil,
	// Compact returns an empty result.
	Compactor *microcompact.Compactor
}

// NewDefaultEngine returns a DefaultEngine with the standard 50%/85%
// thresholds and a fresh microcompact.Compactor configured for
// cache-aligned tiered compression (Verbatim / Summarized / Dropped).
func NewDefaultEngine() *DefaultEngine {
	return &DefaultEngine{
		PrimaryPct: 50,
		SafetyPct:  85,
		Compactor:  microcompact.NewCompactor(microcompact.Config{}),
	}
}

// NewEngineWithThresholds is NewDefaultEngine with explicit threshold
// overrides. Out-of-range values (< 1 or >= 100) fall back to the
// defaults to prevent misconfiguration from disabling compaction.
func NewEngineWithThresholds(primaryPct, safetyPct int) *DefaultEngine {
	e := NewDefaultEngine()
	if primaryPct >= 1 && primaryPct < 100 {
		e.PrimaryPct = primaryPct
	}
	if safetyPct >= 1 && safetyPct < 100 {
		e.SafetyPct = safetyPct
	}
	if e.SafetyPct < e.PrimaryPct {
		// Safety must be >= primary so the two triggers don't invert.
		// When an operator sets them out of order, bump safety to 85.
		e.SafetyPct = 85
	}
	return e
}

// ShouldCompact implements the threshold policy. Returns (true, label)
// when the current token usage crosses one of the configured thresholds
// for the given phase.
//
// Phase-specific rules:
//   - "in_loop": fire at PrimaryPct. This is the cache-aligned trigger.
//   - "between_turn" / "": fire at SafetyPct. Catches cases the in-loop
//     trigger missed.
//   - Any other phase string treats both thresholds as active (stricter
//     behavior — if a caller is unsure which phase they're in, compact
//     sooner rather than later).
func (e *DefaultEngine) ShouldCompact(used, budget int, phase string) (bool, string) {
	if e == nil || budget <= 0 || used <= 0 {
		return false, ""
	}
	pct := (used * 100) / budget
	switch phase {
	case "in_loop":
		if pct >= e.PrimaryPct {
			return true, "in_loop_primary"
		}
		return false, ""
	case "between_turn", "":
		if pct >= e.SafetyPct {
			return true, "between_turn_safety"
		}
		return false, ""
	default:
		// Unknown phase: defensive — fire on either threshold.
		if pct >= e.PrimaryPct {
			return true, "unknown_phase_primary"
		}
		if pct >= e.SafetyPct {
			return true, "unknown_phase_safety"
		}
		return false, ""
	}
}

// Compact delegates to the configured microcompact.Compactor. Returns
// an empty CompactResult when sections is empty or the engine has no
// compactor — callers can rely on the returned .OutputSections being
// safely iterable.
func (e *DefaultEngine) Compact(sections []microcompact.Section) microcompact.CompactResult {
	if e == nil || e.Compactor == nil || len(sections) == 0 {
		return microcompact.CompactResult{}
	}
	return e.Compactor.Compact(sections)
}

// Compile-time confirmation that DefaultEngine satisfies ContextEngine.
var _ ContextEngine = (*DefaultEngine)(nil)
