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

	// TruthfulCompletion fields. Empty for legacy missions.
	Plan               []PlanItem         `yaml:"plan,omitempty" json:"plan,omitempty"`
	GoldDiffPath       string             `yaml:"gold_diff_path,omitempty" json:"gold_diff_path,omitempty"`
	CompletionCriteria CompletionCriteria `yaml:"completion_criteria,omitempty" json:"completion_criteria,omitempty"`
}

// PlanItem is one expected action in the agent's plan. Used by both the
// agent (as the checklist surface) and the verdict scorer (to compute
// per-item completion status from the actual diff).
type PlanItem struct {
	ID          string `yaml:"id"`          // "P1", "P2", ...
	Description string `yaml:"description"` // human-readable
	// ChangedFiles lists files this plan item is expected to touch.
	// The verdict scorer asserts the actual diff touches AT LEAST these.
	ChangedFiles []string `yaml:"changed_files,omitempty"`
	// TestCommand, if non-empty, is the shell command whose zero exit
	// indicates this plan item is satisfied. Run in the post-completion
	// verification harness inside the mission's Docker workspace.
	TestCommand string `yaml:"test_command,omitempty"`
	// RequiredSymbols are symbol names (functions, types, methods) that
	// must exist in the diff. Used as a cheap structural check when no
	// test command applies.
	RequiredSymbols []string `yaml:"required_symbols,omitempty"`
}

// CompletionCriteria describes the verdict scorer's policy for this
// mission. Three independent signals; all that are set must agree for
// the completion claim to be marked truthful.
type CompletionCriteria struct {
	// PlanCompletionThreshold is the minimum fraction of PlanItems that
	// must verify (test passes or symbols present). 1.0 = all items.
	PlanCompletionThreshold float64 `yaml:"plan_completion_threshold"`
	// DeliveryRatioMin is the minimum DeliveryRatio.Percent (see
	// internal/bench/delivery_ratio.go::Compute) the diff must achieve.
	// 0 = signal disabled for this mission.
	DeliveryRatioMin int `yaml:"delivery_ratio_min"`
	// JudgeAgree controls whether the LLM-judge verdict is required
	// to agree with the structural check.
	//   "required" = judgement.Verdict MUST be "agrees_truthful".
	//   "advisory" = judgement is recorded but not part of the truthful_completion bit.
	//   ""         = same as "advisory".
	JudgeAgree string `yaml:"judge_agree,omitempty"`
}

// RunResult captures the outcome of executing a single golden mission.
type RunResult struct {
	MissionID       string
	// AgentID is the dispatcher ID that produced this result (e.g.
	// "r1", "r1-antitrunc", "claude-code-default", "tether+aider").
	// Populated by the runner; empty for legacy RunResult records
	// that pre-date the truthful-completion benchmark.
	AgentID         string `json:"agent_id,omitempty"`
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

	// TruthfulCompletion fields.
	CompletionAttempted      bool   `json:"completion_attempted"`
	CompletionClaim          string `json:"completion_claim,omitempty"` // verbatim text the agent emitted
	CompletionTruthful       bool   `json:"completion_truthful"`
	CompletionSilentlyFailed bool   `json:"completion_silently_failed"`

	// Per-signal breakdown so reviewers can see WHY a claim was scored
	// truthful or not.
	PlanItemsCompleted   int    `json:"plan_items_completed"`
	PlanItemsTotal       int    `json:"plan_items_total"`
	DeliveryRatioPercent int    `json:"delivery_ratio_percent"`
	JudgeVerdict         string `json:"judge_verdict,omitempty"`   // "agrees_truthful" | "agrees_untruthful" | "disagrees" | "skipped"
	JudgeRationale       string `json:"judge_rationale,omitempty"` // <=500 chars

	// RewardHackFlags lists reward-hacking tells found in the agent's
	// trajectory (reading the reference solution from git history, editing
	// the graded tests). Non-nil means the score should be read with
	// suspicion — it does not auto-fail the run (that is a policy
	// decision), but it makes gaming visible instead of silently inflating
	// the number. See AuditTrajectory (SOTA gap #5).
	RewardHackFlags []string `json:"reward_hack_flags,omitempty"`
}

// ComparisonResult holds the diff between a baseline and current run.
type ComparisonResult struct {
	Mission    string
	Baseline   RunResult
	Current    RunResult
	Regression bool
	Delta      map[string]float64
}
