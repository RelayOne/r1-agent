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

// ConvergeStep runs multi-model ConvergedAnswer when deps have ModelAskFn and
// multiple ConvergenceModels configured. Falls back to single-model
// MicroConvergence via ValidateStepFn. Falls back further to single-shot
// execution if neither is configured.
//
// This is the single entry point for convergent execution at any level.
func ConvergeStep(ctx context.Context, deps convergeStepDeps) (output string, converged bool, err error) {
	// Tier 1: Multi-model recursive convergence
	if deps.ModelAskFn != nil && len(deps.Models) > 0 {
		maxDepth := deps.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 20
		}
		result, cErr := ConvergedAnswer(ctx, ConvergedAnswerConfig{
			Models:       deps.Models,
			ArbiterModel: deps.ArbiterModel,
			AskFn:        deps.ModelAskFn,
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

	// Tier 2: Single-model micro-convergence with validation
	if deps.ExecuteFn != nil && deps.ValidateFn != nil {
		maxIter := deps.MaxIterations
		if maxIter <= 0 {
			maxIter = 3
		}
		result, cErr := RunMicroConvergence(ctx, MicroConvergenceConfig{
			MaxIterations: maxIter,
			Scope:         deps.Mission,
			StepName:      deps.StepName,
			ExecuteFn:     deps.ExecuteFn,
			ValidateFn:    deps.ValidateFn,
		})
		if cErr != nil {
			return "", false, cErr
		}
		return result.FinalOutput, result.Converged, nil
	}

	// Tier 3: Single-shot execution (no convergence validation)
	if deps.ExecuteFn != nil {
		output, execErr := deps.ExecuteFn(ctx, "")
		return output, true, execErr // assume converged since we can't validate
	}

	return "", false, fmt.Errorf("converge: no execution function configured")
}

// convergeStepDeps bundles config for ConvergeStep. Not all fields are
// needed — ConvergeStep uses the highest-capability tier available.
type convergeStepDeps struct {
	// Tier 1: Multi-model convergence
	ModelAskFn   ModelAskFn
	Models       []string
	ArbiterModel string
	MaxDepth     int

	// Tier 2: Single-model micro-convergence
	ValidateFn func(ctx context.Context, scope, output string) (gaps []string, err error)
	MaxIterations int

	// Tier 3 (and shared): Execute function
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
