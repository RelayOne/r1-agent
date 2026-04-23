package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGhostWriteDetector_MissingFile verifies the detector fires when
// a write tool reports success but the target path is missing.
func TestGhostWriteDetector_MissingFile(t *testing.T) {
	dir := t.TempDir()
	var events []GhostWriteEvent
	check := NewGhostWriteCheck(dir, func(evt GhostWriteEvent) {
		events = append(events, evt)
	})

	input, _ := json.Marshal(map[string]string{"file_path": "src/foo.ts"})
	tools := []MidturnToolCall{
		{Name: "edit", Input: input, Result: "ok", IsError: false},
	}
	note := check(tools, 1)
	if note == "" {
		t.Fatalf("expected reminder note, got empty")
	}
	if !strings.Contains(note, "src/foo.ts") {
		t.Errorf("note should mention path: %q", note)
	}
	if !strings.Contains(note, "missing") {
		t.Errorf("note should describe 'missing': %q", note)
	}
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0].Path != "src/foo.ts" || events[0].Reason != "missing" {
		t.Errorf("event=%+v", events[0])
	}
}

// TestGhostWriteDetector_EmptyFile verifies detection on a zero-byte file.
func TestGhostWriteDetector_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := "out.txt"
	if err := os.WriteFile(filepath.Join(dir, path), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	var events []GhostWriteEvent
	check := NewGhostWriteCheck(dir, func(evt GhostWriteEvent) {
		events = append(events, evt)
	})
	input, _ := json.Marshal(map[string]string{"path": path})
	tools := []MidturnToolCall{
		{Name: "write", Input: input, Result: "ok"},
	}
	note := check(tools, 0)
	if note == "" {
		t.Fatalf("expected note for empty file")
	}
	if events[0].Reason != "empty" {
		t.Errorf("reason=%q, want empty", events[0].Reason)
	}
}

// TestGhostWriteDetector_NormalWrite verifies a real (non-empty)
// write produces no event or reminder.
func TestGhostWriteDetector_NormalWrite(t *testing.T) {
	dir := t.TempDir()
	path := "hello.ts"
	if err := os.WriteFile(filepath.Join(dir, path), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []GhostWriteEvent
	check := NewGhostWriteCheck(dir, func(evt GhostWriteEvent) {
		events = append(events, evt)
	})
	input, _ := json.Marshal(map[string]string{"file_path": path})
	tools := []MidturnToolCall{
		{Name: "edit", Input: input, Result: "ok"},
	}
	note := check(tools, 0)
	if note != "" {
		t.Errorf("expected no note, got %q", note)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
}

// TestGhostWriteDetector_IgnoresNonWriteTools verifies read_file and
// bash tools are not inspected.
func TestGhostWriteDetector_IgnoresNonWriteTools(t *testing.T) {
	dir := t.TempDir()
	check := NewGhostWriteCheck(dir, nil)
	input, _ := json.Marshal(map[string]string{"file_path": "nonexistent.ts"})
	tools := []MidturnToolCall{
		{Name: "bash", Input: input, Result: "ran"},
		{Name: "read_file", Input: input, Result: "contents"},
	}
	if note := check(tools, 0); note != "" {
		t.Errorf("expected no note for non-write tools, got %q", note)
	}
}

// TestGhostWriteDetector_IgnoresErrorResults verifies is_error=true
// bypasses the check (the model already knows the write failed).
func TestGhostWriteDetector_IgnoresErrorResults(t *testing.T) {
	dir := t.TempDir()
	check := NewGhostWriteCheck(dir, nil)
	input, _ := json.Marshal(map[string]string{"file_path": "nonexistent.ts"})
	tools := []MidturnToolCall{
		{Name: "edit", Input: input, Result: "permission denied", IsError: true},
	}
	if note := check(tools, 0); note != "" {
		t.Errorf("expected no note for error result, got %q", note)
	}
}

// TestGhostWriteDetector_AbsolutePathRespected verifies absolute
// paths are not re-resolved against repoRoot.
func TestGhostWriteDetector_AbsolutePathRespected(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "abs.txt")
	if err := os.WriteFile(abs, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := NewGhostWriteCheck("/ignored/root", nil)
	input, _ := json.Marshal(map[string]string{"path": abs})
	tools := []MidturnToolCall{{Name: "write", Input: input, Result: "ok"}}
	if note := check(tools, 0); note != "" {
		t.Errorf("expected no note for valid absolute path, got %q", note)
	}
}
