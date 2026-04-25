package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/plan"
	"github.com/RelayOne/r1-agent/internal/streamjson"
)

// Spec-2 item 10: per-session streamjson lifecycle events.
//
// Each helper on sowNativeConfig must:
//   - no-op when StreamJSON is nil (legacy sow / ship paths)
//   - emit a correctly-shaped line when StreamJSON is set + enabled

func TestSowNativeStreamJSONNoOpWhenNil(t *testing.T) {
	// No emitter wired — every helper should be a no-op with no
	// output, no panic, no race.
	cfg := sowNativeConfig{}
	session := plan.Session{ID: "S1", Title: "no-op", Tasks: []plan.Task{{ID: "T1"}}}
	cfg.emitSessionStart(session)
	cfg.emitSessionEnd(session, true, "done")
	cfg.emitTaskStart(session.ID, plan.Task{ID: "T1"})
	cfg.emitTaskEnd(session.ID, plan.Task{ID: "T1"}, true, "pass")
	cfg.emitACResult(session.ID, plan.AcceptanceResult{CriterionID: "AC-1", Passed: true})
	cfg.emitPlanReady(&plan.SOW{})
}

func TestSowNativeStreamJSONEmitsSessionStart(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	cfg := sowNativeConfig{StreamJSON: tl}
	session := plan.Session{
		ID:                 "sess-42",
		Title:              "ship the thing",
		Tasks:              []plan.Task{{ID: "T1"}, {ID: "T2"}},
		AcceptanceCriteria: []plan.AcceptanceCriterion{{ID: "AC-1"}},
	}
	cfg.emitSessionStart(session)
	tl.Drain(time.Second)
	out := buf.String()
	if !strings.Contains(out, `"subtype":"stoke.session.start"`) {
		t.Errorf("expected stoke.session.start subtype in %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/session":"sess-42"`) {
		t.Errorf("expected session id in %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/task_count":2`) {
		t.Errorf("expected task_count=2 in %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/ac_count":1`) {
		t.Errorf("expected ac_count=1 in %q", out)
	}
}

func TestSowNativeStreamJSONEmitsTaskEndOnCriticalLane(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	cfg := sowNativeConfig{StreamJSON: tl}
	cfg.emitTaskEnd("sess-42", plan.Task{ID: "T1", Description: "write the thing"}, true, "green")
	tl.Drain(time.Second)
	out := buf.String()
	if !strings.Contains(out, `"subtype":"task.complete"`) {
		t.Errorf("task.complete must use the critical subtype name: %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/success":true`) {
		t.Errorf("expected success flag in %q", out)
	}
}

func TestSowNativeStreamJSONEmitsACResult(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	cfg := sowNativeConfig{StreamJSON: tl}
	cfg.emitACResult("sess-42", plan.AcceptanceResult{
		CriterionID:    "AC-7",
		Description:    "run tests",
		Passed:         false,
		JudgeRuled:     true,
		JudgeReasoning: "soft-pass after env fix",
	})
	tl.Drain(time.Second)
	out := buf.String()
	if !strings.Contains(out, `"subtype":"stoke.ac.result"`) {
		t.Errorf("expected stoke.ac.result subtype in %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/ac_id":"AC-7"`) {
		t.Errorf("expected AC id in %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/judge_ruled":true`) {
		t.Errorf("expected judge_ruled in %q", out)
	}
}
