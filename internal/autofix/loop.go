// Package autofix implements an auto-lint-and-fix loop.
// Inspired by Aider's lint→fix→verify cycle:
// 1. Run linter on changed files
// 2. Parse lint errors into structured issues
// 3. Generate fix instructions for the agent
// 4. Re-lint to verify fixes
// 5. Repeat until clean or max iterations
//
// This prevents the common "fix one thing, break another" cycle by
// catching issues early and iterating automatically.
package autofix

import (
	"fmt"
	"regexp"
	"strings"
)

// Canonical Issue.Level values.
const (
	levelWarning = "warning"
)

// Issue represents a single lint/build error.
type Issue struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
	Linter  string `json:"linter"`
	Level   string `json:"level"` // "error", "warning"
}

// LoopConfig controls the fix loop behavior.
type LoopConfig struct {
	MaxIterations int  // default 3
	FixWarnings   bool // also fix warnings, not just errors
}

// DefaultLoopConfig returns sensible defaults.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		MaxIterations: 3,
		FixWarnings:   false,
	}
}

// RunFunc runs a command and returns its combined output and error.
type RunFunc func(name string, args ...string) (string, error)

// FixFunc generates a fix for an issue. Returns the fix instruction.
type FixFunc func(issue Issue) error

// Result describes what happened during the fix loop.
type Result struct {
	Iterations   int     `json:"iterations"`
	IssuesBefore int     `json:"issues_before"`
	IssuesAfter  int     `json:"issues_after"`
	Fixed        int     `json:"fixed"`
	Remaining    []Issue `json:"remaining,omitempty"`
	Clean        bool    `json:"clean"`
}

// Loop runs the lint→fix→verify cycle.
func Loop(files []string, lintCmd RunFunc, fixFn FixFunc, cfg LoopConfig) Result {
	if cfg.MaxIterations == 0 {
		cfg = DefaultLoopConfig()
	}

	var firstCount int
	var result Result

	for i := 0; i < cfg.MaxIterations; i++ {
		result.Iterations = i + 1

		// Run linter
		output, _ := lintCmd("lint", files...)
		issues := ParseOutput(output)

		if !cfg.FixWarnings {
			issues = filterErrors(issues)
		}

		if i == 0 {
			firstCount = len(issues)
			result.IssuesBefore = firstCount
		}

		if len(issues) == 0 {
			result.Clean = true
			result.IssuesAfter = 0
			result.Fixed = firstCount
			return result
		}

		// Try to fix each issue
		for _, issue := range issues {
			fixFn(issue)
		}
	}

	// Final lint check
	output, _ := lintCmd("lint", files...)
	remaining := ParseOutput(output)
	if !cfg.FixWarnings {
		remaining = filterErrors(remaining)
	}

	result.IssuesAfter = len(remaining)
	result.Remaining = remaining
	result.Fixed = firstCount - len(remaining)
	result.Clean = len(remaining) == 0
	return result
}

// ParseOutput extracts issues from linter output.
// Supports common formats: file:line:col: message, file:line: message
func ParseOutput(output string) []Issue {
	if output == "" {
		return nil
	}

	var issues []Issue
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if issue, ok := parseLine(line); ok {
			issues = append(issues, issue)
		}
	}
	return issues
}

// common patterns: file:line:col: message, file:line: message
var (
	reFileLineCol = regexp.MustCompile(`^(.+?):(\d+):(\d+):\s*(.+)`)
	reFileLine    = regexp.MustCompile(`^(.+?):(\d+):\s*(.+)`)
)

func parseLine(line string) (Issue, bool) {
	if m := reFileLineCol.FindStringSubmatch(line); m != nil {
		var col int
		fmt.Sscanf(m[3], "%d", &col)
		var lineNum int
		fmt.Sscanf(m[2], "%d", &lineNum)
		msg := m[4]
		level := "error"
		if strings.Contains(strings.ToLower(msg), levelWarning) {
			level = levelWarning
		}
		return Issue{
			File:    m[1],
			Line:    lineNum,
			Column:  col,
			Message: msg,
			Level:   level,
		}, true
	}
	if m := reFileLine.FindStringSubmatch(line); m != nil {
		var lineNum int
		fmt.Sscanf(m[2], "%d", &lineNum)
		msg := m[3]
		level := "error"
		if strings.Contains(strings.ToLower(msg), levelWarning) {
			level = levelWarning
		}
		return Issue{
			File:    m[1],
			Line:    lineNum,
			Message: msg,
			Level:   level,
		}, true
	}
	return Issue{}, false
}

// FormatIssue creates a human-readable description of an issue.
func FormatIssue(issue Issue) string {
	if issue.Column > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", issue.File, issue.Line, issue.Column, issue.Message)
	}
	return fmt.Sprintf("%s:%d: %s", issue.File, issue.Line, issue.Message)
}

// FormatFixPrompt generates a prompt for an agent to fix issues.
func FormatFixPrompt(issues []Issue) string {
	if len(issues) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Fix the following lint/build errors:\n\n")

	byFile := make(map[string][]Issue)
	for _, issue := range issues {
		byFile[issue.File] = append(byFile[issue.File], issue)
	}

	for file, fileIssues := range byFile {
		fmt.Fprintf(&b, "## %s\n", file)
		for _, issue := range fileIssues {
			fmt.Fprintf(&b, "- Line %d: %s\n", issue.Line, issue.Message)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func filterErrors(issues []Issue) []Issue {
	var filtered []Issue
	for _, issue := range issues {
		if issue.Level != levelWarning {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}
