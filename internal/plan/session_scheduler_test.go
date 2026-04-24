package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionSchedulerSuccess(t *testing.T) {
	dir := t.TempDir()
	// Create a file that acceptance criteria can check
	os.WriteFile(filepath.Join(dir, "output.txt"), []byte("hello world"), 0o600)

	sow := &SOW{
		ID: "test", Name: "Test",
		Sessions: []Session{
			{
				ID: "S1", Title: "Setup",
				Tasks: []Task{{ID: "T1", Description: "init"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "output exists", FileExists: "output.txt"},
				},
			},
			{
				ID: "S2", Title: "Build",
				Tasks: []Task{{ID: "T2", Description: "build"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC2", Description: "echo works", Command: "echo ok"},
				},
			},
		},
	}

	ss := NewSessionScheduler(sow, dir)
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
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
	if len(results) != 2 {
		t.Fatalf("results=%d, want 2", len(results))
	}
	for _, r := range results {
		if !r.AcceptanceMet {
			t.Errorf("session %s acceptance not met", r.SessionID)
		}
	}
}

func TestSessionSchedulerStopsOnTaskFailure(t *testing.T) {
	sow := &SOW{
		ID: "fail", Name: "Fail",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "fail"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
			{ID: "S2", Title: "B",
				Tasks:              []Task{{ID: "T2", Description: "never runs"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		return []TaskExecResult{{TaskID: "T1", Success: false, Error: fmt.Errorf("boom")}}, nil
	}

	results, err := ss.Run(context.Background(), execFn)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(results) != 1 {
		t.Errorf("should stop after S1: results=%d", len(results))
	}
	if !strings.Contains(err.Error(), "T1 failed") {
		t.Errorf("error=%q", err)
	}
}

func TestSessionSchedulerStopsOnAcceptanceFail(t *testing.T) {
	sow := &SOW{
		ID: "ac-fail", Name: "AC Fail",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks: []Task{{ID: "T1", Description: "runs fine"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "missing file", FileExists: "nonexistent.txt"},
				}},
			{ID: "S2", Title: "B",
				Tasks:              []Task{{ID: "T2", Description: "never runs"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		var results []TaskExecResult
		for _, t := range session.Tasks {
			results = append(results, TaskExecResult{TaskID: t.ID, Success: true})
		}
		return results, nil
	}

	results, err := ss.Run(context.Background(), execFn)
	if err == nil {
		t.Fatal("expected acceptance failure error")
	}
	if len(results) != 1 {
		t.Errorf("should stop after S1: results=%d", len(results))
	}
	if !results[0].AcceptanceMet {
		// Expected: acceptance not met
	} else {
		t.Error("acceptance should not be met")
	}
}

func TestSessionSchedulerInfraCheck(t *testing.T) {
	// Override envLookup to simulate missing vars
	origLookup := envLookup
	defer func() { envLookup = origLookup }()
	envLookup = func(key string) string { return "" }

	sow := &SOW{
		ID: "infra", Name: "Infra",
		Stack: StackSpec{
			Infra: []InfraRequirement{
				{Name: "postgres", EnvVars: []string{"DATABASE_URL"}},
			},
		},
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "needs db"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}},
				InfraNeeded:        []string{"postgres"}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		t.Error("should not execute when infra check fails")
		return nil, nil
	}

	results, err := ss.Run(context.Background(), execFn)
	if err == nil {
		t.Fatal("expected infra check error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error should mention missing var: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("results=%d", len(results))
	}
}

func TestSessionSchedulerInfraCheckPasses(t *testing.T) {
	origLookup := envLookup
	defer func() { envLookup = origLookup }()
	envLookup = func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://localhost/test"
		}
		return ""
	}

	sow := &SOW{
		ID: "infra-ok", Name: "Infra OK",
		Stack: StackSpec{
			Infra: []InfraRequirement{
				{Name: "postgres", EnvVars: []string{"DATABASE_URL"}},
			},
		},
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "needs db"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "echo", Command: "echo ok"}},
				InfraNeeded:        []string{"postgres"}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		return []TaskExecResult{{TaskID: "T1", Success: true}}, nil
	}

	results, err := ss.Run(context.Background(), execFn)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || !results[0].AcceptanceMet {
		t.Error("session should pass")
	}
}

func TestSessionSchedulerContextCancellation(t *testing.T) {
	sow := &SOW{
		ID: "cancel", Name: "Cancel",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "a"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	ss := NewSessionScheduler(sow, t.TempDir())
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		return nil, ctx.Err()
	}

	_, err := ss.Run(ctx, execFn)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestSessionSchedulerExecError(t *testing.T) {
	sow := &SOW{
		ID: "exec-err", Name: "Exec Err",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "a"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		return nil, fmt.Errorf("engine crashed")
	}

	_, err := ss.Run(context.Background(), execFn)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "engine crashed") {
		t.Errorf("error=%q", err)
	}
}

func TestSessionSchedulerDryRun(t *testing.T) {
	sow := &SOW{
		ID: "dry", Name: "Dry Run Test",
		Stack: StackSpec{
			Language: "rust",
			Monorepo: &MonorepoSpec{Tool: "cargo-workspace"},
			Infra: []InfraRequirement{
				{Name: "postgres", Version: "15", Extensions: []string{"pgvector"}},
			},
		},
		Sessions: []Session{
			{ID: "S1", Phase: "foundation", Title: "Core", Tasks: []Task{{ID: "T1", Description: "a"}, {ID: "T2", Description: "b"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
			{ID: "S2", Phase: "core", Title: "API", Tasks: []Task{{ID: "T3", Description: "c"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok"}}},
		},
	}

	ss := NewSessionScheduler(sow, t.TempDir())
	summary := ss.DryRun()

	if !strings.Contains(summary, "Dry Run Test") {
		t.Error("should contain SOW name")
	}
	if !strings.Contains(summary, "rust") {
		t.Error("should contain language")
	}
	if !strings.Contains(summary, "cargo-workspace") {
		t.Error("should contain monorepo tool")
	}
	if !strings.Contains(summary, "postgres") {
		t.Error("should contain infra")
	}
	if !strings.Contains(summary, "Sessions: 2") {
		t.Error("should show session count")
	}
	if !strings.Contains(summary, "Total tasks: 3") {
		t.Error("should show total tasks")
	}
}
