// lintgate.go implements a pre-commit lint gate for edits.
// Inspired by SWE-agent's ACI design: every edit runs a linter before it takes
// effect. Syntactically invalid edits are rejected, and the agent must fix them
// before proceeding. This catches errors BEFORE they propagate.
//
// SWE-agent found this improved performance by 12.29% absolute on SWE-bench.
//
// Integrates with Stoke's hooks system as a PreToolUse check: when an agent
// writes a file, run the linter on just that file and reject if it fails.
package verify

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// LintGate runs a linter on specific files before accepting edits.
type LintGate struct {
	linters map[string]string // file extension -> lint command template
}

// NewLintGate creates a lint gate with configured linters per file type.
func NewLintGate() *LintGate {
	return &LintGate{
		linters: map[string]string{
			".go":   "go vet %s",
			".ts":   "npx tsc --noEmit %s",
			".tsx":  "npx tsc --noEmit %s",
			".js":   "npx eslint %s",
			".jsx":  "npx eslint %s",
			".py":   "python3 -m py_compile %s",
			".rs":   "rustfmt --check %s",
			".json": "python3 -m json.tool %s > /dev/null",
			".yaml": "python3 -c \"import yaml,sys; yaml.safe_load(open(sys.argv[1]))\" %s",
			".yml":  "python3 -c \"import yaml,sys; yaml.safe_load(open(sys.argv[1]))\" %s",
		},
	}
}

// SetLinter configures a custom linter for a file extension.
func (lg *LintGate) SetLinter(ext, cmdTemplate string) {
	lg.linters[ext] = cmdTemplate
}

// LintResult is the outcome of a single-file lint check.
type LintResult struct {
	File    string `json:"file"`
	Passed  bool   `json:"passed"`
	Output  string `json:"output,omitempty"`
	Skipped bool   `json:"skipped"` // no linter for this file type
}

// Check runs the appropriate linter on a file.
// The command template should contain %s which will be replaced with the file path.
func (lg *LintGate) Check(ctx context.Context, dir, filePath string) LintResult {
	ext := filepath.Ext(filePath)
	cmdTemplate, ok := lg.linters[ext]
	if !ok {
		return LintResult{File: filePath, Passed: true, Skipped: true}
	}

	// For Go, vet the package not the file
	fullPath := filePath
	if !filepath.IsAbs(filePath) {
		fullPath = filepath.Join(dir, filePath)
	}

	var cmdStr string
	if ext == ".go" {
		// Go vet operates on packages, not files
		pkgDir := filepath.Dir(fullPath)
		cmdStr = fmt.Sprintf("go vet %s", pkgDir)
	} else {
		cmdStr = fmt.Sprintf(cmdTemplate, fullPath)
	}

	cmd := exec.CommandContext(ctx, "bash", "-lc", cmdStr) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()

	return LintResult{
		File:   filePath,
		Passed: err == nil,
		Output: strings.TrimSpace(string(out)),
	}
}

// CheckMultiple runs lint checks on multiple files.
// Returns all results and whether all passed.
func (lg *LintGate) CheckMultiple(ctx context.Context, dir string, files []string) ([]LintResult, bool) {
	results := make([]LintResult, 0, len(files))
	allPassed := true

	for _, f := range files {
		result := lg.Check(ctx, dir, f)
		results = append(results, result)
		if !result.Passed && !result.Skipped {
			allPassed = false
		}
	}

	return results, allPassed
}

// ShouldGate returns true if the file type has a configured linter.
func (lg *LintGate) ShouldGate(filePath string) bool {
	ext := filepath.Ext(filePath)
	_, ok := lg.linters[ext]
	return ok
}

// FormatRejection creates a human-readable rejection message.
func FormatRejection(results []LintResult) string {
	var sb strings.Builder
	sb.WriteString("EDIT REJECTED: lint check failed\n\n")

	for _, r := range results {
		if r.Skipped || r.Passed {
			continue
		}
		sb.WriteString(fmt.Sprintf("File: %s\n", r.File))
		if r.Output != "" {
			sb.WriteString(fmt.Sprintf("Error:\n%s\n\n", r.Output))
		}
	}

	sb.WriteString("Fix the errors and retry the edit.\n")
	return sb.String()
}
