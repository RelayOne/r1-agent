package ralph

import (
	"context"
	"fmt"
	"testing"
)

func TestRunSuccessFirstAttempt(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3

	execute := func(_ context.Context, _ string) (bool, string, float64, error) {
		return true, "", 0.01, nil
	}
	verify := func(_ context.Context) (bool, string, error) {
		return true, "", nil
	}
	prompt := DefaultPromptBuilder("write hello world")

	result := Run(context.Background(), cfg, "write hello world", prompt, execute, verify)

	if !result.Success {
		t.Error("expected success")
	}
	if result.Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", result.Attempts)
	}
	if result.TotalCostUSD != 0.01 {
		t.Errorf("expected cost 0.01, got %f", result.TotalCostUSD)
	}
}

func TestRunRetryThenSucceed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	cfg.VerifyAfterExec = false

	attempt := 0
	execute := func(_ context.Context, _ string) (bool, string, float64, error) {
		attempt++
		if attempt == 1 {
			return false, "build failed: missing import", 0.01, nil
		}
		if attempt == 2 {
			return false, "test failed: assertion error", 0.01, nil
		}
		return true, "", 0.01, nil
	}
	prompt := DefaultPromptBuilder("fix import")

	result := Run(context.Background(), cfg, "fix import", prompt, execute, nil)

	if !result.Success {
		t.Error("expected success on 3rd attempt")
	}
	if result.Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", result.Attempts)
	}
}

func TestRunEscalateOnRepeatedFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxAttempts = 5
	cfg.EscalateAfter = 2

	execute := func(_ context.Context, _ string) (bool, string, float64, error) {
		return false, "build failed: same error", 0.01, nil
	}
	prompt := DefaultPromptBuilder("fix bug")

	result := Run(context.Background(), cfg, "fix bug", prompt, execute, nil)

	if result.Success {
		t.Error("expected failure")
	}
	if !result.Escalated {
		t.Error("expected escalation")
	}
	if result.Attempts != 2 {
		t.Errorf("expected 2 attempts before escalation, got %d", result.Attempts)
	}
}

func TestRunMaxAttemptsExhausted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	cfg.EscalateAfter = 10 // don't escalate
	cfg.VerifyAfterExec = false

	n := 0
	execute := func(_ context.Context, _ string) (bool, string, float64, error) {
		n++
		return false, fmt.Sprintf("error type %d", n), 0.01, nil // different errors each time
	}
	prompt := DefaultPromptBuilder("impossible task")

	result := Run(context.Background(), cfg, "impossible task", prompt, execute, nil)

	if result.Success {
		t.Error("expected failure")
	}
	if result.Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", result.Attempts)
	}
}

func TestRunVerificationFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	cfg.VerifyAfterExec = true

	attempt := 0
	execute := func(_ context.Context, _ string) (bool, string, float64, error) {
		return true, "", 0.01, nil // execution always succeeds
	}
	verify := func(_ context.Context) (bool, string, error) {
		attempt++
		if attempt < 3 {
			return false, "test suite has 2 failures", nil
		}
		return true, "", nil
	}
	prompt := DefaultPromptBuilder("fix tests")

	result := Run(context.Background(), cfg, "fix tests", prompt, execute, verify)

	if !result.Success {
		t.Error("expected success on 3rd attempt")
	}
	if result.Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", result.Attempts)
	}
}

func TestPickStrategy(t *testing.T) {
	state := &ExecutionState{Attempt: 1}
	if s := pickStrategy(state); s != StrategyDirect {
		t.Errorf("expected direct, got %s", s)
	}

	state.Attempt = 2
	if s := pickStrategy(state); s != StrategyIncremental {
		t.Errorf("expected incremental, got %s", s)
	}

	state.Attempt = 3
	if s := pickStrategy(state); s != StrategyMinimal {
		t.Errorf("expected minimal, got %s", s)
	}

	state.Attempt = 4
	state.History = []AttemptRecord{
		{FailClass: "build_failed"},
		{FailClass: "build_failed"},
		{FailClass: "build_failed"},
	}
	if s := pickStrategy(state); s != StrategyAlternate {
		t.Errorf("expected alternate for repeated failures, got %s", s)
	}
}

func TestDefaultPromptBuilderStrategy(t *testing.T) {
	builder := DefaultPromptBuilder("write code")

	state := &ExecutionState{Attempt: 1, Strategy: StrategyDirect}
	prompt := builder(state)
	if !containsStr(prompt, "directly") {
		t.Error("expected 'directly' in direct strategy prompt")
	}

	state = &ExecutionState{
		Attempt:     2,
		Strategy:    StrategyIncremental,
		Constraints: []string{"do not use global state"},
		History:     []AttemptRecord{{FailSummary: "build error"}},
	}
	prompt = builder(state)
	if !containsStr(prompt, "smallest possible") {
		t.Error("expected incremental instructions")
	}
	if !containsStr(prompt, "do not use global state") {
		t.Error("expected constraint in prompt")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{"Hello World", "hello-world"},
		{"fix_bug_123", "fix-bug-123"},
		{"a very long task description that exceeds fifty characters and should be truncated", "a-very-long-task-description-that-exceeds-fifty-ch"},
	}
	for _, tc := range tests {
		got := slugify(tc.in)
		if got != tc.out {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
