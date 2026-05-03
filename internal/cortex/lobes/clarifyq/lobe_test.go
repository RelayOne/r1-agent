package clarifyq

import (
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
)

// TestClarifyingQLobe_Constructs covers TASK-21: the scaffold must
// compile, expose ID/Description/Kind, and capture every constructor
// argument so downstream tasks have a stable handle.
func TestClarifyingQLobe_Constructs(t *testing.T) {
	t.Parallel()

	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)
	bus := hub.New()

	l := NewClarifyingQLobe(nil, escalate, ws, bus)
	if l == nil {
		t.Fatal("NewClarifyingQLobe returned nil")
	}

	if got, want := l.ID(), "clarifying-q"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if l.Description() == "" {
		t.Error("Description() is empty")
	}
	if got, want := l.Kind(), cortex.KindLLM; got != want {
		t.Errorf("Kind() = %v, want KindLLM", got)
	}
	if got := l.OutstandingCount(); got != 0 {
		t.Errorf("OutstandingCount() at construction = %d, want 0", got)
	}
}
