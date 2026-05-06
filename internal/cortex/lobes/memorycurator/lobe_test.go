package memorycurator

import (
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
)

// TestMemoryCuratorLobe_Constructs covers TASK-26: the scaffold must
// compile, expose ID/Description/Kind, and capture every constructor
// argument so downstream tasks have a stable handle.
func TestMemoryCuratorLobe_Constructs(t *testing.T) {
	t.Parallel()

	mem, err := memory.NewStore(memory.Config{Path: ""})
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)
	bus := hub.New()
	privacy := PrivacyConfig{
		AutoCurateCategories: []memory.Category{memory.CatFact},
		SkipPrivateMessages:  true,
		AuditLogPath:         "/tmp/curator-audit-test.jsonl",
	}

	l := NewMemoryCuratorLobe(nil, escalate, mem, privacy, ws, bus)
	if l == nil {
		t.Fatal("NewMemoryCuratorLobe returned nil")
	}

	if got, want := l.ID(), "memory-curator"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if l.Description() == "" {
		t.Error("Description() is empty")
	}
	if got, want := l.Kind(), cortex.KindLLM; got != want {
		t.Errorf("Kind() = %v, want KindLLM", got)
	}
	if got := l.TurnCount(); got != 0 {
		t.Errorf("TurnCount() at construction = %d, want 0", got)
	}
	if got := l.TriggerCount(); got != 0 {
		t.Errorf("TriggerCount() at construction = %d, want 0", got)
	}
}

// TestPrivacyConfig_Shape verifies the spec-mandated PrivacyConfig
// fields are reachable (compile-time) and carry the documented zero
// values. Catches signature drift early in CI.
func TestPrivacyConfig_Shape(t *testing.T) {
	t.Parallel()
	var p PrivacyConfig
	if p.AutoCurateCategories != nil {
		t.Errorf("zero AutoCurateCategories should be nil, got %v", p.AutoCurateCategories)
	}
	if p.SkipPrivateMessages != false {
		t.Errorf("zero SkipPrivateMessages should be false, got %v", p.SkipPrivateMessages)
	}
	if p.AuditLogPath != "" {
		t.Errorf("zero AuditLogPath should be empty, got %q", p.AuditLogPath)
	}
}
