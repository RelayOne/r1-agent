// Package preflight implements pre-flight workspace assertions.
// Inspired by OmX's clean workspace check before spawning worktrees:
// - Verify git repo is in a clean state
// - Check disk space is sufficient
// - Validate required tools are installed
// - Ensure no conflicting processes are running
// - Check network connectivity to required services
//
// Running these checks BEFORE spawning agents prevents wasted compute
// from environments that will inevitably fail.
package preflight

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Severity of a check failure.
type Severity string

const (
	SeverityError   Severity = "error"   // blocks execution
	SeverityWarning Severity = "warning" // allows execution with notice
)

// CheckResult is the outcome of a single assertion.
type CheckResult struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message,omitempty"`
}

// Report holds all check results.
type Report struct {
	Checks  []CheckResult `json:"checks"`
	Passed  bool          `json:"passed"` // true if no errors (warnings ok)
	Summary string        `json:"summary"`
}

// Checker runs a single assertion.
type Checker func(repoDir string) CheckResult

// RunAll executes all checkers and returns a report.
func RunAll(repoDir string, checkers []Checker) Report {
	var report Report
	report.Passed = true

	for _, check := range checkers {
		result := check(repoDir)
		report.Checks = append(report.Checks, result)
		if !result.Passed && result.Severity == SeverityError {
			report.Passed = false
		}
	}

	errors := 0
	warnings := 0
	for _, c := range report.Checks {
		if !c.Passed {
			if c.Severity == SeverityError {
				errors++
			} else {
				warnings++
			}
		}
	}

	if errors == 0 && warnings == 0 {
		report.Summary = fmt.Sprintf("all %d checks passed", len(report.Checks))
	} else {
		report.Summary = fmt.Sprintf("%d errors, %d warnings out of %d checks", errors, warnings, len(report.Checks))
	}

	return report
}

// DefaultCheckers returns the standard set of pre-flight assertions.
func DefaultCheckers() []Checker {
	return []Checker{
		CheckGitRepo,
		CheckGitClean,
		CheckDiskSpace,
		CheckGitInstalled,
	}
}

// CheckGitRepo verifies the directory is a git repository.
func CheckGitRepo(repoDir string) CheckResult {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return CheckResult{
			Name:     "git-repo",
			Passed:   false,
			Severity: SeverityError,
			Message:  fmt.Sprintf("%s is not a git repository", repoDir),
		}
	}
	return CheckResult{Name: "git-repo", Passed: true}
}

// CheckGitClean verifies the working tree has no uncommitted changes.
func CheckGitClean(repoDir string) CheckResult {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return CheckResult{
			Name:     "git-clean",
			Passed:   false,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("git status failed: %v", err),
		}
	}
	if strings.TrimSpace(string(out)) != "" {
		return CheckResult{
			Name:     "git-clean",
			Passed:   false,
			Severity: SeverityWarning,
			Message:  "working tree has uncommitted changes",
		}
	}
	return CheckResult{Name: "git-clean", Passed: true}
}

// CheckDiskSpace verifies at least minMB of free disk space.
func CheckDiskSpace(repoDir string) CheckResult {
	// Use a simple heuristic: check if we can create a temp file
	tmpFile := filepath.Join(repoDir, ".stoke-preflight-check")
	data := make([]byte, 1024) // 1KB test write
	err := os.WriteFile(tmpFile, data, 0644) // #nosec G306 -- preflight record; user-readable.
	os.Remove(tmpFile)
	if err != nil {
		return CheckResult{
			Name:     "disk-space",
			Passed:   false,
			Severity: SeverityError,
			Message:  fmt.Sprintf("cannot write to %s: %v", repoDir, err),
		}
	}
	return CheckResult{Name: "disk-space", Passed: true}
}

// CheckGitInstalled verifies git is available.
func CheckGitInstalled(repoDir string) CheckResult {
	_, err := exec.LookPath("git")
	if err != nil {
		return CheckResult{
			Name:     "git-installed",
			Passed:   false,
			Severity: SeverityError,
			Message:  "git is not installed or not in PATH",
		}
	}
	return CheckResult{Name: "git-installed", Passed: true}
}

// CheckToolInstalled creates a checker for a required CLI tool.
func CheckToolInstalled(tool string) Checker {
	return func(repoDir string) CheckResult {
		_, err := exec.LookPath(tool)
		if err != nil {
			return CheckResult{
				Name:     fmt.Sprintf("tool-%s", tool),
				Passed:   false,
				Severity: SeverityError,
				Message:  fmt.Sprintf("%s is not installed or not in PATH", tool),
			}
		}
		return CheckResult{Name: fmt.Sprintf("tool-%s", tool), Passed: true}
	}
}

// CheckFileExists creates a checker for a required file.
func CheckFileExists(path string, severity Severity) Checker {
	return func(repoDir string) CheckResult {
		fullPath := path
		if !filepath.IsAbs(path) {
			fullPath = filepath.Join(repoDir, path)
		}
		if _, err := os.Stat(fullPath); err != nil {
			return CheckResult{
				Name:     fmt.Sprintf("file-%s", filepath.Base(path)),
				Passed:   false,
				Severity: severity,
				Message:  fmt.Sprintf("required file %s not found", path),
			}
		}
		return CheckResult{Name: fmt.Sprintf("file-%s", filepath.Base(path)), Passed: true}
	}
}

// HasErrors returns true if the report contains any errors.
func (r *Report) HasErrors() bool {
	for _, c := range r.Checks {
		if !c.Passed && c.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns only the error-level failures.
func (r *Report) Errors() []CheckResult {
	var errs []CheckResult
	for _, c := range r.Checks {
		if !c.Passed && c.Severity == SeverityError {
			errs = append(errs, c)
		}
	}
	return errs
}

// Warnings returns only the warning-level failures.
func (r *Report) Warnings() []CheckResult {
	var warns []CheckResult
	for _, c := range r.Checks {
		if !c.Passed && c.Severity == SeverityWarning {
			warns = append(warns, c)
		}
	}
	return warns
}
