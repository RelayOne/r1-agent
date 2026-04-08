package plan

import (
	"context"
	"testing"
)

func TestAppendSession_PickedUpMidRun(t *testing.T) {
	sow := &SOW{
		ID: "a", Name: "A",
		Sessions: []Session{
			{ID: "S1", Title: "First",
				Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "d", Command: "true"}}},
		},
	}
	ss := NewSessionScheduler(sow, t.TempDir())

	executed := []string{}
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		executed = append(executed, session.ID)
		// When S1 runs, append a new S2 mid-run.
		if session.ID == "S1" {
			ss.AppendSession(Session{
				ID: "S2", Title: "Appended",
				Tasks: []Task{{ID: "T2", Description: "y"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "d", Command: "true"}},
			})
		}
		var results []TaskExecResult
		for _, t := range session.Tasks {
			results = append(results, TaskExecResult{TaskID: t.ID, Success: true})
		}
		return results, nil
	}

	results, err := ss.Run(context.Background(), execFn)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(executed) != 2 || executed[0] != "S1" || executed[1] != "S2" {
		t.Errorf("expected S1,S2 execution order, got %v", executed)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestAppendSession_MultipleAppends(t *testing.T) {
	sow := &SOW{
		ID: "multi", Name: "Multi",
		Sessions: []Session{
			{ID: "S1", Title: "First",
				Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "d", Command: "true"}}},
		},
	}
	ss := NewSessionScheduler(sow, t.TempDir())

	appended := 0
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		// Each session appends another, up to 3 total
		if appended < 2 {
			appended++
			ss.AppendSession(Session{
				ID:    "cont-" + session.ID,
				Title: "cont from " + session.ID,
				Tasks: []Task{{ID: "ct", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "ac", Description: "d", Command: "true"}},
			})
		}
		return []TaskExecResult{{TaskID: session.Tasks[0].ID, Success: true}}, nil
	}
	results, _ := ss.Run(context.Background(), execFn)
	if len(results) != 3 {
		t.Errorf("expected 3 sessions (1 original + 2 appended), got %d", len(results))
	}
}

func TestAppendSession_UpdatesStateFile(t *testing.T) {
	dir := t.TempDir()
	sow := &SOW{
		ID: "st", Name: "State",
		Sessions: []Session{
			{ID: "S1", Title: "First",
				Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "d", Command: "true"}}},
		},
	}
	ss := NewSessionScheduler(sow, dir)
	if err := ss.LoadOrCreateState(); err != nil {
		t.Fatalf("LoadOrCreateState: %v", err)
	}

	ss.AppendSession(Session{
		ID: "S-extra", Title: "extra",
		Tasks: []Task{{ID: "TX", Description: "x"}},
		AcceptanceCriteria: []AcceptanceCriterion{{ID: "ACX", Description: "d"}},
	})

	loaded, err := LoadSOWState(dir)
	if err != nil {
		t.Fatalf("LoadSOWState: %v", err)
	}
	if loaded == nil {
		t.Fatal("no state persisted")
	}
	found := false
	for _, s := range loaded.Sessions {
		if s.SessionID == "S-extra" {
			found = true
			break
		}
	}
	if !found {
		t.Error("appended session not recorded in state file")
	}
}
