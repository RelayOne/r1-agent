package main

// ops_logs_test.go — OPSUX-tail: tests for `stoke logs`. Covers both
// the stream.jsonl primary path and the eventlog fallback, plus the
// forced-fallback behaviour when --session is set.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/bus"
)

// writeTempStream creates a stream.jsonl file with the given lines in
// a tempdir and returns the path. Each line is written as-is plus a
// trailing newline (the emitter always emits one JSON value per
// line).
func writeTempStream(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	var buf bytes.Buffer
	for _, ln := range lines {
		buf.WriteString(ln)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	return path
}

func TestLogs_BadFlag(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runLogsCmd([]string{"--last", "-1"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
}

func TestLogs_NoSources(t *testing.T) {
	// Point both sources at non-existent paths.
	var out, errBuf bytes.Buffer
	code := runLogsCmd([]string{
		"--db", "/no/such/db",
		"--stream", "/no/such/stream.jsonl",
	}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "no log sources") {
		t.Errorf("stderr=%q; want 'no log sources'", errBuf.String())
	}
}

func TestLogs_StreamJSONL_TailLast(t *testing.T) {
	lines := []string{}
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"seq":`+itoa(i)+`}`)
	}
	stream := writeTempStream(t, lines)

	var out, errBuf bytes.Buffer
	code := runLogsCmd([]string{"--stream", stream, "--last", "3"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	// We expect exactly 3 lines: seq 7, 8, 9.
	if countLines(out.String()) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", countLines(out.String()), out.String())
	}
	if !strings.Contains(out.String(), `"seq":9`) {
		t.Errorf("missing seq=9:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"seq":0`) {
		t.Errorf("seq=0 leaked into last-3 window:\n%s", out.String())
	}
}

func TestLogs_StreamJSONL_LastZeroMeansAll(t *testing.T) {
	lines := []string{`{"a":1}`, `{"a":2}`, `{"a":3}`}
	stream := writeTempStream(t, lines)

	var out, errBuf bytes.Buffer
	code := runLogsCmd([]string{"--stream", stream, "--last", "0"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if countLines(out.String()) != 3 {
		t.Fatalf("got %d lines, want 3 (--last 0 = all):\n%s", countLines(out.String()), out.String())
	}
}

func TestLogs_FallsBackToEventlog_WhenStreamMissing(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", map[string]any{"k": "v"}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runLogsCmd([]string{
		"--db", dbPath,
		"--stream", "/no/such/stream.jsonl",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	// Eventlog fallback uses emitEventsJSON → NDJSON of the sole event.
	if !strings.Contains(out.String(), `"type":"task.dispatch"`) {
		t.Errorf("stdout missing task.dispatch JSON:\n%s", out.String())
	}
}

func TestLogs_SessionFilter_ForcesEventlog(t *testing.T) {
	// Stream file exists but --session is set, so the verb MUST use
	// the eventlog (stream has no session filter). Seed different
	// content in each so we can tell which one was read.
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", nil),
		mkEvent("task.dispatch", "M2", "T2", "L2", nil),
	}
	dbPath := seedLog(t, events)

	stream := writeTempStream(t, []string{`{"source":"stream"}`})

	var out, errBuf bytes.Buffer
	code := runLogsCmd([]string{
		"--db", dbPath,
		"--stream", stream,
		"--session", "M1",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if strings.Contains(out.String(), `"source":"stream"`) {
		t.Errorf("--session should have forced eventlog path:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"task_id":"T1"`) {
		t.Errorf("missing filtered T1:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"task_id":"T2"`) {
		t.Errorf("T2 leaked despite --session=M1:\n%s", out.String())
	}
}

// itoa avoids pulling in strconv just to build fixture lines.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var digits [20]byte
	pos := len(digits)
	for i > 0 {
		pos--
		digits[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		digits[pos] = '-'
	}
	return string(digits[pos:])
}
