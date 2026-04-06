package evolver

import (
	"fmt"
	"strings"
)

// TaskTemplate describes a benchmark task that can be used as a seed for
// adversarial variant generation.
type TaskTemplate struct {
	ID           string   `json:"id"`
	Category     string   `json:"category"`
	Title        string   `json:"title"`
	Prompt       string   `json:"prompt"`
	Difficulty   int      `json:"difficulty"`
	Tags         []string `json:"tags,omitempty"`
	ReferenceDiff string  `json:"reference_diff,omitempty"`
}

// GeneratedTask is a harder variant produced by the evolver.
type GeneratedTask struct {
	// ParentID is the ID of the seed task this was derived from.
	ParentID string `json:"parent_id"`

	// ID is the unique identifier for the generated task.
	ID string `json:"id"`

	// Title describes the generated variant.
	Title string `json:"title"`

	// Prompt is the task prompt text.
	Prompt string `json:"prompt"`

	// Difficulty is the estimated difficulty (higher than parent).
	Difficulty int `json:"difficulty"`

	// Category inherits from the parent.
	Category string `json:"category"`

	// TargetFailureClass is the failure class this variant is designed to expose.
	TargetFailureClass FailureClass `json:"target_failure_class"`

	// Rationale explains why this variant is harder.
	Rationale string `json:"rationale"`
}

// Strategy defines how to generate a harder variant for a given failure class.
type Strategy struct {
	// Class is the failure class this strategy targets.
	Class FailureClass

	// PromptModifier returns a modified prompt that makes the task harder
	// for agents that exhibit this failure class.
	PromptModifier func(original string) string

	// DifficultyBump is added to the parent difficulty.
	DifficultyBump int

	// Rationale explains the hardening approach.
	Rationale string
}

// DefaultStrategies returns the built-in set of adversarial generation strategies.
func DefaultStrategies() []Strategy {
	return []Strategy{
		{
			Class:          FailureBuildError,
			DifficultyBump: 1,
			Rationale:      "Adds cross-package dependencies that require coordinated changes across multiple files.",
			PromptModifier: func(p string) string {
				return p + "\n\nConstraint: the solution must compile cleanly with all dependent packages. " +
					"No stub implementations are acceptable."
			},
		},
		{
			Class:          FailureTestFailure,
			DifficultyBump: 1,
			Rationale:      "Adds edge cases and property-based test requirements.",
			PromptModifier: func(p string) string {
				return p + "\n\nAdditional requirement: handle all edge cases including empty inputs, " +
					"nil values, concurrent access, and boundary conditions."
			},
		},
		{
			Class:          FailureScopeCreep,
			DifficultyBump: 1,
			Rationale:      "Adds explicit scope constraints that penalize extraneous changes.",
			PromptModifier: func(p string) string {
				return p + "\n\nScope constraint: only modify the files strictly necessary. " +
					"Any changes to unrelated files will be penalized."
			},
		},
		{
			Class:          FailureTimeout,
			DifficultyBump: 2,
			Rationale:      "Reduces time budget to stress planning efficiency.",
			PromptModifier: func(p string) string {
				return p + "\n\nTime pressure: this task has a reduced time limit. " +
					"Prioritize a working solution over a perfect one."
			},
		},
		{
			Class:          FailureCheating,
			DifficultyBump: 2,
			Rationale:      "Adds hidden tests and integrity checks that detect gaming.",
			PromptModifier: func(p string) string {
				return p + "\n\nIntegrity notice: hidden test suites will verify correctness. " +
					"Do not modify any existing test files."
			},
		},
		{
			Class:          FailurePartialSolution,
			DifficultyBump: 1,
			Rationale:      "Requires completeness verification of all sub-tasks.",
			PromptModifier: func(p string) string {
				return p + "\n\nCompleteness requirement: all parts of this task must be fully implemented. " +
					"Partial solutions will not receive credit."
			},
		},
		{
			Class:          FailureWrongApproach,
			DifficultyBump: 2,
			Rationale:      "Adds architectural constraints that guide away from naive approaches.",
			PromptModifier: func(p string) string {
				return p + "\n\nArchitecture constraint: consider the existing codebase patterns before " +
					"choosing your implementation approach. Follow established conventions."
			},
		},
		{
			Class:          FailureRegressions,
			DifficultyBump: 1,
			Rationale:      "Adds regression-prone code paths near the change site.",
			PromptModifier: func(p string) string {
				return p + "\n\nRegression warning: the area you are modifying has fragile dependencies. " +
					"Ensure all existing tests continue to pass."
			},
		},
		{
			Class:          FailureMergeConflict,
			DifficultyBump: 1,
			Rationale:      "Tasks require changes to high-contention files.",
			PromptModifier: func(p string) string {
				return p + "\n\nConcurrency note: other agents may be modifying nearby code. " +
					"Keep your changes minimal and isolated."
			},
		},
	}
}

// Generate produces adversarial task variants from seed tasks, targeting the
// given failure patterns. For each (seed, pattern) pair where a matching strategy
// exists, a harder variant is produced.
func Generate(seeds []TaskTemplate, failures []FailurePattern) []GeneratedTask {
	strategies := make(map[FailureClass]Strategy)
	for _, s := range DefaultStrategies() {
		strategies[s.Class] = s
	}

	var generated []GeneratedTask
	counter := 0

	for _, fp := range failures {
		strat, ok := strategies[fp.Class]
		if !ok {
			continue
		}

		for _, seed := range seeds {
			// Only generate variants for tasks that actually failed with this pattern.
			if !contains(fp.TaskIDs, seed.ID) {
				continue
			}

			counter++
			gt := GeneratedTask{
				ParentID:           seed.ID,
				ID:                 fmt.Sprintf("%s-evolved-%d", seed.ID, counter),
				Title:              fmt.Sprintf("[%s hardened] %s", fp.Class, seed.Title),
				Prompt:             strat.PromptModifier(seed.Prompt),
				Difficulty:         seed.Difficulty + strat.DifficultyBump,
				Category:           seed.Category,
				TargetFailureClass: fp.Class,
				Rationale:          strat.Rationale,
			}
			generated = append(generated, gt)
		}
	}

	return generated
}

// GenerateSummary produces a human-readable summary of the generated tasks.
func GenerateSummary(tasks []GeneratedTask) string {
	if len(tasks) == 0 {
		return "No adversarial variants generated."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Generated %d adversarial variants:\n", len(tasks))

	byClass := make(map[FailureClass]int)
	for _, t := range tasks {
		byClass[t.TargetFailureClass]++
	}

	for cls, count := range byClass {
		fmt.Fprintf(&sb, "  %s: %d variants\n", cls, count)
	}

	return sb.String()
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
