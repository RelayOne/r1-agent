// Package evolver implements the adversarial evolution loop for the Stoke
// benchmark framework. It collects failure patterns from benchmark results
// and generates harder task variants.
package evolver

import (
	"sort"
	"strings"
)

// FailureClass is a taxonomy tag for a failure pattern.
type FailureClass string

const (
	FailureBuildError      FailureClass = "build_error"
	FailureTestFailure     FailureClass = "test_failure"
	FailureScopeCreep      FailureClass = "scope_creep"
	FailureTimeout         FailureClass = "timeout"
	FailureCheating        FailureClass = "cheating"
	FailurePartialSolution FailureClass = "partial_solution"
	FailureWrongApproach   FailureClass = "wrong_approach"
	FailureRegressions     FailureClass = "regressions"
	FailureMergeConflict   FailureClass = "merge_conflict"
	FailureUnknown         FailureClass = "unknown"
)

// TaskResult captures the outcome of a single benchmark task execution.
type TaskResult struct {
	TaskID   string   `json:"task_id"`
	Harness  string   `json:"harness"`
	Category string   `json:"category"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
	DiffSize int      `json:"diff_size"`
	CostUSD  float64  `json:"cost_usd"`
	TimedOut bool     `json:"timed_out"`
	Cheating bool     `json:"cheating"`
}

// FailurePattern records a recurring failure with its frequency and taxonomy tag.
type FailurePattern struct {
	// Class is the taxonomy tag for this failure.
	Class FailureClass `json:"class"`

	// Pattern is a representative description or fingerprint.
	Pattern string `json:"pattern"`

	// Count is how many times this pattern was observed.
	Count int `json:"count"`

	// TaskIDs lists the tasks that exhibited this failure.
	TaskIDs []string `json:"task_ids"`

	// Harnesses lists the harnesses that exhibited this failure.
	Harnesses []string `json:"harnesses"`
}

// CollectFailures scans benchmark results for failure patterns and tags
// each against the failure taxonomy. Results are sorted by frequency
// (most common first).
func CollectFailures(results []TaskResult) []FailurePattern {
	// Accumulate by class.
	byClass := make(map[FailureClass]*FailurePattern)

	for _, r := range results {
		if r.Passed {
			continue
		}

		classes := classifyResult(r)
		for _, cls := range classes {
			fp, ok := byClass[cls]
			if !ok {
				fp = &FailurePattern{
					Class:   cls,
					Pattern: string(cls),
				}
				byClass[cls] = fp
			}
			fp.Count++
			fp.TaskIDs = appendUnique(fp.TaskIDs, r.TaskID)
			fp.Harnesses = appendUnique(fp.Harnesses, r.Harness)
		}
	}

	patterns := make([]FailurePattern, 0, len(byClass))
	for _, fp := range byClass {
		patterns = append(patterns, *fp)
	}

	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Count > patterns[j].Count
	})

	return patterns
}

// classifyResult determines which failure classes apply to a result.
func classifyResult(r TaskResult) []FailureClass {
	var classes []FailureClass

	if r.TimedOut {
		classes = append(classes, FailureTimeout)
	}
	if r.Cheating {
		classes = append(classes, FailureCheating)
	}

	for _, f := range r.Failures {
		fl := strings.ToLower(f)
		switch {
		case strings.Contains(fl, "build") || strings.Contains(fl, "compile"):
			classes = append(classes, FailureBuildError)
		case strings.Contains(fl, "test"):
			classes = append(classes, FailureTestFailure)
		case strings.Contains(fl, "scope"):
			classes = append(classes, FailureScopeCreep)
		case strings.Contains(fl, "partial"):
			classes = append(classes, FailurePartialSolution)
		case strings.Contains(fl, "approach") || strings.Contains(fl, "wrong"):
			classes = append(classes, FailureWrongApproach)
		case strings.Contains(fl, "regression"):
			classes = append(classes, FailureRegressions)
		case strings.Contains(fl, "merge") || strings.Contains(fl, "conflict"):
			classes = append(classes, FailureMergeConflict)
		}
	}

	if len(classes) == 0 {
		classes = append(classes, FailureUnknown)
	}

	return dedup(classes)
}

func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

func dedup(classes []FailureClass) []FailureClass {
	seen := make(map[FailureClass]bool, len(classes))
	out := classes[:0]
	for _, c := range classes {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}
