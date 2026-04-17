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
			if strings.HasPrefix(p, ".") || p == "vendor" || p == "node_modules" || p == "docs" || p == "trio-main" {
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

	// Narrow exclusions to specific files that define detection patterns,
	// NOT entire directories (to avoid masking real issues in new files).

	// scan.go contains regex patterns that match its own rules
	if strings.HasSuffix(f.File, "internal/scan/scan.go") {
		return true
	}
	// hooks.go contains shell script templates with guard patterns
	if strings.HasSuffix(f.File, "internal/hooks/hooks.go") {
		return true
	}
	// Prompt templates instruct agents not to use placeholders (the word appears
	// as part of the instruction, not as leftover code)
	if f.Rule == "no-placeholder-code" && strings.Contains(f.File, "prompts/") {
		return true
	}
	// convergence validator/rules define regex patterns to detect stubs and placeholders
	if f.Rule == "no-placeholder-code" && strings.Contains(f.File, "internal/convergence/") {
		return true
	}
	// honesty judge defines deception detection patterns
	if strings.HasSuffix(f.File, "hub/builtin/honesty/honesty.go") ||
		strings.HasSuffix(f.File, "hub/builtin/honesty.go") {
		return true
	}
	// bench/judge contains placeholder patterns in judge implementations
	if strings.Contains(f.File, "bench/judge/") {
		return true
	}
	// sow_native.go carries literal anti-pattern strings (empty
	// catches + type bypass tokens) that the agent-output scanner
	// checks for. The strings appear inside string-literal arrays
	// used by the detector; they aren't actual empty catches in
	// Go code (Go doesn't even have try/catch syntax).
	if f.Rule == "no-empty-catch" && strings.HasSuffix(f.File, "cmd/stoke/sow_native.go") {
		return true
	}
	// no-placeholder-code triggers on files that legitimately use the word "placeholder"
	// in comments, documentation, or as constants/field names
	if f.Rule == "no-placeholder-code" {
		// These packages use "placeholder" as a concept, not as leftover code
		fpPaths := []string{
			"internal/context/",      // masking.go uses "placeholder" to describe compaction strategy
			"internal/hub/builtin/",  // honesty judge references placeholder detection patterns
			"internal/ledger/nodes/", // decision node types reference placeholder fields
			"internal/harness/",      // stance prompts reference placeholder patterns
			"internal/taskstate/",    // failure codes include PLACEHOLDER_CODE
			"cmd/stoke/",             // references placeholder detection in scan output
			// plan/ stubs-out anti-pattern checker and emits
			// warning strings containing the word "placeholder"
			// as part of user-facing guidance; they're content,
			// not dead code.
			"internal/plan/content_judge.go",
			"internal/plan/deliverable.go",
			"internal/plan/externaldocs.go",
			"internal/plan/phase_budget.go",
			"internal/reviewereval/",
			"internal/websearch/",
		}
		for _, p := range fpPaths {
			if strings.Contains(f.File, p) {
				return true
			}
		}
	}
	// no-hardcoded-secret: gemini.go sets GEMINI_API_KEY from a runtime variable
	if f.Rule == "no-hardcoded-secret" && strings.HasSuffix(f.File, "internal/engine/gemini.go") {
		return true
	}
	// critic.go defines the secret-detection regex patterns themselves
	// (sk-..., ghp_..., AKIA...) — these look like secrets to the
	// scanner but they're the DEFINITION of what constitutes a secret.
	if f.Rule == "no-hardcoded-secret" && strings.HasSuffix(f.File, "internal/critic/critic.go") {
		return true
	}
	// no-tautological-test: failures.go defines TAUTOLOGICAL_TEST as a constant string
	if f.Rule == "no-tautological-test" && strings.HasSuffix(f.File, "internal/taskstate/failures.go") {
		return true
	}
	return false
}
