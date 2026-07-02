// Package specexec implements speculative parallel execution.
// Inspired by Devin's multi-approach exploration and branch/explorer:
//
// When a task has multiple viable approaches, run them in parallel
// across isolated worktrees and pick the winner. This is the key
// differentiator for handling ambiguous tasks:
// - Fork N approaches with different strategies/prompts
// - Each runs in an isolated git worktree
// - Score full executions by test pass rate, diff size, speed
//   (DefaultScorer); score plan-only explorations by plan structure
//   plus speed (PlanScorer)
// - Select the best, discard the rest
// - Optionally merge insights from failed approaches into retry prompts
package specexec

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Strategy describes one approach to solving a task.
type Strategy struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Prompt      string            `json:"prompt"`
	Model       string            `json:"model,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// Outcome is the result of executing one strategy.
type Outcome struct {
	StrategyID  string        `json:"strategy_id"`
	Success     bool          `json:"success"`
	Score       float64       `json:"score"`        // 0-1, higher is better
	TestsPassed int           `json:"tests_passed"`
	TestsFailed int           `json:"tests_failed"`
	DiffLines   int           `json:"diff_lines"`
	Duration    time.Duration `json:"duration"`
	Error       string        `json:"error,omitempty"`
	Artifacts   []string      `json:"artifacts,omitempty"` // file paths, worktree refs
	Insights    []string      `json:"insights,omitempty"`  // learnings from this approach

	// PlanText carries the plan produced by a plan-only exploration.
	// Populated by executors whose strategies run with PlanOnly=true
	// (scheduler.WithSpecExec); consumed by PlanScorer, which ranks
	// plans by structural quality since plan-only outcomes have no
	// test/diff signal.
	PlanText string `json:"plan_text,omitempty"`
}

// Scorer evaluates an outcome and assigns a score.
type Scorer func(Outcome) float64

// Executor runs a strategy and returns its outcome.
type Executor func(ctx context.Context, strategy Strategy) Outcome

// Spec configures a speculative execution run.
type Spec struct {
	Strategies    []Strategy    `json:"strategies"`
	MaxParallel   int           `json:"max_parallel"`   // concurrency limit
	Timeout       time.Duration `json:"timeout"`        // per-strategy timeout
	EarlyStop     bool          `json:"early_stop"`     // stop all on first success above threshold
	StopThreshold float64       `json:"stop_threshold"` // score threshold for early stop
	Scorer        Scorer        `json:"-"`
}

// Result contains all outcomes from a speculative execution.
type Result struct {
	Outcomes  []Outcome     `json:"outcomes"`
	Winner    *Outcome      `json:"winner,omitempty"`
	Duration  time.Duration `json:"duration"`
	Cancelled int           `json:"cancelled"` // strategies cancelled by early stop
}

// DefaultScorer weights test results, diff size, and duration.
// When test/diff data is absent (zero values), partial credit is awarded
// so that strategies with real signals can differentiate from ones without.
func DefaultScorer(o Outcome) float64 {
	if !o.Success {
		return 0
	}
	score := 0.0

	// Test pass rate (60% weight)
	total := o.TestsPassed + o.TestsFailed
	if total > 0 {
		score += 0.6 * float64(o.TestsPassed) / float64(total)
	} else {
		// No test data: award half credit instead of full.
		// Strategies that ran tests and passed will outrank plan-only ones.
		score += 0.3
	}

	// Smaller diffs preferred (20% weight)
	if o.DiffLines > 0 {
		// Sigmoid-like decay: 100 lines = 1.0, 1000 lines = 0.5
		diffScore := 100.0 / (100.0 + float64(o.DiffLines))
		score += 0.2 * diffScore
	} else {
		// No diff data: award half credit.
		score += 0.1
	}

	// Faster preferred (20% weight)
	if o.Duration > 0 {
		// 30s = 1.0, 5min = 0.1
		speedScore := 30.0 / (30.0 + o.Duration.Seconds())
		score += 0.2 * speedScore
	} else {
		score += 0.1
	}

	return score
}

// PlanStopThreshold is the early-stop threshold for plan-only runs
// scored by PlanScorer. It equals the maximum structural score, so it
// is reached exactly when a plan exhibits full structural quality
// (any nonzero speed credit then pushes the total past it). With
// DefaultScorer, plan-only outcomes capped at ~0.6 and the previous
// 0.9 threshold was unreachable dead code.
const PlanStopThreshold = 0.8

// Plan-structure signals for PlanScorer. Deterministic on purpose —
// no repo access, no LLM judge — so scoring is reproducible and free.
var (
	// planPathRe matches a concrete repo-style file path with a
	// directory component and an extension, e.g. "internal/auth/token.go".
	planPathRe = regexp.MustCompile(`[A-Za-z0-9_.-]+/[A-Za-z0-9_/.-]*[A-Za-z0-9_-]+\.[A-Za-z][A-Za-z0-9]{0,9}`)

	// planStepRe matches ordered steps: "1. ...", "2) ...", "Step 3".
	planStepRe = regexp.MustCompile(`(?mi)^\s*(?:\d+[.)]\s|step\s+\d+)`)

	// planVerifyRe matches a verification story: tests, checks, validation.
	planVerifyRe = regexp.MustCompile(`(?i)\b(?:test|verif|validat|check)`)
)

// PlanScorer scores plan-only outcomes by deterministic structural
// quality of the plan text plus speed. scheduler.WithSpecExec uses it
// because its speculative strategies run with PlanOnly=true and so
// carry no test/diff signal for DefaultScorer — under DefaultScorer
// every successful plan scored a flat ~0.4-0.6 and the winner
// degenerated to "fastest plan" (audit A024).
//
// Weights (max 1.0):
//
//	0.30 substantive plan text (non-empty; half credit under 120 chars)
//	0.20 references at least one concrete file path
//	0.20 has ordered steps (numbered list / "Step N")
//	0.10 states a verification story (test/verify/validate/check)
//	0.20 speed (30s ≈ 1.0, 5min ≈ 0.1 — same decay as DefaultScorer)
//
// Outcomes with empty PlanText degrade to speed-only scoring, which
// matches the previous behavior for callers that do not thread plan
// text into Outcome.PlanText.
func PlanScorer(o Outcome) float64 {
	if !o.Success {
		return 0
	}
	score := 0.0
	text := strings.TrimSpace(o.PlanText)

	switch {
	case len(text) >= 120:
		score += 0.3
	case len(text) > 0:
		score += 0.15
	}
	if planPathRe.MatchString(text) {
		score += 0.2
	}
	if planStepRe.MatchString(text) {
		score += 0.2
	}
	if planVerifyRe.MatchString(text) {
		score += 0.1
	}

	// Faster preferred (20% weight), matching DefaultScorer's decay.
	if o.Duration > 0 {
		score += 0.2 * (30.0 / (30.0 + o.Duration.Seconds()))
	} else {
		score += 0.1
	}
	return score
}

// Run executes strategies in parallel and selects the best outcome.
func Run(ctx context.Context, spec Spec, exec Executor) *Result {
	if spec.MaxParallel <= 0 {
		spec.MaxParallel = len(spec.Strategies)
	}
	if spec.Scorer == nil {
		spec.Scorer = DefaultScorer
	}

	result := &Result{}
	start := time.Now()

	// Create cancellable context for early stopping
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	outcomes := make([]Outcome, len(spec.Strategies))
	var mu sync.Mutex
	var wg sync.WaitGroup

	sem := make(chan struct{}, spec.MaxParallel)
	earlyStop := make(chan struct{})
	stopped := false

	for i, strategy := range spec.Strategies {
		wg.Add(1)
		go func(idx int, s Strategy) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					outcomes[idx] = Outcome{
						StrategyID: s.ID,
						Success:    false,
						Error:      fmt.Sprintf("panic in strategy %s: %v", s.ID, r),
					}
					mu.Unlock()
				}
			}()

			// Check early stop before acquiring semaphore
			select {
			case <-earlyStop:
				mu.Lock()
				result.Cancelled++
				mu.Unlock()
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-execCtx.Done():
				mu.Lock()
				result.Cancelled++
				mu.Unlock()
				return
			}

			// Apply per-strategy timeout
			var stratCtx context.Context
			var stratCancel context.CancelFunc
			if spec.Timeout > 0 {
				stratCtx, stratCancel = context.WithTimeout(execCtx, spec.Timeout)
			} else {
				stratCtx, stratCancel = context.WithCancel(execCtx)
			}
			defer stratCancel()

			outcome := exec(stratCtx, s)
			outcome.StrategyID = s.ID

			// Score the outcome
			if spec.Scorer != nil {
				outcome.Score = spec.Scorer(outcome)
			}

			mu.Lock()
			outcomes[idx] = outcome

			// Early stop check
			if spec.EarlyStop && outcome.Score >= spec.StopThreshold && !stopped {
				stopped = true
				close(earlyStop)
			}
			mu.Unlock()
		}(i, strategy)
	}

	wg.Wait()
	result.Duration = time.Since(start)

	// Collect non-zero outcomes
	for _, o := range outcomes {
		if o.StrategyID != "" {
			result.Outcomes = append(result.Outcomes, o)
		}
	}

	// Sort by score descending
	sort.Slice(result.Outcomes, func(i, j int) bool {
		return result.Outcomes[i].Score > result.Outcomes[j].Score
	})

	// Pick winner
	if len(result.Outcomes) > 0 && result.Outcomes[0].Success {
		winner := result.Outcomes[0]
		result.Winner = &winner
	}

	return result
}

// ExtractInsights collects learnings from all outcomes for retry prompts.
func ExtractInsights(result *Result) []string {
	var insights []string
	seen := make(map[string]bool)

	for _, o := range result.Outcomes {
		for _, insight := range o.Insights {
			if !seen[insight] {
				seen[insight] = true
				insights = append(insights, insight)
			}
		}
		if !o.Success && o.Error != "" {
			msg := fmt.Sprintf("Strategy %q failed: %s", o.StrategyID, o.Error)
			if !seen[msg] {
				seen[msg] = true
				insights = append(insights, msg)
			}
		}
	}
	return insights
}

// GenerateStrategies creates N diverse strategies from a base prompt.
func GenerateStrategies(basePrompt string, approaches []string) []Strategy {
	strategies := make([]Strategy, len(approaches))
	for i, approach := range approaches {
		strategies[i] = Strategy{
			ID:   fmt.Sprintf("strategy-%d", i+1),
			Name: approach,
			Prompt: fmt.Sprintf("%s\n\nApproach: %s\n\nUse this specific approach to solve the task.",
				basePrompt, approach),
		}
	}
	return strategies
}

// CommonApproaches returns standard strategy variations for code tasks.
func CommonApproaches() []string {
	return []string{
		"Direct implementation: Write the simplest, most straightforward solution",
		"Test-first: Write tests first, then implement to make them pass",
		"Refactor-based: Restructure existing code to accommodate the change, then implement",
		"Minimal diff: Make the smallest possible change that satisfies the requirement",
	}
}
