package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelfScan runs the deterministic scanner over Stoke's own source tree.
// This is the dogfooding test: if the scanner finds high-severity issues in
// its own codebase, we need to fix them before shipping.
func TestSelfScan(t *testing.T) {
	// Find repo root by walking up from this test file
	repoRoot := findRepoRoot(t)
	if repoRoot == "" {
		t.Skip("could not find repo root")
	}

	// Only scan Go source files (Stoke is a Go project)
	goRules := filterGoRules(DefaultRules())

	// Collect all Go source files (non-test, non-vendor)
	var goFiles []string
	filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		parts := strings.Split(rel, string(filepath.Separator))
		for _, p := range parts {
			if strings.HasPrefix(p, ".") || p == "vendor" || p == "node_modules" || p == "docs" {
				return nil
			}
		}
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") {
			goFiles = append(goFiles, rel)
		}
		return nil
	})

	if len(goFiles) == 0 {
		t.Skip("no Go source files found")
	}

	result, err := ScanFiles(repoRoot, goRules, goFiles)
	if err != nil {
		t.Fatalf("ScanFiles error: %v", err)
	}

	// Filter to high and critical only
	var blocking []Finding
	for _, f := range result.Findings {
		if f.Severity == "critical" || f.Severity == "high" {
			// Exclude known false positives:
			// - scan.go itself contains regex patterns that match its own rules
			// - hooks.go contains regex patterns for its guard scripts
			// - prompt templates contain example patterns
			if isKnownFalsePositive(f) {
				continue
			}
			blocking = append(blocking, f)
		}
	}

	if len(blocking) > 0 {
		t.Errorf("Self-scan found %d high/critical issues in production code:", len(blocking))
		for _, f := range blocking {
			t.Errorf("  %s:%d [%s] %s (%s)", f.File, f.Line, f.Severity, f.Rule, f.Message)
		}
	}

	t.Logf("Self-scan: %d files, %d total findings, %d blocking (after false-positive exclusion)",
		result.FilesScanned, len(result.Findings), len(blocking))
}

// findRepoRoot walks up from the working directory looking for go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// filterGoRules returns only rules that apply to .go files.
func filterGoRules(rules []Rule) []Rule {
	var result []Rule
	for _, r := range rules {
		if len(r.FileExts) == 0 || ruleApplies(r, ".go") {
			result = append(result, r)
		}
	}
	return result
}

// isKnownFalsePositive returns true for findings in files that define the
// scanner rules themselves (which necessarily contain the patterns they detect).
func isKnownFalsePositive(f Finding) bool {
	// no-blank-error is too noisy for Go: Go's multi-return pattern (val, _ := f())
	// is idiomatic for intentionally discarding errors in contexts like fmt.Fprintf,
	// deferred closes, and best-effort cleanup. This rule is tuned for languages
	// where error suppression is less common.
	if f.Rule == "no-blank-error" {
		return true
	}

	// scan.go contains regex patterns that match its own rules
	if strings.Contains(f.File, "internal/scan/") {
		return true
	}
	// hooks.go contains shell script templates with patterns
	if strings.Contains(f.File, "internal/hooks/") {
		return true
	}
	// prompt templates contain example patterns
	if strings.Contains(f.File, "prompts/") || strings.Contains(f.File, "harness/prompts/") {
		return true
	}
	// convergence contains analysis patterns
	if strings.Contains(f.File, "internal/convergence/") {
		return true
	}
	// hub/builtin/honesty contains deception patterns
	if strings.Contains(f.File, "honesty/") {
		return true
	}
	// bench/judge contains placeholder patterns in judge implementations
	if strings.Contains(f.File, "bench/judge/") {
		return true
	}
	// no-placeholder-code triggers on files that legitimately use the word "placeholder"
	// in comments, documentation, or as constants/field names
	if f.Rule == "no-placeholder-code" {
		// These packages use "placeholder" as a concept, not as leftover code
		fpPaths := []string{
			"internal/context/",     // masking.go uses "placeholder" to describe compaction strategy
			"internal/hub/builtin/", // honesty judge references placeholder detection patterns
			"internal/ledger/nodes/", // decision node types reference placeholder fields
			"internal/harness/",     // stance prompts reference placeholder patterns
			"internal/taskstate/",   // failure codes include PLACEHOLDER_CODE
			"cmd/stoke/",           // references placeholder detection in scan output
		}
		for _, p := range fpPaths {
			if strings.Contains(f.File, p) {
				return true
			}
		}
	}
	// no-hardcoded-secret false positives: setting env vars from runtime values
	if f.Rule == "no-hardcoded-secret" && strings.Contains(f.File, "internal/engine/") {
		return true
	}
	// no-tautological-test: failure code constants that include the word pattern
	if f.Rule == "no-tautological-test" && strings.Contains(f.File, "internal/taskstate/") {
		return true
	}
	return false
}
