package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/plan"
)

func TestGRPWOrdering(t *testing.T) {
	tasks := []plan.Task{
		{ID: "T4"},
		{ID: "T3", Dependencies: []string{"T2"}},
		{ID: "T2", Dependencies: []string{"T1"}},
		{ID: "T1"},
	}
	sorted := sortByGRPW(tasks)
	if sorted[0].ID != "T1" {
		t.Errorf("first=%s, want T1 (deepest chain)", sorted[0].ID)
	}
}

func TestParallelIndependent(t *testing.T) {
	tasks := []plan.Task{
		{ID: "A", Files: []string{"a.go"}},
		{ID: "B", Files: []string{"b.go"}},
		{ID: "C", Files: []string{"c.go"}},
	}
	p := &plan.Plan{Tasks: tasks}

	var maxConcurrent int32
	var current int32

	s := New(3)
	results, err := s.Run(context.Background(), p, func(ctx context.Context, task plan.Task) TaskResult {
		c := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if c <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&maxConcurrent, old, c) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("results=%d", len(results))
	}
	if maxConcurrent < 2 {
		t.Errorf("maxConcurrent=%d, expected >=2 for independent tasks", maxConcurrent)
	}
}

// TestCancelWithInFlightTaskReturns reproduces audit A003: cancelling the
// ctx while a task is executing must return ctx.Err(), not deadlock.
// Workers only wg.Done() via the receive path, so the ctx.Done branch has
// to drain in-flight results before wg.Wait().
func TestCancelWithInFlightTaskReturns(t *testing.T) {
	p := &plan.Plan{Tasks: []plan.Task{{ID: "slow"}}}
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	type runOut struct {
		results []TaskResult
		err     error
	}
	done := make(chan runOut, 1)
	s := New(1)
	go func() {
		results, err := s.Run(ctx, p, func(ctx context.Context, task plan.Task) TaskResult {
			close(started)
			<-ctx.Done() // honor cancellation like the engine runners do
			return TaskResult{TaskID: task.ID, Success: false, Error: ctx.Err()}
		})
		done <- runOut{results, err}
	}()

	<-started
	cancel()

	select {
	case out := <-done:
		if out.err != context.Canceled {
			t.Errorf("err=%v, want context.Canceled", out.err)
		}
		if len(out.results) != 1 {
			t.Errorf("in-flight result dropped: got %d results, want 1", len(out.results))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Scheduler.Run deadlocked on ctx cancel with an in-flight task")
	}
}

func TestFileConflictSequential(t *testing.T) {
	tasks := []plan.Task{
		{ID: "A", Files: []string{"shared.go"}},
		{ID: "B", Files: []string{"shared.go"}},
	}
	p := &plan.Plan{Tasks: tasks}

	var maxConcurrent int32
	var current int32

	s := New(4)
	results, err := s.Run(context.Background(), p, func(ctx context.Context, task plan.Task) TaskResult {
		c := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if c <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&maxConcurrent, old, c) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("results=%d", len(results))
	}
	if maxConcurrent > 1 {
		t.Errorf("maxConcurrent=%d, want 1 (file conflict should force sequential)", maxConcurrent)
	}
}

func TestDependencyOrdering(t *testing.T) {
	tasks := []plan.Task{
		{ID: "B", Dependencies: []string{"A"}},
		{ID: "A"},
	}
	p := &plan.Plan{Tasks: tasks}

	var order []string
	s := New(4)
	_, err := s.Run(context.Background(), p, func(ctx context.Context, task plan.Task) TaskResult {
		order = append(order, task.ID)
		return TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "A" {
		t.Errorf("order=%v, want [A B]", order)
	}
}

func TestFailedDependencyBlocksDownstream(t *testing.T) {
	tasks := []plan.Task{
		{ID: "A"},
		{ID: "B", Dependencies: []string{"A"}},
		{ID: "C", Dependencies: []string{"B"}},
	}
	p := &plan.Plan{Tasks: tasks}

	var executed []string
	s := New(4)
	results, err := s.Run(context.Background(), p, func(ctx context.Context, task plan.Task) TaskResult {
		executed = append(executed, task.ID)
		if task.ID == "A" {
			return TaskResult{TaskID: task.ID, Success: false, Error: fmt.Errorf("A failed")}
		}
		return TaskResult{TaskID: task.ID, Success: true}
	})

	// Should not error (blocked tasks are reported in results, not as scheduler error)
	if err != nil {
		t.Fatalf("unexpected scheduler error: %v", err)
	}

	// Only A should have executed -- B and C should be blocked
	if len(executed) != 1 || executed[0] != "A" {
		t.Errorf("executed=%v, want [A] only (B and C blocked by A's failure)", executed)
	}

	// All 3 should appear in results
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}

	// B and C should have error mentioning blocked dependency
	for _, r := range results {
		if r.TaskID == "B" || r.TaskID == "C" {
			if r.Error == nil {
				t.Errorf("task %s should have error (blocked by failed dep)", r.TaskID)
			}
		}
	}
}

func TestFailedTaskDoesNotBlockUnrelated(t *testing.T) {
	tasks := []plan.Task{
		{ID: "A"},
		{ID: "B"}, // no dependency on A
	}
	p := &plan.Plan{Tasks: tasks}

	var mu sync.Mutex
	var executed []string
	s := New(4)
	_, err := s.Run(context.Background(), p, func(ctx context.Context, task plan.Task) TaskResult {
		mu.Lock()
		executed = append(executed, task.ID)
		mu.Unlock()
		if task.ID == "A" {
			return TaskResult{TaskID: task.ID, Success: false, Error: fmt.Errorf("A failed")}
		}
		return TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both should execute -- B has no dependency on A
	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 2 {
		t.Errorf("executed=%v, want both A and B (independent)", executed)
	}
}

func TestWithSpecExecSelectsWinner(t *testing.T) {
	var calls atomic.Int32

	base := func(ctx context.Context, task plan.Task) TaskResult {
		calls.Add(1)
		success := task.Description != ""
		return TaskResult{TaskID: task.ID, Success: success, DurationMs: 10}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:  []string{"approach A", "approach B"},
		MaxParallel: 2,
		Timeout:     5 * time.Second,
	})

	result := wrapped(context.Background(), plan.Task{
		ID:          "T1",
		Description: "refactor auth module",
	})

	if !result.Success {
		t.Errorf("expected success, got failure: %v", result.Error)
	}
	if result.TaskID != "T1" {
		t.Errorf("task ID = %q, want T1", result.TaskID)
	}
	// At least one strategy must have been called
	if calls.Load() < 1 {
		t.Errorf("expected at least 1 strategy call, got %d", calls.Load())
	}
}

func TestWithSpecExecPrefersHigherQualityPlan(t *testing.T) {
	// Regression for audit A024: with DefaultScorer, plan-only
	// outcomes carried no test/diff signal, so the winner degenerated
	// to "fastest plan". A slower strategy that produces a structured
	// plan must now beat a faster strategy that produces nothing.
	const richPlan = `Plan:
1. Update internal/auth/token.go to add refresh-token support.
2. Add regression coverage in internal/auth/token_test.go.
3. Verify with go test ./internal/auth/... before committing.`

	var chosen atomic.Value // description executed in phase 2

	base := func(ctx context.Context, task plan.Task) TaskResult {
		if task.PlanOnly {
			if strings.Contains(task.Description, "approach QUALITY") {
				time.Sleep(50 * time.Millisecond) // slower, but substantive
				return TaskResult{TaskID: task.ID, Success: true, PlanOutput: richPlan}
			}
			return TaskResult{TaskID: task.ID, Success: true} // instant, empty plan
		}
		chosen.Store(task.Description)
		return TaskResult{TaskID: task.ID, Success: true}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:  []string{"approach FAST", "approach QUALITY"},
		MaxParallel: 2,
		Timeout:     5 * time.Second,
	})

	result := wrapped(context.Background(), plan.Task{
		ID:          "T1",
		Description: "refactor auth module",
	})
	if !result.Success {
		t.Fatalf("expected success, got failure: %v", result.Error)
	}

	got, _ := chosen.Load().(string)
	if got == "" {
		t.Fatal("phase 2 never executed a winning strategy")
	}
	if !strings.Contains(got, "approach QUALITY") {
		t.Errorf("winner = %q, want the structured-plan strategy (approach QUALITY) — faster empty plan must not win", got)
	}
}

func TestWithSpecExecMergesInsightsIntoWinnerPrompt(t *testing.T) {
	// Audit A111: learnings from failed explorations must reach the
	// winning strategy's real run instead of being discarded.
	var chosen atomic.Value

	base := func(ctx context.Context, task plan.Task) TaskResult {
		if task.PlanOnly {
			if strings.Contains(task.Description, "approach BAD") {
				return TaskResult{TaskID: task.ID, Success: false, Error: fmt.Errorf("nil pointer in fooHandler")}
			}
			time.Sleep(20 * time.Millisecond) // let the failing strategy land first
			return TaskResult{TaskID: task.ID, Success: true}
		}
		chosen.Store(task.Description)
		return TaskResult{TaskID: task.ID, Success: true}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:  []string{"approach BAD", "approach GOOD"},
		MaxParallel: 2,
		Timeout:     5 * time.Second,
	})

	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: "refactor auth module"})
	if !result.Success {
		t.Fatalf("expected success, got failure: %v", result.Error)
	}

	got, _ := chosen.Load().(string)
	if got == "" {
		t.Fatal("phase 2 never executed a winning strategy")
	}
	if !strings.Contains(got, "Learnings from other explored approaches") {
		t.Errorf("winner prompt missing insights header:\n%s", got)
	}
	if !strings.Contains(got, "nil pointer in fooHandler") {
		t.Errorf("winner prompt missing the failed strategy's learning:\n%s", got)
	}
}

func TestWithSpecExecAllFailedErrorCarriesInsights(t *testing.T) {
	// Audit A111: when every strategy fails, the returned error must
	// surface the collected insights so the workflow retry loop can
	// learn from them.
	base := func(ctx context.Context, task plan.Task) TaskResult {
		return TaskResult{TaskID: task.ID, Success: false, Error: fmt.Errorf("compile error in %s", task.ID)}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:  []string{"approach A", "approach B"},
		MaxParallel: 2,
		Timeout:     5 * time.Second,
	})

	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: "refactor auth module"})
	if result.Success {
		t.Fatal("expected failure when all strategies fail")
	}
	if result.Error == nil {
		t.Fatal("expected error when all strategies fail")
	}
	msg := result.Error.Error()
	if !strings.Contains(msg, "insights:") {
		t.Errorf("all-failed error missing insights: %q", msg)
	}
	if !strings.Contains(msg, "compile error") {
		t.Errorf("all-failed error missing failure learning: %q", msg)
	}
}

func TestWithSpecExecSkipsNonSpeculative(t *testing.T) {
	var calls atomic.Int32

	base := func(ctx context.Context, task plan.Task) TaskResult {
		calls.Add(1)
		return TaskResult{TaskID: task.ID, Success: true}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches: []string{"approach A", "approach B"},
		ShouldSpeculate: func(task plan.Task) bool {
			return task.Type == "refactor" // only speculate on refactor tasks
		},
	})

	// Non-refactor task should pass through directly
	result := wrapped(context.Background(), plan.Task{
		ID:   "T1",
		Type: "docs",
	})
	if !result.Success {
		t.Fatal("expected success")
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call (passthrough), got %d", calls.Load())
	}
}

func TestWithSpecExecNoApproaches(t *testing.T) {
	var calls atomic.Int32

	base := func(ctx context.Context, task plan.Task) TaskResult {
		calls.Add(1)
		return TaskResult{TaskID: task.ID, Success: true}
	}

	// Empty approaches = no speculation, should return base unchanged
	wrapped := WithSpecExec(base, SpecExecConfig{})

	result := wrapped(context.Background(), plan.Task{ID: "T1"})
	if !result.Success {
		t.Fatal("expected success")
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 call, got %d", calls.Load())
	}
}
