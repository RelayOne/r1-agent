package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudePrepareStrictMCPAndSettings(t *testing.T) {
	dir := t.TempDir()
	runner := NewClaudeRunner("claude")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:        "test",
		WorktreeDir:   dir,
		RuntimeDir:    filepath.Join(dir, "runtime"),
		Mode:          AuthModeMode1,
		PoolConfigDir: filepath.Join(dir, "pool"),
		Phase: PhaseSpec{
			Name:         "plan",
			BuiltinTools: []string{"Read", "Glob", "Grep"},
			AllowedRules: []string{"Read"},
			DeniedRules:  []string{},
			MCPEnabled:   false,
			MaxTurns:     3,
		},
		SandboxEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Error("missing --strict-mcp-config")
	}
	if !strings.Contains(joined, "--settings") {
		t.Error("missing --settings")
	}
}

func TestClaudePrepareMCPDisabledBlocksMCPTools(t *testing.T) {
	dir := t.TempDir()
	runner := NewClaudeRunner("claude")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode2,
		Phase: PhaseSpec{
			Name:         "plan",
			BuiltinTools: []string{"Read"},
			AllowedRules: []string{"Read"},
			DeniedRules:  []string{},
			MCPEnabled:   false,
			MaxTurns:     5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")

	// Belt-and-suspenders: --disallowedTools should include mcp__*
	if !strings.Contains(joined, "mcp__*") {
		t.Error("MCP-disabled phase should add mcp__* to disallowedTools")
	}
	// And --strict-mcp-config with empty config
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Error("missing --strict-mcp-config for MCP-disabled phase")
	}
}

func TestClaudePrepareMCPEnabledNoBlock(t *testing.T) {
	dir := t.TempDir()
	runner := NewClaudeRunner("claude")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode2,
		Phase: PhaseSpec{
			Name:         "execute",
			BuiltinTools: []string{"Read", "Edit"},
			AllowedRules: []string{"Read"},
			DeniedRules:  []string{"Bash(rm *)"},
			MCPEnabled:   true,
			MaxTurns:     20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")

	if strings.Contains(joined, "--strict-mcp-config") {
		t.Error("MCP-enabled phase should not have --strict-mcp-config")
	}
	if strings.Contains(joined, "mcp__*") {
		t.Error("MCP-enabled phase should not block mcp__*")
	}
}

func TestClaudePrepareWritesSettingsJSON(t *testing.T) {
	dir := t.TempDir()
	runner := NewClaudeRunner("claude")
	_, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode1,
		Phase: PhaseSpec{
			Name:         "execute",
			BuiltinTools: []string{"Read"},
			AllowedRules: []string{"Read"},
			DeniedRules:  []string{},
			MCPEnabled:   false,
			MaxTurns:     10,
		},
		SandboxEnabled:    true,
		SandboxDomains:    []string{"github.com"},
		SandboxAllowRead:  []string{dir},
		SandboxAllowWrite: []string{dir},
	})
	if err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(dir, "runtime", "claude-settings-execute.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// apiKeyHelper should be null in Mode 1
	val, exists := parsed["apiKeyHelper"]
	if !exists {
		t.Error("apiKeyHelper key missing from settings")
	}
	if val != nil {
		t.Errorf("apiKeyHelper should be null in Mode 1, got %v", val)
	}

	// Sandbox should be present
	sandbox, ok := parsed["sandbox"].(map[string]interface{})
	if !ok {
		t.Fatal("sandbox section missing")
	}
	if sandbox["enabled"] != true {
		t.Error("sandbox.enabled should be true")
	}
	if sandbox["failIfUnavailable"] != true {
		t.Error("sandbox.failIfUnavailable should be true")
	}
}

func TestClaudePrepareMode1EnvIsolation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "leaked-key")
	t.Setenv("PATH", "/usr/bin")

	runner := NewClaudeRunner("claude")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:        "test",
		WorktreeDir:   dir,
		RuntimeDir:    filepath.Join(dir, "runtime"),
		Mode:          AuthModeMode1,
		PoolConfigDir: "/pool/claude-1",
		Phase: PhaseSpec{
			Name:         "plan",
			BuiltinTools: []string{"Read"},
			MCPEnabled:   false,
			MaxTurns:     3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	envJoined := strings.Join(prepared.Env, "\n")
	if strings.Contains(envJoined, "ANTHROPIC_API_KEY=") {
		t.Error("Mode 1 should strip ANTHROPIC_API_KEY from env")
	}
	if !strings.Contains(envJoined, "CLAUDE_CONFIG_DIR=/pool/claude-1") {
		t.Error("Mode 1 should inject CLAUDE_CONFIG_DIR")
	}
}

func TestClaudePrepareMode2PassesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "should-keep")

	runner := NewClaudeRunner("claude")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode2,
		Phase: PhaseSpec{
			Name:         "plan",
			BuiltinTools: []string{"Read"},
			MCPEnabled:   false,
			MaxTurns:     3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	envJoined := strings.Join(prepared.Env, "\n")
	if !strings.Contains(envJoined, "ANTHROPIC_API_KEY=should-keep") {
		t.Error("Mode 2 should pass through ANTHROPIC_API_KEY")
	}
}
