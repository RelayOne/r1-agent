package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1-agent/internal/taskstate"
)

func TestNewWithDefaultPolicy(t *testing.T) {
	o, err := New(RunConfig{
		RepoRoot: t.TempDir(),
		Task:     "test task",
		AuthMode: AuthModeMode1,
		State:    taskstate.NewTaskState("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	if len(o.policy.Phases) != 3 {
		t.Errorf("phases=%d, want 3", len(o.policy.Phases))
	}
}

func TestNewWithCustomPolicy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte(`
phases:
  plan:
    builtin_tools: [Read]
    mcp_enabled: false
verification:
  build: required
  tests: required
  lint: optional
`), 0o600)

	o, err := New(RunConfig{
		RepoRoot:   dir,
		PolicyPath: filepath.Join(dir, "custom.yaml"),
		Task:       "test",
		AuthMode:   AuthModeMode1,
		State:      taskstate.NewTaskState("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := o.policy.Phases["plan"]
	if len(plan.BuiltinTools) != 1 || plan.BuiltinTools[0] != "Read" {
		t.Errorf("plan tools=%v", plan.BuiltinTools)
	}
}

func TestNewDefaultsToMode1(t *testing.T) {
	o, err := New(RunConfig{
		RepoRoot: t.TempDir(),
		Task:     "test",
		State:    taskstate.NewTaskState("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.cfg.AuthMode != AuthModeMode1 {
		t.Errorf("authMode=%q, want mode1", o.cfg.AuthMode)
	}
}

func TestNewRejectsNilState(t *testing.T) {
	_, err := New(RunConfig{
		RepoRoot: t.TempDir(),
		Task:     "test",
	})
	if err == nil {
		t.Fatal("MUST reject nil State -- no legacy mode allowed")
	}
}

func TestDoctor(t *testing.T) {
	result := Doctor("nonexistent-claude", "nonexistent-codex", false)
	if result == "" {
		t.Error("Doctor should produce output")
	}
}

func TestDoctor_Providers(t *testing.T) {
	result := Doctor("nonexistent-claude", "nonexistent-codex", true)
	if result == "" {
		t.Error("Doctor --providers should produce output")
	}
	if !strings.Contains(result, "Provider fallback chain") {
		t.Error("Doctor --providers should include provider chain info")
	}
	if !strings.Contains(result, "Lint-only") {
		t.Error("Doctor --providers should list lint-only fallback")
	}
}

// TestBuildRunners_NativeExplicitWithoutKey is the regression test for the
// bug where `--runner native` without a NativeAPIKey silently fell back to
// Claude Code because the old construction predicate required key != "".
// Now a bare `--runner native` must produce a non-nil Native runner by
// using env fallbacks (or a stub when BaseURL is set).
func TestBuildRunners_NativeExplicitWithoutKey(t *testing.T) {
	// Clear any env keys that could mask the test.
	for _, k := range []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	cfg := RunConfig{
		RunnerMode:    "native",
		NativeBaseURL: "http://localhost:8000",
	}
	r := buildRunners(cfg)
	if r.Native == nil {
		t.Fatal("buildRunners: --runner=native must produce Native runner even without an explicit key")
	}
	if r.Claude == nil {
		t.Error("Claude runner should still be constructed as a fallback")
	}
}

func TestBuildRunners_NativeExplicit_NoBaseURL_UsesEnv(t *testing.T) {
	t.Setenv("LITELLM_API_KEY", "")
	t.Setenv("LITELLM_MASTER_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-123")
	cfg := RunConfig{RunnerMode: "native"}
	r := buildRunners(cfg)
	if r.Native == nil {
		t.Fatal("buildRunners: --runner=native with ANTHROPIC_API_KEY env should produce Native runner")
	}
}

func TestBuildRunners_NativeExplicit_LiteLLMEnv(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "sk-litellm-master")
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := RunConfig{
		RunnerMode:    "native",
		NativeBaseURL: "http://localhost:4000",
	}
	r := buildRunners(cfg)
	if r.Native == nil {
		t.Fatal("buildRunners: --runner=native with LITELLM_MASTER_KEY should produce Native runner")
	}
}

func TestBuildRunners_NativeImplicit_ApiKeyOnly(t *testing.T) {
	cfg := RunConfig{NativeAPIKey: "sk-explicit"}
	r := buildRunners(cfg)
	if r.Native == nil {
		t.Fatal("buildRunners: explicit NativeAPIKey should produce Native runner even without --runner flag")
	}
}

func TestBuildRunners_ClaudeMode_NoNative(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := RunConfig{RunnerMode: "claude"}
	r := buildRunners(cfg)
	if r.Native != nil {
		t.Error("buildRunners: --runner=claude should NOT produce a Native runner")
	}
	if r.Claude == nil {
		t.Error("buildRunners: --runner=claude should produce a Claude runner")
	}
}

func TestBuildRunners_DefaultModel(t *testing.T) {
	cfg := RunConfig{RunnerMode: "native", NativeAPIKey: "x", NativeBaseURL: "http://localhost:8000"}
	r := buildRunners(cfg)
	if r.Native == nil {
		t.Fatal("expected native runner")
	}
	// Default model is wired via NewNativeRunner — we can't inspect it
	// directly without exporting it, but construction succeeding with
	// no NativeModel set is the contract we care about.
}
