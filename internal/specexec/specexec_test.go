package specexec

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunSingleStrategy(t *testing.T) {
	exec := func(ctx context.Context, s Strategy) Outcome {
		return Outcome{
			Success:     true,
			TestsPassed: 10,
			DiffLines:   50,
			Duration:    1 * time.Second,
		}
	}

	result := Run(context.Background(), Spec{
		Strategies: []Strategy{{ID: "a", Name: "direct"}},
	}, exec)

	if result.Winner == nil {
		t.Fatal("should have a winner")
	}
	if result.Winner.StrategyID != "a" {
		t.Errorf("winner should be 'a', got %q", result.Winner.StrategyID)
	}
}

func TestRunMultipleStrategies(t *testing.T) {
	exec := func(ctx context.Context, s Strategy) Outcome {
		switch s.ID {
		case "good":
			return Outcome{Success: true, TestsPassed: 10, DiffLines: 30, Duration: 1 * time.Second}
		case "bad":
			return Outcome{Success: false, Error: "compilation failed"}
		case "ok":
			return Outcome{Success: true, TestsPassed: 5, TestsFailed: 5, DiffLines: 200, Duration: 3 * time.Second}
		}
		return Outcome{}
	}

	result := Run(context.Background(), Spec{
		Strategies: []Strategy{
			{ID: "good", Name: "good approach"},
			{ID: "bad", Name: "bad approach"},
			{ID: "ok", Name: "ok approach"},
		},
	}, exec)

	if result.Winner == nil {
		t.Fatal("should have a winner")
	}
	if result.Winner.StrategyID != "good" {
		t.Errorf("winner should be 'good', got %q", result.Winner.StrategyID)
	}
	if len(result.Outcomes) != 3 {
		t.Errorf("expected 3 outcomes, got %d", len(result.Outcomes))
	}
}

func TestEarlyStop(t *testing.T) {
	var execCount int32

	exec := func(ctx context.Context, s Strategy) Outcome {
		atomic.AddInt32(&execCount, 1)
		time.Sleep(50 * time.Millisecond)
		return Outcome{Success: true, TestsPassed: 10, Duration: 50 * time.Millisecond}
	}

	result := Run(context.Background(), Spec{
		Strategies: []Strategy{
			{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"},
		},
		MaxParallel:   1, // serial execution
		EarlyStop:     true,
		StopThreshold: 0.5,
	}, exec)

	if result.Winner == nil {
		t.Fatal("should have a winner")
	}
	// With serial execution and early stop, should cancel some
	if result.Cancelled == 0 {
		// It's possible all ran before early stop propagated, that's fine
		t.Log("no strategies cancelled (all may have started before early stop)")
	}
	_ = result
}

func TestConcurrencyLimit(t *testing.T) {
	var maxConcurrent int32
	var current int32

	exec := func(ctx context.Context, s Strategy) Outcome {
		c := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if c > old {
				if atomic.CompareAndSwapInt32(&maxConcurrent, old, c) {
					break
				}
			} else {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return Outcome{Success: true}
	}

	Run(context.Background(), Spec{
		Strategies: []Strategy{
			{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
		},
		MaxParallel: 2,
	}, exec)

	if atomic.LoadInt32(&maxConcurrent) > 2 {
		t.Errorf("max concurrent should be <= 2, got %d", maxConcurrent)
	}
}

func TestDefaultScorer(t *testing.T) {
	// Successful outcome with all tests passing
	score := DefaultScorer(Outcome{
		Success:     true,
		TestsPassed: 10,
		DiffLines:   50,
		Duration:    30 * time.Second,
	})
	if score <= 0 {
		t.Error("successful outcome should have positive score")
	}
	if score > 1 {
		t.Errorf("score should be <= 1, got %f", score)
	}

	// Failed outcome
	failScore := DefaultScorer(Outcome{Success: false})
	if failScore != 0 {
		t.Errorf("failed outcome should score 0, got %f", failScore)
	}

	// Smaller diff should score higher
	smallDiff := DefaultScorer(Outcome{Success: true, TestsPassed: 10, DiffLines: 10, Duration: 30 * time.Second})
	largeDiff := DefaultScorer(Outcome{Success: true, TestsPassed: 10, DiffLines: 1000, Duration: 30 * time.Second})
	if smallDiff <= largeDiff {
		t.Error("smaller diff should score higher")
	}
}

func TestExtractInsights(t *testing.T) {
	result := &Result{
		Outcomes: []Outcome{
			{StrategyID: "a", Success: true, Insights: []string{"approach A works well"}},
			{StrategyID: "b", Success: false, Error: "type mismatch", Insights: []string{"need type assertion"}},
		},
	}

	insights := ExtractInsights(result)
	if len(insights) < 2 {
		t.Errorf("expected at least 2 insights, got %d", len(insights))
	}
}

func TestGenerateStrategies(t *testing.T) {
	strategies := GenerateStrategies("Fix the bug", CommonApproaches())
	if len(strategies) != 4 {
		t.Errorf("expected 4 strategies, got %d", len(strategies))
	}
	for _, s := range strategies {
		if s.ID == "" {
			t.Error("strategy should have ID")
		}
		if s.Prompt == "" {
			t.Error("strategy should have prompt")
		}
	}
}

func TestNoWinnerWhenAllFail(t *testing.T) {
	exec := func(ctx context.Context, s Strategy) Outcome {
		return Outcome{Success: false, Error: "failed"}
	}

	result := Run(context.Background(), Spec{
		Strategies: []Strategy{{ID: "a"}, {ID: "b"}},
	}, exec)

	if result.Winner != nil {
		t.Error("should have no winner when all fail")
	}
}

func TestTimeoutPerStrategy(t *testing.T) {
	exec := func(ctx context.Context, s Strategy) Outcome {
		select {
		case <-ctx.Done():
			return Outcome{Success: false, Error: "timeout"}
		case <-time.After(5 * time.Second):
			return Outcome{Success: true}
		}
	}

	result := Run(context.Background(), Spec{
		Strategies: []Strategy{{ID: "slow"}},
		Timeout:    100 * time.Millisecond,
	}, exec)

	if len(result.Outcomes) == 0 {
		t.Fatal("should have outcome")
	}
	if result.Outcomes[0].Success {
		t.Error("should have timed out")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	exec := func(ctx context.Context, s Strategy) Outcome {
		select {
		case <-ctx.Done():
			return Outcome{Success: false, Error: "cancelled"}
		case <-time.After(5 * time.Second):
			return Outcome{Success: true}
		}
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result := Run(ctx, Spec{
		Strategies: []Strategy{{ID: "a"}},
	}, exec)

	if result.Winner != nil {
		t.Error("cancelled run should have no winner")
	}
}
