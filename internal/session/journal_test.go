package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, "test-session")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	defer j.Close()

	// Append various events
	if err := j.AppendMessage("user", "hello"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := j.AppendToolUse("Read", "tool-1", map[string]any{"path": "/foo"}); err != nil {
		t.Fatalf("AppendToolUse: %v", err)
	}
	if err := j.AppendToolResult("tool-1", "file contents", false); err != nil {
		t.Fatalf("AppendToolResult: %v", err)
	}
	if err := j.AppendCost("task-1", 0.05); err != nil {
		t.Fatalf("AppendCost: %v", err)
	}

	if j.Seq() != 4 {
		t.Errorf("expected seq 4, got %d", j.Seq())
	}

	// Replay
	entries, err := Replay(j.Path())
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
	if entries[0].Type != EventMessage {
		t.Errorf("expected message, got %s", entries[0].Type)
	}
	if entries[0].Text != "hello" {
		t.Errorf("expected 'hello', got %q", entries[0].Text)
	}
	if entries[1].Type != EventToolUse {
		t.Errorf("expected tool_use, got %s", entries[1].Type)
	}
	if entries[3].CostUSD != 0.05 {
		t.Errorf("expected cost 0.05, got %f", entries[3].CostUSD)
	}
}

func TestJournalReplayFiltered(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, "filter-test")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	j.AppendMessage("user", "hello")
	j.AppendToolUse("Read", "t1", nil)
	j.AppendToolResult("t1", "ok", false)
	j.AppendMessage("assistant", "done")

	msgs, err := ReplayFiltered(j.Path(), EventMessage)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}

	tools, err := ReplayFiltered(j.Path(), EventToolUse, EventToolResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Errorf("expected 2 tool events, got %d", len(tools))
	}
}

func TestJournalRotate(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, "rotate-test")
	if err != nil {
		t.Fatal(err)
	}

	j.AppendMessage("user", "before rotation")
	j.AppendMessage("assistant", "response")

	if err := j.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Old segment should exist
	segPath := filepath.Join(dir, "rotate-test.jsonl.0")
	if _, err := os.Stat(segPath); os.IsNotExist(err) {
		t.Error("expected segment file to exist")
	}

	// Old segment should have entries + compaction marker
	oldEntries, _ := Replay(segPath)
	if len(oldEntries) != 3 { // 2 messages + compaction marker
		t.Errorf("expected 3 entries in old segment, got %d", len(oldEntries))
	}

	// New journal should be empty, seq reset
	j.AppendMessage("user", "after rotation")
	if j.Seq() != 1 {
		t.Errorf("expected seq 1 after rotation, got %d", j.Seq())
	}

	newEntries, _ := Replay(j.Path())
	if len(newEntries) != 1 {
		t.Errorf("expected 1 entry in new segment, got %d", len(newEntries))
	}
	j.Close()
}

func TestJournalReopenContinuesSeq(t *testing.T) {
	dir := t.TempDir()

	// Write some entries
	j1, _ := NewJournal(dir, "reopen-test")
	j1.AppendMessage("user", "msg1")
	j1.AppendMessage("user", "msg2")
	j1.Close()

	// Reopen - seq should continue
	j2, _ := NewJournal(dir, "reopen-test")
	defer j2.Close()
	if j2.Seq() != 2 {
		t.Errorf("expected seq 2 on reopen, got %d", j2.Seq())
	}

	j2.AppendMessage("user", "msg3")
	entries, _ := Replay(j2.Path())
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	if entries[2].Seq != 3 {
		t.Errorf("expected seq 3 for new entry, got %d", entries[2].Seq)
	}
}
