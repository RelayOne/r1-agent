package session

import (
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
)

func TestSQLStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Save state
	state := &State{
		PlanID:       "sql-test",
		Tasks:        []plan.Task{{ID: "T1", Description: "first", Status: plan.StatusDone}},
		TotalCostUSD: 1.50,
		StartedAt:    time.Now(),
	}
	if err := s.SaveState(state); err != nil {
		t.Fatal(err)
	}

	// Load state
	loaded, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("expected state")
	}
	if loaded.PlanID != "sql-test" {
		t.Errorf("plan=%q", loaded.PlanID)
	}
	if loaded.TotalCostUSD != 1.50 {
		t.Errorf("cost=%f", loaded.TotalCostUSD)
	}
	if len(loaded.Tasks) != 1 || loaded.Tasks[0].Status != plan.StatusDone {
		t.Errorf("tasks=%v", loaded.Tasks)
	}

	// Clear
	s.ClearState()
	cleared, _ := s.LoadState()
	if cleared != nil {
		t.Error("state should be nil after clear")
	}
}

func TestSQLStoreAttempts(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.SaveAttempt(Attempt{
		TaskID: "T1", Number: 1, Success: false,
		FailClass: "BuildFailed", FailSummary: "TS errors",
		RootCause: "missing type", DiffSummary: "+++ auth.ts",
	})
	s.SaveAttempt(Attempt{
		TaskID: "T1", Number: 2, Success: true, CostUSD: 0.05,
	})

	attempts, err := s.LoadAttempts("T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d", len(attempts))
	}
	if attempts[0].FailClass != "BuildFailed" {
		t.Errorf("class=%q", attempts[0].FailClass)
	}
	if !attempts[1].Success {
		t.Error("attempt 2 should be success")
	}

	// Auto-learning should have fired
	learning, _ := s.LoadLearning()
	if len(learning.Patterns) == 0 {
		t.Error("expected auto-learned pattern")
	}
}

func TestSQLStoreStats(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.SaveAttempt(Attempt{TaskID: "A", Number: 1, Success: true, CostUSD: 0.10})
	s.SaveAttempt(Attempt{TaskID: "B", Number: 1, Success: false, CostUSD: 0.05})
	s.SaveAttempt(Attempt{TaskID: "B", Number: 2, Success: true, CostUSD: 0.08})

	total, succ, fail, cost := s.Stats()
	if total != 3 {
		t.Errorf("total=%d", total)
	}
	if succ != 2 {
		t.Errorf("successes=%d", succ)
	}
	if fail != 1 {
		t.Errorf("failures=%d", fail)
	}
	if cost < 0.22 || cost > 0.24 {
		t.Errorf("cost=%f", cost)
	}
}

func TestSQLStoreMissing(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	state, _ := s.LoadState()
	if state != nil {
		t.Error("empty db should return nil state")
	}

	attempts, _ := s.LoadAttempts("nonexistent")
	if len(attempts) != 0 {
		t.Error("empty db should return empty attempts")
	}
}
