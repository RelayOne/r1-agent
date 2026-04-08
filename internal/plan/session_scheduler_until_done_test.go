package plan

import (
	"context"
	"fmt"
	"testing"
)

// TestSchedulerAttemptsAllSessionsWhenContinueOnFailure verifies the
// multi-session "build until it's all done" contract: with
// ContinueOnFailure=true, a failure in S1 does NOT halt S2-S13. The
// scheduler attempts every session and returns results for all of
// them.
func TestSchedulerAttemptsAllSessionsWhenContinueOnFailure(t *testing.T) {
	sow := &SOW{
		ID: "multi", Name: "Multi Session Build",
		Sessions: []Session{
			{ID: "S1", Title: "first",
				Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "d", Command: "true"}}},
			{ID: "S2", Title: "second",
				Tasks: []Task{{ID: "T2", Description: "y"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "d", Command: "true"}}},
			{ID: "S3", Title: "third",
				Tasks: []Task{{ID: "T3", Description: "z"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC3", Description: "d", Command: "true"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	ss.ContinueOnFailure = true

	// S1 "succeeds", S2 "fails", S3 "succeeds". With ContinueOnFailure
	// on, all three should run.
	executed := []string{}
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		executed = append(executed, session.ID)
		if session.ID == "S2" {
			return []TaskExecResult{{TaskID: "T2", Success: false, Error: fmt.Errorf("simulated failure")}}, nil
		}
		return []TaskExecResult{{TaskID: session.Tasks[0].ID, Success: true}}, nil
	}

	results, _ := ss.Run(context.Background(), execFn)

	// All three sessions must have been attempted.
	if len(executed) != 3 {
		t.Errorf("expected all 3 sessions to run, got %v", executed)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}

	// S2 should be marked failed; S1 and S3 should be marked passing.
	var s1Ok, s2Fail, s3Ok bool
	for _, r := range results {
		switch r.SessionID {
		case "S1":
			s1Ok = r.AcceptanceMet && r.Error == nil
		case "S2":
			s2Fail = r.Error != nil
		case "S3":
			s3Ok = r.AcceptanceMet && r.Error == nil
		}
	}
	if !s1Ok {
		t.Error("S1 should be marked successful")
	}
	if !s2Fail {
		t.Error("S2 should be marked failed")
	}
	if !s3Ok {
		t.Error("S3 should have run and succeeded even though S2 failed")
	}
}

// TestSchedulerHaltsOnFailureWhenContinueOnFailureFalse is the negative
// case: single-session or user-configured ContinueOnFailure=false halts
// at the first failure.
func TestSchedulerHaltsOnFailureWhenContinueOnFailureFalse(t *testing.T) {
	sow := &SOW{
		ID: "halt", Name: "Halt Test",
		Sessions: []Session{
			{ID: "S1", Title: "first",
				Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "d", Command: "true"}}},
			{ID: "S2", Title: "second",
				Tasks: []Task{{ID: "T2", Description: "y"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "d", Command: "true"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	ss.ContinueOnFailure = false
	ss.MaxSessionRetries = 1 // skip retry for clarity

	executed := []string{}
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		executed = append(executed, session.ID)
		return []TaskExecResult{{TaskID: session.Tasks[0].ID, Success: false, Error: fmt.Errorf("fail")}}, nil
	}

	_, err := ss.Run(context.Background(), execFn)
	if err == nil {
		t.Error("should have returned an error")
	}
	if len(executed) != 1 {
		t.Errorf("ContinueOnFailure=false should halt after S1: %v", executed)
	}
}

// TestSchedulerContinueAcrossMixedFailureTypes exercises a realistic
// multi-session build where S1 succeeds, S2 fails task-level, S3 fails
// acceptance, and S4 succeeds. With ContinueOnFailure=true all four
// should be reached.
func TestSchedulerContinueAcrossMixedFailureTypes(t *testing.T) {
	sow := &SOW{
		ID: "mixed", Name: "Mixed",
		Sessions: []Session{
			{ID: "S1", Title: "ok", Tasks: []Task{{ID: "T1"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "d", Command: "true"}}},
			{ID: "S2", Title: "task fail", Tasks: []Task{{ID: "T2"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "d", Command: "true"}}},
			{ID: "S3", Title: "acceptance fail", Tasks: []Task{{ID: "T3"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC3", Description: "d", Command: "false"}}}, // will fail
			{ID: "S4", Title: "ok again", Tasks: []Task{{ID: "T4"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC4", Description: "d", Command: "true"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	ss.ContinueOnFailure = true
	ss.MaxSessionRetries = 1

	executed := []string{}
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		executed = append(executed, session.ID)
		success := session.ID != "S2" // S2 is a task-level failure
		return []TaskExecResult{{TaskID: session.Tasks[0].ID, Success: success}}, nil
	}

	results, _ := ss.Run(context.Background(), execFn)
	if len(executed) != 4 {
		t.Errorf("expected all 4 sessions to run, got %v", executed)
	}
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}
}
