package boulder

import (
	"testing"
	"time"
)

func TestTrackAndUpdateTask(t *testing.T) {
	dir := t.TempDir()
	e := New(dir, DefaultConfig())

	e.TrackTask("t1", "implement login", "wt-1")
	e.TrackTask("t2", "add tests", "wt-2")

	incomplete := e.IncompleteTasks()
	if len(incomplete) != 2 {
		t.Fatalf("expected 2 incomplete, got %d", len(incomplete))
	}

	e.UpdateStatus("t1", StatusComplete)
	incomplete = e.IncompleteTasks()
	if len(incomplete) != 1 {
		t.Fatalf("expected 1 incomplete, got %d", len(incomplete))
	}
	if incomplete[0].ID != "t2" {
		t.Errorf("expected t2, got %s", incomplete[0].ID)
	}
}

func TestScanNudgesIdleAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.IdleTimeout = 10 * time.Millisecond
	cfg.ScanInterval = 0
	e := New(dir, cfg)

	e.TrackTask("t1", "do work", "wt-1")
	e.RecordActivity()

	var nudged []string
	nudgeFn := func(taskID, msg string) bool {
		nudged = append(nudged, taskID)
		return true
	}

	// Not idle yet
	now := time.Now()
	sent := e.Scan(now, nudgeFn)
	if sent != 0 {
		t.Error("should not nudge when not idle")
	}

	// Wait for idle timeout
	now = now.Add(50 * time.Millisecond)
	sent = e.Scan(now, nudgeFn)
	if sent != 1 {
		t.Errorf("expected 1 nudge, got %d", sent)
	}
	if len(nudged) != 1 || nudged[0] != "t1" {
		t.Errorf("expected nudge for t1, got %v", nudged)
	}
}

func TestScanRespectsMaxNudges(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.IdleTimeout = 0
	cfg.ScanInterval = 0
	cfg.BaseBackoff = 0
	cfg.MaxNudges = 2
	e := New(dir, cfg)

	e.TrackTask("t1", "stuck task", "wt-1")

	nudgeFn := func(taskID, msg string) bool { return true }
	now := time.Now()

	e.Scan(now, nudgeFn) // nudge 1
	now = now.Add(time.Millisecond)
	e.Scan(now, nudgeFn) // nudge 2
	now = now.Add(time.Millisecond)
	sent := e.Scan(now, nudgeFn) // should be blocked
	if sent != 0 {
		t.Error("should not nudge beyond max")
	}
	if e.NudgeCount("t1") != 2 {
		t.Errorf("expected 2 nudges recorded, got %d", e.NudgeCount("t1"))
	}
}

func TestExponentialBackoff(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.IdleTimeout = 0
	cfg.ScanInterval = 0
	cfg.BaseBackoff = 100 * time.Millisecond
	cfg.BackoffMultiple = 2.0
	cfg.MaxFailures = 5
	cfg.MaxNudges = 10
	e := New(dir, cfg)

	e.TrackTask("t1", "task", "wt-1")

	// Fail delivery
	failFn := func(taskID, msg string) bool { return false }
	now := time.Now()

	for i := 0; i < 4; i++ {
		now = now.Add(time.Second) // enough time to pass backoff
		e.Scan(now, failFn)
	}

	if e.ConsecFailures() != 4 {
		t.Errorf("expected 4 failures, got %d", e.ConsecFailures())
	}
}

func TestPauseAfterMaxFailures(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.IdleTimeout = 0
	cfg.ScanInterval = 0
	cfg.BaseBackoff = 0
	cfg.MaxFailures = 2
	cfg.PauseDuration = time.Hour
	cfg.MaxNudges = 10
	e := New(dir, cfg)

	e.TrackTask("t1", "task", "wt-1")

	failFn := func(taskID, msg string) bool { return false }
	now := time.Now()

	e.Scan(now, failFn) // fail 1
	now = now.Add(time.Millisecond)
	e.Scan(now, failFn) // fail 2 → triggers pause

	now = now.Add(time.Millisecond)
	if !e.IsPaused(now) {
		t.Error("should be paused after max failures")
	}

	sent := e.Scan(now, func(string, string) bool { return true })
	if sent != 0 {
		t.Error("should not send during pause")
	}
}

func TestNoNudgeWhenAllComplete(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.IdleTimeout = 0
	cfg.ScanInterval = 0
	e := New(dir, cfg)

	e.TrackTask("t1", "done task", "wt-1")
	e.UpdateStatus("t1", StatusComplete)

	sent := e.Scan(time.Now(), func(string, string) bool { return true })
	if sent != 0 {
		t.Error("should not nudge when all tasks complete")
	}
}

func TestStatePersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()

	e1 := New(dir, cfg)
	e1.TrackTask("t1", "persisted task", "wt-1")
	e1.UpdateStatus("t1", StatusInProgress)

	// Reload
	e2 := New(dir, cfg)
	tasks := e2.IncompleteTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 persisted task, got %d", len(tasks))
	}
	if tasks[0].Description != "persisted task" {
		t.Errorf("expected 'persisted task', got %q", tasks[0].Description)
	}
}

func TestScanThrottle(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.IdleTimeout = 0
	cfg.ScanInterval = time.Hour // very long throttle
	cfg.BaseBackoff = 0
	e := New(dir, cfg)

	e.TrackTask("t1", "task", "wt-1")
	now := time.Now()

	nudgeFn := func(string, string) bool { return true }
	e.Scan(now, nudgeFn) // first scan goes through

	now = now.Add(time.Second)
	sent := e.Scan(now, nudgeFn) // throttled
	if sent != 0 {
		t.Error("second scan should be throttled")
	}
}

func TestDuplicateTrack(t *testing.T) {
	dir := t.TempDir()
	e := New(dir, DefaultConfig())

	e.TrackTask("t1", "first", "wt-1")
	e.TrackTask("t1", "updated", "wt-1")

	tasks := e.IncompleteTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task (deduplicated), got %d", len(tasks))
	}
	if tasks[0].Description != "updated" {
		t.Errorf("expected updated description, got %q", tasks[0].Description)
	}
}
