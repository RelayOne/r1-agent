package session

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/plan"
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

// TestSaveLearningDuplicateIssueDoesNotWipe proves SaveLearning is atomic:
// input containing a duplicate `issue` used to DELETE all patterns and then
// abort mid-loop on the UNIQUE(issue) constraint, leaving the table empty and
// erasing accumulated learning. It must now either preserve the old data
// (rollback) or commit the deduplicated new set — never lose everything.
func TestSaveLearningDuplicateIssueDoesNotWipe(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Seed a durable pattern.
	if err := s.SaveLearning(&Learning{Patterns: []Pattern{
		{Issue: "seed-issue", Fix: "seed-fix", Occurrences: 3},
	}}); err != nil {
		t.Fatalf("seed SaveLearning: %v", err)
	}

	// Save a set that contains a duplicate issue — the historical footgun.
	err = s.SaveLearning(&Learning{Patterns: []Pattern{
		{Issue: "dup", Fix: "fix-a", Occurrences: 1},
		{Issue: "dup", Fix: "fix-b", Occurrences: 2},
		{Issue: "other", Fix: "fix-c", Occurrences: 1},
	}})
	if err != nil {
		t.Fatalf("SaveLearning with duplicate issue should not error: %v", err)
	}

	// The table must NOT be empty (no silent total wipe).
	got, err := s.LoadLearning()
	if err != nil {
		t.Fatalf("LoadLearning: %v", err)
	}
	if len(got.Patterns) == 0 {
		t.Fatal("patterns table was wiped by a mid-loop failure")
	}
	// The deduplicated commit should contain both distinct issues.
	issues := map[string]bool{}
	for _, p := range got.Patterns {
		issues[p.Issue] = true
	}
	if !issues["dup"] || !issues["other"] {
		t.Fatalf("expected issues {dup, other}, got %v", issues)
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

// TestAddPatternSurfacesError proves the learned-pattern write no longer
// swallows its INSERT error. Previously addPattern discarded the Exec result,
// so a disk/constraint/closed-DB failure silently lost the learning.
func TestAddPatternSurfacesError(t *testing.T) {
	s, err := NewSQLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Happy path: a normal add persists and is recallable.
	if err := s.addPattern("build fails on missing import", "run goimports"); err != nil {
		t.Fatalf("addPattern (open db): %v", err)
	}
	l, err := s.LoadLearning()
	if err != nil {
		t.Fatalf("LoadLearning: %v", err)
	}
	found := false
	for _, p := range l.Patterns {
		if p.Issue == "build fails on missing import" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected learned pattern to persist")
	}

	// Failure path: after Close, the write must error rather than be dropped.
	s.Close()
	if err := s.addPattern("this cannot be saved", "never"); err == nil {
		t.Error("addPattern on a closed DB should return an error, not silently lose the learning")
	}
}

// TestSaveAttemptAutoLearnPersists proves the SaveAttempt auto-learn path
// (success following a prior failure) records the resolved pattern.
func TestSaveAttemptAutoLearnPersists(t *testing.T) {
	s, err := NewSQLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SaveAttempt(Attempt{TaskID: "T9", Number: 1, Success: false, FailSummary: "nil pointer in handler"}); err != nil {
		t.Fatalf("SaveAttempt #1: %v", err)
	}
	if err := s.SaveAttempt(Attempt{TaskID: "T9", Number: 2, Success: true}); err != nil {
		t.Fatalf("SaveAttempt #2: %v", err)
	}

	l, err := s.LoadLearning()
	if err != nil {
		t.Fatalf("LoadLearning: %v", err)
	}
	found := false
	for _, p := range l.Patterns {
		if p.Issue == "nil pointer in handler" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected auto-learned pattern from resolved failure, got %+v", l.Patterns)
	}
}
