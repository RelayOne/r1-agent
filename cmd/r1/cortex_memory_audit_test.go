package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/cortex/lobes/memorycurator"
)

// TestCmdCortexMemoryAudit_PrintsEntries covers TASK-31.
//
// Writes a fixture audit log with two entries to a tempdir, sets HOME
// to that tempdir so the CLI resolves the audit-log path under it, runs
// the subcommand, captures stdout, and asserts each entry's
// timestamp/category/content/decision appears in the output table.
func TestCmdCortexMemoryAudit_PrintsEntries(t *testing.T) {
	home := t.TempDir()
	auditPath := filepath.Join(home, ".r1", "cortex", "curator-audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	entries := []memorycurator.AuditEntry{
		{
			Timestamp:  "2026-05-02T10:00:00Z",
			EntryID:    "mem-1",
			Category:   "fact",
			Content:    "build with go build ./cmd/r1",
			ContentSHA: "abc123",
			Decision:   "auto-applied",
		},
		{
			Timestamp:  "2026-05-02T10:05:00Z",
			EntryID:    "mem-2",
			Category:   "fact",
			Content:    "deploy target is gcp/us-central1",
			ContentSHA: "def456",
			Decision:   "auto-applied",
		},
	}

	f, err := os.Create(auditPath)
	if err != nil {
		t.Fatalf("create audit log: %v", err)
	}
	for _, ent := range entries {
		line, err := json.Marshal(ent)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := runCortexCmd([]string{"memory", "audit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runCortexCmd exit=%d, stderr=%q", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "TIMESTAMP") {
		t.Errorf("output missing header row, got:\n%s", out)
	}
	for _, ent := range entries {
		if !strings.Contains(out, ent.Timestamp) {
			t.Errorf("output missing timestamp %q, got:\n%s", ent.Timestamp, out)
		}
		if !strings.Contains(out, ent.Content) {
			t.Errorf("output missing content %q, got:\n%s", ent.Content, out)
		}
		if !strings.Contains(out, ent.Decision) {
			t.Errorf("output missing decision %q, got:\n%s", ent.Decision, out)
		}
	}
	if !strings.Contains(out, "2 audit entries") {
		t.Errorf("output missing entry-count footer, got:\n%s", out)
	}
}

// TestCmdCortexMemoryAudit_NoLogFile asserts the CLI exits 0 and prints
// a friendly "(no curator audit log...)" message when the audit log
// does not exist. This is the first-run case — operators run the
// command before the curator has had a chance to write anything.
func TestCmdCortexMemoryAudit_NoLogFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := runCortexCmd([]string{"memory", "audit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runCortexCmd exit=%d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no curator audit log") {
		t.Errorf("expected friendly missing-file message, got:\n%s", stdout.String())
	}
}

// TestCmdCortexMemoryAudit_UnknownVerb asserts unknown verbs under
// `r1 cortex memory` return exit code 2 with a usage hint on stderr.
func TestCmdCortexMemoryAudit_UnknownVerb(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := runCortexCmd([]string{"memory", "bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("runCortexCmd exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		t.Errorf("expected unknown-verb error on stderr, got:\n%s", stderr.String())
	}
}
