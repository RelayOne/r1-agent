package main

// replay_cmd_test.go — O4: tests for the `r1 replay` reader. Recordings are
// produced through the real internal/replay writer path (NewRecorder → Finish
// → Save) so the tests exercise the exact on-disk shape the workflow persists,
// then read them back through runReplayCmd.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/replay"
)

// seedReplay writes one recording under r1dir.JoinFor(repo, "replays") — the
// same path the reader resolves — and returns its on-disk path.
func seedReplay(t *testing.T, repo, id, taskID, outcome string, withError bool) string {
	t.Helper()
	r := replay.NewRecorder(id, taskID)
	r.Record(replay.EventPhase, map[string]any{"phase": "start", "task": "demo"})
	r.RecordToolCall("read_file", map[string]any{"path": "x.go"})
	r.RecordMessage("assistant", "did the thing\nover two lines")
	if withError {
		r.RecordError("boom: something failed", map[string]any{"code": "E42"})
	}
	rec := r.Finish(outcome, "coding")

	dir := r1dir.JoinFor(repo, "replays")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir replays: %v", err)
	}
	path := filepath.Join(dir, rec.ID+".json")
	if err := replay.Save(rec, path); err != nil {
		t.Fatalf("save recording: %v", err)
	}
	return path
}

func TestReplay_NoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runReplayCmd(nil, &out, &errBuf); code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
}

func TestReplay_UnknownVerb(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"frobnicate"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown verb") {
		t.Errorf("stderr=%q; want 'unknown verb'", errBuf.String())
	}
}

func TestReplayList_NoDir(t *testing.T) {
	repo := t.TempDir()
	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"list", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no replay recordings") {
		t.Errorf("stdout=%q; want 'no replay recordings'", out.String())
	}
}

func TestReplayList_Table(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", false)
	seedReplay(t, repo, "task-b-1700000000001", "task-b", "failure", true)

	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"list", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"ID", "OUTCOME", "task-a-1700000000000", "task-b-1700000000001", "success", "failure"} {
		if !strings.Contains(s, want) {
			t.Errorf("list table missing %q\n%s", want, s)
		}
	}
}

func TestReplayList_JSON(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", true)

	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"list", "--repo", repo, "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	line := strings.TrimSpace(out.String())
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("list --json not valid NDJSON: %v\n%q", err, line)
	}
	if obj["id"] != "task-a-1700000000000" {
		t.Errorf("id=%v, want task-a-1700000000000", obj["id"])
	}
	if obj["outcome"] != "success" {
		t.Errorf("outcome=%v, want success", obj["outcome"])
	}
	// One RecordError → exactly one error event.
	if got, _ := obj["errors"].(float64); got != 1 {
		t.Errorf("errors=%v, want 1", obj["errors"])
	}
}

func TestReplayShow_ByPrefix(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", false)

	var out, errBuf bytes.Buffer
	// Unique prefix resolution.
	if code := runReplayCmd([]string{"show", "task-a", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "Session task-a-1700000000000") {
		t.Errorf("show output missing Summary header:\n%s", s)
	}
	// tool_call and message events should appear in the table.
	if !strings.Contains(s, "tool_call") || !strings.Contains(s, "message") {
		t.Errorf("show output missing event rows:\n%s", s)
	}
	// Newlines in the message payload must be collapsed to keep the grid.
	if strings.Contains(s, "did the thing\nover") {
		t.Errorf("show output leaked a raw newline into the table:\n%s", s)
	}
}

func TestReplayShow_TypeFilter(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "failure", true)

	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"show", "task-a-1700000000000", "--type", "error", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "boom: something failed") {
		t.Errorf("type=error filter dropped the error detail:\n%s", s)
	}
	// A non-error event type must not appear in the filtered table BODY. The
	// Summary header legitimately lists per-type counts ("tool_call: 1"), so
	// scope the assertion to the event table that follows the "SEQ" header.
	if idx := strings.Index(s, "SEQ"); idx >= 0 {
		body := s[idx:]
		if strings.Contains(body, "tool_call") || strings.Contains(body, "message") {
			t.Errorf("type=error filter leaked a non-error row into the table body:\n%s", body)
		}
	} else {
		t.Errorf("show output missing event table:\n%s", s)
	}
}

func TestReplayShow_JSON_FlagAfterID(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", true)

	var out, errBuf bytes.Buffer
	// id BEFORE flags — exercises splitReplayArgs so --json is still parsed.
	if code := runReplayCmd([]string{"show", "task-a-1700000000000", "--json", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	var sawError bool
	var lines int
	for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if ln == "" {
			continue
		}
		lines++
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			t.Fatalf("show --json line not valid: %v\n%q", err, ln)
		}
		if obj["type"] == "error" {
			sawError = true
			// data must be nested JSON, not a stringified blob.
			data, ok := obj["data"].(map[string]any)
			if !ok {
				t.Fatalf("error event data not a nested object: %T", obj["data"])
			}
			if data["error"] != "boom: something failed" {
				t.Errorf("data.error=%v", data["error"])
			}
		}
	}
	if lines == 0 {
		t.Fatal("show --json produced no NDJSON lines")
	}
	if !sawError {
		t.Error("show --json never emitted the error event")
	}
}

func TestReplayShow_MissingID(t *testing.T) {
	repo := t.TempDir()
	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"show", "--repo", repo}, &out, &errBuf); code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
}

func TestReplayShow_NotFound(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", false)
	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"show", "does-not-exist", "--repo", repo}, &out, &errBuf); code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
}

func TestReplayShow_AmbiguousPrefix(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", false)
	seedReplay(t, repo, "task-a-1700000000009", "task-a", "failure", false)
	var out, errBuf bytes.Buffer
	// "task-a" is a prefix of both -> ambiguous.
	if code := runReplayCmd([]string{"show", "task-a", "--repo", repo}, &out, &errBuf); code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "ambiguous") {
		t.Errorf("stderr=%q; want 'ambiguous'", errBuf.String())
	}
}

func TestReplayErrors(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "failure", true)

	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"errors", "task-a-1700000000000", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "boom: something failed") {
		t.Errorf("errors verb dropped the error message:\n%s", out.String())
	}
}

func TestReplayErrors_NoErrors(t *testing.T) {
	repo := t.TempDir()
	seedReplay(t, repo, "task-a-1700000000000", "task-a", "success", false)

	var out, errBuf bytes.Buffer
	if code := runReplayCmd([]string{"errors", "task-a-1700000000000", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no error events") {
		t.Errorf("stdout=%q; want 'no error events'", out.String())
	}
}

func TestSplitReplayArgs(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantID   string
		wantFlag []string
	}{
		{"id then bool flag", []string{"abc", "--json"}, "abc", []string{"--json"}},
		{"id then value flag", []string{"abc", "--type", "error"}, "abc", []string{"--type", "error"}},
		{"value flag then id", []string{"--type", "error", "abc"}, "abc", []string{"--type", "error"}},
		{"repo before id", []string{"--repo", "/x", "abc", "--json"}, "abc", []string{"--repo", "/x", "--json"}},
		{"eq form", []string{"abc", "--type=error"}, "abc", []string{"--type=error"}},
		{"only flags", []string{"--json"}, "", []string{"--json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, flags := splitReplayArgs(c.in)
			if id != c.wantID {
				t.Errorf("id=%q, want %q", id, c.wantID)
			}
			if strings.Join(flags, " ") != strings.Join(c.wantFlag, " ") {
				t.Errorf("flags=%v, want %v", flags, c.wantFlag)
			}
		})
	}
}
