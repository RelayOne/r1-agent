package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadWorkerLogExcerpt_RendersToolCalls verifies the reviewer-facing
// excerpt formatter parses a hand-crafted JSONL (matching the schema
// native_runner.go writes) into a compact, readable format.
func TestLoadWorkerLogExcerpt_RendersToolCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	content := `{"ts":"2026-04-20T19:45:12.123Z","type":"dispatch_start","phase":"execute","dir":"/repo"}
{"ts":"2026-04-20T19:45:13.000Z","tool":"bash","input":"{\"command\":\"pnpm test\"}","result":"> vitest\n✓ 5 passed\nEXIT: 0","result_len":50,"duration_ms":2340}
{"ts":"2026-04-20T19:45:20.000Z","tool":"edit_file","input":"{\"path\":\"src/foo.ts\"}","result":"Edit applied","result_len":12,"duration_ms":8}
{"ts":"2026-04-20T19:45:25.000Z","tool":"bash","input":"{\"command\":\"false\"}","err":"exit 1","duration_ms":5}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	excerpt := LoadWorkerLogExcerpt(path, 100)
	if excerpt == "" {
		t.Fatal("expected non-empty excerpt")
	}
	// Dispatch header must be skipped (it's metadata, not a tool call).
	if strings.Contains(excerpt, "dispatch_start") {
		t.Errorf("excerpt should skip dispatch_start header line, got:\n%s", excerpt)
	}
	// Count should reflect the 3 tool calls, not 4 lines.
	if !strings.Contains(excerpt, "(3 tool call(s) captured)") {
		t.Errorf("expected '(3 tool call(s) captured)', got:\n%s", excerpt)
	}
	// Bash + exit code should be visible for reviewer grep.
	if !strings.Contains(excerpt, "bash") || !strings.Contains(excerpt, "EXIT: 0") {
		t.Errorf("expected bash + exit in excerpt, got:\n%s", excerpt)
	}
	// Error should be rendered.
	if !strings.Contains(excerpt, "exit 1") {
		t.Errorf("expected err line in excerpt, got:\n%s", excerpt)
	}
	// HH:MM:SS compactness check.
	if !strings.Contains(excerpt, "[19:45:13]") {
		t.Errorf("expected HH:MM:SS timestamp, got:\n%s", excerpt)
	}
}

// TestLoadWorkerLogExcerpt_MaxCalls verifies only the last N calls
// are retained when the log exceeds the cap.
func TestLoadWorkerLogExcerpt_MaxCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString(`{"ts":"2026-04-20T19:45:12.000Z","tool":"bash","input":"{}","result":"ok","result_len":2,"duration_ms":1}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	excerpt := LoadWorkerLogExcerpt(path, 5)
	if !strings.Contains(excerpt, "(5 tool call(s) captured)") {
		t.Errorf("expected exactly 5 retained, got:\n%s", excerpt)
	}
}

// TestLoadWorkerLogExcerpt_MissingFile returns empty when the file
// doesn't exist — reviewer degrades to no-log path.
func TestLoadWorkerLogExcerpt_MissingFile(t *testing.T) {
	if got := LoadWorkerLogExcerpt("/nonexistent/path.jsonl", 10); got != "" {
		t.Errorf("expected empty excerpt for missing file, got %q", got)
	}
	if got := LoadWorkerLogExcerpt("", 10); got != "" {
		t.Errorf("expected empty excerpt for empty path, got %q", got)
	}
}

// TestLatestWorkerLogForTask finds the newest log matching the task ID.
func TestLatestWorkerLogForTask(t *testing.T) {
	repo := t.TempDir()
	logsDir := filepath.Join(repo, ".stoke", "worker-logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write in non-sorted order to confirm lexicographic (= chronological
	// for nanosecond suffixes) selection picks the newest.
	older := filepath.Join(logsDir, "T1-1000000000000000000.jsonl")
	newer := filepath.Join(logsDir, "T1-1000000000000000005.jsonl")
	otherTask := filepath.Join(logsDir, "T2-1000000000000000009.jsonl")
	for _, p := range []string{older, newer, otherTask} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := LatestWorkerLogForTask(repo, "T1")
	if got != newer {
		t.Errorf("expected %q, got %q", newer, got)
	}

	// Unknown task → empty.
	if got := LatestWorkerLogForTask(repo, "T99"); got != "" {
		t.Errorf("expected empty for unknown task, got %q", got)
	}

	// Missing repo → empty.
	if got := LatestWorkerLogForTask("/nonexistent", "T1"); got != "" {
		t.Errorf("expected empty for missing repo, got %q", got)
	}
}

// TestWorkerLogRoundTrip replicates the exact byte-for-byte write path
// from engine/native_runner.go (Handle-wrapping closure appends one JSON
// object per line to an os.OpenFile-opened append-only file) and verifies
// the reader sees the same data back as a reviewer excerpt. This catches
// schema drift between the writer and the LoadWorkerLogExcerpt parser.
func TestWorkerLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T42-" + fmt.Sprintf("%d", time.Now().UnixNano()) + ".jsonl")

	// Open the file the same way native_runner.go does.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	// Header line (identical to native_runner.go line 115-121).
	hdr, _ := json.Marshal(map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"type":  "dispatch_start",
		"phase": "execute",
		"dir":   "/repo",
	})
	fmt.Fprintln(f, string(hdr))

	// Simulate 3 tool calls written by the handler closure at line 137-156.
	calls := []struct {
		tool       string
		input      string
		result     string
		err        string
		durationMs int64
	}{
		{"bash", `{"command":"pnpm build"}`, "✓ Compiled successfully\nEXIT: 0", "", 1234},
		{"edit_file", `{"path":"src/foo.ts"}`, "Edit applied at /tmp/foo.ts", "", 8},
		{"bash", `{"command":"false"}`, "", "exit status 1", 3},
	}
	for _, c := range calls {
		entry := map[string]any{
			"ts":          time.Now().UTC().Format(time.RFC3339Nano),
			"tool":        c.tool,
			"input":       c.input,
			"duration_ms": c.durationMs,
			"result_len":  len(c.result),
		}
		if c.err != "" {
			entry["err"] = c.err
		} else {
			entry["result"] = c.result
		}
		b, _ := json.Marshal(entry)
		fmt.Fprintln(f, string(b))
	}
	_ = f.Close()

	// --- Read back via LoadWorkerLogExcerpt ---
	excerpt := LoadWorkerLogExcerpt(path, 100)
	if !strings.Contains(excerpt, "(3 tool call(s) captured)") {
		t.Errorf("round-trip: expected 3 tool calls, got:\n%s", excerpt)
	}
	if !strings.Contains(excerpt, "pnpm build") {
		t.Errorf("round-trip: bash command lost, got:\n%s", excerpt)
	}
	if !strings.Contains(excerpt, "EXIT: 0") {
		t.Errorf("round-trip: exit code lost, got:\n%s", excerpt)
	}
	if !strings.Contains(excerpt, "exit status 1") {
		t.Errorf("round-trip: err field lost, got:\n%s", excerpt)
	}

	// --- Read back via LatestWorkerLogForTask ---
	// Move file into the expected layout and verify the lookup works.
	logsDir := filepath.Join(dir, ".stoke", "worker-logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetName := filepath.Base(path)
	targetPath := filepath.Join(logsDir, targetName)
	if err := os.Rename(path, targetPath); err != nil {
		t.Fatal(err)
	}
	if got := LatestWorkerLogForTask(dir, "T42"); got != targetPath {
		t.Errorf("LatestWorkerLogForTask: expected %q, got %q", targetPath, got)
	}
}

// TestShortenOneLine collapses newlines + truncates for inline display.
func TestShortenOneLine(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello\nworld", 100, "hello \\n world"},
		{"short", 100, "short"},
		{"0123456789abcd", 5, "01234…"},
	}
	for _, c := range cases {
		if got := shortenOneLine(c.in, c.max); got != c.want {
			t.Errorf("shortenOneLine(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
