package flowtrack

import (
	"testing"
	"time"
)

func TestRecordAction(t *testing.T) {
	tr := NewTracker(Config{})
	tr.Record(Action{Type: ActionFileOpen, Target: "main.go"})

	state := tr.State()
	if state.ActionCount != 1 {
		t.Errorf("expected 1 action, got %d", state.ActionCount)
	}
}

func TestInferExploring(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	for i := 0; i < 5; i++ {
		tr.Record(Action{Type: ActionFileOpen, Target: "file.go", Timestamp: now.Add(time.Duration(i) * time.Second)})
	}
	tr.Record(Action{Type: ActionSearch, Target: "func", Timestamp: now.Add(6 * time.Second)})
	tr.Record(Action{Type: ActionNavigate, Target: "main.go:42", Timestamp: now.Add(7 * time.Second)})

	state := tr.State()
	if state.Phase != PhaseExploring {
		t.Errorf("expected exploring, got %s", state.Phase)
	}
}

func TestInferImplementing(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	tr.Record(Action{Type: ActionFileCreate, Target: "new.go", Timestamp: now})
	for i := 1; i <= 5; i++ {
		tr.Record(Action{Type: ActionFileEdit, Target: "new.go", Timestamp: now.Add(time.Duration(i) * time.Second)})
	}

	state := tr.State()
	if state.Phase != PhaseImplementing {
		t.Errorf("expected implementing, got %s", state.Phase)
	}
}

func TestInferTesting(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	for i := 0; i < 5; i++ {
		tr.Record(Action{Type: ActionRunTest, Target: "go test ./...", Timestamp: now.Add(time.Duration(i) * time.Second)})
	}

	state := tr.State()
	if state.Phase != PhaseTesting {
		t.Errorf("expected testing, got %s", state.Phase)
	}
}

func TestActiveFiles(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	tr.Record(Action{Type: ActionFileEdit, Target: "a.go", Timestamp: now})
	tr.Record(Action{Type: ActionFileEdit, Target: "a.go", Timestamp: now.Add(1 * time.Second)})
	tr.Record(Action{Type: ActionFileEdit, Target: "b.go", Timestamp: now.Add(2 * time.Second)})

	state := tr.State()
	if state.FocusFile != "a.go" {
		t.Errorf("expected focus on a.go, got %s", state.FocusFile)
	}
	if len(state.ActiveFiles) < 2 {
		t.Error("should have at least 2 active files")
	}
}

func TestDetectTDDPattern(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	for i := 0; i < 4; i++ {
		tr.Record(Action{Type: ActionFileEdit, Timestamp: now.Add(time.Duration(i*2) * time.Second)})
		tr.Record(Action{Type: ActionRunTest, Timestamp: now.Add(time.Duration(i*2+1) * time.Second)})
	}

	state := tr.State()
	found := false
	for _, p := range state.Patterns {
		if p == "TDD cycle detected" {
			found = true
		}
	}
	if !found {
		t.Error("should detect TDD pattern")
	}
}

func TestDetectDebugLoop(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	tr.Record(Action{Type: ActionError, Timestamp: now})
	tr.Record(Action{Type: ActionSearch, Timestamp: now.Add(1 * time.Second)})
	tr.Record(Action{Type: ActionFileEdit, Timestamp: now.Add(2 * time.Second)})

	state := tr.State()
	found := false
	for _, p := range state.Patterns {
		if p == "debug-fix loop" {
			found = true
		}
	}
	if !found {
		t.Error("should detect debug-fix loop")
	}
}

func TestExplorationSequence(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	for i := 0; i < 4; i++ {
		tr.Record(Action{Type: ActionFileOpen, Timestamp: now.Add(time.Duration(i) * time.Second)})
	}

	state := tr.State()
	found := false
	for _, p := range state.Patterns {
		if p == "code exploration sequence" {
			found = true
		}
	}
	if !found {
		t.Error("should detect exploration sequence")
	}
}

func TestForPrompt(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	tr.Record(Action{Type: ActionFileEdit, Target: "main.go", Timestamp: now})
	tr.Record(Action{Type: ActionRunTest, Target: "go test ./...", Timestamp: now.Add(1 * time.Second)})

	prompt := tr.ForPrompt()
	if prompt == "" {
		t.Error("should generate prompt content")
	}
}

func TestForPromptEmpty(t *testing.T) {
	tr := NewTracker(Config{})
	prompt := tr.ForPrompt()
	if prompt != "" {
		t.Error("empty tracker should produce empty prompt")
	}
}

func TestWindowTrimming(t *testing.T) {
	tr := NewTracker(Config{WindowSize: 5})
	now := time.Now()
	for i := 0; i < 20; i++ {
		tr.Record(Action{Type: ActionFileEdit, Timestamp: now.Add(time.Duration(i) * time.Second)})
	}

	state := tr.State()
	if state.ActionCount > 5 {
		t.Errorf("should trim to window size, got %d", state.ActionCount)
	}
}

func TestRecentActions(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	for i := 0; i < 10; i++ {
		tr.Record(Action{Type: ActionFileEdit, Target: "file.go", Timestamp: now.Add(time.Duration(i) * time.Second)})
	}

	recent := tr.RecentActions(3)
	if len(recent) != 3 {
		t.Errorf("expected 3, got %d", len(recent))
	}
}

func TestActionsSince(t *testing.T) {
	tr := NewTracker(Config{})
	now := time.Now()
	tr.Record(Action{Type: ActionFileEdit, Timestamp: now.Add(-5 * time.Second)})
	tr.Record(Action{Type: ActionFileEdit, Timestamp: now.Add(-1 * time.Second)})
	tr.Record(Action{Type: ActionFileEdit, Timestamp: now})

	since := tr.ActionsSince(now.Add(-2 * time.Second))
	if len(since) != 2 {
		t.Errorf("expected 2 actions since cutoff, got %d", len(since))
	}
}

func TestClear(t *testing.T) {
	tr := NewTracker(Config{})
	tr.Record(Action{Type: ActionFileEdit})
	tr.Clear()

	state := tr.State()
	if state.ActionCount != 0 {
		t.Error("clear should remove all actions")
	}
}

func TestListener(t *testing.T) {
	tr := NewTracker(Config{})
	var received []FlowState
	tr.OnStateChange(func(s FlowState) {
		received = append(received, s)
	})

	tr.Record(Action{Type: ActionFileEdit})
	if len(received) != 1 {
		t.Errorf("expected 1 notification, got %d", len(received))
	}
}

func TestEmptyState(t *testing.T) {
	tr := NewTracker(Config{})
	state := tr.State()
	if state.Phase != PhaseUnknown {
		t.Errorf("empty tracker should be unknown phase, got %s", state.Phase)
	}
}
