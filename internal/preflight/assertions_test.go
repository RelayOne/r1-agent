package preflight

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckGitRepo(t *testing.T) {
	dir := t.TempDir()

	// Not a git repo
	result := CheckGitRepo(dir)
	if result.Passed {
		t.Error("should fail for non-git directory")
	}

	// Create .git dir
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	result = CheckGitRepo(dir)
	if !result.Passed {
		t.Error("should pass for git directory")
	}
}

func TestCheckDiskSpace(t *testing.T) {
	dir := t.TempDir()
	result := CheckDiskSpace(dir)
	if !result.Passed {
		t.Error("should pass on writable directory")
	}
}

func TestCheckGitInstalled(t *testing.T) {
	dir := t.TempDir()
	result := CheckGitInstalled(dir)
	if !result.Passed {
		t.Skip("git not installed")
	}
}

func TestCheckToolInstalled(t *testing.T) {
	checker := CheckToolInstalled("nonexistent-tool-xyz")
	result := checker(t.TempDir())
	if result.Passed {
		t.Error("should fail for nonexistent tool")
	}
	if result.Severity != SeverityError {
		t.Error("should be error severity")
	}
}

func TestCheckFileExists(t *testing.T) {
	dir := t.TempDir()

	// File doesn't exist
	checker := CheckFileExists("go.mod", SeverityError)
	result := checker(dir)
	if result.Passed {
		t.Error("should fail for missing file")
	}

	// Create the file
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0644)
	result = checker(dir)
	if !result.Passed {
		t.Error("should pass for existing file")
	}
}

func TestRunAll(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0755)

	checkers := []Checker{
		CheckGitRepo,
		CheckDiskSpace,
	}

	report := RunAll(dir, checkers)
	if !report.Passed {
		t.Errorf("expected passed, got: %s", report.Summary)
	}
	if len(report.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(report.Checks))
	}
}

func TestRunAllWithFailure(t *testing.T) {
	dir := t.TempDir()
	// No .git directory

	checkers := []Checker{
		CheckGitRepo,
		CheckDiskSpace,
	}

	report := RunAll(dir, checkers)
	if report.Passed {
		t.Error("should not pass when git repo check fails")
	}
	if !report.HasErrors() {
		t.Error("should have errors")
	}
}

func TestReportErrors(t *testing.T) {
	report := Report{
		Checks: []CheckResult{
			{Name: "a", Passed: true},
			{Name: "b", Passed: false, Severity: SeverityError, Message: "fail"},
			{Name: "c", Passed: false, Severity: SeverityWarning, Message: "warn"},
		},
	}

	errs := report.Errors()
	if len(errs) != 1 || errs[0].Name != "b" {
		t.Errorf("expected 1 error (b), got %v", errs)
	}

	warns := report.Warnings()
	if len(warns) != 1 || warns[0].Name != "c" {
		t.Errorf("expected 1 warning (c), got %v", warns)
	}
}

func TestDefaultCheckers(t *testing.T) {
	checkers := DefaultCheckers()
	if len(checkers) < 3 {
		t.Errorf("expected at least 3 default checkers, got %d", len(checkers))
	}
}
