package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreFromCheckpoint_FullCycle(t *testing.T) {
	dir := t.TempDir()

	// Simulate a run that completed S1 + S3 and is mid-S2.
	tl, _ := NewTimeline(dir, "run-1")
	tl.Checkpoint("sow-converted", "sha0", nil, 0, 0, "", nil)
	tl.Checkpoint("session-start:S1", "sha1", nil, 0.5, 5, "S1", nil)
	tl.Checkpoint("session-done:S1", "sha2", []string{"S1"}, 1.0, 10, "S1", nil)
	tl.Checkpoint("session-start:S3", "sha3", []string{"S1"}, 1.0, 10, "S3", nil)
	tl.Checkpoint("session-done:S3", "sha4", []string{"S1", "S3"}, 1.5, 15, "S3", nil)
	tl.Checkpoint("session-start:S2", "sha5", []string{"S1", "S3"}, 1.5, 15, "S2", nil)
	tl.Checkpoint("ac-attempt:S2:1", "sha5", []string{"S1", "S3"}, 2.0, 20, "S2", nil)
	tl.Checkpoint("ac-attempt:S2:2", "sha5", []string{"S1", "S3"}, 2.5, 25, "S2", nil)
	tl.Close()

	// Write session markers as if S1, S3, and S2 all completed.
	markerDir := filepath.Join(dir, ".stoke", "sow-state-markers")
	os.MkdirAll(markerDir, 0o755)
	os.WriteFile(filepath.Join(markerDir, "S1.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(markerDir, "S2.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(markerDir, "S3.json"), []byte(`{}`), 0o644)

	// Resume from CP-006 (session-start:S2) — S2's marker
	// should be DELETED because it's post-checkpoint work.
	rs, err := RestoreFromCheckpoint(dir, "CP-006")
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint: %v", err)
	}
	if rs.CheckpointID != "CP-006" {
		t.Errorf("id=%q want CP-006", rs.CheckpointID)
	}
	if rs.ResumeSessionID != "S2" {
		t.Errorf("resume session=%q want S2", rs.ResumeSessionID)
	}
	if rs.CostUSD != 1.5 {
		t.Errorf("cost=%f want 1.5", rs.CostUSD)
	}
	if !rs.Skip("S1") {
		t.Error("S1 should be skipped (completed before checkpoint)")
	}
	if !rs.Skip("S3") {
		t.Error("S3 should be skipped")
	}
	if rs.Skip("S2") {
		t.Error("S2 should NOT be skipped (it's the resume session)")
	}

	// Verify S2's marker was removed from disk.
	if _, err := os.Stat(filepath.Join(markerDir, "S2.json")); !os.IsNotExist(err) {
		t.Error("S2 marker should have been deleted on resume")
	}
	// S1 + S3 markers should still exist.
	if _, err := os.Stat(filepath.Join(markerDir, "S1.json")); err != nil {
		t.Error("S1 marker should still exist")
	}
	if _, err := os.Stat(filepath.Join(markerDir, "S3.json")); err != nil {
		t.Error("S3 marker should still exist")
	}
}

func TestRestoreFromCheckpoint_NotFound(t *testing.T) {
	dir := t.TempDir()
	tl, _ := NewTimeline(dir, "run-1")
	tl.Checkpoint("test", "", nil, 0, 0, "", nil)
	tl.Close()

	_, err := RestoreFromCheckpoint(dir, "CP-999")
	if err == nil {
		t.Fatal("expected error for missing checkpoint")
	}
}

func TestRestoreFromCheckpoint_EmptyWAL(t *testing.T) {
	dir := t.TempDir()
	_, err := RestoreFromCheckpoint(dir, "CP-001")
	if err == nil {
		t.Fatal("expected error for empty WAL")
	}
}

func TestPruneTimelineAfter(t *testing.T) {
	dir := t.TempDir()
	tl, _ := NewTimeline(dir, "run-1")
	tl.Checkpoint("a", "", nil, 0, 0, "", nil)
	tl.Checkpoint("b", "", nil, 0, 0, "", nil)
	tl.Checkpoint("c", "", nil, 0, 0, "", nil)
	tl.Checkpoint("d", "", nil, 0, 0, "", nil)
	tl.Close()

	if err := PruneTimelineAfter(dir, "CP-002"); err != nil {
		t.Fatalf("PruneTimelineAfter: %v", err)
	}

	entries, _ := ListCheckpoints(dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after prune, got %d", len(entries))
	}
	if entries[0].Label != "a" || entries[1].Label != "b" {
		t.Errorf("wrong entries: %v %v", entries[0].Label, entries[1].Label)
	}
}

func TestFormatCheckpointList(t *testing.T) {
	entries := []TimelineEntry{
		{ID: "CP-001", Label: "sow-converted", CostUSD: 0},
		{ID: "CP-002", Label: "session-start:S1", CostUSD: 0.5, TasksCompleted: 5, CompletedSessions: nil},
		{ID: "CP-003", Label: "session-done:S1", CostUSD: 1.0, TasksCompleted: 10, CompletedSessions: []string{"S1"}},
	}
	out := FormatCheckpointList(entries)
	if out == "" {
		t.Fatal("empty output")
	}
	if !contains(out, "CP-001") || !contains(out, "session-start:S1") || !contains(out, "S1") {
		t.Errorf("output missing expected content:\n%s", out)
	}
}

func TestFormatCheckpointList_Empty(t *testing.T) {
	out := FormatCheckpointList(nil)
	if !contains(out, "No checkpoints") {
		t.Errorf("expected 'No checkpoints', got %q", out)
	}
}

func TestResumeState_Skip(t *testing.T) {
	var nilRS *ResumeState
	if nilRS.Skip("S1") {
		t.Error("nil ResumeState should not skip")
	}
	rs := &ResumeState{CompletedSessions: map[string]bool{"S1": true}}
	if !rs.Skip("S1") {
		t.Error("S1 should be skipped")
	}
	if rs.Skip("S2") {
		t.Error("S2 should not be skipped")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsInner(s, sub))
}

func containsInner(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
