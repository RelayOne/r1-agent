// Package bench is Stoke's self-evaluation infrastructure. It runs golden
// missions, collects metrics from the ledger and bus event logs, compares
// against baselines, and produces reports.
package bench

// MissionConfig describes a golden mission used for benchmarking.
type MissionConfig struct {
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Category    string   `yaml:"category"`  // greenfield, brownfield, bugfix, multi_branch, impossible, long_horizon, footgun
	Difficulty  string   `yaml:"difficulty"` // easy, medium, hard
	Intent      string   `yaml:"intent"`
	Acceptance  []string `yaml:"acceptance_criteria"`
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
