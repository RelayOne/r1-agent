package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSOWState_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadSOWState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if state != nil {
		t.Errorf("expected nil state for fresh dir, got %+v", state)
	}
}

func TestSOWState_SaveAndReload(t *testing.T) {
	dir := t.TempDir()
	sow := &SOW{
		ID: "test", Name: "Test",
		Sessions: []Session{
			{ID: "S1", Title: "first"},
			{ID: "S2", Title: "second"},
		},
	}
	st := NewSOWState(sow)
	st.Sessions[0].Status = "done"
	st.Sessions[0].AcceptanceMet = true
	if err := SaveSOWState(dir, st); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadSOWState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil || loaded.SOWID != "test" {
		t.Fatalf("loaded wrong state: %+v", loaded)
	}
	if !loaded.IsSessionComplete("S1") {
		t.Errorf("S1 should be complete")
	}
	if loaded.IsSessionComplete("S2") {
		t.Errorf("S2 should not be complete")
	}
}

func TestSOWState_MergeSOW_NewSessions(t *testing.T) {
	sow1 := &SOW{
		ID: "test",
		Sessions: []Session{
			{ID: "S1", Title: "first"},
		},
	}
	st := NewSOWState(sow1)
	st.Sessions[0].Status = "done"
	st.Sessions[0].AcceptanceMet = true

	sow2 := &SOW{
		ID: "test",
		Sessions: []Session{
			{ID: "S1", Title: "first"},
			{ID: "S2", Title: "second"},
		},
	}
	st.MergeSOW(sow2)
	if len(st.Sessions) != 2 {
		t.Fatalf("expected 2 sessions after merge, got %d", len(st.Sessions))
	}
	if !st.IsSessionComplete("S1") {
		t.Errorf("S1 completion should survive merge")
	}
	if st.IsSessionComplete("S2") {
		t.Errorf("S2 should be pending")
	}
}

func TestSOWState_MergeSOW_RemovedSessions(t *testing.T) {
	sow1 := &SOW{
		ID: "test",
		Sessions: []Session{
			{ID: "S1"},
			{ID: "S2"},
		},
	}
	st := NewSOWState(sow1)
	sow2 := &SOW{ID: "test", Sessions: []Session{{ID: "S1"}}}
	st.MergeSOW(sow2)
	s2 := st.SessionByID("S2")
	if s2 == nil {
		t.Fatal("S2 should be preserved")
	}
	if s2.Status != "skipped" {
		t.Errorf("S2 status = %q, want skipped", s2.Status)
	}
}

func TestSOWState_RemainingSessions(t *testing.T) {
	st := &SOWState{
		Sessions: []SessionRecord{
			{SessionID: "S1", Status: "done", AcceptanceMet: true},
			{SessionID: "S2", Status: "failed"},
			{SessionID: "S3", Status: "pending"},
			{SessionID: "S4", Status: "skipped"},
		},
	}
	remaining := st.RemainingSessions()
	if len(remaining) != 2 || remaining[0] != "S2" || remaining[1] != "S3" {
		t.Errorf("remaining=%v, want [S2 S3]", remaining)
	}
}

func TestSessionScheduler_Resume_SkipsCompleted(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("ok"), 0644)

	sow := &SOW{
		ID: "resume", Name: "Resume",
		Sessions: []Session{
			{ID: "S1", Title: "first", Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok", FileExists: "ok.txt"}}},
			{ID: "S2", Title: "second", Tasks: []Task{{ID: "T2", Description: "y"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok", FileExists: "ok.txt"}}},
		},
	}

	// First run: S1 completes, S2 fails hard
	ss := NewSessionScheduler(sow, dir)
	ss.LoadOrCreateState()
	attempts := map[string]int{}
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		attempts[session.ID]++
		if session.ID == "S2" {
			return []TaskExecResult{{TaskID: "T2", Success: false, Error: fmt.Errorf("boom")}}, nil
		}
		return []TaskExecResult{{TaskID: "T1", Success: true}}, nil
	}
	_, _ = ss.Run(context.Background(), execFn)

	// S1 should be recorded as done
	state, err := LoadSOWState(dir)
	if err != nil || state == nil {
		t.Fatalf("no state after first run: err=%v state=%v", err, state)
	}
	if !state.IsSessionComplete("S1") {
		t.Errorf("S1 should be complete after first run")
	}
	if state.IsSessionComplete("S2") {
		t.Errorf("S2 should NOT be complete after first run")
	}

	// Second run with Resume=true should skip S1 entirely
	attempts = map[string]int{}
	ss2 := NewSessionScheduler(sow, dir)
	ss2.Resume = true
	ss2.LoadOrCreateState()
	execFn2 := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		attempts[session.ID]++
		return []TaskExecResult{{TaskID: session.Tasks[0].ID, Success: true}}, nil
	}
	results, err := ss2.Run(context.Background(), execFn2)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if attempts["S1"] != 0 {
		t.Errorf("S1 should have been skipped on resume, but ran %d times", attempts["S1"])
	}
	if attempts["S2"] != 1 {
		t.Errorf("S2 should have run exactly once, ran %d times", attempts["S2"])
	}

	// S1 result should be marked skipped
	var s1Result SessionResult
	for _, r := range results {
		if r.SessionID == "S1" {
			s1Result = r
		}
	}
	if !s1Result.Skipped {
		t.Errorf("S1 result should have Skipped=true: %+v", s1Result)
	}
}

func TestSessionScheduler_Retry_SucceedsOnSecondAttempt(t *testing.T) {
	dir := t.TempDir()
	sow := &SOW{
		ID: "retry", Name: "Retry",
		Sessions: []Session{
			{ID: "S1", Title: "flaky", Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "echo", Command: "echo ok"}}},
		},
	}
	ss := NewSessionScheduler(sow, dir)
	ss.MaxSessionRetries = 3

	attempts := 0
	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		attempts++
		if attempts < 2 {
			return []TaskExecResult{{TaskID: "T1", Success: false, Error: fmt.Errorf("flaky")}}, nil
		}
		return []TaskExecResult{{TaskID: "T1", Success: true}}, nil
	}
	results, err := ss.Run(context.Background(), execFn)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].Attempts != 2 {
		t.Errorf("expected 2 attempts, got results=%+v", results)
	}
	if !results[0].AcceptanceMet {
		t.Errorf("acceptance should be met after retry")
	}
}

func TestSessionScheduler_ContinueOnFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("ok"), 0644)
	sow := &SOW{
		ID: "cof", Name: "Cof",
		Sessions: []Session{
			{ID: "S1", Title: "fail me", Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "missing", FileExists: "missing.txt"}}},
			{ID: "S2", Title: "succeed", Tasks: []Task{{ID: "T2", Description: "y"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok", FileExists: "ok.txt"}}},
		},
	}
	ss := NewSessionScheduler(sow, dir)
	ss.ContinueOnFailure = true

	execFn := func(ctx context.Context, session Session) ([]TaskExecResult, error) {
		return []TaskExecResult{{TaskID: session.Tasks[0].ID, Success: true}}, nil
	}
	results, err := ss.Run(context.Background(), execFn)
	if err == nil {
		t.Error("expected error to surface from S1 failure")
	}
	if len(results) != 2 {
		t.Errorf("ContinueOnFailure should still run S2, got %d results", len(results))
	}
	if results[0].AcceptanceMet {
		t.Errorf("S1 should not have passed")
	}
	if !results[1].AcceptanceMet {
		t.Errorf("S2 should have passed")
	}
}

func TestLoadSOW_YAML(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `id: test-yaml
name: Test YAML
sessions:
  - id: S1
    title: First session
    tasks:
      - id: T1
        description: do the thing
    acceptance_criteria:
      - id: AC1
        description: check
        command: echo ok
`
	path := filepath.Join(dir, "stoke-sow.yaml")
	os.WriteFile(path, []byte(yamlContent), 0644)

	sow, err := LoadSOW(path)
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}
	if sow.ID != "test-yaml" {
		t.Errorf("id = %q", sow.ID)
	}
	if len(sow.Sessions) != 1 || sow.Sessions[0].ID != "S1" {
		t.Errorf("sessions = %+v", sow.Sessions)
	}
	if len(sow.Sessions[0].AcceptanceCriteria) != 1 || sow.Sessions[0].AcceptanceCriteria[0].Command != "echo ok" {
		t.Errorf("criteria = %+v", sow.Sessions[0].AcceptanceCriteria)
	}
}

func TestLoadSOW_JSONStillWorks(t *testing.T) {
	dir := t.TempDir()
	jsonContent := `{"id":"test-json","name":"Test","sessions":[{"id":"S1","title":"t","tasks":[{"id":"T1","description":"x"}],"acceptance_criteria":[{"id":"AC1","description":"d"}]}]}`
	path := filepath.Join(dir, "stoke-sow.json")
	os.WriteFile(path, []byte(jsonContent), 0644)

	sow, err := LoadSOW(path)
	if err != nil {
		t.Fatalf("load json: %v", err)
	}
	if sow.ID != "test-json" {
		t.Errorf("id = %q", sow.ID)
	}
}

func TestLoadSOWFromDir_PrefersJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stoke-sow.json"), []byte(`{"id":"json-wins","name":"j","sessions":[]}`), 0644)
	os.WriteFile(filepath.Join(dir, "stoke-sow.yaml"), []byte("id: yaml-loses\nname: y\nsessions: []\n"), 0644)

	sow, err := LoadSOWFromDir(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if sow.ID != "json-wins" {
		t.Errorf("expected json precedence, got %q", sow.ID)
	}
}

func TestLoadSOWFromDir_YAMLFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stoke-sow.yaml"), []byte("id: yaml-only\nname: y\nsessions: []\n"), 0644)

	sow, err := LoadSOWFromDir(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if sow.ID != "yaml-only" {
		t.Errorf("expected yaml fallback, got %q", sow.ID)
	}
}

func TestDryRun_ShowsAcceptanceCommands(t *testing.T) {
	sow := &SOW{
		ID: "dry", Name: "Dry",
		Sessions: []Session{
			{ID: "S1", Title: "x",
				Tasks: []Task{{ID: "T1", Description: "x"}},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "cmd", Command: "go test ./..."},
					{ID: "AC2", Description: "exists", FileExists: "README.md"},
					{ID: "AC3", Description: "match", ContentMatch: &ContentMatchCriterion{File: "go.mod", Pattern: "stoke"}},
				}},
		},
	}
	ss := NewSessionScheduler(sow, t.TempDir())
	out := ss.DryRun()
	for _, want := range []string{"$ go test ./...", "exists: README.md", "go.mod ~ \"stoke\""} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run missing %q:\n%s", want, out)
		}
	}
}
