// Package bench is Stoke's self-evaluation infrastructure. It runs golden
// missions, collects metrics from the ledger and bus event logs, compares
// against baselines, and produces reports.
package bench

import "errors"

// ErrFixtureBoundary is returned (wrapped) by Runner.Run when execution hit a
// known-acceptable limitation of running the substrate from a unit-test
// context — for example, a concern Template not registered in the minimal
// bench harness. Tests use errors.Is(err, ErrFixtureBoundary) to distinguish
// these expected boundary cases from real regressions; every other error
// must fail the test.
//
// Surfaced by audit/scan-test-quality.md (the "silently buries per-mission
// failures" finding) and the post-merge-audit-cleanup spec TASK-2.
var ErrFixtureBoundary = errors.New("bench: fixture boundary (expected in unit-test context)")

// MissionConfig describes a golden mission used for benchmarking. The JSON
// tags let testdata/missions/*.json fixtures decode through the same struct
// the legacy YAML loader uses.
type MissionConfig struct {
	ID          string   `yaml:"id" json:"id"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Category    string   `yaml:"category" json:"category"`     // greenfield, brownfield, bugfix, multi_branch, impossible, long_horizon, footgun
	Difficulty  string   `yaml:"difficulty" json:"difficulty"` // easy, medium, hard
	Intent      string   `yaml:"intent" json:"intent"`
	Acceptance  []string `yaml:"acceptance_criteria" json:"acceptance_criteria"`
}

// RunResult captures the outcome of executing a single golden mission.
type RunResult struct {
	MissionID       string
	TerminalState   string // converged, escalated, timed_out
	AcceptanceMet   int
	AcceptanceTotal int
	WallTimeMs      int64
	CostUSD         float64
	TokensUsed      int64
	LoopIterations  int
	TrustFirings    int
	DissentCount    int
	EscalationCount int
	LedgerCorrupted bool
}

// ComparisonResult holds the diff between a baseline and current run.
type ComparisonResult struct {
	Mission    string
	Baseline   RunResult
	Current    RunResult
	Regression bool
	Delta      map[string]float64
}
