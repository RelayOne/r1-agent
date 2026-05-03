package planupdate

import (
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
)

// TestPlanUpdateLobe_Constructs covers TASK-16: the scaffold must
// compile, expose ID/Description/Kind, and capture every constructor
// argument so downstream tasks have a stable handle.
func TestPlanUpdateLobe_Constructs(t *testing.T) {
	t.Parallel()

	planPath := filepath.Join(t.TempDir(), "stoke-plan.json")
	runtime := conversation.NewRuntime("you are a planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewPlanUpdateLobe(planPath, runtime, nil, escalate, ws, nil)
	if l == nil {
		t.Fatal("NewPlanUpdateLobe returned nil")
	}

	if got, want := l.ID(), "plan-update"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if l.Description() == "" {
		t.Error("Description() is empty")
	}
	if got, want := l.Kind(), cortex.KindLLM; got != want {
		t.Errorf("Kind() = %v, want KindLLM", got)
	}
	if got, want := l.PlanPath(), planPath; got != want {
		t.Errorf("PlanPath() = %q, want %q", got, want)
	}
	if got := l.TurnCount(); got != 0 {
		t.Errorf("TurnCount() at construction = %d, want 0", got)
	}
}
