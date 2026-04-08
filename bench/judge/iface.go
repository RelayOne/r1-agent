package judge

import "context"

// Task is the benchmark task metadata.
type Task struct {
	ID                   string   `json:"id" yaml:"id"`
	Category             string   `json:"category" yaml:"category"`
	Title                string   `json:"title" yaml:"title"`
	Language             string   `json:"language" yaml:"language"`
	Difficulty           int      `json:"difficulty" yaml:"difficulty"`
	TimeLimitSeconds     int      `json:"time_limit_seconds" yaml:"time_limit_seconds"`
	CostLimitUSD         float64  `json:"cost_limit_usd" yaml:"cost_limit_usd"`
	PromptFile           string   `json:"prompt_file" yaml:"prompt_file"`
	InitialRepo          string   `json:"initial_repo" yaml:"initial_repo"`
	VisibleTests         string   `json:"visible_tests" yaml:"visible_tests"`
	HiddenTests          string   `json:"hidden_tests" yaml:"hidden_tests"`
	ReferencePatch       string   `json:"reference_patch" yaml:"reference_patch"`
	HiddenRequirements   []string `json:"hidden_requirements" yaml:"hidden_requirements"`
	ExpectedFailureModes []string `json:"expected_failure_modes" yaml:"expected_failure_modes"`
}

// Verdict is the judge's assessment of a harness run on a task.
type Verdict struct {
	Passed       bool     `json:"passed"`
	Score        float64  `json:"score"`         // 0.0-1.0
	HonestyScore float64  `json:"honesty_score"` // 0.0-1.0
	Reasons      []string `json:"reasons,omitempty"`
	Failures     []string `json:"failures,omitempty"`
}

// Judge evaluates a harness run against a task.
type Judge interface {
	Name() string
	Judge(ctx context.Context, task *Task, workspace string) Verdict
}
