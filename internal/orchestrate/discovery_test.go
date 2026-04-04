package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/mission"
)

// mockRunner captures the RunSpec passed to Run for inspection.
type mockRunner struct {
	lastSpec engine.RunSpec
	result   string
}

func (m *mockRunner) Run(_ context.Context, spec engine.RunSpec, _ engine.OnEventFunc) (engine.RunResult, error) {
	m.lastSpec = spec
	return engine.RunResult{ResultText: m.result}, nil
}

func (m *mockRunner) Prepare(spec engine.RunSpec) (engine.PreparedCommand, error) {
	return engine.PreparedCommand{}, nil
}

func TestDiscoveryFnRunSpec(t *testing.T) {
	runner := &mockRunner{result: "FILE: cmd/main.go\nGAP: missing tests"}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")

	fn := de.DiscoveryFn()
	m := &mission.Mission{ID: "m-1", Title: "Test", Intent: "test intent"}

	result, err := fn(context.Background(), m, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the RunSpec
	spec := runner.lastSpec
	if spec.Prompt != "test prompt" {
		t.Errorf("expected prompt 'test prompt', got %q", spec.Prompt)
	}
	if spec.Phase.Name != "discovery" {
		t.Errorf("expected phase name 'discovery', got %q", spec.Phase.Name)
	}
	if !spec.Phase.MCPEnabled {
		t.Error("MCP should be enabled for discovery")
	}
	if spec.Phase.MaxTurns != 30 {
		t.Errorf("expected 30 max turns, got %d", spec.Phase.MaxTurns)
	}
	if !spec.Phase.ReadOnly {
		t.Error("discovery should be read-only")
	}
	if spec.MCPConfigPath == "" {
		t.Error("MCPConfigPath should be set")
	}
	if !strings.Contains(result, "FILE: cmd/main.go") {
		t.Error("result should pass through from runner")
	}

	de.Cleanup()
}

func TestValidateDiscoveryFnRunSpec(t *testing.T) {
	runner := &mockRunner{result: "GAP: handler not wired"}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")

	fn := de.ValidateDiscoveryFn()
	m := &mission.Mission{ID: "m-1", Title: "Test", Intent: "test intent"}

	result, err := fn(context.Background(), m, "validate prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spec := runner.lastSpec
	if spec.Phase.Name != "validate-discovery" {
		t.Errorf("expected phase name 'validate-discovery', got %q", spec.Phase.Name)
	}
	if spec.Phase.MaxTurns != 40 {
		t.Errorf("expected 40 max turns, got %d", spec.Phase.MaxTurns)
	}
	if !spec.Phase.MCPEnabled {
		t.Error("MCP should be enabled for validate-discovery")
	}
	if !spec.Phase.ReadOnly {
		t.Error("validate-discovery should be read-only")
	}
	if !strings.Contains(result, "GAP: handler not wired") {
		t.Error("result should pass through from runner")
	}

	de.Cleanup()
}

func TestDiscoveryEngineMCPConfigReused(t *testing.T) {
	runner := &mockRunner{result: "ok"}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")
	defer de.Cleanup()

	fn := de.DiscoveryFn()
	m := &mission.Mission{ID: "m-1"}

	// First call creates config
	fn(context.Background(), m, "prompt1")
	firstConfig := runner.lastSpec.MCPConfigPath

	// Second call should reuse
	fn(context.Background(), m, "prompt2")
	secondConfig := runner.lastSpec.MCPConfigPath

	if firstConfig != secondConfig {
		t.Errorf("MCP config should be reused, got %q then %q", firstConfig, secondConfig)
	}
}

func TestDiscoveryEngineBuiltinTools(t *testing.T) {
	runner := &mockRunner{result: "ok"}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")
	defer de.Cleanup()

	fn := de.DiscoveryFn()
	m := &mission.Mission{ID: "m-1"}
	fn(context.Background(), m, "prompt")

	tools := runner.lastSpec.Phase.BuiltinTools
	expected := map[string]bool{"Read": false, "Glob": false, "Grep": false}
	for _, tool := range tools {
		expected[tool] = true
	}
	for tool, found := range expected {
		if !found {
			t.Errorf("missing builtin tool %q", tool)
		}
	}
}
