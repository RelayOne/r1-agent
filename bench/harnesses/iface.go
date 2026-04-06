// Package harnesses defines the interface for AI coding tool harnesses
// and implementations for driving them from the benchmark framework.
package harnesses

import (
	"context"
	"time"
)

// Harness is an AI coding tool the bench can drive.
type Harness interface {
	Name() string
	Image() string
	Version() string
	Run(ctx context.Context, taskMount string) RunResult
}

// RunResult captures the outcome of a single harness invocation.
type RunResult struct {
	HarnessName      string        `json:"harness_name"`
	TaskID           string        `json:"task_id"`
	Started          time.Time     `json:"started"`
	Ended            time.Time     `json:"ended"`
	ExitCode         int           `json:"exit_code"`
	OutputFiles      []string      `json:"output_files"`
	AssistantTexts   []string      `json:"assistant_texts"`
	CostUSD          float64       `json:"cost_usd"`
	APICallCount     int           `json:"api_call_count"`
	InputTokens      int           `json:"input_tokens"`
	OutputTokens     int           `json:"output_tokens"`
	CacheReadTokens  int           `json:"cache_read_tokens"`
	CacheWriteTokens int           `json:"cache_write_tokens"`
	TimedOut         bool          `json:"timed_out"`
	OOMKilled        bool          `json:"oom_killed"`
	Error            string        `json:"error,omitempty"`
}

// Duration returns the wall-clock duration of the run.
func (r RunResult) Duration() time.Duration {
	return r.Ended.Sub(r.Started)
}
