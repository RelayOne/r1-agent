package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSnapshotRoundTrip proves a collector's telemetry survives the
// write→read cycle with its aggregate metrics intact (O6 persist path).
func TestSnapshotRoundTrip(t *testing.T) {
	c := New()
	c.Record(Event{Name: "task.start", Category: "task", Success: true})
	c.Record(Event{Name: "task.end", Category: "task", Duration: 2 * time.Second, Success: true, Cost: 0.25})
	c.Record(Event{Name: "task.end", Category: "task", Duration: 4 * time.Second, Success: false, Cost: 0.75})

	dir := filepath.Join(t.TempDir(), "telemetry")
	snap := c.Snapshot("20260702T120000.000000000Z")
	path, err := WriteSnapshot(dir, snap)
	if err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("snapshot written outside dir: %s", path)
	}

	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if got.RunID != snap.RunID {
		t.Errorf("run id: got %q, want %q", got.RunID, snap.RunID)
	}
	if got.Summary.TotalEvents != 3 {
		t.Errorf("total events: got %d, want 3", got.Summary.TotalEvents)
	}
	if got.Summary.Successes != 2 || got.Summary.Failures != 1 {
		t.Errorf("success/failure: got %d/%d, want 2/1", got.Summary.Successes, got.Summary.Failures)
	}
	if got.Summary.TotalCost != 1.0 {
		t.Errorf("total cost: got %v, want 1.0", got.Summary.TotalCost)
	}
	// Percentiles for the "task" category survive the round trip.
	if len(got.TaskPercentiles) == 0 {
		t.Error("task percentiles empty after round trip")
	}
}

// TestWriteSnapshotSanitizesRunID proves a hostile run id cannot escape
// the telemetry directory via path traversal.
func TestWriteSnapshotSanitizesRunID(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteSnapshot(dir, Snapshot{RunID: "../../etc/passwd", CapturedAt: time.Now()})
	if err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("sanitized path escaped dir: %s (dir=%s)", path, dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected sanitized file to exist: %v", err)
	}
}

// TestListSnapshotsOrdering proves ListSnapshots returns snapshots
// oldest→newest (the order `r1 stats telemetry` relies on to diff the
// newest two) and treats a missing dir as empty, not an error.
func TestListSnapshotsOrdering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "telemetry")

	// Missing dir → empty, no error.
	empty, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots(missing): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("missing dir should list empty, got %d", len(empty))
	}

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// Write out of order to prove the sort, not the write order.
	for _, s := range []Snapshot{
		{RunID: "run-c", CapturedAt: base.Add(2 * time.Hour)},
		{RunID: "run-a", CapturedAt: base},
		{RunID: "run-b", CapturedAt: base.Add(1 * time.Hour)},
	} {
		if _, err := WriteSnapshot(dir, s); err != nil {
			t.Fatalf("WriteSnapshot %s: %v", s.RunID, err)
		}
	}

	got, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	want := []string{"run-a", "run-b", "run-c"}
	if len(got) != len(want) {
		t.Fatalf("got %d snapshots, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].RunID != w {
			t.Errorf("snapshot[%d] = %q, want %q (not sorted oldest→newest)", i, got[i].RunID, w)
		}
	}
}
