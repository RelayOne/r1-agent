// MicroConvergence wraps any unit of work in an execute→validate→fix cycle.
//
// Every step in the mission lifecycle — not just the top-level loop — must
// independently converge. A work node doesn't just execute once; it executes,
// is adversarially validated, fixes gaps, re-validates, and loops until its
// specific scope is fully satisfied.
//
// MicroConvergence applies to:
//   - DAG work nodes (implement, test, review, research)
//   - Decomposition decisions (are items truly minimum scope? missing items?)
//   - Research findings (is the finding accurate? verified against code?)
//   - Plan steps (right approach? missing steps? wrong ordering?)
//
// This is recursive convergence — convergence all the way down.
package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// MicroConvergenceConfig controls the convergence loop for a single work unit.
type MicroConvergenceConfig struct {
	// MaxIterations caps the execute→validate→fix cycle. Default: 3.
	MaxIterations int

	// Scope describes what this work unit must accomplish.
	// Used in validation prompts to check completeness.
	Scope string

	// StepName identifies this step for logging (e.g., "work-node:implement-auth",
	// "decompose:root", "research:find-endpoints").
	StepName string

	// ExecuteFn does the work. Called on each iteration with accumulated feedback.
	// First call gets empty feedback; subsequent calls get validation gaps.
	ExecuteFn func(ctx context.Context, feedback string) (output string, err error)

	// ValidateFn adversarially checks whether the output satisfies the scope.
	// Returns gaps (empty = converged) and an error if validation itself fails.
	ValidateFn func(ctx context.Context, scope, output string) (gaps []string, err error)
}

// MicroConvergenceResult captures the outcome of a micro-convergence loop.
type MicroConvergenceResult struct {
	// FinalOutput is the output from the last successful execution.
	FinalOutput string

	// Iterations is how many execute→validate cycles ran.
	Iterations int

	// Converged is true if validation passed with no gaps.
	Converged bool

	// RemainingGaps holds gaps from the final validation if not converged.
	RemainingGaps []string

	// Duration is wall-clock time for the entire convergence loop.
	Duration time.Duration

	// History records each iteration's output and gaps for debugging.
	History []MicroConvergenceIteration
}

// MicroConvergenceIteration records a single execute→validate cycle.
type MicroConvergenceIteration struct {
	Iteration int      `json:"iteration"`
	Output    string   `json:"output"`
	Gaps      []string `json:"gaps"`
	Duration  time.Duration `json:"duration"`
}

// RunMicroConvergence drives a single work unit through execute→validate→fix
// cycles until converged or max iterations exhausted.
func RunMicroConvergence(ctx context.Context, cfg MicroConvergenceConfig) (*MicroConvergenceResult, error) {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 3
	}
	if cfg.ExecuteFn == nil {
		return nil, fmt.Errorf("microconv: ExecuteFn is required")
	}
	if cfg.ValidateFn == nil {
		return nil, fmt.Errorf("microconv: ValidateFn is required")
	}

	start := time.Now()
	result := &MicroConvergenceResult{}
	feedback := ""

	for i := 1; i <= cfg.MaxIterations; i++ {
		select {
		case <-ctx.Done():
			result.Duration = time.Since(start)
			return result, ctx.Err()
		default:
		}

		iterStart := time.Now()

		// Execute
		output, err := cfg.ExecuteFn(ctx, feedback)
		if err != nil {
			result.Duration = time.Since(start)
			return result, fmt.Errorf("microconv %s iteration %d execute: %w", cfg.StepName, i, err)
		}
		result.FinalOutput = output

		// Validate adversarially
		gaps, err := cfg.ValidateFn(ctx, cfg.Scope, output)
		if err != nil {
			// Validation error is non-fatal — treat as unconverged
			log.Printf("[microconv] %s iteration %d: validation error: %v", cfg.StepName, i, err)
			gaps = []string{fmt.Sprintf("validation error: %v", err)}
		}

		iter := MicroConvergenceIteration{
			Iteration: i,
			Output:    truncateForHistory(output, 500),
			Gaps:      gaps,
			Duration:  time.Since(iterStart),
		}
		result.History = append(result.History, iter)
		result.Iterations = i

		if len(gaps) == 0 {
			result.Converged = true
			result.Duration = time.Since(start)
			log.Printf("[microconv] %s converged after %d iteration(s) in %s",
				cfg.StepName, i, result.Duration.Round(time.Millisecond))
			return result, nil
		}

		log.Printf("[microconv] %s iteration %d: %d gaps remain", cfg.StepName, i, len(gaps))
		result.RemainingGaps = gaps

		// Build feedback for next iteration
		feedback = buildMicroFeedback(i, gaps, cfg.Scope)
	}

	result.Duration = time.Since(start)
	log.Printf("[microconv] %s did NOT converge after %d iterations (%d remaining gaps)",
		cfg.StepName, cfg.MaxIterations, len(result.RemainingGaps))
	return result, nil
}

// buildMicroFeedback constructs the feedback prompt for the next iteration.
func buildMicroFeedback(iteration int, gaps []string, scope string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CONVERGENCE FEEDBACK (iteration %d):\n\n", iteration)
	fmt.Fprintf(&b, "Your previous output was adversarially validated and the following gaps were found.\n")
	fmt.Fprintf(&b, "You MUST fix ALL of these before your work can be considered complete.\n\n")
	fmt.Fprintf(&b, "SCOPE (what must be satisfied):\n%s\n\n", scope)
	fmt.Fprintf(&b, "GAPS FOUND:\n")
	for i, gap := range gaps {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, gap)
	}
	fmt.Fprintf(&b, "\nFix every gap. Do not rationalize why a gap doesn't apply — fix it.\n")
	fmt.Fprintf(&b, "Do not claim a gap is 'already addressed' unless you can cite the exact file:line.\n")
	return b.String()
}

// ConvergeStep is the single entry point for convergent execution at any level.
//
// Strategy selection (highest available tier wins):
//
//   Tier 1: Multi-model ConvergedAnswer (ModelAskFn + 2+ models)
//     All models answer in parallel, arbiter synthesizes, recurses until done.
//
//   Tier 2: Single-model ConvergedAnswer (ModelAskFn + 1 model)
//     SAME model answers AND reviews in separate fresh invocations.
//     A fresh invocation with no sunk-cost bias catches what the executor missed.
//     Even one model produces different results across stateless calls.
//
//   Tier 3: ExecuteFn + ValidateFn loop (no ModelAskFn, but ValidateStepFn set)
//     Execute work, validate with separate call, fix gaps, repeat.
//     Uses MicroConvergence with arbiter-driven termination.
//
//   Tier 4: ExecuteFn + ModelAskFn review (ExecuteFn does real work, model reviews)
//     Execute does work (writes files), then a fresh model invocation reviews.
//     If incomplete, execute is called again with the review as feedback.
//     Loops until the reviewer says done.
//
//   Tier 5: Single-shot (last resort, no convergence possible)
//     Only when nothing else is available. Assumes converged.
func ConvergeStep(ctx context.Context, deps convergeStepDeps) (output string, converged bool, err error) {
	maxDepth := deps.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 20
	}

	// Tier 1 & 2: ConvergedAnswer — works with ANY number of models (even 1).
	// A single model in separate fresh invocations produces different results.
	// The arbiter (same model, fresh context, no sunk cost) catches gaps.
	if deps.ModelAskFn != nil && len(deps.Models) > 0 && deps.ExecuteFn == nil {
		result, cErr := ConvergedAnswer(ctx, ConvergedAnswerConfig{
			Models:        deps.Models,
			ArbiterModel:  deps.ArbiterModel,
			AskFn:         deps.ModelAskFn,
			BiggerMission: deps.BiggerMission,
			Mission:       deps.Mission,
			MaxDepth:      maxDepth,
			StepName:      deps.StepName,
		})
		if cErr != nil {
			return "", false, cErr
		}
		return result.Answer, result.Converged, nil
	}

	// Tier 3 & 4: ExecuteFn does real work — need a reviewer to check it.
	// Use ModelAskFn as reviewer if available, fall back to ValidateFn.
	if deps.ExecuteFn != nil {
		reviewFn := deps.ValidateFn

		// When ModelAskFn is available, use it as the reviewer even without
		// a dedicated ValidateStepFn. A fresh model invocation reviewing
		// the executor's output catches what the executor missed — even
		// if it's the same model, the fresh context has no sunk-cost bias.
		if reviewFn == nil && deps.ModelAskFn != nil {
			arbiter := deps.ArbiterModel
			if arbiter == "" && len(deps.Models) > 0 {
				arbiter = deps.Models[0]
			}
			if arbiter != "" {
				reviewFn = func(rCtx context.Context, scope, execOutput string) ([]string, error) {
					reviewPrompt := buildExecutionReviewPrompt(scope, execOutput)
					verdict, askErr := deps.ModelAskFn(rCtx, arbiter, reviewPrompt)
					if askErr != nil {
						return nil, askErr
					}
					return parseReviewVerdict(verdict), nil
				}
			}
		}

		if reviewFn != nil {
			// Convergent loop: execute → review → fix → review → ...
			// NO artificial cap. The reviewer decides when it's done.
			// maxDepth is a safety circuit breaker, not the convergence condition.
			result, cErr := RunMicroConvergence(ctx, MicroConvergenceConfig{
				MaxIterations: maxDepth, // use maxDepth, NOT a small cap
				Scope:         deps.Mission,
				StepName:      deps.StepName,
				ExecuteFn:     deps.ExecuteFn,
				ValidateFn:    reviewFn,
			})
			if cErr != nil {
				return "", false, cErr
			}
			return result.FinalOutput, result.Converged, nil
		}

		// Tier 5: Single-shot — no reviewer available at all.
		output, execErr := deps.ExecuteFn(ctx, "")
		return output, true, execErr
	}

	// Pure model query with ConvergedAnswer (no ExecuteFn)
	if deps.ModelAskFn != nil && len(deps.Models) > 0 {
		result, cErr := ConvergedAnswer(ctx, ConvergedAnswerConfig{
			Models:        deps.Models,
			ArbiterModel:  deps.ArbiterModel,
			AskFn:         deps.ModelAskFn,
			BiggerMission: deps.BiggerMission,
			Mission:       deps.Mission,
			MaxDepth:      maxDepth,
			StepName:      deps.StepName,
		})
		if cErr != nil {
			return "", false, cErr
		}
		return result.Answer, result.Converged, nil
	}

	return "", false, fmt.Errorf("converge: no execution function or model configured")
}

// buildExecutionReviewPrompt asks a model (in a fresh invocation) to review
// the output of an execution step. The fresh context has no sunk-cost bias.
func buildExecutionReviewPrompt(scope, executionOutput string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are reviewing work done by another agent (or a previous invocation of yourself).
You have NO context from the execution — you're seeing this fresh. That's intentional:
fresh eyes catch what the executor missed.

## Scope (what MUST be satisfied)
%s

## Execution Output
%s

## Your Job

Determine if the execution FULLY satisfies the scope. Not partially. Not "mostly." FULLY.

Ask yourself:
- Is every aspect of the scope addressed?
- Is there evidence the work was actually done (file:line citations, test results)?
- Are there gaps, shortcuts, or deferred work?
- Would a domain expert find anything missing?
- Did the executor hand-wave anything?

Respond with EXACTLY one of:
- "COMPLETE" — the work fully satisfies the scope
- "INCOMPLETE: <specific gap 1>; <specific gap 2>; ..." — what's missing

Do not hedge. Do not say "looks good enough." Either it's done or it's not.
`, scope, executionOutput)
	return b.String()
}

// parseReviewVerdict extracts gaps from a reviewer's COMPLETE/INCOMPLETE verdict.
func parseReviewVerdict(verdict string) []string {
	v := strings.TrimSpace(verdict)
	upper := strings.ToUpper(v)

	if strings.HasPrefix(upper, "COMPLETE") && !strings.HasPrefix(upper, "INCOMPLETE") {
		return nil // converged
	}

	// Extract the gap descriptions after "INCOMPLETE:"
	if idx := strings.Index(upper, "INCOMPLETE:"); idx >= 0 {
		gapText := strings.TrimSpace(v[idx+len("INCOMPLETE:"):])
		if gapText == "" {
			return []string{"reviewer said incomplete but gave no specifics"}
		}
		// Split on semicolons or newlines
		var gaps []string
		for _, part := range strings.FieldsFunc(gapText, func(r rune) bool {
			return r == ';' || r == '\n'
		}) {
			part = strings.TrimSpace(part)
			if part != "" {
				gaps = append(gaps, part)
			}
		}
		if len(gaps) == 0 {
			return []string{gapText}
		}
		return gaps
	}

	// Can't parse — treat as incomplete
	return []string{fmt.Sprintf("reviewer response unparseable (treating as incomplete): %s", truncateForHistory(v, 200))}
}

// convergeStepDeps bundles config for ConvergeStep.
type convergeStepDeps struct {
	// Multi-model convergence (works with 1+ models)
	ModelAskFn   ModelAskFn
	Models       []string
	ArbiterModel string
	MaxDepth     int // safety circuit breaker, NOT convergence condition. Default: 20.

	// Single-model validation (used when ModelAskFn not available)
	ValidateFn func(ctx context.Context, scope, output string) (gaps []string, err error)
	MaxIterations int // DEPRECATED: use MaxDepth. Kept for backward compat.

	// Execute function — does real work (writes files, runs commands)
	ExecuteFn func(ctx context.Context, feedback string) (output string, err error)

	// Context
	BiggerMission string
	Mission       string
	StepName      string
}

// truncateForHistory keeps history entries from bloating memory.
func truncateForHistory(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}

// ParseValidationGaps extracts gaps from a validation response.
// Expects JSON: {"gaps": ["gap1", "gap2"]} — handles markdown fences
// and extra text around the JSON.
func ParseValidationGaps(response string) []string {
	type gapResponse struct {
		Gaps []string `json:"gaps"`
	}

	// Try direct parse first
	var resp gapResponse
	if err := json.Unmarshal([]byte(response), &resp); err == nil {
		return resp.Gaps
	}

	// Strip markdown code fences
	cleaned := response
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), "```")
	cleaned = strings.TrimSpace(cleaned)

	if err := json.Unmarshal([]byte(cleaned), &resp); err == nil {
		return resp.Gaps
	}

	// Find JSON object in response
	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(cleaned[start:end+1]), &resp); err == nil {
			return resp.Gaps
		}
	}

	// Can't parse — treat entire response as a single gap if non-empty
	trimmed := strings.TrimSpace(response)
	if trimmed != "" && trimmed != "{}" {
		return []string{fmt.Sprintf("unparseable validation response: %s", truncateForHistory(trimmed, 200))}
	}
	return nil
}
