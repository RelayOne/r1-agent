package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/plan"
)

// TestNoDispatchAfterCancel is the R5 regression: Scheduler.Run's dispatch
// section never checked ctx.Err(), so on Ctrl-C / timeout every still-queued
// ready task was launched into a dead context, failed with "context
// canceled", and got written up as a real task failure. With the guard, a
// cancelled context short-circuits dispatch: no queued task runs, no results
// are produced for them, and no failed-state entries are recorded.
func TestNoDispatchAfterCancel(t *testing.T) {
	tasks := []plan.Task{
		{ID: "A", Files: []string{"a.go"}},
		{ID: "B", Files: []string{"b.go"}},
		{ID: "C", Files: []string{"c.go"}},
	}
	p := &plan.Plan{Tasks: tasks}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // models Ctrl-C landing exactly as the run loop begins

	var calls int32
	s := New(3)
	results, err := s.Run(ctx, p, func(ctx context.Context, task plan.Task) TaskResult {
		atomic.AddInt32(&calls, 1)
		return TaskResult{TaskID: task.ID, Success: false, Error: ctx.Err()}
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v, want context.Canceled", err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("execFn called %d times after cancel; want 0 (no queued task dispatched)", n)
	}
	if len(results) != 0 {
		t.Errorf("results=%d, want 0 (undispatched tasks must produce no results)", len(results))
	}

	// No task may be recorded as failed for work that never ran — that is the
	// state corruption R5 fixes (phantom failed tasks on resume).
	s.stateMu.Lock()
	nfailed := len(s.failed)
	ncompleted := len(s.completed)
	s.stateMu.Unlock()
	if nfailed != 0 {
		t.Errorf("failed entries=%d, want 0 (no phantom failures)", nfailed)
	}
	if ncompleted != 0 {
		t.Errorf("completed entries=%d, want 0", ncompleted)
	}
}

// TestCancelMidRunDoesNotDispatchQueued cancels while one task is in flight and
// asserts that the dependent tasks that become ready are not dispatched into
// the dead context. The in-flight task's result is still drained and returned.
func TestCancelMidRunDoesNotDispatchQueued(t *testing.T) {
	tasks := []plan.Task{
		{ID: "A", Files: []string{"a.go"}},
		{ID: "B", Dependencies: []string{"A"}, Files: []string{"b.go"}},
		{ID: "C", Dependencies: []string{"A"}, Files: []string{"c.go"}},
	}
	p := &plan.Plan{Tasks: tasks}

	ctx, cancel := context.WithCancel(context.Background())
	startedA := make(chan struct{})
	release := make(chan struct{})
	var bcCalls int32
	var aCalls int32

	type runOut struct {
		results []TaskResult
		err     error
	}
	done := make(chan runOut, 1)

	s := New(3)
	go func() {
		results, err := s.Run(ctx, p, func(ctx context.Context, task plan.Task) TaskResult {
			if task.ID == "A" {
				atomic.AddInt32(&aCalls, 1)
				close(startedA)
				<-release
				return TaskResult{TaskID: "A", Success: true}
			}
			atomic.AddInt32(&bcCalls, 1)
			return TaskResult{TaskID: task.ID, Success: false, Error: ctx.Err()}
		})
		done <- runOut{results, err}
	}()

	<-startedA // A is in flight; B and C are queued behind their dep on A
	cancel()   // Ctrl-C mid-run
	close(release)

	select {
	case out := <-done:
		if !errors.Is(out.err, context.Canceled) {
			t.Errorf("err=%v, want context.Canceled", out.err)
		}
		if n := atomic.LoadInt32(&bcCalls); n != 0 {
			t.Errorf("dependent tasks B/C dispatched %d times after cancel; want 0", n)
		}
		if atomic.LoadInt32(&aCalls) != 1 {
			t.Errorf("A dispatched %d times, want 1", aCalls)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Scheduler.Run deadlocked on mid-run cancel")
	}
}
