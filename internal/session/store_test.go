package session

import (
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
)

func TestSaveLoadState(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	state := &State{
		PlanID:       "test-plan",
		Tasks:        []plan.Task{{ID: "T1", Description: "first"}},
		TotalCostUSD: 1.23,
		StartedAt:    time.Now(),
	}
	if err := s.SaveState(state); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("expected state")
	}
	if loaded.PlanID != "test-plan" {
		t.Errorf("plan_id=%q", loaded.PlanID)
	}
	if loaded.TotalCostUSD != 1.23 {
		t.Errorf("cost=%f", loaded.TotalCostUSD)
	}

	s.ClearState()
	cleared, _ := s.LoadState()
	if cleared != nil {
		t.Error("expected nil after clear")
	}
}

func TestLearning(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	l := &Learning{Patterns: []Pattern{{Issue: "ts-ignore", Fix: "use proper types", Occurrences: 3}}}
	if err := s.SaveLearning(l); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.LoadLearning()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Patterns) != 1 {
		t.Fatalf("patterns=%d", len(loaded.Patterns))
	}
	if loaded.Patterns[0].Issue != "ts-ignore" {
		t.Errorf("issue=%q", loaded.Patterns[0].Issue)
	}
}

func TestAttemptHistory(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Save a failed attempt with full context
	s.SaveAttempt(Attempt{
		TaskID:      "T1",
		Number:      1,
		Success:     false,
		Error:       "build failed",
		FailClass:   "BuildFailed",
		FailSummary: "2 TypeScript errors in src/auth.ts",
		RootCause:   "Property 'user' does not exist on type 'Request'",
		DiffSummary: "src/auth.ts | 45 +++",
	})

	// Save a successful retry
	s.SaveAttempt(Attempt{
		TaskID:  "T1",
		Number:  2,
		Success: true,
		CostUSD: 0.05,
	})

	// LoadAttempts should return both
	attempts, err := s.LoadAttempts("T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d, want 2", len(attempts))
	}
	if attempts[0].FailClass != "BuildFailed" {
		t.Errorf("attempt 1 class=%q", attempts[0].FailClass)
	}
	if attempts[0].DiffSummary != "src/auth.ts | 45 +++" {
		t.Errorf("attempt 1 diff=%q", attempts[0].DiffSummary)
	}
	if !attempts[1].Success {
		t.Error("attempt 2 should be successful")
	}

	// Successful retry after failure should create a learned pattern
	learning, err := s.LoadLearning()
	if err != nil {
		t.Fatal(err)
	}
	if len(learning.Patterns) == 0 {
		t.Error("expected learned pattern from successful retry")
	}
}

func TestLoadAttemptsMissing(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	attempts, err := s.LoadAttempts("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if attempts != nil {
		t.Errorf("expected nil for missing task, got %d attempts", len(attempts))
	}
}
