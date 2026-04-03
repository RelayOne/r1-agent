package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cp := Checkpoint{
		ID:       "cp-1",
		TaskID:   "task-1",
		Phase:    PhaseRunning,
		Step:     1,
		CostUSD:  0.05,
		Attempt:  1,
	}

	if err := store.Save(cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected checkpoint")
	}
	if loaded.TaskID != "task-1" {
		t.Errorf("expected task-1, got %s", loaded.TaskID)
	}
	if loaded.Phase != PhaseRunning {
		t.Errorf("expected running, got %s", loaded.Phase)
	}
}

func TestRollingBuffer(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save 3 checkpoints
	for i := 1; i <= 3; i++ {
		store.Save(Checkpoint{ID: "cp", TaskID: "task", Phase: PhaseRunning, Step: i})
	}

	// Current should be step 3
	loaded, _ := store.Load()
	if loaded.Step != 3 {
		t.Errorf("expected step 3, got %d", loaded.Step)
	}

	// Previous should exist
	if _, err := os.Stat(filepath.Join(dir, "checkpoint-previous.json")); err != nil {
		t.Error("expected previous checkpoint file")
	}

	// Recovery should exist
	if _, err := os.Stat(filepath.Join(dir, "checkpoint-recovery.json")); err != nil {
		t.Error("expected recovery checkpoint file")
	}
}

func TestLoadFallback(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Save(Checkpoint{TaskID: "task", Phase: PhaseRunning, Step: 1})

	// Delete current, should fall back to previous
	os.Remove(filepath.Join(dir, "checkpoint-current.json"))

	// No previous exists yet (only one save), so load returns nil
	loaded, _ := store.Load()
	if loaded != nil {
		t.Error("expected nil after deleting only checkpoint")
	}

	// Save twice so previous exists
	store.Save(Checkpoint{TaskID: "task", Phase: PhaseRunning, Step: 1})
	store.Save(Checkpoint{TaskID: "task", Phase: PhaseRunning, Step: 2})

	// Delete current
	os.Remove(filepath.Join(dir, "checkpoint-current.json"))

	loaded, _ = store.Load()
	if loaded == nil {
		t.Fatal("expected fallback to previous")
	}
	if loaded.Step != 1 {
		t.Errorf("expected step 1 from previous, got %d", loaded.Step)
	}
}

func TestClear(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Save(Checkpoint{TaskID: "task", Phase: PhaseCompleted, Step: 1})
	store.Clear()

	loaded, _ := store.Load()
	if loaded != nil {
		t.Error("expected nil after clear")
	}
}

func TestValidate(t *testing.T) {
	issues := Validate(&Checkpoint{TaskID: "task", Phase: PhaseRunning, WorktreePath: "/nonexistent/path"})
	if len(issues) != 1 {
		t.Errorf("expected 1 issue (missing worktree), got %d: %v", len(issues), issues)
	}

	issues = Validate(&Checkpoint{TaskID: "", Phase: ""})
	if len(issues) != 2 {
		t.Errorf("expected 2 issues, got %d: %v", len(issues), issues)
	}

	issues = Validate(&Checkpoint{TaskID: "task", Phase: PhaseRunning})
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %v", issues)
	}
}

func TestShouldResume(t *testing.T) {
	if ShouldResume(nil) {
		t.Error("nil should not resume")
	}
	if !ShouldResume(&Checkpoint{Phase: PhaseRunning}) {
		t.Error("running should resume")
	}
	if !ShouldResume(&Checkpoint{Phase: PhaseCheckpointed}) {
		t.Error("checkpointed should resume")
	}
	if ShouldResume(&Checkpoint{Phase: PhaseCompleted}) {
		t.Error("completed should not resume")
	}
	if ShouldResume(&Checkpoint{Phase: PhaseFailed}) {
		t.Error("failed should not resume")
	}
}

func TestIdempotencyKey(t *testing.T) {
	key := IdempotencyKey("task-1", 3, 2)
	if key != "task-1:step3:attempt2" {
		t.Errorf("unexpected key: %s", key)
	}
}

func TestLoadForTask(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Save(Checkpoint{TaskID: "task-1", Phase: PhaseRunning})

	cp, _ := store.LoadForTask("task-1")
	if cp == nil {
		t.Error("expected checkpoint for task-1")
	}

	cp, _ = store.LoadForTask("task-2")
	if cp != nil {
		t.Error("expected nil for task-2")
	}
}
