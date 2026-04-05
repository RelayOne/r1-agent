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

func TestConsensusModelFnRunSpec(t *testing.T) {
	runner := &mockRunner{result: "VERDICT: incomplete\nGAP: Missing error handling in auth flow\nGAP: No rate limiting tests"}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")
	defer de.Cleanup()

	fn := de.ConsensusModelFn()

	verdict, reasoning, gaps, err := fn(context.Background(), "m-1", "claude", "consensus prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spec := runner.lastSpec
	if spec.Phase.Name != "consensus-claude" {
		t.Errorf("expected phase name 'consensus-claude', got %q", spec.Phase.Name)
	}
	if !spec.Phase.ReadOnly {
		t.Error("consensus should be read-only")
	}

	if verdict != "incomplete" {
		t.Errorf("expected verdict 'incomplete', got %q", verdict)
	}
	if len(gaps) != 2 {
		t.Errorf("expected 2 gaps, got %d: %v", len(gaps), gaps)
	}
	if reasoning == "" {
		t.Error("reasoning should not be empty")
	}
}

func TestConsensusModelFnAutoIncomplete(t *testing.T) {
	// When model finds gaps but doesn't explicitly say VERDICT: incomplete
	runner := &mockRunner{result: "Looks mostly good.\nGAP: No pagination on list endpoint"}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")
	defer de.Cleanup()

	fn := de.ConsensusModelFn()
	verdict, _, gaps, err := fn(context.Background(), "m-1", "codex", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "incomplete" {
		t.Errorf("should auto-set incomplete when gaps found, got %q", verdict)
	}
	if len(gaps) != 1 {
		t.Errorf("expected 1 gap, got %d", len(gaps))
	}
}

func TestExecuteFnRunSpec(t *testing.T) {
	runner := &mockRunner{result: "FILE: auth.go\nFILE: auth_test.go\nDone."}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")
	defer de.Cleanup()

	fn := de.ExecuteFn()
	m := &mission.Mission{ID: "m-1", Title: "Test", Intent: "test"}

	files, err := fn(context.Background(), m, "implement auth", "add JWT auth")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spec := runner.lastSpec
	if spec.Phase.Name != "execute" {
		t.Errorf("expected phase name 'execute', got %q", spec.Phase.Name)
	}
	if spec.Phase.ReadOnly {
		t.Error("execute should NOT be read-only")
	}
	if !spec.Phase.MCPEnabled {
		t.Error("MCP should be enabled for execution")
	}
	if spec.Phase.MaxTurns != 50 {
		t.Errorf("expected 50 max turns, got %d", spec.Phase.MaxTurns)
	}

	// Should have write tools
	toolSet := map[string]bool{}
	for _, tool := range spec.Phase.BuiltinTools {
		toolSet[tool] = true
	}
	for _, required := range []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash"} {
		if !toolSet[required] {
			t.Errorf("missing execution tool %q", required)
		}
	}

	// Should parse FILE: lines from output
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestValidateFnRunSpec(t *testing.T) {
	runner := &mockRunner{result: "All criteria met. No gaps found."}
	de := NewDiscoveryEngine(runner, "/tmp/fakerepo")
	defer de.Cleanup()

	fn := de.ValidateFn()
	m := &mission.Mission{ID: "m-1"}

	result, err := fn(context.Background(), m, "validate prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spec := runner.lastSpec
	if spec.Phase.Name != "validate" {
		t.Errorf("expected phase name 'validate', got %q", spec.Phase.Name)
	}
	if !spec.Phase.ReadOnly {
		t.Error("validate should be read-only")
	}
	if spec.Phase.MaxTurns != 20 {
		t.Errorf("expected 20 max turns, got %d", spec.Phase.MaxTurns)
	}
	if !strings.Contains(result, "No gaps found") {
		t.Error("result should pass through from runner")
	}
}
