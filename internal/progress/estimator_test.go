package progress

import (
	"testing"
	"time"
)

func TestNewEstimator(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "task a"},
		{ID: "b", Name: "task b"},
	})

	snap := e.Progress()
	if snap.Total != 2 {
		t.Errorf("expected 2 tasks, got %d", snap.Total)
	}
	if snap.Percentage != 0 {
		t.Errorf("expected 0%%, got %.1f%%", snap.Percentage)
	}
}

func TestStartComplete(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "task a", Weight: 1},
		{ID: "b", Name: "task b", Weight: 1},
	})

	e.Start("a")
	snap := e.Progress()
	if snap.Running != 1 {
		t.Errorf("expected 1 running, got %d", snap.Running)
	}

	e.Complete("a")
	snap = e.Progress()
	if snap.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", snap.Completed)
	}
	if snap.Percentage < 49 || snap.Percentage > 51 {
		t.Errorf("expected ~50%%, got %.1f%%", snap.Percentage)
	}
}

func TestFail(t *testing.T) {
	e := New([]Task{{ID: "a", Name: "task a"}})
	e.Start("a")
	e.Fail("a")

	snap := e.Progress()
	if snap.Failed != 1 {
		t.Error("expected 1 failed")
	}
}

func TestSkip(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "a", Weight: 1},
		{ID: "b", Name: "b", Weight: 1},
	})

	e.Skip("a")
	snap := e.Progress()
	if snap.Skipped != 1 {
		t.Error("expected 1 skipped")
	}
	// Skipped counts as done for progress
	if snap.Percentage < 49 || snap.Percentage > 51 {
		t.Errorf("skipped should count as progress, got %.1f%%", snap.Percentage)
	}
}

func TestRetry(t *testing.T) {
	e := New([]Task{{ID: "a", Name: "a"}})
	e.Start("a")
	e.Fail("a")
	e.Retry("a")

	snap := e.Progress()
	if snap.Pending != 1 {
		t.Error("retried task should be pending")
	}
}

func TestWeightedProgress(t *testing.T) {
	e := New([]Task{
		{ID: "small", Name: "small", Weight: 1},
		{ID: "big", Name: "big", Weight: 3},
	})

	e.Start("small")
	e.Complete("small")

	snap := e.Progress()
	// 1 out of 4 weight = 25%
	if snap.Percentage < 24 || snap.Percentage > 26 {
		t.Errorf("expected ~25%%, got %.1f%%", snap.Percentage)
	}
}

func TestDependencies(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "build"},
		{ID: "b", Name: "test", Dependencies: []string{"a"}},
		{ID: "c", Name: "deploy", Dependencies: []string{"b"}},
	})

	ready := e.Ready()
	if len(ready) != 1 || ready[0] != "a" {
		t.Errorf("only 'a' should be ready, got %v", ready)
	}

	blocked := e.Blocked()
	if len(blocked) != 2 {
		t.Errorf("expected 2 blocked, got %d", len(blocked))
	}

	e.Start("a")
	e.Complete("a")

	ready = e.Ready()
	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("'b' should be ready after 'a' completes, got %v", ready)
	}
}

func TestCriticalPath(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "a", Weight: 1},
		{ID: "b", Name: "b", Weight: 3, Dependencies: []string{"a"}},
		{ID: "c", Name: "c", Weight: 1},
	})

	path := e.CriticalPath()
	if len(path) < 2 {
		t.Errorf("critical path should have at least 2 tasks, got %d", len(path))
	}
}

func TestVelocityAndETA(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "a", Weight: 1},
		{ID: "b", Name: "b", Weight: 1},
		{ID: "c", Name: "c", Weight: 1},
	})

	// Simulate completing a task
	e.Start("a")
	e.tasks["a"].StartTime = time.Now().Add(-1 * time.Second)
	e.Complete("a")

	snap := e.Progress()
	if snap.Velocity == 0 {
		t.Error("velocity should be non-zero after completing a task")
	}
	if snap.ETA == 0 {
		t.Error("ETA should be non-zero with remaining tasks")
	}
}

func TestProgressBar(t *testing.T) {
	e := New([]Task{
		{ID: "a", Weight: 1},
		{ID: "b", Weight: 1},
	})

	bar := e.ProgressBar(20)
	if bar == "" {
		t.Error("progress bar should not be empty")
	}

	e.Start("a")
	e.Complete("a")
	e.Start("b")
	e.Complete("b")

	bar = e.ProgressBar(20)
	if bar == "" {
		t.Error("bar should not be empty at 100%")
	}
}

func TestSummary(t *testing.T) {
	e := New([]Task{
		{ID: "a", Name: "build", Weight: 1},
		{ID: "b", Name: "test", Weight: 2},
	})

	s := e.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestAllComplete(t *testing.T) {
	e := New([]Task{
		{ID: "a", Weight: 1},
		{ID: "b", Weight: 1},
	})

	e.Start("a")
	e.Complete("a")
	e.Start("b")
	e.Complete("b")

	snap := e.Progress()
	if snap.Percentage < 99.9 {
		t.Errorf("expected 100%%, got %.1f%%", snap.Percentage)
	}
	if snap.Pending != 0 || snap.Running != 0 {
		t.Error("all should be done")
	}
}

func TestDefaultWeight(t *testing.T) {
	e := New([]Task{{ID: "a", Name: "a"}})
	if e.tasks["a"].Weight != 1.0 {
		t.Error("default weight should be 1.0")
	}
}

func TestEmptyEstimator(t *testing.T) {
	e := New(nil)
	snap := e.Progress()
	if snap.Total != 0 {
		t.Error("empty estimator should have 0 tasks")
	}
}
