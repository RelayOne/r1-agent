package checkpoint

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTimeline_AppendAndList(t *testing.T) {
	dir := t.TempDir()
	tl, err := NewTimeline(dir, "run-1")
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	defer tl.Close()

	id1, err := tl.Checkpoint("sow-converted", "abc123", nil, 0, 0, "", nil)
	if err != nil {
		t.Fatalf("Checkpoint 1: %v", err)
	}
	if id1 != "CP-001" {
		t.Errorf("id1=%q want CP-001", id1)
	}

	id2, err := tl.Checkpoint("session-start:S1", "abc123", nil, 0.5, 3, "S1", map[string]any{"workers": 4})
	if err != nil {
		t.Fatalf("Checkpoint 2: %v", err)
	}
	if id2 != "CP-002" {
		t.Errorf("id2=%q want CP-002", id2)
	}

	entries, err := ListCheckpoints(dir)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Label != "sow-converted" {
		t.Errorf("entry[0].Label=%q", entries[0].Label)
	}
	if entries[1].SessionID != "S1" {
		t.Errorf("entry[1].SessionID=%q", entries[1].SessionID)
	}
	if entries[1].CostUSD != 0.5 {
		t.Errorf("entry[1].CostUSD=%f", entries[1].CostUSD)
	}
}

func TestTimeline_FindCheckpoint(t *testing.T) {
	dir := t.TempDir()
	tl, _ := NewTimeline(dir, "run-1")
	defer tl.Close()

	tl.Checkpoint("a", "", nil, 0, 0, "", nil)
	tl.Checkpoint("b", "def456", []string{"S1", "S2"}, 1.5, 10, "S3", nil)
	tl.Checkpoint("c", "", nil, 0, 0, "", nil)

	found, err := FindCheckpoint(dir, "CP-002")
	if err != nil {
		t.Fatalf("FindCheckpoint: %v", err)
	}
	if found == nil {
		t.Fatal("CP-002 not found")
	}
	if found.Label != "b" || found.GitSHA != "def456" {
		t.Errorf("wrong entry: %+v", found)
	}
	if len(found.CompletedSessions) != 2 || found.CompletedSessions[0] != "S1" {
		t.Errorf("completed sessions: %v", found.CompletedSessions)
	}

	notFound, _ := FindCheckpoint(dir, "CP-999")
	if notFound != nil {
		t.Error("CP-999 should not exist")
	}
}

func TestTimeline_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	tl, _ := NewTimeline(dir, "run-1")
	defer tl.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := tl.Checkpoint("concurrent", "", nil, float64(n), n, "", nil)
			if err != nil {
				t.Errorf("concurrent write %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	entries, _ := ListCheckpoints(dir)
	if len(entries) != 20 {
		t.Errorf("expected 20 entries from concurrent writes, got %d", len(entries))
	}
}

func TestTimeline_ContinuesSequenceOnReopen(t *testing.T) {
	dir := t.TempDir()

	tl1, _ := NewTimeline(dir, "run-1")
	tl1.Checkpoint("first", "", nil, 0, 0, "", nil)
	tl1.Close()

	tl2, _ := NewTimeline(dir, "run-2")
	id, _ := tl2.Checkpoint("second", "", nil, 0, 0, "", nil)
	tl2.Close()

	if id != "CP-002" {
		t.Errorf("reopened timeline should continue sequence; got %q want CP-002", id)
	}
}

func TestTimeline_EmptyWAL(t *testing.T) {
	dir := t.TempDir()
	entries, err := ListCheckpoints(dir)
	if err != nil {
		t.Fatalf("ListCheckpoints on empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestTimeline_WALSurvivesCrash(t *testing.T) {
	dir := t.TempDir()
	tl, _ := NewTimeline(dir, "run-1")
	tl.Checkpoint("before-crash", "sha1", []string{"S1"}, 2.0, 15, "S2", nil)
	// Simulate crash: close without explicit Close().
	tl.file.Close()

	// Verify WAL is readable after "crash".
	entries, err := ListCheckpoints(dir)
	if err != nil {
		t.Fatalf("ListCheckpoints after crash: %v", err)
	}
	if len(entries) != 1 || entries[0].Label != "before-crash" {
		t.Errorf("WAL not durable after crash: %+v", entries)
	}

	// Reopen and continue.
	tl2, _ := NewTimeline(dir, "run-2")
	tl2.Checkpoint("after-crash", "", nil, 0, 0, "", nil)
	tl2.Close()

	entries2, _ := ListCheckpoints(dir)
	if len(entries2) != 2 {
		t.Errorf("expected 2 entries after recovery, got %d", len(entries2))
	}
}

func TestTimeline_WALPath(t *testing.T) {
	dir := t.TempDir()
	tl, _ := NewTimeline(dir, "run-1")
	tl.Checkpoint("test", "", nil, 0, 0, "", nil)
	tl.Close()

	expected := filepath.Join(dir, ".stoke", "checkpoints", "timeline.jsonl")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("WAL not at expected path %s: %v", expected, err)
	}
}
