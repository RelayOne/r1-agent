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

	prompt := buildSOWNativePrompt(sow, session, task)
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
}

func TestBuildSOWNativePrompt_MinimalInput(t *testing.T) {
	sow := &plan.SOW{ID: "minimal", Name: "Minimal"}
	session := plan.Session{ID: "S1", Title: "Go"}
	task := plan.Task{ID: "T1", Description: "do a thing"}

	prompt := buildSOWNativePrompt(sow, session, task)
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
