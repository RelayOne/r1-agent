package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/harness"
	htools "github.com/ericmacdougall/stoke/internal/harness/tools"
	"github.com/ericmacdougall/stoke/internal/ledger"
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
