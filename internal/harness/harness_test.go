package harness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/concern"
	"github.com/RelayOne/r1-agent/internal/harness"
	htools "github.com/RelayOne/r1-agent/internal/harness/tools"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

// setup creates a Harness backed by real ledger+bus in temp dirs and
// registers a minimal concern template so spawns succeed.
func setup(t *testing.T) (*harness.Harness, func()) {
	t.Helper()

	tmp := t.TempDir()
	ledgerDir := filepath.Join(tmp, "ledger")
	busDir := filepath.Join(tmp, "bus")

	if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(busDir, 0o755); err != nil {
		t.Fatal(err)
	}

	l, err := ledger.New(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bus.New(busDir)
	if err != nil {
		l.Close()
		t.Fatal(err)
	}

	cb := concern.NewBuilder(l, b)
	// Register a minimal template for "dev" + "proposing" so spawns work.
	cb.RegisterTemplate("dev_proposing", concern.Template{
		Role: concern.RoleDev,
		Face: concern.FaceProposing,
	})
	// Register reviewer template.
	cb.RegisterTemplate("reviewer_reviewing", concern.Template{
		Role: concern.RoleReviewer,
		Face: concern.FaceReviewing,
	})

	cfg := harness.Config{
		MissionID:    "test-mission",
		DefaultModel: "claude-opus-4-6",
		ModelOverrides: map[string]string{
			"reviewer": "claude-sonnet-4-20250514",
		},
		OperatingMode: "full_auto",
		BudgetUSD:     10.0,
	}

	h := harness.New(cfg, l, b, cb)

	cleanup := func() {
		b.Close()
		l.Close()
	}
	return h, cleanup
}

func TestSpawnStance_Valid(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
		SupervisorID: "sup-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	if handle.Role != "dev" {
		t.Errorf("role = %q, want %q", handle.Role, "dev")
	}
	if handle.State != harness.StatusRunning {
		t.Errorf("state = %q, want %q", handle.State, harness.StatusRunning)
	}
	if handle.ID == "" {
		t.Error("handle.ID is empty")
	}
}

func TestSpawnStance_InvalidRole(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	_, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role: "nonexistent_role",
		Face: "proposing",
	})
	if err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
}

func TestSpawnStance_EmptyRole(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	_, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Face: "proposing",
	})
	if err == nil {
		t.Fatal("expected error for empty role, got nil")
	}
}

func TestPauseStance(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	checkpointFn, err := h.StanceCheckpointer(handle.ID)
	if err != nil {
		t.Fatalf("StanceCheckpointer: %v", err)
	}

	// Simulate a stance runner that periodically hits checkpoints.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if err := checkpointFn(ctx); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	if err := h.PauseStance(context.Background(), handle.ID, "waiting for input"); err != nil {
		t.Fatalf("PauseStance: %v", err)
	}

	state, err := h.InspectStance(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusPaused {
		t.Errorf("state = %q, want %q", state.State, harness.StatusPaused)
	}
	if state.PauseReason != "waiting for input" {
		t.Errorf("pause reason = %q, want %q", state.PauseReason, "waiting for input")
	}
}

func TestResumeStance(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	checkpointFn, err := h.StanceCheckpointer(handle.ID)
	if err != nil {
		t.Fatalf("StanceCheckpointer: %v", err)
	}

	// Simulate a stance runner that periodically hits checkpoints.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if err := checkpointFn(ctx); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	if err := h.PauseStance(context.Background(), handle.ID, "paused"); err != nil {
		t.Fatalf("PauseStance: %v", err)
	}

	if err := h.ResumeStance(context.Background(), handle.ID, "extra context"); err != nil {
		t.Fatalf("ResumeStance: %v", err)
	}

	state, err := h.InspectStance(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusRunning {
		t.Errorf("state = %q, want %q", state.State, harness.StatusRunning)
	}
	if state.PauseReason != "" {
		t.Errorf("pause reason = %q, want empty", state.PauseReason)
	}
}

func TestTerminateStance(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	if err := h.TerminateStance(context.Background(), handle.ID); err != nil {
		t.Fatalf("TerminateStance: %v", err)
	}

	state, err := h.InspectStance(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusTerminated {
		t.Errorf("state = %q, want %q", state.State, harness.StatusTerminated)
	}
}

func TestListStances(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	// Spawn two stances.
	_, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance 1: %v", err)
	}
	_, err = h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "reviewer",
		Face:         "reviewing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance 2: %v", err)
	}

	list := h.ListStances(context.Background())
	if len(list) != 2 {
		t.Errorf("ListStances returned %d, want 2", len(list))
	}
}

func TestInspectStance(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	state, err := h.InspectStance(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}

	if state.ID != handle.ID {
		t.Errorf("ID = %q, want %q", state.ID, handle.ID)
	}
	if state.Role != "dev" {
		t.Errorf("role = %q, want %q", state.Role, "dev")
	}
	if state.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", state.Model, "claude-opus-4-6")
	}
	if state.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}
}

func TestInspectStance_NotFound(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	_, err := h.InspectStance(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent stance, got nil")
	}
}

func TestModelOverride(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "reviewer",
		Face:         "reviewing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	state, err := h.InspectStance(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q (from config override)", state.Model, "claude-sonnet-4-20250514")
	}
}

func TestToolAuthorization(t *testing.T) {
	tests := []struct {
		role       string
		tool       htools.ToolName
		authorized bool
	}{
		{"dev", htools.ToolFileRead, true},
		{"dev", htools.ToolFileWrite, true},
		{"dev", htools.ToolCodeRun, true},
		{"dev", htools.ToolWebSearch, false},
		{"reviewer", htools.ToolFileRead, true},
		{"reviewer", htools.ToolFileWrite, false},
		{"judge", htools.ToolFileRead, false},
		{"judge", htools.ToolLedgerQuery, true},
		{"stakeholder", htools.ToolFileWrite, false},
		{"stakeholder", htools.ToolFileRead, true},
		{"sdm", htools.ToolLedgerQuery, true},
		{"sdm", htools.ToolResearchRequest, false},
		{"researcher", htools.ToolSkillImportPropose, true},
		{"researcher", htools.ToolFileWrite, false},
	}

	for _, tt := range tests {
		got := htools.IsAuthorized(tt.role, tt.tool)
		if got != tt.authorized {
			t.Errorf("IsAuthorized(%q, %q) = %v, want %v", tt.role, tt.tool, got, tt.authorized)
		}
	}
}

func TestDefaultToolsForRole_Unknown(t *testing.T) {
	tools := htools.DefaultToolsForRole("nonexistent")
	if tools != nil {
		t.Errorf("expected nil for unknown role, got %v", tools)
	}
}

func TestPauseStanceWaitsForAcknowledgment(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	checkpointFn, err := h.StanceCheckpointer(handle.ID)
	if err != nil {
		t.Fatalf("StanceCheckpointer: %v", err)
	}

	// Track whether PauseStance has returned.
	var pauseReturned sync.WaitGroup
	pauseReturned.Add(1)
	pauseDone := make(chan struct{})

	// Start a goroutine that will call PauseStance. It should block until the
	// stance's checkpoint acknowledges.
	go func() {
		defer pauseReturned.Done()
		if err := h.PauseStance(context.Background(), handle.ID, "review needed"); err != nil {
			t.Errorf("PauseStance: %v", err)
		}
		close(pauseDone)
	}()

	// Give PauseStance time to signal and start waiting. It should NOT return
	// yet because no checkpoint has fired.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-pauseDone:
		t.Fatal("PauseStance returned before stance acknowledged the pause")
	default:
		// Expected: PauseStance is still blocking.
	}

	// Now simulate the stance hitting a checkpoint, which will acknowledge
	// the pause. CheckpointCheck will block on resumeCh after acknowledging,
	// so run it in a goroutine. We use a short-lived context since we never
	// resume in this test.
	checkpointCtx, checkpointCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkpointCancel()
	go func() {
		// Will return context.DeadlineExceeded since we never resume -- that's expected.
		_ = checkpointFn(checkpointCtx)
	}()

	// PauseStance should now return.
	select {
	case <-pauseDone:
		// Success.
	case <-time.After(5 * time.Second):
		t.Fatal("PauseStance did not return after stance acknowledged")
	}

	pauseReturned.Wait()

	state, err := h.InspectStance(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusPaused {
		t.Errorf("state = %q, want %q", state.State, harness.StatusPaused)
	}
}

func TestPausedStanceCanBeResumed(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	checkpointFn, err := h.StanceCheckpointer(handle.ID)
	if err != nil {
		t.Fatalf("StanceCheckpointer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Simulate a stance runner that periodically hits checkpoints.
	// It exits when the context is cancelled.
	stanceExited := make(chan error, 1)
	go func() {
		for {
			if err := checkpointFn(ctx); err != nil {
				stanceExited <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Pause the stance. The goroutine will hit a checkpoint and acknowledge.
	if err := h.PauseStance(ctx, handle.ID, "review"); err != nil {
		t.Fatalf("PauseStance: %v", err)
	}

	// Verify paused state.
	state, err := h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusPaused {
		t.Errorf("state = %q, want %q", state.State, harness.StatusPaused)
	}

	// Resume the stance.
	if err := h.ResumeStance(ctx, handle.ID, "continue working"); err != nil {
		t.Fatalf("ResumeStance: %v", err)
	}

	// Verify running state.
	state, err = h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusRunning {
		t.Errorf("state = %q, want %q", state.State, harness.StatusRunning)
	}

	// Cancel to stop the runner goroutine and verify it exits cleanly.
	cancel()
	select {
	case err := <-stanceExited:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("stance runner error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stance runner did not exit after context cancellation")
	}
}

func TestStanceCanBePausedMultipleTimes(t *testing.T) {
	h, cleanup := setup(t)
	defer cleanup()

	handle, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	checkpointFn, err := h.StanceCheckpointer(handle.ID)
	if err != nil {
		t.Fatalf("StanceCheckpointer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Runner goroutine that continuously hits checkpoints.
	stanceExited := make(chan error, 1)
	go func() {
		for {
			if err := checkpointFn(ctx); err != nil {
				stanceExited <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// First pause/resume cycle.
	if err := h.PauseStance(ctx, handle.ID, "review-1"); err != nil {
		t.Fatalf("PauseStance 1: %v", err)
	}
	state, err := h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusPaused {
		t.Errorf("after pause 1: state = %q, want %q", state.State, harness.StatusPaused)
	}

	if err := h.ResumeStance(ctx, handle.ID, "continue-1"); err != nil {
		t.Fatalf("ResumeStance 1: %v", err)
	}

	// Give the runner time to resume and hit a few checkpoints.
	time.Sleep(50 * time.Millisecond)

	// Second pause/resume cycle.
	if err := h.PauseStance(ctx, handle.ID, "review-2"); err != nil {
		t.Fatalf("PauseStance 2: %v", err)
	}
	state, err = h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusPaused {
		t.Errorf("after pause 2: state = %q, want %q", state.State, harness.StatusPaused)
	}

	if err := h.ResumeStance(ctx, handle.ID, "continue-2"); err != nil {
		t.Fatalf("ResumeStance 2: %v", err)
	}

	state, err = h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusRunning {
		t.Errorf("after resume 2: state = %q, want %q", state.State, harness.StatusRunning)
	}

	cancel()
	<-stanceExited
}
