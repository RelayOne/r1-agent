package mission

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConvergedAnswerSingleRound(t *testing.T) {
	result, err := ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models:       []string{"claude", "codex"},
		ArbiterModel: "claude",
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness Judgment") {
				return "COMPLETE", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "The answer is: auth uses JWT tokens at auth.go:15", nil
			}
			return fmt.Sprintf("%s says: use JWT", model), nil
		},
		BiggerMission: "build auth system",
		Mission:       "implement login endpoint",
		StepName:      "test-single-round",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Converged {
		t.Fatal("expected convergence")
	}
	if result.Depth != 1 {
		t.Fatalf("expected depth 1, got %d", result.Depth)
	}
	if len(result.Rounds) != 1 {
		t.Fatalf("expected 1 round, got %d", len(result.Rounds))
	}
	if result.Answer == "" {
		t.Fatal("expected non-empty answer")
	}
	// Both models should have answered
	if len(result.Rounds[0].ModelAnswers) != 2 {
		t.Fatalf("expected 2 model answers, got %d", len(result.Rounds[0].ModelAnswers))
	}
}

func TestConvergedAnswerMultipleRounds(t *testing.T) {
	var arbiterCalls int32

	result, err := ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models:       []string{"claude", "codex"},
		ArbiterModel: "claude",
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness Judgment") {
				n := atomic.AddInt32(&arbiterCalls, 1)
				if n < 3 {
					return "INCOMPLETE: missing error handling and tests", nil
				}
				return "COMPLETE", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "synthesized answer with all parts", nil
			}
			return fmt.Sprintf("%s answer at depth", model), nil
		},
		BiggerMission: "build complete auth",
		Mission:       "implement login with tests",
		StepName:      "test-multi-round",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Converged {
		t.Fatal("expected convergence after 3 rounds")
	}
	if result.Depth != 3 {
		t.Fatalf("expected depth 3, got %d", result.Depth)
	}
	if len(result.Rounds) != 3 {
		t.Fatalf("expected 3 rounds, got %d", len(result.Rounds))
	}
	// First two rounds should be incomplete
	if result.Rounds[0].Complete {
		t.Error("round 0 should be incomplete")
	}
	if result.Rounds[1].Complete {
		t.Error("round 1 should be incomplete")
	}
	if !result.Rounds[2].Complete {
		t.Error("round 2 should be complete")
	}
}

func TestConvergedAnswerSafetyDepthLimit(t *testing.T) {
	result, err := ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models:       []string{"model-a"},
		ArbiterModel: "model-a",
		MaxDepth:     3,
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness Judgment") {
				return "INCOMPLETE: always more to do", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "partial answer", nil
			}
			return "model answer", nil
		},
		Mission:  "infinite task",
		StepName: "test-depth-limit",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Converged {
		t.Fatal("should NOT converge — depth limit should trigger")
	}
	if result.Depth != 3 {
		t.Fatalf("expected depth 3 (safety limit), got %d", result.Depth)
	}
	if len(result.Rounds) != 3 {
		t.Fatalf("expected 3 rounds, got %d", len(result.Rounds))
	}
}

func TestConvergedAnswerContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := ConvergedAnswer(ctx, ConvergedAnswerConfig{
		Models:       []string{"claude"},
		ArbiterModel: "claude",
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			return "answer", nil
		},
		Mission:  "cancelled",
		StepName: "test-cancel",
	})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if result.Depth != 0 {
		t.Fatalf("expected depth 0, got %d", result.Depth)
	}
}

func TestConvergedAnswerModelFailure(t *testing.T) {
	result, err := ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models:       []string{"good-model", "bad-model"},
		ArbiterModel: "good-model",
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if model == "bad-model" && !strings.Contains(prompt, "Arbiter") && !strings.Contains(prompt, "Completeness") {
				return "", fmt.Errorf("model offline")
			}
			if strings.Contains(prompt, "Completeness Judgment") {
				return "COMPLETE", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "only good-model's answer was usable", nil
			}
			return "good-model answer", nil
		},
		Mission:  "task with flaky model",
		StepName: "test-model-failure",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Converged {
		t.Fatal("should converge despite one model failing")
	}
	// bad-model's answer should contain error marker
	badAnswer := result.Rounds[0].ModelAnswers["bad-model"]
	if !strings.Contains(badAnswer, "ERROR") {
		t.Errorf("bad model answer should contain ERROR marker: %s", badAnswer)
	}
}

func TestConvergedAnswerAccumulatesContext(t *testing.T) {
	var modelRoundCount int32

	ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models:       []string{"claude"},
		ArbiterModel: "claude",
		MaxDepth:     3,
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness Judgment") {
				return "INCOMPLETE: need more detail", nil
			}
			if strings.Contains(prompt, "Your Role: Arbiter") {
				return "review of answers", nil
			}
			// This is a model prompt
			atomic.AddInt32(&modelRoundCount, 1)
			// After first round, the mission text should contain accumulated context
			if strings.Contains(prompt, "Previous Mission") {
				if !strings.Contains(prompt, "Arbiter Review") {
					t.Error("accumulated prompt should contain Arbiter Review")
				}
				if !strings.Contains(prompt, "NOT complete") {
					t.Error("accumulated prompt should explain previous round was incomplete")
				}
			}
			return "model answer", nil
		},
		BiggerMission: "the big picture",
		Mission:       "initial task",
		StepName:      "test-accumulation",
	})

	rounds := atomic.LoadInt32(&modelRoundCount)
	if rounds < 2 {
		t.Fatalf("expected at least 2 model rounds, got %d", rounds)
	}
}

func TestConvergedAnswerOnIterationCallback(t *testing.T) {
	var callbacks []struct {
		depth    int
		complete bool
	}

	ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models:       []string{"claude"},
		ArbiterModel: "claude",
		MaxDepth:     3,
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness Judgment") {
				return "INCOMPLETE: more work needed", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "review", nil
			}
			return "answer", nil
		},
		Mission:  "tracked task",
		StepName: "test-callback",
		OnIteration: func(depth int, review string, complete bool) {
			callbacks = append(callbacks, struct {
				depth    int
				complete bool
			}{depth, complete})
		},
	})

	if len(callbacks) != 3 {
		t.Fatalf("expected 3 callbacks, got %d", len(callbacks))
	}
	for i, cb := range callbacks {
		if cb.depth != i {
			t.Errorf("callback %d: expected depth %d, got %d", i, i, cb.depth)
		}
		if cb.complete {
			t.Errorf("callback %d: should not be complete", i)
		}
	}
}

func TestConvergedAnswerDefaultArbiter(t *testing.T) {
	// When ArbiterModel is empty, should default to first model
	var arbiterModel string
	ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models: []string{"first-model", "second-model"},
		// ArbiterModel intentionally empty
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			if strings.Contains(prompt, "Completeness Judgment") {
				arbiterModel = model
				return "COMPLETE", nil
			}
			if strings.Contains(prompt, "Arbiter") {
				return "combined answer", nil
			}
			return "answer", nil
		},
		Mission:  "test default arbiter",
		StepName: "test-default-arbiter",
	})

	if arbiterModel != "first-model" {
		t.Fatalf("expected default arbiter 'first-model', got %q", arbiterModel)
	}
}

func TestConvergedAnswerRequiresConfig(t *testing.T) {
	_, err := ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		Models: []string{"claude"},
		// AskFn missing
		Mission: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "AskFn") {
		t.Fatalf("expected AskFn error, got: %v", err)
	}

	_, err = ConvergedAnswer(context.Background(), ConvergedAnswerConfig{
		// Models missing
		AskFn: func(ctx context.Context, model, prompt string) (string, error) {
			return "", nil
		},
		Mission: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected models error, got: %v", err)
	}
}

func TestIsCompleteVerdict(t *testing.T) {
	tests := []struct {
		verdict string
		want    bool
	}{
		{"COMPLETE", true},
		{"Complete", true},
		{"COMPLETE — all criteria satisfied", true},
		{"INCOMPLETE: missing tests", false},
		{"INCOMPLETE", false},
		{"incomplete: gaps remain", false},
		{"maybe", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isCompleteVerdict(tt.verdict)
		if got != tt.want {
			t.Errorf("isCompleteVerdict(%q) = %v, want %v", tt.verdict, got, tt.want)
		}
	}
}
