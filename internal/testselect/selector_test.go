package testselect

import (
	"os"
	"path/filepath"
	"testing"
)

func setupProject(t *testing.T) string {
	dir := t.TempDir()

	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, []byte(content), 0644)
	}

	write("internal/config/policy.go", `package config

import "fmt"

type Policy struct{ Name string }

func Load() *Policy {
	fmt.Println("loading")
	return &Policy{}
}
`)

	write("internal/config/policy_test.go", `package config

import "testing"

func TestLoad(t *testing.T) {}
`)

	write("internal/engine/runner.go", `package engine

import (
	"github.com/example/stoke/internal/config"
)

type Runner struct{ Cfg *config.Policy }
`)

	write("internal/engine/runner_test.go", `package engine

import "testing"

func TestRunner(t *testing.T) {}
`)

	write("internal/tui/display.go", `package tui

func Render() {}
`)

	write("internal/tui/display_test.go", `package tui

import "testing"

func TestRender(t *testing.T) {}
`)

	return dir
}

func TestBuildGraph(t *testing.T) {
	dir := setupProject(t)
	g, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}

	if g.TestCount() != 3 {
		t.Errorf("expected 3 test files, got %d", g.TestCount())
	}
	if g.PackageCount() != 3 {
		t.Errorf("expected 3 packages with tests, got %d", g.PackageCount())
	}
}

func TestSelectDirectChange(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.Select([]string{"internal/tui/display.go"})
	if len(sel.Selected) != 1 {
		t.Errorf("expected 1 selected test, got %d", len(sel.Selected))
	}
	if len(sel.Selected) > 0 && sel.Selected[0] != "internal/tui/display_test.go" {
		t.Errorf("expected tui test, got %s", sel.Selected[0])
	}
}

func TestSelectTransitive(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	// Changing config should also select engine tests (engine imports config)
	sel := g.Select([]string{"internal/config/policy.go"})
	if len(sel.Selected) < 1 {
		t.Error("should select at least config tests")
	}
}

func TestSelectTestFile(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.Select([]string{"internal/config/policy_test.go"})
	if len(sel.Selected) == 0 {
		t.Error("changing a test file should select that test")
	}
}

func TestSelectNoChanges(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.Select(nil)
	if len(sel.Selected) != 0 {
		t.Error("no changes should select no tests")
	}
}

func TestSelectAll(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.SelectAll()
	if len(sel.Selected) != 3 {
		t.Errorf("select all should return all 3 tests, got %d", len(sel.Selected))
	}
}

func TestTestsForFile(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	tests := g.TestsForFile("internal/config/policy.go")
	if len(tests) == 0 {
		t.Error("should find tests for config/policy.go")
	}
}

func TestSelectUnknownFile(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.Select([]string{"nonexistent.go"})
	if len(sel.Selected) != 0 {
		t.Error("unknown file should not select any tests")
	}
}

func TestSkippedTests(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.Select([]string{"internal/tui/display.go"})
	if len(sel.Skipped) == 0 {
		t.Error("should have skipped tests for unaffected packages")
	}
}

func TestPackagesOutput(t *testing.T) {
	dir := setupProject(t)
	g, _ := BuildGraph(dir)

	sel := g.Select([]string{"internal/tui/display.go"})
	if len(sel.Packages) == 0 {
		t.Error("should output package paths")
	}
}
