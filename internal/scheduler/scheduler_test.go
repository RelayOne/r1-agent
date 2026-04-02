package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
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
			if c <= old { break }
			if atomic.CompareAndSwapInt32(&maxConcurrent, old, c) { break }
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil { t.Fatal(err) }
	if len(results) != 3 { t.Errorf("results=%d", len(results)) }
	if maxConcurrent < 2 {
		t.Errorf("maxConcurrent=%d, expected >=2 for independent tasks", maxConcurrent)
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
			if c <= old { break }
			if atomic.CompareAndSwapInt32(&maxConcurrent, old, c) { break }
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil { t.Fatal(err) }
	if len(results) != 2 { t.Errorf("results=%d", len(results)) }
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
	if err != nil { t.Fatal(err) }
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
	if err != nil { t.Fatalf("unexpected scheduler error: %v", err) }

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
	if err != nil { t.Fatal(err) }

	// Both should execute -- B has no dependency on A
	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 2 {
		t.Errorf("executed=%v, want both A and B (independent)", executed)
	}
}
