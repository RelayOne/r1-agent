package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
)

func TestReconstructSession_EmptyLog(t *testing.T) {
	st := reconstructSession("sess-a", nil)
	if st.EventCount != 0 {
		t.Errorf("count=%d", st.EventCount)
	}
	if !strings.Contains(st.ResumeHint, "no events") {
		t.Errorf("hint=%q", st.ResumeHint)
	}
}

func TestReconstructSession_CountsAndLast(t *testing.T) {
	now := time.Now().UTC()
	events := []bus.Event{
		{ID: "e1", Type: "task.dispatch", Timestamp: now, Scope: bus.Scope{LoopID: "sess-a"}, Sequence: 1},
		{ID: "e2", Type: "tool.call", Timestamp: now.Add(time.Second), Scope: bus.Scope{LoopID: "sess-a", TaskID: "T1"}, Sequence: 2},
		{ID: "e3", Type: "task.complete", Timestamp: now.Add(2 * time.Second), Scope: bus.Scope{LoopID: "sess-a", TaskID: "T1"}, Sequence: 3},
		{ID: "e4", Type: "task.dispatch", Timestamp: now.Add(3 * time.Second), Scope: bus.Scope{LoopID: "sess-other"}, Sequence: 4},
	}
	st := reconstructSession("sess-a", events)
	if st.EventCount != 3 {
		t.Errorf("count=%d, want 3 (other-session event excluded)", st.EventCount)
	}
	if st.LastEventID != "e3" {
		t.Errorf("last=%q", st.LastEventID)
	}
	if st.LastEventType != "task.complete" {
		t.Errorf("last type=%q", st.LastEventType)
	}
	if len(st.TaskIDs) != 1 || st.TaskIDs[0] != "T1" {
		t.Errorf("tasks=%v", st.TaskIDs)
	}
	if !strings.Contains(st.ResumeHint, "completed") {
		t.Errorf("hint should reflect task.complete: %q", st.ResumeHint)
	}
}

func TestReconstructSession_MatchesMissionIDFallback(t *testing.T) {
	events := []bus.Event{
		{ID: "e1", Type: "mission.start", Scope: bus.Scope{MissionID: "M-1"}, Sequence: 1},
		{ID: "e2", Type: "mission.progress", Scope: bus.Scope{MissionID: "M-1"}, Sequence: 2},
	}
	st := reconstructSession("M-1", events)
	if st.EventCount != 2 {
		t.Errorf("MissionID fallback failed: count=%d", st.EventCount)
	}
	if len(st.MissionIDs) != 1 || st.MissionIDs[0] != "M-1" {
		t.Errorf("missions=%v", st.MissionIDs)
	}
}

func TestResumeHintMapsKnownTypes(t *testing.T) {
	cases := map[string]string{
		"task.dispatch":   "task dispatch",
		"task.complete":   "completed",
		"task.fail":       "failed",
		"verify.tier":     "mid-descent",
		"hitl_required":   "human-in-the-loop",
		"session.complete": "finished cleanly",
	}
	for typ, marker := range cases {
		got := resumeHint(bus.Event{Type: bus.EventType(typ)})
		if !strings.Contains(got, marker) {
			t.Errorf("hint for %q missing %q: %q", typ, marker, got)
		}
	}
}

func TestEventMatchesSession_ByID(t *testing.T) {
	e := bus.Event{ID: "sess-b-task-1-evt-xyz"}
	if !eventMatchesSession(e, "sess-b") {
		t.Error("substring match on event ID should work")
	}
	if eventMatchesSession(e, "sess-z") {
		t.Error("non-matching substring should return false")
	}
}
