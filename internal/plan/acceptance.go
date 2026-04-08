package plan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AcceptanceResult is the outcome of checking one acceptance criterion.
type AcceptanceResult struct {
	CriterionID string
	Description string
	Passed      bool
	Output      string // command output or diagnostic message
}

// CheckAcceptanceCriteria evaluates all criteria for a session against the
// project directory. Returns results for each criterion and an overall pass/fail.
func CheckAcceptanceCriteria(ctx context.Context, projectRoot string, criteria []AcceptanceCriterion) ([]AcceptanceResult, bool) {
	var results []AcceptanceResult
	allPassed := true

	for _, ac := range criteria {
		result := checkOneCriterion(ctx, projectRoot, ac)
		results = append(results, result)
		if !result.Passed {
			allPassed = false
		}
	}

	return results, allPassed
}

func checkOneCriterion(ctx context.Context, projectRoot string, ac AcceptanceCriterion) AcceptanceResult {
	result := AcceptanceResult{
		CriterionID: ac.ID,
		Description: ac.Description,
	}

	// Command check: run a shell command and check exit code
	if ac.Command != "" {
		cmd := exec.CommandContext(ctx, "bash", "-lc", ac.Command)
		cmd.Dir = projectRoot
		out, err := cmd.CombinedOutput()
		result.Output = string(out)
		result.Passed = err == nil
		if !result.Passed {
			result.Output = fmt.Sprintf("command failed: %v\n%s", err, result.Output)
		}
		return result
	}

	// File existence check
	if ac.FileExists != "" {
		path := ac.FileExists
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectRoot, path)
		}
		if _, err := os.Stat(path); err == nil {
			result.Passed = true
			result.Output = fmt.Sprintf("file exists: %s", ac.FileExists)
		} else {
			result.Passed = false
			result.Output = fmt.Sprintf("file not found: %s", ac.FileExists)
		}
		return result
	}

	// Content match check
	if ac.ContentMatch != nil {
		path := ac.ContentMatch.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectRoot, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			result.Passed = false
			result.Output = fmt.Sprintf("cannot read %s: %v", ac.ContentMatch.File, err)
			return result
		}
		if strings.Contains(string(data), ac.ContentMatch.Pattern) {
			result.Passed = true
			result.Output = fmt.Sprintf("pattern %q found in %s", ac.ContentMatch.Pattern, ac.ContentMatch.File)
		} else {
			result.Passed = false
			result.Output = fmt.Sprintf("pattern %q not found in %s", ac.ContentMatch.Pattern, ac.ContentMatch.File)
		}
		return result
	}

	// No verifiable check configured — pass by default (description-only criterion)
	result.Passed = true
	result.Output = "no automated check configured (manual verification)"
	return result
}

// FormatAcceptanceResults returns a human-readable summary of acceptance check results.
func FormatAcceptanceResults(results []AcceptanceResult) string {
	var b strings.Builder
	passed := 0
	for _, r := range results {
		status := "FAIL"
		if r.Passed {
			status = "PASS"
			passed++
		}
		fmt.Fprintf(&b, "  [%s] %s: %s\n", status, r.CriterionID, r.Description)
		if !r.Passed && r.Output != "" {
			// Indent output lines
			for _, line := range strings.Split(strings.TrimSpace(r.Output), "\n") {
				fmt.Fprintf(&b, "         %s\n", line)
			}
		}
	}
	fmt.Fprintf(&b, "  %d/%d criteria passed\n", passed, len(results))
	return b.String()
}
