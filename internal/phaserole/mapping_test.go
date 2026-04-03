package phaserole

import (
	"testing"
)

func TestDefaultMapping(t *testing.T) {
	m := DefaultMapping()

	phases := m.Phases()
	if len(phases) < 7 {
		t.Errorf("expected at least 7 phases, got %d", len(phases))
	}

	// Plan should use architect
	plan := m.Resolve(PhasePlan)
	if plan.ModelClass != "architect" {
		t.Errorf("expected architect for plan, got %s", plan.ModelClass)
	}

	// Test should use fast
	test := m.Resolve(PhaseTest)
	if test.ModelClass != "fast" {
		t.Errorf("expected fast for test, got %s", test.ModelClass)
	}

	// Review should use different provider than implement
	if !m.IsCrossModel() {
		t.Error("default mapping should use cross-model review")
	}
}

func TestResolveUnknownPhase(t *testing.T) {
	m := DefaultMapping()
	role := m.Resolve("unknown")
	if role.ModelClass != "editor" {
		t.Errorf("expected fallback to editor, got %s", role.ModelClass)
	}
	if role.Phase != "unknown" {
		t.Errorf("expected phase set to unknown, got %s", role.Phase)
	}
}

func TestCustomMapping(t *testing.T) {
	m := NewMapping()
	m.Set(PhasePlan, Role{
		ModelClass: "custom-arch",
		Provider:   "openrouter",
		MaxTokens:  10000,
	})
	m.SetFallback(Role{
		ModelClass: "default",
		Provider:   "claude",
	})

	plan := m.Resolve(PhasePlan)
	if plan.ModelClass != "custom-arch" {
		t.Errorf("expected custom-arch, got %s", plan.ModelClass)
	}

	impl := m.Resolve(PhaseImplement)
	if impl.ModelClass != "default" {
		t.Errorf("expected fallback default, got %s", impl.ModelClass)
	}
}

func TestCanWrite(t *testing.T) {
	m := DefaultMapping()

	if !m.CanWrite(PhaseImplement) {
		t.Error("implement should allow writes")
	}
	if m.CanWrite(PhaseTest) {
		t.Error("test should not allow writes")
	}
	if m.CanWrite(PhasePlan) {
		t.Error("plan should not allow writes")
	}
}

func TestToolsAllowed(t *testing.T) {
	m := DefaultMapping()

	planTools := m.ToolsAllowed(PhasePlan)
	hasWrite := false
	for _, tool := range planTools {
		if tool == "write" {
			hasWrite = true
		}
	}
	if hasWrite {
		t.Error("plan phase should not have write tool")
	}

	implTools := m.ToolsAllowed(PhaseImplement)
	hasBash := false
	for _, tool := range implTools {
		if tool == "bash" {
			hasBash = true
		}
	}
	if !hasBash {
		t.Error("implement phase should have bash tool")
	}
}

func TestHasPhase(t *testing.T) {
	m := DefaultMapping()
	if !m.HasPhase(PhasePlan) {
		t.Error("should have plan phase")
	}
	if m.HasPhase("nonexistent") {
		t.Error("should not have nonexistent phase")
	}
}

func TestModelClassForPhase(t *testing.T) {
	m := DefaultMapping()
	if m.ModelClassForPhase(PhaseReview) != "reviewer" {
		t.Errorf("expected reviewer, got %s", m.ModelClassForPhase(PhaseReview))
	}
}

func TestProviderForPhase(t *testing.T) {
	m := DefaultMapping()
	if m.ProviderForPhase(PhaseReview) != "codex" {
		t.Errorf("expected codex for review, got %s", m.ProviderForPhase(PhaseReview))
	}
}
