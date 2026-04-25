package session

import (
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/plan"
)

func TestForkSession(t *testing.T) {
	store := New(t.TempDir())

	// Save initial state
	state := &State{
		PlanID:       "plan-001",
		Tasks:        []plan.Task{{ID: "TASK-1", Description: "test"}},
		TotalCostUSD: 1.50,
		StartedAt:    time.Now(),
	}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}

	// Fork
	fork, err := store.ForkSession("plan-001", "experiment-branch", "trying new approach")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if fork.ParentSessionID != "plan-001" {
		t.Error("wrong parent ID")
	}
	if fork.State != "active" {
		t.Error("expected active state")
	}
	if fork.BranchName != "experiment-branch" {
		t.Error("wrong branch name")
	}

	// Load fork state
	forkState, err := store.LoadForkState(fork.ID)
	if err != nil {
		t.Fatalf("load fork state: %v", err)
	}
	if forkState.TotalCostUSD != 1.50 {
		t.Error("fork should copy parent cost")
	}
}

func TestListForks(t *testing.T) {
	store := New(t.TempDir())

	store.ForkSession("plan-001", "branch-a", "first")
	store.ForkSession("plan-001", "branch-b", "second")

	forks, err := store.ListForks()
	if err != nil {
		t.Fatal(err)
	}
	if len(forks) != 2 {
		t.Errorf("expected 2 forks, got %d", len(forks))
	}
}

func TestMergeFork(t *testing.T) {
	store := New(t.TempDir())

	fork, _ := store.ForkSession("plan-001", "branch", "test")
	if err := store.MergeFork(fork.ID); err != nil {
		t.Fatal(err)
	}

	forks, _ := store.ListForks()
	for _, f := range forks {
		if f.ID == fork.ID && f.State != "merged" {
			t.Error("expected merged state")
		}
	}
}

func TestAbandonFork(t *testing.T) {
	store := New(t.TempDir())

	fork, _ := store.ForkSession("plan-001", "branch", "test")
	if err := store.AbandonFork(fork.ID); err != nil {
		t.Fatal(err)
	}

	forks, _ := store.ListForks()
	for _, f := range forks {
		if f.ID == fork.ID && f.State != "abandoned" {
			t.Error("expected abandoned state")
		}
	}
}

func TestListForksEmpty(t *testing.T) {
	store := New(t.TempDir())
	forks, err := store.ListForks()
	if err != nil {
		t.Fatal(err)
	}
	if forks != nil {
		t.Error("expected nil forks for new store")
	}
}
