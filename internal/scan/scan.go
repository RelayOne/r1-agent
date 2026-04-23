package scan

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one issue detected by the scan pipeline.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // critical, high, medium, low, info
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// ScanResult is the output of a full scan.
type ScanResult struct {
	FilesScanned int       `json:"files_scanned"`
	Findings     []Finding `json:"findings"`
}

// Rule defines a pattern to scan for.
type Rule struct {
	ID          string
	Severity    string
	Pattern     *regexp.Regexp
	Message     string
	Fix         string
	FileExts    []string       // only apply to these extensions (empty = all)
	PathPattern *regexp.Regexp // if set, only apply to files whose relative path matches this regex
}

// DefaultRules returns the built-in scan rules.
func DefaultRules() []Rule {
	return []Rule{
		// Type/lint bypasses
		{ID: "no-ts-ignore", Severity: "critical", Pattern: regexp.MustCompile(`@ts-ignore`), Message: "@ts-ignore bypasses type checking", Fix: "Fix the underlying type error", FileExts: []string{".ts", ".tsx"}},
		{ID: "no-ts-nocheck", Severity: "critical", Pattern: regexp.MustCompile(`@ts-nocheck`), Message: "@ts-nocheck disables type checking for entire file", Fix: "Fix type errors individually", FileExts: []string{".ts", ".tsx"}},
		{ID: "no-as-any", Severity: "high", Pattern: regexp.MustCompile(`as\s+any`), Message: "'as any' assertion bypasses type safety", Fix: "Use a proper type", FileExts: []string{".ts", ".tsx"}},
		{ID: "no-eslint-disable", Severity: "high", Pattern: regexp.MustCompile(`eslint-disable`), Message: "eslint-disable suppresses lint rules", Fix: "Fix the lint issue", FileExts: []string{".ts", ".tsx", ".js", ".jsx"}},
		{ID: "no-noqa", Severity: "high", Pattern: regexp.MustCompile(`#\s*noqa`), Message: "noqa suppresses linter warnings", Fix: "Fix the linter issue", FileExts: []string{".py"}},
		{ID: "no-type-ignore", Severity: "high", Pattern: regexp.MustCompile(`#\s*type:\s*ignore`), Message: "type: ignore suppresses type checker", Fix: "Fix the type error", FileExts: []string{".py"}},
		{ID: "no-clippy-allow", Severity: "high", Pattern: regexp.MustCompile(`#\[allow\(clippy::`), Message: "allow(clippy::) suppresses Rust lints", Fix: "Fix the clippy warning", FileExts: []string{".rs"}},
		{ID: "no-nolint", Severity: "high", Pattern: regexp.MustCompile(`//\s*nolint`), Message: "nolint suppresses Go linter", Fix: "Fix the linter issue", FileExts: []string{".go"}},

		// Debug artifacts
		{ID: "no-console-log", Severity: "medium", Pattern: regexp.MustCompile(`console\.log\(`), Message: "console.log left in production code", Fix: "Remove or replace with proper logging", FileExts: []string{".ts", ".tsx", ".js", ".jsx"}},
		{ID: "no-fmt-println", Severity: "medium", Pattern: regexp.MustCompile(`fmt\.Println\(`), Message: "fmt.Println left in code", Fix: "Use structured logging", FileExts: []string{".go"}},
		{ID: "no-print-fn", Severity: "medium", Pattern: regexp.MustCompile(`(?m)^\s*print\(`), Message: "print() left in code", Fix: "Use proper logging", FileExts: []string{".py"}},
		{ID: "no-dbg-macro", Severity: "medium", Pattern: regexp.MustCompile(`dbg!\(`), Message: "dbg! macro left in code", Fix: "Remove debug macro", FileExts: []string{".rs"}},

		// Test artifacts
		{ID: "no-test-only", Severity: "critical", Pattern: regexp.MustCompile(`\.(only|skip)\(`), Message: ".only() or .skip() left on test", Fix: "Remove .only()/.skip() so all tests run", FileExts: []string{".ts", ".tsx", ".js", ".jsx"}},
		{ID: "no-todo-fixme", Severity: "low", Pattern: regexp.MustCompile(`(?i)(TODO|FIXME|HACK|XXX):`), Message: "TODO/FIXME marker", FileExts: []string{}},

		// Security
		{ID: "no-hardcoded-secret", Severity: "critical", Pattern: regexp.MustCompile(`(?i)(password|secret|api_key|apikey|token)\s*[:=]+\s*["'][^"']{8,}`), Message: "Possible hardcoded secret", Fix: "Use environment variables or a secrets manager"},
		{ID: "no-eval", Severity: "high", Pattern: regexp.MustCompile(`\beval\(`), Message: "eval() is a code injection risk", Fix: "Use a safe alternative", FileExts: []string{".ts", ".tsx", ".js", ".jsx", ".py"}},
		{ID: "no-innerhtml", Severity: "high", Pattern: regexp.MustCompile(`innerHTML\s*=`), Message: "innerHTML assignment is an XSS risk", Fix: "Use textContent or a sanitizer", FileExts: []string{".ts", ".tsx", ".js", ".jsx"}},
		{ID: "no-exec", Severity: "high", Pattern: regexp.MustCompile(`exec\(|os\.system\(`), Message: "Shell execution is a command injection risk", Fix: "Use parameterized commands", FileExts: []string{".py"}},

		// Deception patterns (anti-laziness)
		{ID: "no-empty-catch", Severity: "high", Pattern: regexp.MustCompile(`catch\s*\([^)]*\)\s*\{\s*\}`), Message: "Empty catch block swallows errors", Fix: "Handle or rethrow the error", FileExts: []string{".ts", ".tsx", ".js", ".jsx", ".go"}},
		{ID: "no-tautological-test", Severity: "critical", Pattern: regexp.MustCompile(`expect\(true\)\.toBe\(true\)|assert\.ok\(true\)|assert\.Equal\(t,\s*true,\s*true\)|assert 1 == 1`), Message: "Tautological test always passes", Fix: "Assert real behavior", FileExts: []string{".ts", ".tsx", ".js", ".jsx", ".py", ".go"}},
		{ID: "no-weak-assertion", Severity: "high", Pattern: regexp.MustCompile(`toBeTruthy\(\)|toBeFalsy\(\)|toBeDefined\(\)|toHaveBeenCalled\(\)`), Message: "Weak assertion hides correctness failures", Fix: "Assert exact values or arguments", FileExts: []string{".ts", ".tsx", ".js", ".jsx"}},
		{ID: "no-test-todo", Severity: "high", Pattern: regexp.MustCompile(`test\.todo\(|it\.todo\(`), Message: "Unfinished test placeholder", Fix: "Implement or remove placeholder", FileExts: []string{".ts", ".tsx", ".js", ".jsx"}},
		{ID: "no-placeholder-code", Severity: "high", Pattern: regexp.MustCompile(`NotImplementedError|CHANGEME|placeholder|pass\s*#\s*TODO`), Message: "Placeholder code left in implementation", Fix: "Complete the implementation", FileExts: []string{".ts", ".tsx", ".js", ".jsx", ".py", ".go", ".rs"}},

		// Frontend UX quality
		{ID: "no-inline-style-color", Severity: "medium", Pattern: regexp.MustCompile(`style\s*=\s*\{?\{[^}]*(color|background)[^}]*#[0-9a-fA-F]`), Message: "Hardcoded color in inline style breaks theming", Fix: "Use CSS variables or design tokens", FileExts: []string{".tsx", ".jsx"}},
		{ID: "no-outline-none", Severity: "high", Pattern: regexp.MustCompile(`outline\s*:\s*(none|0)\s*[;}]`), Message: "Focus outline removed — keyboard users lose navigation", Fix: "Use :focus-visible with a visible custom indicator", FileExts: []string{".css", ".scss", ".less", ".tsx", ".jsx"}},
		{ID: "no-dangerous-html", Severity: "critical", Pattern: regexp.MustCompile(`dangerouslySetInnerHTML`), Message: "dangerouslySetInnerHTML is an XSS vector", Fix: "Use DOMPurify or render content with React components", FileExts: []string{".tsx", ".jsx"}},
		{ID: "no-div-onclick", Severity: "high", Pattern: regexp.MustCompile(`<(div|span)\s[^>]*onClick[^>]*>`), Message: "onClick on non-semantic element — inaccessible to keyboard", Fix: "Use <button> or add role=\"button\" tabIndex={0}", FileExts: []string{".tsx", ".jsx"}},

		// PII / compliance
		{ID: "no-pii-logging", Severity: "high", Pattern: regexp.MustCompile(`(?i)(log|logger|logging)\.\w+\([^)]*\b(email|ssn|social.security|phone.number|credit.card|password)\b`), Message: "Possible PII in log statement — email, SSN, phone, or password", Fix: "Redact PII before logging or use a PII-safe logger"},
		{ID: "no-time-now-naive", Severity: "medium", Pattern: regexp.MustCompile(`time\.Now\(\)\s*\.\s*Format`), Message: "time.Now().Format() without timezone — may produce inconsistent timestamps", Fix: "Use time.Now().UTC() or explicitly set timezone", FileExts: []string{".go"}},

		// AI deception: silently removed error handling
		{ID: "no-blank-error", Severity: "critical", Pattern: regexp.MustCompile(`\w+,\s*_\s*:?=\s*\w+\.\w+\(`), Message: "Error return assigned to blank identifier — error silently discarded", Fix: "Handle the error: check and return or log it"},

		// CI-config safety: MCP gate bypass must never land in shared infrastructure
		{ID: "env_mcp_ungated", Severity: "critical", Pattern: regexp.MustCompile(`STOKE_MCP_UNGATED=1`), Message: "STOKE_MCP_UNGATED=1 in CI config disables MCP gating — do not use in shared infrastructure", Fix: "Remove STOKE_MCP_UNGATED=1 or confine it to ephemeral developer envs", PathPattern: regexp.MustCompile(`^(\.github/|scripts/ci/)`)},
	}
}

// ScanFiles scans files in a directory against the rules.
func ScanFiles(dir string, rules []Rule, modifiedOnly []string) (*ScanResult, error) {
	result := &ScanResult{}

	// Build a set of files to scan
	filesToScan := modifiedOnly
	if len(filesToScan) == 0 {
		// Scan all source files
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() { return nil }
			rel, _ := filepath.Rel(dir, path)
			if shouldScan(rel) || pathPatternMatches(rel, rules) {
				filesToScan = append(filesToScan, rel)
			}
			return nil
		})
	}

	for _, relPath := range filesToScan {
		fullPath := filepath.Join(dir, relPath)
		findings, err := scanFile(fullPath, relPath, rules)
		if err != nil { continue }
		result.Findings = append(result.Findings, findings...)
		result.FilesScanned++
	}

	return result, nil
}

func scanFile(fullPath, relPath string, rules []Rule) ([]Finding, error) {
	f, err := os.Open(fullPath)
	if err != nil { return nil, err }
	defer f.Close()

	ext := filepath.Ext(relPath)
	var findings []Finding

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, rule := range rules {
			if !ruleApplies(rule, ext) { continue }
			if rule.PathPattern != nil && !rule.PathPattern.MatchString(filepath.ToSlash(relPath)) { continue }
			if rule.Pattern.MatchString(line) {
				findings = append(findings, Finding{
					Rule: rule.ID, Severity: rule.Severity,
					File: relPath, Line: lineNum,
					Message: rule.Message, Fix: rule.Fix,
				})
			}
		}
	}
	return findings, scanner.Err()
}

// pathPatternMatches returns true if any rule's PathPattern matches the given relative path.
// Used to include files in a full-directory walk even if the default extension/hidden-dir
// filter would normally skip them (e.g. .github/workflows/*.yml).
func pathPatternMatches(relPath string, rules []Rule) bool {
	slash := filepath.ToSlash(relPath)
	for _, r := range rules {
		if r.PathPattern != nil && r.PathPattern.MatchString(slash) {
			return true
		}
	}
	return false
}

func ruleApplies(rule Rule, ext string) bool {
	if len(rule.FileExts) == 0 { return true }
	for _, e := range rule.FileExts {
		if e == ext { return true }
	}
	return false
}

func shouldScan(relPath string) bool {
	// Skip hidden dirs, node_modules, vendor, build artifacts
	parts := strings.Split(relPath, string(filepath.Separator))
	for _, p := range parts {
		if strings.HasPrefix(p, ".") || p == "node_modules" || p == "vendor" || p == "dist" || p == "build" || p == "__pycache__" || p == "target" {
			return false
		}
	}
	ext := filepath.Ext(relPath)
	scannable := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".py": true, ".rs": true, ".rb": true, ".java": true, ".kt": true,
		".css": true, ".scss": true, ".less": true, ".html": true, ".vue": true, ".svelte": true,
	}
	return scannable[ext]
}

// Summary returns a human-readable summary.
func (r *ScanResult) Summary() string {
	if len(r.Findings) == 0 {
		return fmt.Sprintf("Scanned %d files: no issues found", r.FilesScanned)
	}
	counts := map[string]int{}
	for _, f := range r.Findings { counts[f.Severity]++ }
	return fmt.Sprintf("Scanned %d files: %d critical, %d high, %d medium, %d low",
		r.FilesScanned, counts["critical"], counts["high"], counts["medium"], counts["low"])
}

// HasBlocking returns true if any critical or high findings exist.
func (r *ScanResult) HasBlocking() bool {
	for _, f := range r.Findings {
		if f.Severity == "critical" || f.Severity == "high" { return true }
	}
	return false
}
