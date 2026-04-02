package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ericmacdougall/stoke/internal/taskstate"
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
`), 0644)

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
	result := Doctor("nonexistent-claude", "nonexistent-codex")
	if result == "" {
		t.Error("Doctor should produce output")
	}
}
