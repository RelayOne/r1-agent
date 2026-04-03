// Package ralph implements persistent execution discipline with completion enforcement.
// Inspired by oh-my-codex's $ralph mode, which ensures tasks run to completion
// with architect-level verification, automatic retry on failure, and escalation
// when stuck.
//
// The core idea: an execution loop that refuses to give up. It runs the task,
// verifies the output, and if verification fails, it feeds the failure back
// as context and tries again — with escalating constraints and different strategies.
//
// Named after $ralph mode in OmX: "persistent execution loops with architect-level verification."
package ralph

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Strategy defines how to approach a task execution attempt.
type Strategy string

const (
	StrategyDirect     Strategy = "direct"      // Just do it
	StrategyIncremental Strategy = "incremental" // Break into smaller steps
	StrategyMinimal    Strategy = "minimal"      // Minimum viable change only
	StrategyAlternate  Strategy = "alternate"    // Try a completely different approach
)

// ExecutionState tracks the state of a persistent execution loop.
type ExecutionState struct {
	TaskID        string          `json:"task_id"`
	Description   string          `json:"description"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"max_attempts"`
	Strategy      Strategy        `json:"strategy"`
	History       []AttemptRecord `json:"history"`
	Constraints   []string        `json:"constraints"`   // accumulated constraints from failures
	LearnedFacts  []string        `json:"learned_facts"`  // things discovered during execution
	StartedAt     time.Time       `json:"started_at"`
	Deadline      time.Time       `json:"deadline"`        // hard deadline for the whole loop
	EscalateAfter int             `json:"escalate_after"`  // escalate after N same-class failures
}

// AttemptRecord captures everything about one execution attempt.
type AttemptRecord struct {
	Number       int           `json:"number"`
	Strategy     Strategy      `json:"strategy"`
	StartedAt    time.Time     `json:"started_at"`
	Duration     time.Duration `json:"duration"`
	Success      bool          `json:"success"`
	FailClass    string        `json:"fail_class,omitempty"`
	FailSummary  string        `json:"fail_summary,omitempty"`
	DiffSummary  string        `json:"diff_summary,omitempty"`
	CostUSD      float64       `json:"cost_usd"`
	Verified     bool          `json:"verified"`       // passed build/test/lint
	ReviewPassed bool          `json:"review_passed"`  // passed team review
}

// VerifyFunc checks if the task was completed correctly.
// Returns (pass, findings summary, error).
type VerifyFunc func(ctx context.Context) (bool, string, error)

// ExecuteFunc runs one attempt of the task with the given prompt.
// Returns (success, fail summary, cost, error).
type ExecuteFunc func(ctx context.Context, prompt string) (bool, string, float64, error)

// PromptBuilder generates the execution prompt for each attempt,
// incorporating history, constraints, and strategy.
type PromptBuilder func(state *ExecutionState) string

// Result is the outcome of the persistent execution loop.
type Result struct {
	Success      bool            `json:"success"`
	Attempts     int             `json:"attempts"`
	FinalVerdict string          `json:"final_verdict"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	WallClockMs  int64           `json:"wall_clock_ms"`
	Escalated    bool            `json:"escalated"`
	EscalateReason string        `json:"escalate_reason,omitempty"`
	History      []AttemptRecord `json:"history"`
}

// Config controls the persistent execution loop behavior.
type Config struct {
	MaxAttempts    int           // maximum total attempts (default: 5)
	EscalateAfter  int           // escalate after N same-class failures (default: 2)
	Deadline       time.Duration // hard time limit for the whole loop (default: 30min)
	VerifyAfterExec bool         // run verification after each attempt (default: true)
}

// DefaultConfig returns production-safe defaults.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:    5,
		EscalateAfter:  2,
		Deadline:       30 * time.Minute,
		VerifyAfterExec: true,
	}
}

// Run executes the persistent execution loop.
// It will retry with escalating strategies until:
// - The task succeeds and passes verification
// - Max attempts are exhausted
// - The deadline is reached
// - The same failure class repeats EscalateAfter times (escalate to human)
func Run(ctx context.Context, cfg Config, task string, buildPrompt PromptBuilder, execute ExecuteFunc, verify VerifyFunc) Result {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.EscalateAfter <= 0 {
		cfg.EscalateAfter = 2
	}
	if cfg.Deadline <= 0 {
		cfg.Deadline = 30 * time.Minute
	}

	start := time.Now()
	deadline := start.Add(cfg.Deadline)

	state := &ExecutionState{
		TaskID:        slugify(task),
		Description:   task,
		MaxAttempts:   cfg.MaxAttempts,
		Strategy:      StrategyDirect,
		StartedAt:     start,
		Deadline:      deadline,
		EscalateAfter: cfg.EscalateAfter,
	}

	result := Result{}

	for state.Attempt < cfg.MaxAttempts {
		// Check deadline
		if time.Now().After(deadline) {
			result.Escalated = true
			result.EscalateReason = "deadline exceeded"
			break
		}

		state.Attempt++
		state.Strategy = pickStrategy(state)

		attemptStart := time.Now()
		prompt := buildPrompt(state)

		// Execute
		success, failSummary, cost, err := execute(ctx, prompt)
		duration := time.Since(attemptStart)

		record := AttemptRecord{
			Number:    state.Attempt,
			Strategy:  state.Strategy,
			StartedAt: attemptStart,
			Duration:  duration,
			Success:   success,
			CostUSD:   cost,
		}
		result.TotalCostUSD += cost

		if err != nil {
			record.FailClass = "execution_error"
			record.FailSummary = err.Error()
			state.History = append(state.History, record)
			state.Constraints = append(state.Constraints, "Previous attempt hit execution error: "+err.Error())
			continue
		}

		if !success {
			record.FailSummary = failSummary
			record.FailClass = classifyFailure(failSummary)
			state.History = append(state.History, record)

			// Add constraint for next attempt
			state.Constraints = append(state.Constraints,
				fmt.Sprintf("Attempt %d failed (%s): %s", state.Attempt, record.FailClass, failSummary))

			// Check for repeated same-class failures with same summary (exact same error)
			if shouldEscalate(state) {
				result.Escalated = true
				result.EscalateReason = fmt.Sprintf("same failure repeated %d times",
					cfg.EscalateAfter)
				break
			}
			continue
		}

		// Verify if configured
		if cfg.VerifyAfterExec && verify != nil {
			verifyPass, verifyFindings, verifyErr := verify(ctx)
			record.Verified = verifyPass
			if verifyErr != nil {
				record.FailClass = "verify_error"
				record.FailSummary = verifyErr.Error()
				state.History = append(state.History, record)
				continue
			}
			if !verifyPass {
				record.Success = false
				record.FailClass = "verification_failed"
				record.FailSummary = verifyFindings
				state.History = append(state.History, record)
				state.Constraints = append(state.Constraints,
					fmt.Sprintf("Attempt %d passed execution but failed verification: %s", state.Attempt, verifyFindings))
				state.LearnedFacts = append(state.LearnedFacts,
					"Code must pass build/test/lint after changes")
				continue
			}
		} else {
			record.Verified = true
		}

		// Success!
		state.History = append(state.History, record)
		result.Success = true
		result.FinalVerdict = "completed and verified"
		break
	}

	result.Attempts = state.Attempt
	result.History = state.History
	result.WallClockMs = time.Since(start).Milliseconds()

	if !result.Success && !result.Escalated {
		result.FinalVerdict = fmt.Sprintf("failed after %d attempts", state.Attempt)
	}

	return result
}

// pickStrategy selects the execution strategy based on attempt history.
// Progressive escalation: direct → incremental → minimal → alternate
func pickStrategy(state *ExecutionState) Strategy {
	if state.Attempt <= 1 {
		return StrategyDirect
	}

	// Count failure types
	lastFailClass := ""
	sameClassCount := 0
	if len(state.History) > 0 {
		lastFailClass = state.History[len(state.History)-1].FailClass
		for _, h := range state.History {
			if h.FailClass == lastFailClass {
				sameClassCount++
			}
		}
	}

	switch {
	case state.Attempt == 2:
		return StrategyIncremental
	case state.Attempt == 3:
		return StrategyMinimal
	case sameClassCount >= 2:
		return StrategyAlternate // same failure twice → try completely different approach
	default:
		return StrategyAlternate
	}
}

// shouldEscalate checks if the loop should give up and escalate to human.
// Escalates when the exact same failure summary repeats EscalateAfter times,
// indicating the task description or approach is fundamentally wrong.
func shouldEscalate(state *ExecutionState) bool {
	if len(state.History) < state.EscalateAfter {
		return false
	}

	// Check last N attempts for exact same failure summary
	lastSummary := state.History[len(state.History)-1].FailSummary
	if lastSummary == "" {
		return false
	}
	count := 0
	for i := len(state.History) - 1; i >= 0 && i >= len(state.History)-state.EscalateAfter; i-- {
		if state.History[i].FailSummary == lastSummary {
			count++
		}
	}
	return count >= state.EscalateAfter
}

func classifyFailure(summary string) string {
	lc := strings.ToLower(summary)
	switch {
	case strings.Contains(lc, "build") || strings.Contains(lc, "compile"):
		return "build_failed"
	case strings.Contains(lc, "test"):
		return "tests_failed"
	case strings.Contains(lc, "lint"):
		return "lint_failed"
	case strings.Contains(lc, "timeout"):
		return "timeout"
	case strings.Contains(lc, "rate limit"):
		return "rate_limited"
	case strings.Contains(lc, "scope") || strings.Contains(lc, "wrong file"):
		return "scope_violation"
	default:
		return "unknown"
	}
}

// DefaultPromptBuilder returns a prompt builder that incorporates history and constraints.
func DefaultPromptBuilder(baseTask string) PromptBuilder {
	return func(state *ExecutionState) string {
		var sb strings.Builder

		sb.WriteString(fmt.Sprintf("Task: %s\n\n", baseTask))

		// Strategy-specific instructions
		switch state.Strategy {
		case StrategyDirect:
			sb.WriteString("Execute this task directly. Implement the full change.\n\n")
		case StrategyIncremental:
			sb.WriteString("STRATEGY: Break this into the smallest possible steps. Implement one step at a time, verifying after each.\n\n")
		case StrategyMinimal:
			sb.WriteString("STRATEGY: Make the MINIMUM viable change. No refactoring, no cleanup, no extras. Just the core functionality.\n\n")
		case StrategyAlternate:
			sb.WriteString("STRATEGY: The previous approaches failed. Try a COMPLETELY DIFFERENT approach. Rethink the solution from scratch.\n\n")
		}

		// Accumulated constraints from failures
		if len(state.Constraints) > 0 {
			sb.WriteString("## Constraints (from previous attempts — MUST follow)\n")
			for _, c := range state.Constraints {
				sb.WriteString(fmt.Sprintf("- %s\n", c))
			}
			sb.WriteString("\n")
		}

		// Learned facts
		if len(state.LearnedFacts) > 0 {
			sb.WriteString("## Known facts (discovered during execution)\n")
			for _, f := range state.LearnedFacts {
				sb.WriteString(fmt.Sprintf("- %s\n", f))
			}
			sb.WriteString("\n")
		}

		// Attempt context
		if state.Attempt > 1 {
			sb.WriteString(fmt.Sprintf("## This is attempt %d of %d\n", state.Attempt, state.MaxAttempts))
			lastAttempt := state.History[len(state.History)-1]
			sb.WriteString(fmt.Sprintf("Last attempt failed: %s\n", lastAttempt.FailSummary))
			sb.WriteString("You MUST avoid the same failure. Read the constraint list carefully.\n\n")
		}

		sb.WriteString("## Rules\n")
		sb.WriteString("- Run build/test/lint BEFORE claiming done\n")
		sb.WriteString("- If blocked, say BLOCKED with the specific reason\n")
		sb.WriteString("- Do NOT use type bypasses, lint suppressions, or test.skip()\n")

		return sb.String()
	}
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
