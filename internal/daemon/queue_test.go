package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempQueue(t *testing.T) *Queue {
	t.Helper()
	dir := t.TempDir()
	q, err := NewQueue(filepath.Join(dir, "queue.json"))
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func TestEnqueueAndNext(t *testing.T) {
	q := tempQueue(t)
	if err := q.Enqueue(&Task{ID: "a", Title: "first", EstimateBytes: 1000}); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if err := q.Enqueue(&Task{ID: "b", Title: "second", EstimateBytes: 1000}); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	t1, err := q.Next("worker-1")
	if err != nil || t1 == nil || t1.ID != "a" {
		t.Fatalf("expected task a, got %+v err=%v", t1, err)
	}
	if t1.State != StateRunning || t1.WorkerID != "worker-1" {
		t.Fatalf("task not marked running: %+v", t1)
	}

	t2, err := q.Next("worker-2")
	if err != nil || t2 == nil || t2.ID != "b" {
		t.Fatalf("expected task b, got %+v err=%v", t2, err)
	}

	t3, _ := q.Next("worker-3")
	if t3 != nil {
		t.Fatalf("expected nil (queue empty of queued), got %+v", t3)
	}
}

func TestPriorityOrdering(t *testing.T) {
	q := tempQueue(t)
	q.Enqueue(&Task{ID: "low", Priority: 1})
	q.Enqueue(&Task{ID: "high", Priority: 100})
	q.Enqueue(&Task{ID: "med", Priority: 50})

	first, _ := q.Next("w")
	if first.ID != "high" {
		t.Fatalf("expected high first, got %s", first.ID)
	}
	second, _ := q.Next("w")
	if second.ID != "med" {
		t.Fatalf("expected med second, got %s", second.ID)
	}
	third, _ := q.Next("w")
	if third.ID != "low" {
		t.Fatalf("expected low third, got %s", third.ID)
	}
}

func TestDuplicateID(t *testing.T) {
	q := tempQueue(t)
	if err := q.Enqueue(&Task{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Task{ID: "x"}); err != ErrDuplicateID {
		t.Fatalf("expected ErrDuplicateID, got %v", err)
	}
}

func TestIdempotencyKeyDeduplicatesLiveTask(t *testing.T) {
	q := tempQueue(t)
	first, dedup, err := q.EnqueueOrGet(&Task{ID: "x1", IdempotencyKey: "same", Title: "first"})
	if err != nil || dedup {
		t.Fatalf("first enqueue err=%v dedup=%v", err, dedup)
	}
	second, dedup, err := q.EnqueueOrGet(&Task{ID: "x2", IdempotencyKey: "same", Title: "second"})
	if err != nil {
		t.Fatalf("second enqueue err=%v", err)
	}
	if !dedup {
		t.Fatalf("expected second enqueue to deduplicate")
	}
	if second.ID != first.ID {
		t.Fatalf("dedup reused id %q want %q", second.ID, first.ID)
	}
	if got := len(q.List("")); got != 1 {
		t.Fatalf("queue len = %d want 1", got)
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")

	q, err := NewQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Task{ID: "persist-me", Title: "hello", EstimateBytes: 5000}); err != nil {
		t.Fatal(err)
	}

	// Reopen.
	q2, err := NewQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	got := q2.Get("persist-me")
	if got == nil || got.Title != "hello" || got.EstimateBytes != 5000 {
		t.Fatalf("did not persist: %+v", got)
	}
}

func TestCompleteUnderdelivered(t *testing.T) {
	q := tempQueue(t)
	q.Enqueue(&Task{ID: "u", EstimateBytes: 10000})
	q.Next("w")
	if err := q.Complete("u", 5000, "mission-1", "/tmp/PROOFS.md"); err != nil {
		t.Fatal(err)
	}
	got := q.Get("u")
	if got.State != StateDone {
		t.Fatalf("expected done state, got %s", got.State)
	}
	if got.ActualBytes != 5000 {
		t.Fatalf("actual bytes: %d", got.ActualBytes)
	}
	if got.DeltaPct == nil || *got.DeltaPct != 50 {
		t.Fatalf("expected delta 50%%, got %+v", got.DeltaPct)
	}
	if !got.Underdelivered {
		t.Fatalf("expected underdelivered flag")
	}
}

func TestCompleteOnTarget(t *testing.T) {
	q := tempQueue(t)
	q.Enqueue(&Task{ID: "ok", EstimateBytes: 10000})
	q.Next("w")
	q.Complete("ok", 9000, "mission-2", "")
	got := q.Get("ok")
	if got.Underdelivered {
		t.Fatalf("90%% should NOT be underdelivered")
	}
	if got.DeltaPct == nil || *got.DeltaPct != 90 {
		t.Fatalf("expected delta 90, got %+v", got.DeltaPct)
	}
}

func TestFailAndCancel(t *testing.T) {
	q := tempQueue(t)
	q.Enqueue(&Task{ID: "f"})
	q.Next("w")
	if err := q.Fail("f", "compile error"); err != nil {
		t.Fatal(err)
	}
	got := q.Get("f")
	if got.State != StateFailed || got.Error != "compile error" {
		t.Fatalf("fail not recorded: %+v", got)
	}

	q.Enqueue(&Task{ID: "c"})
	if err := q.Cancel("c"); err != nil {
		t.Fatal(err)
	}
	gotC := q.Get("c")
	if gotC.State != StateCancelled {
		t.Fatalf("cancel not recorded: %+v", gotC)
	}
	// Cancelling a finished task fails.
	if err := q.Cancel("f"); err == nil {
		t.Fatalf("expected error cancelling finished task")
	}
}

func TestResumeRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")

	q, _ := NewQueue(path)
	q.Enqueue(&Task{ID: "r1"})
	q.Enqueue(&Task{ID: "r2"})
	q.Next("w") // marks r1 running
	q.Next("w") // marks r2 running

	// Simulate crash by reopening.
	q2, _ := NewQueue(path)
	n, err := q2.ResumeRunning()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 resumed, got %d", n)
	}
	for _, id := range []string{"r1", "r2"} {
		if got := q2.Get(id); got.State != StateQueued || got.WorkerID != "" {
			t.Fatalf("task %s not reset: %+v", id, got)
		}
	}
}

func TestRetryRequeuesTaskWithDelay(t *testing.T) {
	q := tempQueue(t)
	q.Enqueue(&Task{ID: "retry", MaxAttempts: 3})
	task, err := q.Next("w")
	if err != nil || task == nil {
		t.Fatalf("Next err=%v task=%+v", err, task)
	}
	when := time.Now().UTC().Add(2 * time.Second)
	if err := q.Retry("retry", "timed out", "transient_timeout", "write proofs", when); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	got := q.Get("retry")
	if got.State != StateQueued {
		t.Fatalf("state = %s want queued", got.State)
	}
	if got.NextRetryAt == nil || got.NextRetryAt.Before(when.Add(-time.Second)) {
		t.Fatalf("next retry = %+v want around %s", got.NextRetryAt, when)
	}
	if got.ResumeCheckpoint != "write proofs" {
		t.Fatalf("resume checkpoint = %q", got.ResumeCheckpoint)
	}
	if ready := q.ReadyQueuedCount(); ready != 0 {
		t.Fatalf("ready queued count = %d want 0 before retry window", ready)
	}
}

func TestIdempotencyKeyAllowsReenqueueAfterFailure(t *testing.T) {
	q := tempQueue(t)
	if _, _, err := q.EnqueueOrGet(&Task{ID: "once", IdempotencyKey: "same"}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	q.Next("w")
	if err := q.Fail("once", "permanent"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	again, dedup, err := q.EnqueueOrGet(&Task{ID: "twice", IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if dedup {
		t.Fatalf("failed task should not deduplicate future enqueue")
	}
	if again.ID != "twice" {
		t.Fatalf("new task id = %q want twice", again.ID)
	}
}

func TestListAndCounts(t *testing.T) {
	q := tempQueue(t)
	q.Enqueue(&Task{ID: "a"})
	q.Enqueue(&Task{ID: "b"})
	q.Enqueue(&Task{ID: "c"})
	q.Next("w") // a -> running
	q.Complete("a", 100, "", "")
	q.Next("w") // b -> running
	q.Fail("b", "x")

	all := q.List("")
	if len(all) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(all))
	}
	queued := q.List(StateQueued)
	if len(queued) != 1 || queued[0].ID != "c" {
		t.Fatalf("expected only c queued, got %+v", queued)
	}
	counts := q.Counts()
	if counts[StateDone] != 1 || counts[StateFailed] != 1 || counts[StateQueued] != 1 {
		t.Fatalf("counts wrong: %+v", counts)
	}
}

func TestCorruptQueueFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewQueue(path); err == nil {
		t.Fatalf("expected parse error on corrupt queue")
	}
}

func TestJSONRoundTripStable(t *testing.T) {
	// Sanity: a Task survives a marshal/unmarshal round trip with all fields.
	now := mustParse(t, "2026-04-30T18:00:00Z")
	pct := 75
	orig := &Task{
		ID:             "rt",
		Title:          "round trip",
		Prompt:         "do thing",
		Repo:           "/repo",
		Runner:         "hybrid",
		EstimateBytes:  1000,
		ActualBytes:    750,
		DeltaPct:       &pct,
		Underdelivered: true,
		Priority:       5,
		State:          StateDone,
		EnqueuedAt:     now,
		StartedAt:      &now,
		FinishedAt:     &now,
		WorkerID:       "w-1",
		MissionID:      "m-1",
		ProofsPath:     "/PROOFS.md",
		Tags:           []string{"a", "b"},
		Meta:           map[string]string{"k": "v"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var got Task
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != orig.ID || got.Title != orig.Title || got.EstimateBytes != orig.EstimateBytes ||
		!got.Underdelivered || got.DeltaPct == nil || *got.DeltaPct != 75 ||
		len(got.Tags) != 2 || got.Meta["k"] != "v" {
		t.Fatalf("round-trip lost data: %+v", got)
	}
}
