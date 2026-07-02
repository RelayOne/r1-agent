package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeFakeCLI writes an executable shell script that prints stdout to stdout,
// stderr to stderr, then exits with exitCode. It returns the script path. This
// lets ClaudeRunner.Run drive a deterministic fake "claude" binary.
func writeFakeCLI(t *testing.T, stdout, stderr string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI script requires a POSIX shell")
	}
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")
	errFile := filepath.Join(dir, "err.txt")
	if err := os.WriteFile(outFile, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errFile, []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat \"" + outFile + "\"\ncat \"" + errFile + "\" 1>&2\nexit " + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "fake-claude.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { // #nosec G306 -- test helper needs an executable script.
		t.Fatal(err)
	}
	return path
}

func fakeRunSpec(t *testing.T) RunSpec {
	t.Helper()
	dir := t.TempDir()
	return RunSpec{
		Prompt:      "do the thing",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode2,
		Phase: PhaseSpec{
			Name:         "execute",
			BuiltinTools: []string{"Read"},
			MCPEnabled:   false,
			MaxTurns:     5,
		},
	}
}

// TestClaudeRunFailClosedOnExit1NoResult is the R1 regression guard: a CLI that
// exits nonzero without ever emitting a terminal 'result' event must be flagged
// IsError (not a silent empty success), and its stderr must survive as the
// failure reason.
func TestClaudeRunFailClosedOnExit1NoResult(t *testing.T) {
	runner := NewClaudeRunner(writeFakeCLI(t, "", "boom: credit balance too low / auth expired", 1))
	res, err := runner.Run(context.Background(), fakeRunSpec(t), nil)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("exit-1 run with no result event must set IsError=true, got false (silent empty success)")
	}
	if res.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", res.ExitCode)
	}
	if res.Subtype != "error_during_execution" {
		t.Errorf("Subtype = %q, want error_during_execution", res.Subtype)
	}
	if !strings.Contains(res.ResultText, "auth expired") {
		t.Errorf("bounded stderr not preserved as failure reason: ResultText = %q", res.ResultText)
	}
}

// TestClaudeRunFailClosedOnNoResultExit0 covers a CLI that exits 0 but never
// emits a 'result' event (a dead/hung upgrade path): still not a success.
func TestClaudeRunFailClosedOnNoResultExit0(t *testing.T) {
	runner := NewClaudeRunner(writeFakeCLI(t, "", "", 0))
	res, err := runner.Run(context.Background(), fakeRunSpec(t), nil)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("exit-0 run with no result event must set IsError=true, got false")
	}
	if res.Subtype != "no_result" {
		t.Errorf("Subtype = %q, want no_result", res.Subtype)
	}
}

// TestClaudeRunSuccessNotFlagged is the negative control: a CLI that emits a
// well-formed result event and exits 0 must remain a success (no over-flagging).
func TestClaudeRunSuccessNotFlagged(t *testing.T) {
	resultLine := `{"type":"result","subtype":"success","result":"done","is_error":false,"num_turns":1,"total_cost_usd":0.01}` + "\n"
	runner := NewClaudeRunner(writeFakeCLI(t, resultLine, "", 0))
	res, err := runner.Run(context.Background(), fakeRunSpec(t), nil)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("successful result run wrongly flagged IsError; Subtype=%q ResultText=%q", res.Subtype, res.ResultText)
	}
	if res.Subtype != "success" {
		t.Errorf("Subtype = %q, want success", res.Subtype)
	}
	if res.ResultText != "done" {
		t.Errorf("ResultText = %q, want done", res.ResultText)
	}
}

// TestClaudeRunCancelledClassified asserts a context-cancelled run reports
// Subtype 'cancelled' rather than being misread as an agent failure.
func TestClaudeRunCancelledClassified(t *testing.T) {
	// A script that sleeps long enough for us to cancel it.
	dir := t.TempDir()
	path := filepath.Join(dir, "sleeper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil { // #nosec G306 -- test helper needs an executable script.
		t.Fatal(err)
	}
	runner := NewClaudeRunner(path)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel shortly after start so the CLI is killed mid-run.
		<-time.After(200 * time.Millisecond)
		cancel()
	}()
	res, err := runner.Run(ctx, fakeRunSpec(t), nil)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("cancelled run must set IsError=true")
	}
	if res.Subtype != "cancelled" {
		t.Errorf("Subtype = %q, want cancelled", res.Subtype)
	}
}

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
