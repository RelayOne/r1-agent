package main

import (
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/plan"
)

func TestInferBaselineCriteria_Go(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "go"})
	if len(crit) < 2 {
		t.Fatalf("expected multiple baseline criteria for Go, got %d", len(crit))
	}
	commands := []string{}
	for _, c := range crit {
		commands = append(commands, c.Command)
	}
	joined := strings.Join(commands, " ")
	for _, want := range []string{"go build", "go vet"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Go baseline missing %q: %v", want, commands)
		}
	}
}

func TestInferBaselineCriteria_Rust(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "rust"})
	if len(crit) == 0 {
		t.Fatal("expected Rust baseline criteria")
	}
	// Collect commands + file checks into a flat blob so the assertion
	// doesn't care about ordering (we now prepend a root Cargo.toml
	// check + workspace-consistency check before the build step).
	var blob strings.Builder
	for _, c := range crit {
		blob.WriteString(c.Command)
		blob.WriteString(" ")
		blob.WriteString(c.FileExists)
		blob.WriteString(" ")
	}
	joined := blob.String()
	for _, want := range []string{"cargo build", "Cargo.toml"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Rust baseline missing %q: %v", want, crit)
		}
	}
}

func TestInferBaselineCriteria_TypeScript(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "typescript"})
	if len(crit) == 0 {
		t.Fatal("expected TS baseline criteria")
	}
	var haveInstall, haveBuild bool
	for _, c := range crit {
		if strings.Contains(c.Command, "install") {
			haveInstall = true
		}
		if strings.Contains(c.Command, "build") {
			haveBuild = true
		}
	}
	if !haveInstall || !haveBuild {
		t.Errorf("TS baseline missing install or build step: %+v", crit)
	}
}

func TestInferBaselineCriteria_Python(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "python"})
	if len(crit) == 0 {
		t.Fatal("expected Python baseline criteria")
	}
	if !strings.Contains(crit[0].Command, "compileall") {
		t.Errorf("Python first criterion should use compileall, got %q", crit[0].Command)
	}
}

func TestInferBaselineCriteria_UnknownStack_Empty(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "haskell"})
	if len(crit) != 0 {
		t.Errorf("unknown stack should produce no baseline criteria, got %d", len(crit))
	}
}

func TestInferBaselineCriteria_EmptyStack_Empty(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{})
	if len(crit) != 0 {
		t.Errorf("empty stack should produce no criteria, got %d", len(crit))
	}
}

func TestFormatAcceptanceFailures_OnlyFailed(t *testing.T) {
	results := []plan.AcceptanceResult{
		{CriterionID: "AC1", Description: "build", Passed: true, Output: "ok"},
		{CriterionID: "AC2", Description: "test", Passed: false, Output: "FAIL: TestFoo\n  expected 1, got 2"},
		{CriterionID: "AC3", Description: "lint", Passed: false, Output: "foo.go:3:1: undefined: bar"},
	}
	blob := formatAcceptanceFailures(results, plan.Session{})
	if strings.Contains(blob, "[AC1]") {
		t.Error("passing criteria should not appear in failure blob")
	}
	if !strings.Contains(blob, "[AC2]") || !strings.Contains(blob, "TestFoo") {
		t.Errorf("blob should include AC2 failure:\n%s", blob)
	}
	if !strings.Contains(blob, "[AC3]") || !strings.Contains(blob, "undefined: bar") {
		t.Errorf("blob should include AC3 failure:\n%s", blob)
	}
	// Failure output should be indented so the model sees structure.
	if !strings.Contains(blob, "    ") {
		t.Error("failure output should be indented")
	}
}

func TestCountFailed(t *testing.T) {
	results := []plan.AcceptanceResult{
		{Passed: true},
		{Passed: false},
		{Passed: false},
		{Passed: true},
	}
	if n := countFailed(results); n != 2 {
		t.Errorf("countFailed = %d, want 2", n)
	}
}
