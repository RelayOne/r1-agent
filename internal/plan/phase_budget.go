// Package plan — phase_budget.go
//
// Typed budgets and verdict validators that move stoke's review loop
// closer to the "deterministic engine with LLM at typed boundaries"
// shape. The existing LLM call sites (reviewer, decomposer, judge)
// keep their freeform outputs — this file adds the typed scaffolding
// the orchestrator uses to bound what those LLMs can force the system
// to do.
//
// Two concrete guarantees this adds:
//
//  1. Per-task cumulative dispatch cap. Today reviewAndFollowupRecursive
//     is bounded by maxReviewDepth (3) but a single recursion level can
//     spawn arbitrarily many workers via decomposer SubDirectives. Run
//     5 observed T1 fan out to 13 sub-directives at depth 1. The
//     cumulative cap (default 12) makes that explosion a typed error —
//     the orchestrator truncates or escalates rather than silently
//     burning budget.
//
//  2. Validated LLM verdicts. A malformed DecomposeVerdict ({empty
//     SubDirectives, Abandon=false} or {Abandon=true with empty
//     AbandonReason}) used to fall through the recursion with
//     implicit no-op semantics. ValidateDecomposeVerdict turns those
//     cases into explicit typed errors so the orchestrator can decide
//     deterministically (escalate vs retry vs accept as Abandon).
//
// Neither guarantee removes the LLM's job. Both move the "what
// happens next" decision back under deterministic code.

package plan

import (
	"fmt"
	"strings"
)

// ReviewBudget bounds the review + decomposition loop for one task.
// Zero values fall back to the defaults so callers can leave the
// struct unset when they want legacy behavior.
type ReviewBudget struct {
	// MaxDepth caps recursion depth on the reviewer→decomposer→worker
	// chain. Default 3 (matches the legacy const).
	MaxDepth int
	// MaxTotalDispatches caps the cumulative number of worker
	// dispatches for one originalTask across all recursion levels.
	// Default 12 — enough to let a complex task decompose once or
	// twice and dispatch a handful of sub-directives per level,
	// without allowing a single stuck task to monopolize the run.
	MaxTotalDispatches int
	// MaxDecompBreadth caps SubDirectives per decomposer verdict.
	// The decomposer prompt already asks for 5-9 but the LLM
	// occasionally returns 12+. Default 10.
	MaxDecompBreadth int
}

// WithDefaults returns a ReviewBudget with unset fields populated by
// the conservative defaults. Does not mutate the receiver.
func (b ReviewBudget) WithDefaults() ReviewBudget {
	if b.MaxDepth <= 0 {
		b.MaxDepth = 3
	}
	if b.MaxTotalDispatches <= 0 {
		b.MaxTotalDispatches = 12
	}
	if b.MaxDecompBreadth <= 0 {
		b.MaxDecompBreadth = 10
	}
	return b
}

// ValidateDecomposeVerdict returns the list of problems with a verdict
// the LLM produced. Empty slice means the verdict is well-formed and
// safe for the orchestrator to act on. A verdict flagged by any
// problem must NOT be used as-is; the caller should either escalate
// (treat as Abandon) or reject and retry.
//
// Rules:
//   - Abandon=true requires non-empty AbandonReason
//   - Abandon=false requires at least one SubDirective
//   - Each SubDirective must be at least 20 characters (rejects
//     "fix it" / "." / empty)
//   - SubDirectives count must not exceed maxBreadth
func ValidateDecomposeVerdict(v *DecomposeVerdict, maxBreadth int) []string {
	if v == nil {
		return []string{"decomposer returned nil verdict"}
	}
	var errs []string
	if v.Abandon {
		if strings.TrimSpace(v.AbandonReason) == "" {
			errs = append(errs, "Abandon=true but AbandonReason is empty")
		}
		return errs
	}
	if len(v.SubDirectives) == 0 {
		errs = append(errs, "Abandon=false but no SubDirectives provided")
		return errs
	}
	if maxBreadth > 0 && len(v.SubDirectives) > maxBreadth {
		errs = append(errs, fmt.Sprintf("SubDirectives count %d exceeds MaxDecompBreadth=%d", len(v.SubDirectives), maxBreadth))
	}
	for i, sd := range v.SubDirectives {
		if len(strings.TrimSpace(sd)) < 20 {
			errs = append(errs, fmt.Sprintf("SubDirective[%d] is too short (<20 chars); treats as placeholder", i))
		}
	}
	return errs
}

// TruncateSubDirectives returns a copy of v with SubDirectives bounded
// at maxBreadth. Preserves the first maxBreadth entries — the
// decomposer prompt ranks them in priority order, so first-N is the
// right truncation. Used when the orchestrator decides to proceed
// with a broadth-exceeding verdict rather than discarding it entirely.
func TruncateSubDirectives(v *DecomposeVerdict, maxBreadth int) *DecomposeVerdict {
	if v == nil || maxBreadth <= 0 || len(v.SubDirectives) <= maxBreadth {
		return v
	}
	out := *v
	out.SubDirectives = append([]string{}, v.SubDirectives[:maxBreadth]...)
	return &out
}

// ValidateTaskWorkVerdict performs a light sanity check on a reviewer
// verdict. Rules:
//   - Complete=false requires at least one of {FollowupDirective,
//     GapsFound} to be non-empty so the orchestrator has something
//     concrete to dispatch
//   - Reasoning should be non-empty (operator-facing audit trail)
//
// Empty return = well-formed; any entries = caller should treat the
// verdict as malformed and handle deterministically (e.g. accept as
// complete-with-warning or escalate).
func ValidateTaskWorkVerdict(v *TaskWorkVerdict) []string {
	if v == nil {
		return []string{"reviewer returned nil verdict"}
	}
	var errs []string
	if strings.TrimSpace(v.Reasoning) == "" {
		errs = append(errs, "Reasoning is empty — no audit trail for verdict")
	}
	if !v.Complete {
		if strings.TrimSpace(v.FollowupDirective) == "" && len(v.GapsFound) == 0 {
			errs = append(errs, "Complete=false but no FollowupDirective or GapsFound — orchestrator has nothing to dispatch")
		}
	}
	return errs
}
