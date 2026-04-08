package main

import (
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/plan"
)

func TestBuildSOWNativePrompt_IncludesAllContext(t *testing.T) {
	sow := &plan.SOW{
		ID:          "test",
		Name:        "Test Project",
		Description: "a description",
		Stack: plan.StackSpec{
			Language:  "rust",
			Framework: "actix-web",
		},
	}
	session := plan.Session{
		ID:          "S1",
		Title:       "Foundation",
		Description: "set up the repo",
		Inputs:      []string{"design doc"},
		Outputs:     []string{"Cargo.toml", "src/main.rs"},
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", Description: "build succeeds", Command: "cargo build"},
			{ID: "AC2", Description: "readme exists", FileExists: "README.md"},
			{ID: "AC3", Description: "main exists", ContentMatch: &plan.ContentMatchCriterion{File: "src/main.rs", Pattern: "fn main"}},
		},
	}
	task := plan.Task{
		ID:           "T1",
		Description:  "create the Cargo.toml and main.rs",
		Files:        []string{"Cargo.toml", "src/main.rs"},
		Dependencies: []string{"T0"},
	}

	prompt := buildSOWNativePrompt(sow, session, task, nil, 0, nil)
	for _, want := range []string{
		"Test Project",
		"a description",
		"stack: rust / actix-web",
		"SESSION S1: Foundation",
		"set up the repo",
		"inputs from prior sessions: design doc",
		"expected outputs: Cargo.toml, src/main.rs",
		"TASK T1:",
		"create the Cargo.toml and main.rs",
		"expected files: Cargo.toml, src/main.rs",
		"depends on: T0",
		"ACCEPTANCE CRITERIA",
		"cargo build",
		"README.md",
		"fn main",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n---\n%s\n---", want, prompt)
		}
	}

	// Split prompts: the stable context should live in the system block
	// and the per-task lines in the user block.
	sys, usr := buildSOWNativePrompts(sow, session, task, nil, 0, nil, nil)
	if !strings.Contains(sys, "PROJECT: Test Project") {
		t.Error("system prompt should include project header")
	}
	if !strings.Contains(sys, "SESSION S1: Foundation") {
		t.Error("system prompt should include session framing")
	}
	if !strings.Contains(sys, "ACCEPTANCE CRITERIA") {
		t.Error("system prompt should include criteria")
	}
	if strings.Contains(sys, "TASK T1:") {
		t.Error("task header should NOT be in the system (cached) block")
	}
	if !strings.Contains(usr, "TASK T1:") {
		t.Error("user prompt should include task header")
	}
	if !strings.Contains(usr, "create the Cargo.toml and main.rs") {
		t.Error("user prompt should include task description")
	}
}

func TestBuildSOWNativePrompts_RepairMode(t *testing.T) {
	sow := &plan.SOW{ID: "r", Name: "Repair Project"}
	session := plan.Session{ID: "S1", Title: "t", AcceptanceCriteria: []plan.AcceptanceCriterion{
		{ID: "AC1", Description: "build", Command: "go build ./..."},
	}}
	task := plan.Task{ID: "repair-1", Description: "repair"}
	failBlob := "- [AC1] build\n    cannot find main package"
	sys, usr := buildSOWNativePrompts(sow, session, task, nil, 0, &failBlob, nil)

	if !strings.Contains(sys, "REPAIR mode") {
		t.Error("system prompt should switch to REPAIR mode framing")
	}
	if strings.Contains(sys, "TASK repair-1") {
		t.Error("repair system prompt should not include a task header")
	}
	if !strings.Contains(usr, "FAILING ACCEPTANCE CRITERIA") {
		t.Error("user prompt should include the failure block")
	}
	if !strings.Contains(usr, "cannot find main package") {
		t.Error("user prompt should include the specific failure output")
	}
}

func TestBuildSOWNativePrompt_MinimalInput(t *testing.T) {
	sow := &plan.SOW{ID: "minimal", Name: "Minimal"}
	session := plan.Session{ID: "S1", Title: "Go"}
	task := plan.Task{ID: "T1", Description: "do a thing"}

	prompt := buildSOWNativePrompt(sow, session, task, nil, 0, nil)
	if !strings.Contains(prompt, "TASK T1") {
		t.Error("minimal prompt should still include task header")
	}
	if !strings.Contains(prompt, "do a thing") {
		t.Error("minimal prompt should include task description")
	}
}

func TestRunSessionNative_RejectsNilRunner(t *testing.T) {
	cfg := sowNativeConfig{RepoRoot: "/tmp"}
	_, err := runSessionNative(nil, plan.Session{}, &plan.SOW{}, cfg)
	if err == nil || !strings.Contains(err.Error(), "native runner is nil") {
		t.Errorf("expected nil-runner error, got %v", err)
	}
}

func TestRunSessionNative_RejectsEmptyRepoRoot(t *testing.T) {
	// Can't actually exercise a real runner without a live provider,
	// but we can verify the guard clause fires.
	cfg := sowNativeConfig{Runner: nil, RepoRoot: ""}
	_, err := runSessionNative(nil, plan.Session{}, &plan.SOW{}, cfg)
	if err == nil {
		t.Error("expected error for empty config")
	}
}
