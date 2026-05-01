package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionIDForTask verifies the task-ID → session-ID pattern
// extraction. This is the fallback path used when task.Description is
// empty — the H-91d empty-description fix relies on knowing which
// session the task belongs to so we can synthesize a sensible prompt.
func TestSessionIDForTask(t *testing.T) {
	cfg := sowNativeConfig{}
	cases := []struct {
		name, taskID, want string
	}{
		{"descent repair", "S1-descent-repair-1776745306884", "S1"},
		{"fix-DAG", "S1-fix-FIX1", "S1"},
		{"continuation", "S2-cont-t3", "S2"},
		{"integrity fix", "S3-integrity-fix-FIX1", "S3"},
		{"primary task (no prefix)", "T1", ""},
		{"primary task T42", "T42", ""},
		{"empty", "", ""},
		// Compound session prefix: the descent-repair was dispatched
		// against session "S1-fix" (itself a fix-DAG session), not the
		// original "S1". sessionIDForTask correctly returns the direct
		// parent so correlation points at the actual executing session.
		{"compound session+descent", "S1-fix-descent-repair-123", "S1-fix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cfg.sessionIDForTask(c.taskID)
			if got != c.want {
				t.Errorf("sessionIDForTask(%q) = %q, want %q", c.taskID, got, c.want)
			}
		})
	}
}

// TestMaybeWriteSOWSnapshot confirms the snapshot is written once per
// session and reused on subsequent calls — the second dispatch for S1
// should NOT rewrite the file (which would lose the "spec as of
// session start" guarantee when the SOW gets amended mid-run).
func TestMaybeWriteSOWSnapshot(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := sowNativeConfig{
		RepoRoot:   repoRoot,
		RawSOWText: "# SOW content\n\nSession S1: do the thing.",
	}

	path := maybeWriteSOWSnapshot(cfg, "S1")
	if path == "" {
		t.Fatal("expected non-empty path")
	}
	expected := filepath.Join(repoRoot, ".stoke", "sessions", "S1", "sow-snapshot.md")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}

	// File should exist with the right content.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "SOW content") {
		t.Errorf("snapshot content missing expected text: %s", string(data))
	}

	// Call again with different RawSOWText — the snapshot should be
	// preserved (cached), NOT overwritten.
	cfg2 := cfg
	cfg2.RawSOWText = "# Amended SOW\n\nSession S1: different."
	path2 := maybeWriteSOWSnapshot(cfg2, "S1")
	if path2 != path {
		t.Errorf("second call returned different path: %q vs %q", path2, path)
	}
	data2, _ := os.ReadFile(path)
	if !strings.Contains(string(data2), "SOW content") {
		t.Error("snapshot was overwritten — should have been cached")
	}
	if strings.Contains(string(data2), "Amended SOW") {
		t.Error("snapshot should not reflect later RawSOWText changes")
	}
}

func TestMaybeWriteSOWSnapshot_EmptyInputs(t *testing.T) {
	cases := []struct {
		name string
		cfg  sowNativeConfig
		sess string
	}{
		{"empty repo root", sowNativeConfig{RawSOWText: "x"}, "S1"},
		{"empty session id", sowNativeConfig{RepoRoot: t.TempDir(), RawSOWText: "x"}, ""},
		{"empty SOW text", sowNativeConfig{RepoRoot: t.TempDir()}, "S1"},
		{"whitespace-only SOW", sowNativeConfig{RepoRoot: t.TempDir(), RawSOWText: "   \n\t  "}, "S1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maybeWriteSOWSnapshot(c.cfg, c.sess); got != "" {
				t.Errorf("expected empty path, got %q", got)
			}
		})
	}
}

func TestMaybeWriteSOWSnapshot_PerSessionIsolation(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := sowNativeConfig{
		RepoRoot:   repoRoot,
		RawSOWText: "session one content",
	}

	p1 := maybeWriteSOWSnapshot(cfg, "S1")

	cfg2 := cfg
	cfg2.RawSOWText = "session two content"
	p2 := maybeWriteSOWSnapshot(cfg2, "S2")

	if p1 == p2 {
		t.Fatal("different sessions should write to different paths")
	}
	d1, _ := os.ReadFile(p1)
	d2, _ := os.ReadFile(p2)
	if string(d1) == string(d2) {
		t.Error("per-session snapshots should not share content")
	}
	if !strings.Contains(string(d1), "session one") {
		t.Error("S1 snapshot missing expected content")
	}
	if !strings.Contains(string(d2), "session two") {
		t.Error("S2 snapshot missing expected content")
	}
}

// TestNewRunID_FormatAndUniqueness verifies the RunID generator
// produces stable, sortable, unique IDs.
func TestNewRunID_FormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := newRunID()
		if !strings.HasPrefix(id, "run-") {
			t.Errorf("RunID should start with run-, got %q", id)
		}
		if seen[id] {
			t.Errorf("duplicate RunID: %q", id)
		}
		seen[id] = true
	}
}

// TestReadStokeBuild verifies the build-version resolver returns
// something (non-empty) when invoked from this test binary. The
// actual value depends on debug.ReadBuildInfo/git availability;
// we only assert a truthy result.
func TestReadStokeBuild(t *testing.T) {
	v := readStokeBuild()
	// Either source (VCS or git fallback) should have produced a
	// short hash. Empty is a failure mode worth noting — it means
	// workers can't be tied back to a specific binary version.
	if v == "" {
		t.Skip("no VCS revision available in test environment — skipping (expected in some CI configs)")
	}
	if len(v) > 40 {
		t.Errorf("build hash unexpectedly long: %q (%d chars)", v, len(v))
	}
}
