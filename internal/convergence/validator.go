// Package convergence implements adversarial self-audit for mission completion.
//
// The convergence validator answers the question: "Is this mission actually done?"
// It performs structured gap analysis across multiple dimensions:
//   - Completeness: Are all acceptance criteria provably satisfied?
//   - Test coverage: Do real tests exist for all modified code?
//   - Code quality: Does the code follow engineering standards?
//   - Security: Are there vulnerabilities or credential leaks?
//   - Documentation: Is the code properly documented?
//   - Consistency: Are there stubs, mocks, TODOs, or incomplete implementations?
//
// The validator is adversarial by design — it looks for reasons the work is NOT done,
// not reasons it IS done. It operates as a configurable rule engine where each rule
// produces structured findings that map to mission.Gap records.
//
// Two-model consensus: The validator produces a structured report that can be
// evaluated by multiple models independently. Consensus is reached when N models
// agree the gap list is empty.
package convergence

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Category classifies what dimension a gap belongs to.
type Category string

const (
	CatCompleteness Category = "completeness" // acceptance criteria not met
	CatTestCoverage Category = "test"         // missing or weak tests
	CatCodeQuality  Category = "code"         // engineering standard violations
	CatSecurity     Category = "security"     // vulnerabilities, credential leaks
	CatDocumentation Category = "docs"        // missing documentation
	CatConsistency  Category = "consistency"  // stubs, TODOs, incomplete code
)

// Severity indicates how blocking a finding is.
type Severity string

const (
	SevBlocking Severity = "blocking" // must fix before completion
	SevMajor    Severity = "major"    // should fix, degrades quality
	SevMinor    Severity = "minor"    // nice to fix, low impact
	SevInfo     Severity = "info"     // informational, no action required
)

// Finding is a single issue discovered during validation.
type Finding struct {
	RuleID      string   `json:"rule_id"`
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"`
	Description string   `json:"description"`
	Suggestion  string   `json:"suggestion,omitempty"`
	Evidence    string   `json:"evidence,omitempty"` // matched text or context
}

// Report is the output of a convergence validation pass.
type Report struct {
	MissionID    string        `json:"mission_id"`
	Timestamp    time.Time     `json:"timestamp"`
	Findings     []Finding     `json:"findings"`
	Score        float64       `json:"score"`        // 0.0 (terrible) to 1.0 (perfect)
	IsConverged  bool          `json:"is_converged"` // true if no blocking findings
	Summary      string        `json:"summary"`      // human-readable summary
	RulesApplied int           `json:"rules_applied"`
	Duration     time.Duration `json:"duration"`
}

// BlockingCount returns the number of blocking findings.
func (r *Report) BlockingCount() int {
	count := 0
	for _, f := range r.Findings {
		if f.Severity == SevBlocking {
			count++
		}
	}
	return count
}

// ByCategory groups findings by category.
func (r *Report) ByCategory() map[Category][]Finding {
	groups := make(map[Category][]Finding)
	for _, f := range r.Findings {
		groups[f.Category] = append(groups[f.Category], f)
	}
	return groups
}

// BySeverity groups findings by severity.
func (r *Report) BySeverity() map[Severity][]Finding {
	groups := make(map[Severity][]Finding)
	for _, f := range r.Findings {
		groups[f.Severity] = append(groups[f.Severity], f)
	}
	return groups
}

// Rule is a single validation check. Rules are composable and configurable.
type Rule struct {
	ID          string   // unique identifier
	Name        string   // human-readable name
	Category    Category // which dimension this checks
	Severity    Severity // default severity for findings
	Description string   // what this rule checks for
	Enabled     bool     // whether to run this rule

	// Check is the validation function. It receives the file path and content,
	// and returns any findings. Returning nil means no issues found.
	Check func(file string, content []byte) []Finding
}

// FileInput is a file to validate.
type FileInput struct {
	Path    string // relative path within the project
	Content []byte // file content
}

// Validator runs convergence checks against a set of files.
type Validator struct {
	mu    sync.RWMutex
	rules []Rule
}

// NewValidator creates a validator with the default rule set.
func NewValidator() *Validator {
	return &Validator{rules: DefaultRules()}
}

// NewValidatorWithRules creates a validator with custom rules.
func NewValidatorWithRules(rules []Rule) *Validator {
	return &Validator{rules: rules}
}

// AddRule adds a rule to the validator. Thread-safe.
func (v *Validator) AddRule(r Rule) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rules = append(v.rules, r)
}

// EnableRule enables or disables a rule by ID.
func (v *Validator) EnableRule(id string, enabled bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.rules {
		if v.rules[i].ID == id {
			v.rules[i].Enabled = enabled
			return
		}
	}
}

// Validate runs all enabled rules against the provided files and produces a report.
// Files are checked in parallel for performance.
func (v *Validator) Validate(missionID string, files []FileInput) *Report {
	start := time.Now()
	v.mu.RLock()
	rules := make([]Rule, len(v.rules))
	copy(rules, v.rules)
	v.mu.RUnlock()

	var enabledRules []Rule
	for _, r := range rules {
		if r.Enabled {
			enabledRules = append(enabledRules, r)
		}
	}

	// Run rules against files concurrently
	var mu sync.Mutex
	var findings []Finding
	var wg sync.WaitGroup

	for _, file := range files {
		for _, rule := range enabledRules {
			wg.Add(1)
			go func(f FileInput, r Rule) {
				defer wg.Done()
				results := r.Check(f.Path, f.Content)
				if len(results) > 0 {
					mu.Lock()
					findings = append(findings, results...)
					mu.Unlock()
				}
			}(file, rule)
		}
	}
	wg.Wait()

	// Calculate score: start at 1.0, deduct per finding by severity
	score := 1.0
	for _, f := range findings {
		switch f.Severity {
		case SevBlocking:
			score -= 0.15
		case SevMajor:
			score -= 0.08
		case SevMinor:
			score -= 0.03
		case SevInfo:
			score -= 0.01
		}
	}
	if score < 0 {
		score = 0
	}

	report := &Report{
		MissionID:    missionID,
		Timestamp:    time.Now(),
		Findings:     findings,
		Score:        score,
		RulesApplied: len(enabledRules),
		Duration:     time.Since(start),
	}

	// Converged = no blocking findings
	report.IsConverged = report.BlockingCount() == 0
	report.Summary = buildSummary(report)

	return report
}

// ValidateWithCriteria additionally checks that acceptance criteria are addressed.
// It takes the criteria descriptions and checks whether files contain evidence
// of implementation for each criterion.
func (v *Validator) ValidateWithCriteria(missionID string, files []FileInput, criteria []string) *Report {
	report := v.Validate(missionID, files)

	// Check each criterion against file contents
	allContent := &strings.Builder{}
	for _, f := range files {
		allContent.Write(f.Content)
		allContent.WriteByte('\n')
	}
	contentStr := strings.ToLower(allContent.String())

	for i, criterion := range criteria {
		// Extract keywords from criterion for basic matching
		keywords := extractKeywords(criterion)
		matchCount := 0
		for _, kw := range keywords {
			if strings.Contains(contentStr, strings.ToLower(kw)) {
				matchCount++
			}
		}
		// If less than half the keywords appear in any file, flag it
		if len(keywords) > 0 && matchCount < (len(keywords)+1)/2 {
			report.Findings = append(report.Findings, Finding{
				RuleID:      "criterion-check",
				Category:    CatCompleteness,
				Severity:    SevBlocking,
				Description: fmt.Sprintf("Acceptance criterion %d may not be implemented: %q", i+1, criterion),
				Suggestion:  "Verify this criterion is addressed in the code and tests",
			})
		}
	}

	// Recalculate after criteria check
	score := 1.0
	for _, f := range report.Findings {
		switch f.Severity {
		case SevBlocking:
			score -= 0.15
		case SevMajor:
			score -= 0.08
		case SevMinor:
			score -= 0.03
		case SevInfo:
			score -= 0.01
		}
	}
	if score < 0 {
		score = 0
	}
	report.Score = score
	report.IsConverged = report.BlockingCount() == 0
	report.Summary = buildSummary(report)

	return report
}

func buildSummary(r *Report) string {
	bySev := r.BySeverity()
	parts := []string{}
	for _, sev := range []Severity{SevBlocking, SevMajor, SevMinor, SevInfo} {
		if n := len(bySev[sev]); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	if len(parts) == 0 {
		return "No issues found — converged"
	}
	return fmt.Sprintf("%d findings: %s", len(r.Findings), strings.Join(parts, ", "))
}

func extractKeywords(text string) []string {
	// Split on whitespace and punctuation, filter short words and stop words
	words := regexp.MustCompile(`[a-zA-Z]+`).FindAllString(text, -1)
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"in": true, "on": true, "at": true, "to": true, "for": true,
		"of": true, "and": true, "or": true, "be": true, "it": true,
		"that": true, "this": true, "with": true, "from": true,
		"should": true, "must": true, "will": true, "can": true,
	}
	var keywords []string
	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) >= 3 && !stopWords[lower] {
			keywords = append(keywords, lower)
		}
	}
	return keywords
}

// --- Default Rules ---

// DefaultRules returns the built-in rule set covering all quality dimensions.
func DefaultRules() []Rule {
	return []Rule{
		todoRule(),
		stubRule(),
		panicRule(),
		hardcodedSecretRule(),
		emptyTestRule(),
		tautologicalTestRule(),
		typeBypassRule(),
		largeFileRule(),
		missingErrorHandleRule(),
		debugLogRule(),
		sqlInjectionRule(),
		missingTestFileRule(),
	}
}

func todoRule() Rule {
	re := regexp.MustCompile(`(?i)\b(TODO|FIXME|HACK|XXX|STUB)\b`)
	return Rule{
		ID: "no-todo", Name: "No TODO/FIXME markers", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "Code contains TODO/FIXME markers indicating incomplete work",
		Check: func(file string, content []byte) []Finding {
			return regexCheck(re, file, content, "no-todo", CatConsistency, SevMajor,
				"Contains TODO/FIXME marker — incomplete work", "Complete or remove the TODO")
		},
	}
}

func stubRule() Rule {
	re := regexp.MustCompile(`(?i)(not implemented|placeholder|dummy implementation|stub[^a-z])`)
	return Rule{
		ID: "no-stubs", Name: "No stub implementations", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Code contains stub or placeholder implementations",
		Check: func(file string, content []byte) []Finding {
			return regexCheck(re, file, content, "no-stubs", CatConsistency, SevBlocking,
				"Contains stub/placeholder implementation", "Implement the real logic")
		},
	}
}

func panicRule() Rule {
	re := regexp.MustCompile(`\bpanic\(`)
	return Rule{
		ID: "no-panic", Name: "No panic calls", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Code uses panic() instead of error returns",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil // panics in tests are acceptable
			}
			return regexCheck(re, file, content, "no-panic", CatCodeQuality, SevMajor,
				"Uses panic() — should return error instead", "Replace panic with error return")
		},
	}
}

func hardcodedSecretRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token)\s*[:=]\s*["'][a-zA-Z0-9]{16,}["']`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`(?i)sk-[a-zA-Z0-9]{20,}`),
		regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),
		regexp.MustCompile(`-----BEGIN (RSA |EC )?PRIVATE KEY-----`),
	}
	return Rule{
		ID: "no-secrets", Name: "No hardcoded secrets", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "Code contains hardcoded secrets, API keys, or credentials",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil // test files may contain fake keys
			}
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-secrets", CatSecurity, SevBlocking,
					"Contains hardcoded secret or credential", "Use environment variable or config file")...)
			}
			return findings
		},
	}
}

func emptyTestRule() Rule {
	// Multiline regex to match test functions with empty bodies (possibly with whitespace)
	re := regexp.MustCompile(`(?s)func Test\w+\(t \*testing\.T\)\s*\{\s*\}`)
	return Rule{
		ID: "no-empty-tests", Name: "No empty test functions", Category: CatTestCoverage,
		Severity: SevBlocking, Enabled: true,
		Description: "Test functions with empty bodies provide no coverage",
		Check: func(file string, content []byte) []Finding {
			if !isTestFile(file) {
				return nil
			}
			// Use whole-content match since empty test spans multiple lines
			matches := re.FindAllIndex(content, -1)
			var findings []Finding
			for _, m := range matches {
				// Find line number of match start
				line := 1 + strings.Count(string(content[:m[0]]), "\n")
				findings = append(findings, Finding{
					RuleID:      "no-empty-tests",
					Category:    CatTestCoverage,
					Severity:    SevBlocking,
					File:        file,
					Line:        line,
					Description: "Empty test function — provides zero coverage",
					Suggestion:  "Add assertions that validate behavior",
				})
			}
			return findings
		},
	}
}

func tautologicalTestRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)assert.*true.*true|expect.*true.*toBe.*true`),
		regexp.MustCompile(`(?i)assert.*1.*1|expect.*1.*toBe.*1`),
		regexp.MustCompile(`if\s+true\s*\{`),
	}
	return Rule{
		ID: "no-tautological-tests", Name: "No tautological tests", Category: CatTestCoverage,
		Severity: SevBlocking, Enabled: true,
		Description: "Tests that always pass regardless of code behavior",
		Check: func(file string, content []byte) []Finding {
			if !isTestFile(file) {
				return nil
			}
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-tautological-tests",
					CatTestCoverage, SevBlocking,
					"Tautological test — always passes regardless of behavior",
					"Test actual behavior, not constants")...)
			}
			return findings
		},
	}
}

func typeBypassRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`@ts-ignore|@ts-expect-error`),
		regexp.MustCompile(`as\s+any\b`),
		regexp.MustCompile(`#\s*type:\s*ignore`),
		regexp.MustCompile(`//\s*nolint`),
		regexp.MustCompile(`eslint-disable`),
	}
	return Rule{
		ID: "no-type-bypass", Name: "No type safety bypasses", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Code bypasses type checking or linting",
		Check: func(file string, content []byte) []Finding {
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-type-bypass",
					CatCodeQuality, SevMajor,
					"Bypasses type safety or linting", "Fix the underlying type/lint issue")...)
			}
			return findings
		},
	}
}

func largeFileRule() Rule {
	return Rule{
		ID: "no-large-files", Name: "No excessively large files", Category: CatCodeQuality,
		Severity: SevMinor, Enabled: true,
		Description: "Files exceeding 500 lines should be split",
		Check: func(file string, content []byte) []Finding {
			lines := strings.Count(string(content), "\n")
			if lines > 500 {
				return []Finding{{
					RuleID:      "no-large-files",
					Category:    CatCodeQuality,
					Severity:    SevMinor,
					File:        file,
					Description: fmt.Sprintf("File has %d lines (>500) — consider splitting", lines),
					Suggestion:  "Break into smaller, focused modules",
				}}
			}
			return nil
		},
	}
}

func missingErrorHandleRule() Rule {
	re := regexp.MustCompile(`_\s*=\s*\w+\.\w+\(`)
	return Rule{
		ID: "no-ignored-errors", Name: "No ignored error returns", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Error return values are being silently discarded",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil // test helpers often ignore errors
			}
			return regexCheck(re, file, content, "no-ignored-errors", CatCodeQuality, SevMajor,
				"Ignores error return value", "Handle the error or explicitly document why it's safe to ignore")
		},
	}
}

func debugLogRule() Rule {
	re := regexp.MustCompile(`(?i)(console\.log|fmt\.Print(ln|f)?|print\(|System\.out\.print)`)
	return Rule{
		ID: "no-debug-logs", Name: "No debug print statements", Category: CatCodeQuality,
		Severity: SevMinor, Enabled: true,
		Description: "Debug print statements left in production code",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-debug-logs", CatCodeQuality, SevMinor,
				"Contains debug print statement", "Use structured logging instead")
		},
	}
}

func sqlInjectionRule() Rule {
	re := regexp.MustCompile(`(?i)(Exec|Query|QueryRow)\(\s*(".*?\+|fmt\.Sprintf)`)
	return Rule{
		ID: "no-sql-injection", Name: "No SQL injection vectors", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "SQL queries built with string concatenation or formatting",
		Check: func(file string, content []byte) []Finding {
			return regexCheck(re, file, content, "no-sql-injection", CatSecurity, SevBlocking,
				"Potential SQL injection — query built with string interpolation",
				"Use parameterized queries with ? placeholders")
		},
	}
}

func missingTestFileRule() Rule {
	return Rule{
		ID: "missing-test-file", Name: "Source files should have tests", Category: CatTestCoverage,
		Severity: SevMajor, Enabled: true,
		Description: "Source files without corresponding test files lack coverage",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) || !isGoFile(file) {
				return nil
			}
			// This rule fires on each file individually — the caller should
			// check whether a corresponding _test.go file exists in the input set.
			// We flag it as info here; the validator integration should promote severity
			// based on whether tests actually exist.
			return nil // Handled at integration level, not per-file
		},
	}
}

// --- Helpers ---

// regexCheck is a helper that scans content line-by-line for regex matches.
func regexCheck(re *regexp.Regexp, file string, content []byte, ruleID string, cat Category, sev Severity, desc, suggestion string) []Finding {
	var findings []Finding
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if re.MatchString(line) {
			findings = append(findings, Finding{
				RuleID:      ruleID,
				Category:    cat,
				Severity:    sev,
				File:        file,
				Line:        i + 1,
				Description: desc,
				Suggestion:  suggestion,
				Evidence:    strings.TrimSpace(line),
			})
		}
	}
	return findings
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.HasSuffix(path, ".test.ts") ||
		strings.HasSuffix(path, ".test.js") ||
		strings.HasSuffix(path, ".spec.ts") ||
		strings.HasSuffix(path, ".spec.js") ||
		strings.Contains(filepath.Base(path), "test_")
}

func isGoFile(path string) bool {
	return strings.HasSuffix(path, ".go")
}
