package mission

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMicroConvergenceConvergesOnFirstTry(t *testing.T) {
	result, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		MaxIterations: 3,
		Scope:         "implement auth login",
		StepName:      "test-first-try",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "implemented auth login in auth.go:10", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return nil, nil // no gaps
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Converged {
		t.Fatal("expected convergence on first try")
	}
	if result.Iterations != 1 {
		t.Fatalf("expected 1 iteration, got %d", result.Iterations)
	}
	if result.FinalOutput != "implemented auth login in auth.go:10" {
		t.Fatalf("unexpected output: %s", result.FinalOutput)
	}
}

func TestMicroConvergenceFixesGaps(t *testing.T) {
	var callCount int32

	result, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		MaxIterations: 5,
		Scope:         "implement auth with error handling",
		StepName:      "test-fix-gaps",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 1 {
				return "implemented auth, happy path only", nil
			}
			if !strings.Contains(feedback, "error handling") {
				t.Error("feedback should mention error handling gap")
			}
			return "implemented auth with full error handling in auth.go:10-25", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			if !strings.Contains(output, "full error handling") {
				return []string{"missing error handling for invalid credentials"}, nil
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Converged {
		t.Fatal("expected convergence after fix")
	}
	if result.Iterations != 2 {
		t.Fatalf("expected 2 iterations, got %d", result.Iterations)
	}
	if len(result.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(result.History))
	}
}

func TestMicroConvergenceExhaustsIterations(t *testing.T) {
	result, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		MaxIterations: 3,
		Scope:         "impossible task",
		StepName:      "test-exhaust",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "partial work", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return []string{"still incomplete"}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Converged {
		t.Fatal("should not converge")
	}
	if result.Iterations != 3 {
		t.Fatalf("expected 3 iterations, got %d", result.Iterations)
	}
	if len(result.RemainingGaps) != 1 {
		t.Fatalf("expected 1 remaining gap, got %d", len(result.RemainingGaps))
	}
}

func TestMicroConvergenceExecuteError(t *testing.T) {
	_, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		MaxIterations: 3,
		Scope:         "fail",
		StepName:      "test-exec-error",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "", fmt.Errorf("model unavailable")
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected error from ExecuteFn")
	}
	if !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error should mention model unavailable: %v", err)
	}
}

func TestMicroConvergenceValidationErrorNonFatal(t *testing.T) {
	result, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		MaxIterations: 2,
		Scope:         "test validation error",
		StepName:      "test-val-error",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "output", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return nil, fmt.Errorf("validator crashed")
		},
	})
	if err != nil {
		t.Fatalf("validation error should be non-fatal: %v", err)
	}
	// Should not converge because validation error becomes a gap
	if result.Converged {
		t.Fatal("should not converge when validator errors")
	}
	if result.Iterations != 2 {
		t.Fatalf("expected 2 iterations, got %d", result.Iterations)
	}
}

func TestMicroConvergenceContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result, err := RunMicroConvergence(ctx, MicroConvergenceConfig{
		MaxIterations: 5,
		Scope:         "cancelled",
		StepName:      "test-cancel",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "output", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return nil, nil
		},
	})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if result.Iterations != 0 {
		t.Fatalf("expected 0 iterations, got %d", result.Iterations)
	}
}

func TestMicroConvergenceDefaultMaxIterations(t *testing.T) {
	result, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		// MaxIterations omitted — should default to 3
		Scope:    "default iterations",
		StepName: "test-default",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "partial", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return []string{"still incomplete"}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Iterations != 3 {
		t.Fatalf("expected default 3 iterations, got %d", result.Iterations)
	}
}

func TestMicroConvergenceFeedbackIncludesGaps(t *testing.T) {
	var receivedFeedback string

	RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		MaxIterations: 2,
		Scope:         "implement with tests",
		StepName:      "test-feedback",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			if feedback != "" {
				receivedFeedback = feedback
			}
			return "output", nil
		},
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return []string{"missing unit tests", "no error path coverage"}, nil
		},
	})

	if !strings.Contains(receivedFeedback, "missing unit tests") {
		t.Error("feedback should contain first gap")
	}
	if !strings.Contains(receivedFeedback, "no error path coverage") {
		t.Error("feedback should contain second gap")
	}
	if !strings.Contains(receivedFeedback, "implement with tests") {
		t.Error("feedback should contain scope")
	}
	if !strings.Contains(receivedFeedback, "CONVERGENCE FEEDBACK") {
		t.Error("feedback should have convergence header")
	}
}

func TestParseValidationGapsValid(t *testing.T) {
	gaps := ParseValidationGaps(`{"gaps": ["missing tests", "no error handling"]}`)
	if len(gaps) != 2 {
		t.Fatalf("expected 2 gaps, got %d", len(gaps))
	}
	if gaps[0] != "missing tests" || gaps[1] != "no error handling" {
		t.Fatalf("unexpected gaps: %v", gaps)
	}
}

func TestParseValidationGapsEmpty(t *testing.T) {
	gaps := ParseValidationGaps(`{"gaps": []}`)
	if len(gaps) != 0 {
		t.Fatalf("expected 0 gaps, got %d", len(gaps))
	}
}

func TestParseValidationGapsMarkdownFence(t *testing.T) {
	gaps := ParseValidationGaps("```json\n{\"gaps\": [\"gap1\"]}\n```")
	if len(gaps) != 1 || gaps[0] != "gap1" {
		t.Fatalf("unexpected gaps from markdown: %v", gaps)
	}
}

func TestParseValidationGapsEmbeddedJSON(t *testing.T) {
	gaps := ParseValidationGaps("Here are the gaps:\n{\"gaps\": [\"found a gap\"]}\nDone.")
	if len(gaps) != 1 || gaps[0] != "found a gap" {
		t.Fatalf("unexpected gaps from embedded JSON: %v", gaps)
	}
}

func TestParseValidationGapsUnparseable(t *testing.T) {
	gaps := ParseValidationGaps("this is not json at all")
	if len(gaps) != 1 {
		t.Fatalf("expected 1 fallback gap, got %d", len(gaps))
	}
	if !strings.Contains(gaps[0], "unparseable") {
		t.Fatalf("expected unparseable marker, got: %s", gaps[0])
	}
}

func TestMicroConvergenceRequiresCallbacks(t *testing.T) {
	_, err := RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		Scope:    "test",
		StepName: "test-no-execute",
		ValidateFn: func(ctx context.Context, scope, output string) ([]string, error) {
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "ExecuteFn") {
		t.Fatalf("expected error about missing ExecuteFn, got: %v", err)
	}

	_, err = RunMicroConvergence(context.Background(), MicroConvergenceConfig{
		Scope:    "test",
		StepName: "test-no-validate",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "output", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "ValidateFn") {
		t.Fatalf("expected error about missing ValidateFn, got: %v", err)
	}
}

// --- ConvergeStep Tests ---

func TestConvergeStepSingleModelReviewsExecution(t *testing.T) {
	// Even with ONE model, a fresh invocation reviewing the executor's
	// output catches what the executor missed. The reviewer has no sunk-cost
	// bias — it sees the work fresh and can say "no actually..."
	var execCount, reviewCount int32

	output, converged, err := ConvergeStep(context.Background(), convergeStepDeps{
		ModelAskFn: func(ctx context.Context, model, prompt string) (string, error) {
			atomic.AddInt32(&reviewCount, 1)
			if strings.Contains(prompt, "reviewing work") {
				// Fresh invocation reviewing executor's output
				n := atomic.LoadInt32(&execCount)
				if n < 2 {
					return "INCOMPLETE: missing error handling for auth failures", nil
				}
				return "COMPLETE", nil
			}
			return "model answer", nil
		},
		Models:       []string{"claude"},
		ArbiterModel: "claude",
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			n := atomic.AddInt32(&execCount, 1)
			if n == 1 {
				return "implemented login, happy path only", nil
			}
			return "implemented login with full error handling at auth.go:15-30", nil
		},
		Mission:  "implement login with error handling",
		StepName: "test-single-model-review",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !converged {
		t.Fatal("should converge after fixing gap")
	}

	execs := atomic.LoadInt32(&execCount)
	if execs < 2 {
		t.Fatalf("expected at least 2 executions (initial + fix), got %d", execs)
	}
	if output == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestConvergeStepModelAskFnAutoReviewsWithoutValidateStepFn(t *testing.T) {
	// When ModelAskFn is set but ValidateStepFn is NOT, ConvergeStep should
	// still use the model as a reviewer. No step should be single-shot when
	// a model is available to review it.
	var reviewCalled bool

	_, _, err := ConvergeStep(context.Background(), convergeStepDeps{
		ModelAskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "reviewing work") {
				reviewCalled = true
				return "COMPLETE", nil
			}
			return "answer", nil
		},
		Models:       []string{"claude"},
		ArbiterModel: "claude",
		// ValidateFn intentionally nil — model should auto-review
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "work done", nil
		},
		Mission:  "test auto-review",
		StepName: "test-auto-review",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reviewCalled {
		t.Fatal("model should have been used as reviewer even without ValidateStepFn")
	}
}

func TestConvergeStepPureModelQuery(t *testing.T) {
	// When there's no ExecuteFn, ConvergedAnswer should be used directly
	output, converged, err := ConvergeStep(context.Background(), convergeStepDeps{
		ModelAskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness") {
				return "COMPLETE", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "synthesized answer", nil
			}
			return "model answer", nil
		},
		Models:       []string{"claude"},
		ArbiterModel: "claude",
		Mission:      "answer a question",
		StepName:     "test-pure-query",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !converged {
		t.Fatal("should converge")
	}
	if output == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestConvergeStepFallsBackToSingleShot(t *testing.T) {
	// When nothing is configured except ExecuteFn, single-shot is the only option
	output, converged, err := ConvergeStep(context.Background(), convergeStepDeps{
		ExecuteFn: func(ctx context.Context, feedback string) (string, error) {
			return "single shot output", nil
		},
		Mission:  "no reviewer",
		StepName: "test-single-shot",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !converged {
		t.Fatal("single-shot should assume converged")
	}
	if output != "single shot output" {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestConvergeStepNoConfigError(t *testing.T) {
	_, _, err := ConvergeStep(context.Background(), convergeStepDeps{
		Mission:  "nothing configured",
		StepName: "test-no-config",
	})
	if err == nil {
		t.Fatal("expected error when nothing is configured")
	}
}

func TestParseReviewVerdict(t *testing.T) {
	tests := []struct {
		verdict  string
		wantGaps int
	}{
		{"COMPLETE", 0},
		{"COMPLETE — fully satisfies all criteria", 0},
		{"INCOMPLETE: missing tests", 1},
		{"INCOMPLETE: missing tests; no error handling; needs docs", 3},
		{"INCOMPLETE: gap one\ngap two\ngap three", 3},
		{"INCOMPLETE:", 1}, // "said incomplete but no specifics"
		{"gibberish", 1},  // unparseable
	}
	for _, tt := range tests {
		gaps := parseReviewVerdict(tt.verdict)
		if len(gaps) != tt.wantGaps {
			t.Errorf("parseReviewVerdict(%q): got %d gaps, want %d: %v", tt.verdict, len(gaps), tt.wantGaps, gaps)
		}
	}
}
