package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/streamjson"
)

// TestEmitSessionEnd_CS5Fields asserts all 5 canonical snapshot fields
// are present in the stoke.session.end event: session_id,
// ledger_digest, memory_delta_ref, cost_total, plan_summary
// (with tasks_completed + tasks_failed).
func TestEmitSessionEnd_CS5Fields(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	cfg := sowNativeConfig{StreamJSON: tl}

	sess := plan.Session{ID: "sess-xyz"}
	cfg.emitSessionEnd(sess, true, "all-ac-passed")
	tl.Drain(500 * time.Millisecond)

	// Parse each emitted line until we find the session.end system event.
	var got map[string]any
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["subtype"] == "stoke.session.end" || m["type"] == "stoke.session.end" {
			got = m
			break
		}
	}
	if got == nil {
		t.Fatalf("no stoke.session.end line found in: %s", buf.String())
	}

	for _, k := range []string{"session_id", "ledger_digest", "memory_delta_ref", "cost_total", "plan_summary"} {
		if _, has := got[k]; !has {
			t.Errorf("missing field %q in payload: %v", k, got)
		}
	}
	if got["session_id"] != "sess-xyz" {
		t.Errorf("session_id = %v", got["session_id"])
	}
	ps, ok := got["plan_summary"].(map[string]any)
	if !ok {
		t.Fatalf("plan_summary not an object: %v", got["plan_summary"])
	}
	if _, has := ps["tasks_completed"]; !has {
		t.Error("plan_summary missing tasks_completed")
	}
	if _, has := ps["tasks_failed"]; !has {
		t.Error("plan_summary missing tasks_failed")
	}
}

// TestSummarizeSessionTasks_Pass covers the happy path: all tasks
// counted as completed.
func TestSummarizeSessionTasks_Pass(t *testing.T) {
	sess := plan.Session{Tasks: []plan.Task{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	completed, failed := summarizeSessionTasks(sess, true)
	if completed != 3 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 3/0", completed, failed)
	}
}

// TestSummarizeSessionTasks_Fail covers the heuristic: one task failed.
func TestSummarizeSessionTasks_Fail(t *testing.T) {
	sess := plan.Session{Tasks: []plan.Task{{ID: "a"}, {ID: "b"}}}
	completed, failed := summarizeSessionTasks(sess, false)
	if completed != 1 || failed != 1 {
		t.Errorf("completed=%d failed=%d, want 1/1", completed, failed)
	}
}

// TestSummarizeSessionTasks_EmptyPlanFail zero-task + fail -> zero counts.
func TestSummarizeSessionTasks_EmptyPlanFail(t *testing.T) {
	sess := plan.Session{}
	completed, failed := summarizeSessionTasks(sess, false)
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}
