package eventlog

import (
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
)

// mkEvent is a tiny constructor to keep the tests readable. Timestamps
// are generated monotonically from a fixed base so two events created
// back-to-back are strictly ordered, matching real log semantics.
func mkEvent(seq uint64, typ bus.EventType, taskID string) bus.Event {
	base := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	return bus.Event{
		ID:        "ev-" + string(typ) + "-" + taskID,
		Type:      typ,
		Sequence:  seq,
		Timestamp: base.Add(time.Duration(seq) * time.Second),
		Scope:     bus.Scope{TaskID: taskID, LoopID: "sess-test"},
	}
}

func TestDecideResume_Empty(t *testing.T) {
	t.Parallel()
	id, mode := DecideResume(nil)
	if mode != ResumeFreshStart {
		t.Errorf("empty events: got mode=%s, want fresh_start", mode)
	}
	if id != "" {
		t.Errorf("empty events: got taskID=%q, want empty", id)
	}
}

func TestDecideResume_NoTaskOrSessionMarkers(t *testing.T) {
	t.Parallel()
	// A log containing only unrelated events (no task.* or session.*)
	// must be treated as a fresh start — there's nothing to resume from.
	events := []bus.Event{
		mkEvent(1, "worker.spawned", ""),
		mkEvent(2, "ledger.node.added", ""),
	}
	id, mode := DecideResume(events)
	if mode != ResumeFreshStart {
		t.Errorf("got mode=%s, want fresh_start", mode)
	}
	if id != "" {
		t.Errorf("got taskID=%q, want empty", id)
	}
}

func TestDecideResume_MidTaskCrash(t *testing.T) {
	t.Parallel()
	// Last marker is a task.dispatch with no matching complete/fail —
	// the worker crashed mid-task and the caller should re-dispatch.
	events := []bus.Event{
		mkEvent(1, "session.start", ""),
		mkEvent(2, "task.dispatch", "T1"),
	}
	id, mode := DecideResume(events)
	if mode != ResumeRetryTask {
		t.Errorf("got mode=%s, want retry_task", mode)
	}
	if id != "T1" {
		t.Errorf("got taskID=%q, want T1", id)
	}
}

func TestDecideResume_MidSessionAfterComplete(t *testing.T) {
	t.Parallel()
	// task.complete observed but no session.end — the caller should
	// advance to the next task in the plan.
	events := []bus.Event{
		mkEvent(1, "session.start", ""),
		mkEvent(2, "task.dispatch", "T1"),
		mkEvent(3, "task.complete", "T1"),
	}
	id, mode := DecideResume(events)
	if mode != ResumeNextTask {
		t.Errorf("got mode=%s, want next_task", mode)
	}
	if id != "T1" {
		t.Errorf("got taskID=%q, want T1", id)
	}
}

func TestDecideResume_MidSessionAfterFail(t *testing.T) {
	t.Parallel()
	// task.fail is terminal for the task (just like task.complete) —
	// the resume logic treats both as "advance to next task". Whether
	// the plan skips or retries is the plan's concern, not resume's.
	events := []bus.Event{
		mkEvent(1, "session.start", ""),
		mkEvent(2, "task.dispatch", "T1"),
		mkEvent(3, "task.fail", "T1"),
	}
	id, mode := DecideResume(events)
	if mode != ResumeNextTask {
		t.Errorf("got mode=%s, want next_task", mode)
	}
	if id != "T1" {
		t.Errorf("got taskID=%q, want T1", id)
	}
}

func TestDecideResume_SessionEnd(t *testing.T) {
	t.Parallel()
	// session.end is terminal — the session already finished, nothing
	// to do on resume.
	events := []bus.Event{
		mkEvent(1, "session.start", ""),
		mkEvent(2, "task.dispatch", "T1"),
		mkEvent(3, "task.complete", "T1"),
		mkEvent(4, "session.end", ""),
	}
	id, mode := DecideResume(events)
	if mode != ResumeAlreadyDone {
		t.Errorf("got mode=%s, want already_done", mode)
	}
	if id != "" {
		t.Errorf("got taskID=%q, want empty", id)
	}
}

func TestDecideResume_SessionComplete(t *testing.T) {
	t.Parallel()
	// "session.complete" is the cloudswarm-protocol.md variant of
	// "session.end"; resume must treat both as terminal.
	events := []bus.Event{
		mkEvent(1, "session.start", ""),
		mkEvent(2, "task.complete", "T1"),
		mkEvent(3, "session.complete", ""),
	}
	_, mode := DecideResume(events)
	if mode != ResumeAlreadyDone {
		t.Errorf("got mode=%s, want already_done", mode)
	}
}

func TestDecideResume_PrefixedEventType(t *testing.T) {
	t.Parallel()
	// r1-server emits "stoke.session.start"-style type strings. The
	// suffix-based matcher should still classify these correctly.
	events := []bus.Event{
		mkEvent(1, "stoke.session.start", ""),
		mkEvent(2, "stoke.task.dispatch", "T7"),
	}
	id, mode := DecideResume(events)
	if mode != ResumeRetryTask {
		t.Errorf("got mode=%s, want retry_task", mode)
	}
	if id != "T7" {
		t.Errorf("got taskID=%q, want T7", id)
	}
}

func TestDecideResume_MostRecentMarkerWins(t *testing.T) {
	t.Parallel()
	// A long log with many task markers: only the most recent task.*
	// or session.* event should influence the decision. Here T3 was
	// dispatched last and never completed → retry T3, not T1 or T2.
	events := []bus.Event{
		mkEvent(1, "session.start", ""),
		mkEvent(2, "task.dispatch", "T1"),
		mkEvent(3, "task.complete", "T1"),
		mkEvent(4, "task.dispatch", "T2"),
		mkEvent(5, "task.complete", "T2"),
		mkEvent(6, "task.dispatch", "T3"),
		// Noise after the dispatch — should not change the decision.
		mkEvent(7, "worker.action.started", ""),
		mkEvent(8, "ledger.node.added", ""),
	}
	id, mode := DecideResume(events)
	if mode != ResumeRetryTask {
		t.Errorf("got mode=%s, want retry_task", mode)
	}
	if id != "T3" {
		t.Errorf("got taskID=%q, want T3", id)
	}
}

func TestDecideResume_TaskIDFallback(t *testing.T) {
	t.Parallel()
	// If the dispatch event has no Scope.TaskID (malformed producer,
	// legacy data), we fall back to the event ID so the caller at least
	// has an anchor rather than an empty string.
	ev := bus.Event{
		ID:       "dispatch-abc",
		Type:     "task.dispatch",
		Sequence: 1,
	}
	id, mode := DecideResume([]bus.Event{ev})
	if mode != ResumeRetryTask {
		t.Errorf("got mode=%s, want retry_task", mode)
	}
	if id != "dispatch-abc" {
		t.Errorf("got taskID=%q, want dispatch-abc", id)
	}
}

func TestResumeMode_String(t *testing.T) {
	t.Parallel()
	// Guard against accidental reordering of the iota values — every
	// mode must render to a stable, human-readable tag.
	cases := map[ResumeMode]string{
		ResumeFreshStart: "fresh_start",
		ResumeRetryTask:  "retry_task",
		ResumeNextTask:   "next_task",
		ResumeAlreadyDone: "already_done",
		ResumeMode(999):  "unknown",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("ResumeMode(%d).String() = %q, want %q", m, got, want)
		}
	}
}
