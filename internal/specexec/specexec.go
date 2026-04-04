// Package specexec implements speculative parallel execution.
// Inspired by Devin's multi-approach exploration and branch/explorer:
//
// When a task has multiple viable approaches, run them in parallel
// across isolated worktrees and pick the winner. This is the key
// differentiator for handling ambiguous tasks:
// - Fork N approaches with different strategies/prompts
// - Each runs in an isolated git worktree
// - Score results by test pass rate, code quality, diff size
// - Select the best, discard the rest
// - Optionally merge insights from failed approaches into retry prompts
package specexec

import (
	"context"
	"fmt"
	"sort"
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
		score += 0.6 // no tests = assume pass
	}

	// Smaller diffs preferred (20% weight)
	if o.DiffLines > 0 {
		// Sigmoid-like decay: 100 lines = 1.0, 1000 lines = 0.5
		diffScore := 100.0 / (100.0 + float64(o.DiffLines))
		score += 0.2 * diffScore
	} else {
		score += 0.2
	}

	// Faster preferred (20% weight)
	if o.Duration > 0 {
		// 30s = 1.0, 5min = 0.1
		speedScore := 30.0 / (30.0 + o.Duration.Seconds())
		score += 0.2 * speedScore
	} else {
		score += 0.2
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
