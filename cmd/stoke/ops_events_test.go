package main

// ops_events_test.go — OPSUX-events: tests for the `stoke events`
// read-only verb. We exercise the command via runEventsCmd against a
// real eventlog.Log seeded in a tempdir, asserting on stdout.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/eventlog"
)

// seedLog opens a fresh events.db in a tempdir and appends the given
// events in order. Returns the db path. Closes the log before returning
// so runEventsCmd can re-open it (eventlog.Log is single-process WAL).
func seedLog(t *testing.T, events []bus.Event) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".stoke", "events.db")
	log, err := eventlog.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := range events {
		if err := log.Append(&events[i]); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dbPath
}

// mkEvent is a small helper that builds a bus.Event with a json payload.
func mkEvent(typ, mission, task, loop string, payload map[string]any) bus.Event {
	raw, _ := json.Marshal(payload)
	return bus.Event{
		Type:      bus.EventType(typ),
		EmitterID: "test",
		Scope:     bus.Scope{MissionID: mission, TaskID: task, LoopID: loop},
		Payload:   raw,
	}
}

func TestEvents_DBNotFound(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", "/no/such/path/events.db"}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "db not found") {
		t.Errorf("stderr=%q; want 'db not found'", errBuf.String())
	}
}

func TestEvents_BadFlag(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--last", "-1"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
}

func TestEvents_EmptyDB(t *testing.T) {
	dbPath := seedLog(t, nil)
	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no events") {
		t.Errorf("stdout=%q; want 'no events'", out.String())
	}
}

func TestEvents_TableRender(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", map[string]any{"i": 1}),
		mkEvent("task.complete", "M1", "T1", "L1", map[string]any{"i": 2}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{"SEQ", "TIMESTAMP", "TYPE", "task.dispatch", "task.complete", "M1", "T1", "L1"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q\n--stdout--\n%s", want, got)
		}
	}
}

func TestEvents_LastN_TrimsToTail(t *testing.T) {
	var events []bus.Event
	for i := 0; i < 10; i++ {
		events = append(events, mkEvent("task.progress", "M1", "T1", "L1", map[string]any{"i": i}))
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", dbPath, "--last", "3"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	// Header + 3 data rows == 4 lines. tabwriter emits trailing \n.
	lineCount := countLines(out.String())
	if lineCount != 4 {
		t.Fatalf("got %d lines, want 4 (header + 3 rows):\n%s", lineCount, out.String())
	}
	// The trailing 3 of 10 should be sequences 8, 9, 10. Verify the
	// highest expected sequence appears and the excluded ones do not.
	if !strings.Contains(out.String(), "10") {
		t.Errorf("stdout missing seq 10:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "8") {
		t.Errorf("stdout missing seq 8 (start of last-3 window):\n%s", out.String())
	}
	// Sequence 1 is well outside the last-3 window. Look for it as a
	// standalone first column to avoid false matches on "10".
	if hasFirstColumn(out.String(), "1") {
		t.Errorf("seq 1 leaked into last-3 output:\n%s", out.String())
	}
}

func TestEvents_TypePrefixFilter(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", nil),
		mkEvent("operator.approve", "M1", "", "", nil),
		mkEvent("task.complete", "M1", "T1", "L1", nil),
		mkEvent("operator.pause", "M1", "", "", nil),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", dbPath, "--type", "operator."}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	if !strings.Contains(got, "operator.approve") || !strings.Contains(got, "operator.pause") {
		t.Errorf("stdout missing operator events:\n%s", got)
	}
	if strings.Contains(got, "task.dispatch") || strings.Contains(got, "task.complete") {
		t.Errorf("stdout should not contain non-operator events:\n%s", got)
	}
}

func TestEvents_SessionFilter_MatchesTaskID(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "TaskA", "L1", nil),
		mkEvent("task.dispatch", "M1", "TaskB", "L1", nil),
		mkEvent("task.complete", "M1", "TaskA", "L1", nil),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	// ReplaySession matches against any of session/mission/task/loop.
	code := runEventsCmd([]string{"--db", dbPath, "--session", "TaskA"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	// We want only the TaskA-tagged events. Both are type task.* and
	// scope task=TaskA. The TaskB row should not appear.
	if strings.Contains(got, "TaskB") {
		t.Errorf("stdout leaked TaskB row:\n%s", got)
	}
	if !strings.Contains(got, "TaskA") {
		t.Errorf("stdout missing TaskA rows:\n%s", got)
	}
}

func TestEvents_SinceFilter(t *testing.T) {
	var events []bus.Event
	for i := 0; i < 5; i++ {
		events = append(events, mkEvent("task.progress", "M1", "T1", "L1", map[string]any{"i": i}))
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", dbPath, "--since", "3", "--last", "0"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	// Should see sequences 3, 4, 5 only (--since is inclusive).
	got := out.String()
	// Header + 3 data rows.
	lineCount := countLines(got)
	if lineCount != 4 {
		t.Fatalf("got %d lines, want 4 (header + seqs 3,4,5):\n%s", lineCount, got)
	}
}

// countLines returns the number of newline-terminated lines in s,
// trimming a single trailing newline so a final \n doesn't inflate the
// count. Implemented via strings.Count to keep the body trivial.
func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// hasFirstColumn reports whether any line in s has want as its first
// whitespace-separated field. Used to verify a tabwriter row's leading
// sequence column without false-matching substrings (e.g. "1" inside
// "10"). Iterates via bufio.Scanner to avoid materialising a slice.
func hasFirstColumn(s, want string) bool {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 && fields[0] == want {
			return true
		}
	}
	return false
}

func TestEvents_JSONOutput(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", map[string]any{"hello": "world"}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runEventsCmd([]string{"--db", dbPath, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	// NDJSON: one JSON object per line. Single seeded event → single line.
	line := strings.TrimSpace(out.String())
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", line, err)
	}
	if got["type"] != "task.dispatch" {
		t.Errorf("type=%v, want task.dispatch", got["type"])
	}
	scope, ok := got["scope"].(map[string]any)
	if !ok {
		t.Fatalf("scope missing or wrong type: %#v", got["scope"])
	}
	if scope["mission_id"] != "M1" || scope["task_id"] != "T1" || scope["loop_id"] != "L1" {
		t.Errorf("scope mismatch: %#v", scope)
	}
	payload, ok := got["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing or wrong type: %#v", got["payload"])
	}
	if payload["hello"] != "world" {
		t.Errorf("payload.hello=%v, want world", payload["hello"])
	}
}

// TestCollectEvents_LastBoundsRing verifies the ring-buffer behaviour
// directly, since that's the most subtle bit of the rendering pipeline.
// (Pulling more than `last` events should never blow O(last) memory in
// the ring.)
func TestCollectEvents_LastBoundsRing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "events.db")
	log, err := eventlog.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer log.Close()
	for i := 0; i < 50; i++ {
		ev := mkEvent("task.progress", "M1", "T1", "L1", map[string]any{"i": i})
		if err := log.Append(&ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := collectEvents(context.Background(), log, "", 0, "", 5)
	if err != nil {
		t.Fatalf("collectEvents: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	if got[0].Sequence != 46 || got[4].Sequence != 50 {
		t.Errorf("trailing window wrong: got first=%d last=%d, want 46/50",
			got[0].Sequence, got[4].Sequence)
	}
}
