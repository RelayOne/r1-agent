package main

// ops_tasks_test.go — OPSUX-tail: tests for the `r1 tasks` verb.
// Follows the ops_events_test.go pattern — seed a fresh events.db in
// a tempdir and exercise runTasksCmd over stdout/stderr.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/eventlog"
)

func TestTasks_DBNotFound(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runTasksCmd([]string{"--db", "/no/such/path/events.db"}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "db not found") {
		t.Errorf("stderr=%q; want 'db not found'", errBuf.String())
	}
}

func TestTasks_EmptyDB(t *testing.T) {
	dbPath := seedLog(t, nil)
	var out, errBuf bytes.Buffer
	code := runTasksCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no tasks") {
		t.Errorf("stdout=%q; want 'no tasks'", out.String())
	}
}

func TestTasks_GroupsByTaskID(t *testing.T) {
	// Two tasks in one mission. Ensure each task gets a single row
	// with the correct event count.
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "TaskA", "L1", nil),
		mkEvent("task.progress", "M1", "TaskA", "L1", nil),
		mkEvent("task.complete", "M1", "TaskA", "L1", nil),
		mkEvent("task.dispatch", "M1", "TaskB", "L1", nil),
		mkEvent("task.complete", "M1", "TaskB", "L1", nil),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runTasksCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	// Header + 2 rows.
	if countLines(got) != 3 {
		t.Fatalf("got %d lines, want 3 (header + 2 rows):\n%s", countLines(got), got)
	}
	for _, want := range []string{"TASK", "EVENTS", "TaskA", "TaskB", "M1", "task.complete"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q\n--stdout--\n%s", want, got)
		}
	}
}

func TestTasks_IgnoresEventsWithoutTaskID(t *testing.T) {
	// An operator.approve (no task_id) must not create a row.
	events := []bus.Event{
		mkEvent("operator.approve", "M1", "", "", nil),
		mkEvent("task.dispatch", "M1", "TaskA", "L1", nil),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runTasksCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	// Header + single task row.
	if countLines(out.String()) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", countLines(out.String()), out.String())
	}
	if !strings.Contains(out.String(), "TaskA") {
		t.Errorf("TaskA row missing:\n%s", out.String())
	}
}

func TestTasks_SessionFilter(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "TaskA", "L1", nil),
		mkEvent("task.dispatch", "M2", "TaskB", "L2", nil),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runTasksCmd([]string{"--db", dbPath, "--session", "M1"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if strings.Contains(out.String(), "TaskB") {
		t.Errorf("TaskB leaked under --session=M1:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "TaskA") {
		t.Errorf("TaskA missing under --session=M1:\n%s", out.String())
	}
}

func TestTasks_JSONOutput(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.dispatch", "M1", "TaskA", "L1", nil),
		mkEvent("task.complete", "M1", "TaskA", "L1", nil),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runTasksCmd([]string{"--db", dbPath, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	line := strings.TrimSpace(out.String())
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", line, err)
	}
	if got["task_id"] != "TaskA" {
		t.Errorf("task_id=%v, want TaskA", got["task_id"])
	}
	if got["mission_id"] != "M1" {
		t.Errorf("mission_id=%v, want M1", got["mission_id"])
	}
	evCount, ok := got["event_count"].(float64)
	if !ok {
		t.Fatalf("event_count: unexpected type: %T", got["event_count"])
	}
	if evCount != 2 {
		t.Errorf("event_count=%v, want 2", got["event_count"])
	}
	if got["last_type"] != "task.complete" {
		t.Errorf("last_type=%v, want task.complete", got["last_type"])
	}
}

// TestCollectTaskSummaries_Direct exercises the aggregator directly,
// bypassing the CLI, to assert sort order and the mission backfill
// behaviour (a later event supplies a mission_id the first didn't).
func TestCollectTaskSummaries_Direct(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/events.db"
	log, err := eventlog.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer log.Close()

	// TaskA arrives with no mission, TaskB with mission. TaskA is
	// seen before TaskB so it must sort first.
	events := []bus.Event{
		mkEvent("task.dispatch", "", "TaskA", "L1", nil),
		mkEvent("task.dispatch", "M2", "TaskB", "L2", nil),
		mkEvent("task.complete", "Mlate", "TaskA", "L1", nil),
	}
	for i := range events {
		if err := log.Append(&events[i]); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := collectTaskSummaries(context.Background(), log, "")
	if err != nil {
		t.Fatalf("collectTaskSummaries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want 2", len(got))
	}
	if got[0].TaskID != "TaskA" {
		t.Errorf("first task TaskID=%q, want TaskA (earliest FirstSeen)", got[0].TaskID)
	}
	// Backfill: TaskA first event had no mission, third event had
	// "Mlate" — summary should reflect "Mlate".
	if got[0].MissionID != "Mlate" {
		t.Errorf("TaskA mission backfill=%q, want Mlate", got[0].MissionID)
	}
	if got[0].Count != 2 || got[1].Count != 1 {
		t.Errorf("counts wrong: TaskA=%d TaskB=%d (want 2/1)", got[0].Count, got[1].Count)
	}
}
