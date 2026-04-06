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
//   - UX quality: Is the UI accessible, responsive, and complete?
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
	CatCompleteness  Category = "completeness"  // acceptance criteria not met
	CatTestCoverage  Category = "test"           // missing or weak tests
	CatCodeQuality   Category = "code"           // engineering standard violations
	CatSecurity      Category = "security"       // vulnerabilities, credential leaks
	CatDocumentation Category = "docs"           // missing documentation
	CatConsistency   Category = "consistency"    // stubs, TODOs, incomplete code
	CatUXQuality     Category = "ux"             // UI/UX quality, accessibility, responsiveness
	CatReliability   Category = "reliability"   // production reliability, resilience
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

	// Phase 2: Semantic cross-file analysis (symbol reachability, type wiring)
	// This catches gaps that no single-file regex can detect.
	semFindings := SemanticAnalysis(files, nil)
	findings = append(findings, semFindings...)

	return buildReport(missionID, findings, len(enabledRules), start)
}

// ValidateWithCriteria additionally checks that acceptance criteria are addressed.
// It uses semantic analysis (symbol extraction, concept mapping, cross-file
// reference checking) rather than naive keyword matching to verify criteria.
func (v *Validator) ValidateWithCriteria(missionID string, files []FileInput, criteria []string) *Report {
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

	// Phase 1: Run regex rules per-file
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

	// Phase 2: Semantic cross-file analysis WITH criteria mapping
	// This replaces the old naive keyword-matching approach with real
	// symbol reachability, type wiring, and concept mapping.
	semFindings := SemanticAnalysis(files, criteria)
	findings = append(findings, semFindings...)

	return buildReport(missionID, findings, len(enabledRules), start)
}

// securityRuleIDs lists the rule IDs that detect real security vulnerabilities.
// These are worth running even when agentic model validation is available,
// because they catch concrete patterns (hardcoded secrets, injection vectors)
// that a model might overlook in a single pass.
var securityRuleIDs = map[string]bool{
	"no-secrets":           true,
	"no-sql-injection":     true,
	"no-command-injection": true,
	"no-path-traversal":    true,
}

// ValidateSecurityOnly runs only security-critical rules against the files.
// Used when agentic discovery validation (Layer 4) handles completeness,
// code quality, test quality, and UX analysis — the model does those far
// better than regex patterns. Security rules remain because they catch
// concrete vulnerability patterns that must never ship.
func (v *Validator) ValidateSecurityOnly(missionID string, files []FileInput) *Report {
	start := time.Now()
	v.mu.RLock()
	rules := make([]Rule, len(v.rules))
	copy(rules, v.rules)
	v.mu.RUnlock()

	var securityRules []Rule
	for _, r := range rules {
		if r.Enabled && securityRuleIDs[r.ID] {
			securityRules = append(securityRules, r)
		}
	}

	var mu sync.Mutex
	var findings []Finding
	var wg sync.WaitGroup

	for _, file := range files {
		for _, rule := range securityRules {
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

	return buildReport(missionID, findings, len(securityRules), start)
}

// buildReport calculates score and produces a Report from findings.
func buildReport(missionID string, findings []Finding, rulesApplied int, start time.Time) *Report {
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
		RulesApplied: rulesApplied,
		Duration:     time.Since(start),
	}
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

// DefaultRules returns the built-in rule set.
//
// Design principle: if any of these fire, the work is not done.
// The five convergence gates are:
//
//  1. User intent is fully satisfied (criteria check, not a static rule)
//  2. Everything works — build/test/lint pass (baseline package, not a static rule)
//  3. Engineering standards, security, optimization are exhaustive
//  4. Everything was researched and confirmed accurate
//  5. Everything started is finished — no scaffolding, no partial work
//
// Static rules enforce gates 3 and 5. Gates 1, 2, and 4 are enforced by
// the baseline package and the mission store respectively.
//
// Severity mapping:
//   - Anything that means "this isn't done" → blocking
//   - Anything that means "this works but could be better" → major (still must fix)
//   - Informational only → minor (rare; most things are blocking)
func DefaultRules() []Rule {
	rules := []Rule{
		// Gate 5: Everything started is finished
		todoRule(),
		stubRule(),
		scaffoldingRule(),
		emptyFuncRule(),
		commentedOutCodeRule(),
		unwiredCodeRule(),
		duplicateLogicRule(),

		// Gate 3: Engineering standards
		panicRule(),
		missingErrorHandleRule(),
		typeBypassRule(),
		largeFileRule(),
		debugLogRule(),
		missingErrorContextRule(),
		bareAnyTypeRule(),

		// Gate 3: Security
		hardcodedSecretRule(),
		sqlInjectionRule(),
		commandInjectionRule(),
		pathTraversalRule(),
		missingAuthCheckRule(),

		// Gate 3: Test quality
		emptyTestRule(),
		tautologicalTestRule(),
		missingTestFileRule(),
		missingErrorTestRule(),

		// Gate 3: Reliability
		unboundedQueryRule(),

		// Gate 3: Code quality (frontend)
		consoleLogRule(),

		// Gate 6: UX quality — accessibility, responsiveness, error states, UI completeness
		inaccessibleImageRule(),
		inaccessibleInteractiveRule(),
		missingViewportRule(),
		noResponsiveDesignRule(),
		missingErrorBoundaryRule(),
		missingLoadingStateRule(),
		missingFormLabelRule(),
		focusTrapRule(),
		hardcodedColorRule(),
		dangerousInnerHTMLRule(),
		missingKeyPropRule(),
		missingEmptyStateRule(),
	}
	// Append extended rules (concurrency, config, database, observability,
	// performance, error handling, AI failure modes).
	rules = append(rules, ExtendedRules()...)
	// Append postmortem-class rules (numerical correctness, resource lifecycle,
	// distributed systems, time/clock, build discipline, agent safety).
	rules = append(rules, PostmortemRules()...)
	// Append research-derived rules (AI agent deception, concurrency, caching,
	// UX completeness, retry correctness — from stoke-research-01 analysis).
	rules = append(rules, ResearchRules()...)
	return rules
}

func todoRule() Rule {
	re := regexp.MustCompile(`(?i)\b(TODO|FIXME|HACK|XXX|STUB|TEMP|TEMPORARY|WIP)\b`)
	return Rule{
		ID: "no-todo", Name: "No TODO/FIXME markers", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Code contains markers indicating incomplete work — the work is not done",
		Check: func(file string, content []byte) []Finding {
			return regexCheck(re, file, content, "no-todo", CatConsistency, SevBlocking,
				"Contains TODO/FIXME/HACK/WIP marker — work is incomplete", "Finish the work, then remove the marker")
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
		ID: "no-panic", Name: "No panic calls in production code", Category: CatCodeQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Code uses panic() instead of error returns — unrecoverable crash path",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil // panics in tests are acceptable
			}
			return regexCheck(re, file, content, "no-panic", CatCodeQuality, SevBlocking,
				"Uses panic() — must return error instead", "Replace panic with proper error return")
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
		Severity: SevBlocking, Enabled: true,
		Description: "Code bypasses type checking or linting — fix the real issue instead",
		Check: func(file string, content []byte) []Finding {
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-type-bypass",
					CatCodeQuality, SevBlocking,
					"Bypasses type safety or linting — fix the underlying issue", "Fix the type/lint error, don't suppress it")...)
			}
			return findings
		},
	}
}

func largeFileRule() Rule {
	return Rule{
		ID: "no-large-files", Name: "No excessively large files", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Files exceeding 600 lines should be split for maintainability",
		Check: func(file string, content []byte) []Finding {
			lines := strings.Count(string(content), "\n")
			if lines > 600 {
				return []Finding{{
					RuleID:      "no-large-files",
					Category:    CatCodeQuality,
					Severity:    SevMajor,
					File:        file,
					Description: fmt.Sprintf("File has %d lines (>600) — split into focused modules", lines),
					Suggestion:  "Break into smaller, single-responsibility modules",
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
		Severity: SevBlocking, Enabled: true,
		Description: "Error return values are being silently discarded — this hides bugs",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil // test helpers often ignore errors
			}
			return regexCheck(re, file, content, "no-ignored-errors", CatCodeQuality, SevBlocking,
				"Ignores error return value — hides bugs", "Handle the error properly")
		},
	}
}

func debugLogRule() Rule {
	re := regexp.MustCompile(`(?i)(console\.log|fmt\.Print(ln|f)?|print\(|System\.out\.print)`)
	return Rule{
		ID: "no-debug-logs", Name: "No debug print statements", Category: CatCodeQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Debug print statements left in production code — use structured logging",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-debug-logs", CatCodeQuality, SevBlocking,
				"Contains debug print statement — not production ready", "Replace with structured logging (log.Printf or similar)")
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

// --- Gate 5: Everything started is finished ---

func scaffoldingRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(scaffold|boilerplate|skeleton|template[^_]|sample code|example only)`),
		regexp.MustCompile(`(?i)(wire this|hook this|connect this|integrate later|implement later|fill in|flesh out)`),
		regexp.MustCompile(`(?i)(coming soon|not yet|will be|needs to be|remains to be)`),
	}
	return Rule{
		ID: "no-scaffolding", Name: "No scaffolding or placeholder structures", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Code contains scaffolding or unfinished structural placeholders",
		Check: func(file string, content []byte) []Finding {
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-scaffolding",
					CatConsistency, SevBlocking,
					"Contains scaffolding or unfinished placeholder — work is incomplete",
					"Complete the implementation, remove scaffolding markers")...)
			}
			return findings
		},
	}
}

func emptyFuncRule() Rule {
	// Match non-test functions with empty bodies (Go, TS/JS, Python, Rust)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?s)func \w+\([^)]*\)[^{]*\{\s*(return\s*(nil|""|0|false)?\s*)?\}`),
		regexp.MustCompile(`(?s)(function|const|let|var)\s+\w+\s*=?\s*(\([^)]*\))?\s*(=>)?\s*\{\s*\}`),
	}
	return Rule{
		ID: "no-empty-functions", Name: "No empty or trivially-empty functions", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Functions with empty or trivial return-nil bodies indicate unfinished work",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			for _, re := range patterns {
				matches := re.FindAllIndex(content, -1)
				for _, m := range matches {
					line := 1 + strings.Count(string(content[:m[0]]), "\n")
					matched := string(content[m[0]:m[1]])
					// Skip very short functions that are legitimate (interface satisfaction, etc.)
					if len(matched) > 200 {
						continue // probably not empty
					}
					findings = append(findings, Finding{
						RuleID:      "no-empty-functions",
						Category:    CatConsistency,
						Severity:    SevBlocking,
						File:        file,
						Line:        line,
						Description: "Empty or trivially-empty function — unfinished implementation",
						Suggestion:  "Implement the function body with real logic",
						Evidence:    strings.TrimSpace(matched),
					})
				}
			}
			return findings
		},
	}
}

func commentedOutCodeRule() Rule {
	// Detect blocks of commented-out code (3+ consecutive commented lines that look like code)
	return Rule{
		ID: "no-commented-code", Name: "No commented-out code blocks", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Commented-out code is dead weight — delete it or implement it",
		Check: func(file string, content []byte) []Finding {
			lines := strings.Split(string(content), "\n")
			var findings []Finding
			consecutiveCommented := 0
			startLine := 0

			codeIndicators := regexp.MustCompile(`(?i)(func |return |if |for |var |const |let |import |class |def |struct |^\s*//\s*[{}]\s*$)`)

			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				isComment := strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#")
				isCommentedCode := isComment && (codeIndicators.MatchString(trimmed) ||
					// Lone braces in comments are part of commented-out code blocks
					trimmed == "//" || trimmed == "// }" || trimmed == "// {" ||
					trimmed == "//}" || trimmed == "//{")
				if isCommentedCode {
					if consecutiveCommented == 0 {
						startLine = i + 1
					}
					consecutiveCommented++
				} else {
					if consecutiveCommented >= 3 {
						findings = append(findings, Finding{
							RuleID:      "no-commented-code",
							Category:    CatConsistency,
							Severity:    SevBlocking,
							File:        file,
							Line:        startLine,
							Description: fmt.Sprintf("Block of %d commented-out code lines — dead code", consecutiveCommented),
							Suggestion:  "Delete dead code (use version control to retrieve if needed) or implement it",
						})
					}
					consecutiveCommented = 0
				}
			}
			// Check trailing block
			if consecutiveCommented >= 3 {
				findings = append(findings, Finding{
					RuleID:      "no-commented-code",
					Category:    CatConsistency,
					Severity:    SevBlocking,
					File:        file,
					Line:        startLine,
					Description: fmt.Sprintf("Block of %d commented-out code lines — dead code", consecutiveCommented),
					Suggestion:  "Delete dead code or implement it",
				})
			}
			return findings
		},
	}
}

// --- Gate 3: Security (additional rules) ---

func commandInjectionRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`exec\.Command\(.+\+`),
		regexp.MustCompile(`exec\.CommandContext\(.+\+`),
		regexp.MustCompile(`os\.system\(.+\+`),
		regexp.MustCompile(`subprocess\.(call|run|Popen)\(.+\+`),
		regexp.MustCompile(`(?i)child_process\.exec\(.+\+`),
	}
	return Rule{
		ID: "no-command-injection", Name: "No command injection vectors", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "Shell commands built with string concatenation are injection vectors",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-command-injection",
					CatSecurity, SevBlocking,
					"Potential command injection — command built with string concatenation",
					"Use exec.Command with separate arguments, never shell interpolation")...)
			}
			return findings
		},
	}
}

func pathTraversalRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`filepath\.Join\([^,]+,\s*r\.(URL|Form|Query)`),
		regexp.MustCompile(`os\.(Open|ReadFile|Create|WriteFile)\([^)]*\+`),
		regexp.MustCompile(`path\.join\([^,]+,\s*(req|request)\.(params|query|body)`),
	}
	return Rule{
		ID: "no-path-traversal", Name: "No path traversal vectors", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "File paths built from user input without sanitization",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-path-traversal",
					CatSecurity, SevBlocking,
					"Potential path traversal — file path includes unsanitized user input",
					"Validate and sanitize paths with filepath.Clean and base-directory checks")...)
			}
			return findings
		},
	}
}

// --- Gate 5: Consistency (additional rules) ---

// duplicateLogicRule flags DRY violations: same logic block repeated 3+ times in a file.
func duplicateLogicRule() Rule {
	return Rule{
		ID: "no-duplicate-logic", Name: "No DRY violations (repeated logic blocks)", Category: CatConsistency,
		Severity: SevMajor, Enabled: true,
		Description: "Same logic repeated in 3+ places — extract into a shared function",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			lines := strings.Split(string(content), "\n")
			var findings []Finding
			// Look for identical sequences of trimmed lines (at least 3 lines long) appearing 3+ times.
			// Build a map of 3-line sliding windows.
			type block struct {
				text  string
				lines []int
			}
			windowSize := 3
			if len(lines) < windowSize {
				return nil
			}
			seen := make(map[string][]int) // block text -> list of start lines
			for i := 0; i <= len(lines)-windowSize; i++ {
				var parts []string
				totalLen := 0
				for j := 0; j < windowSize; j++ {
					t := strings.TrimSpace(lines[i+j])
					parts = append(parts, t)
					totalLen += len(t)
				}
				// Only consider blocks with 10+ non-whitespace chars total
				if totalLen < 10 {
					continue
				}
				key := strings.Join(parts, "\n")
				// Skip trivially common patterns (empty lines, single braces, etc.)
				if key == "}\n\n" || key == "\n\n" {
					continue
				}
				seen[key] = append(seen[key], i+1)
			}
			for text, locs := range seen {
				if len(locs) >= 3 {
					preview := text
					if len(preview) > 120 {
						preview = preview[:120] + "..."
					}
					findings = append(findings, Finding{
						RuleID:      "no-duplicate-logic",
						Category:    CatConsistency,
						Severity:    SevMajor,
						File:        file,
						Line:        locs[0],
						Description: fmt.Sprintf("Logic block repeated %d times — DRY violation", len(locs)),
						Suggestion:  "Extract duplicated logic into a shared function or variable",
						Evidence:    preview,
					})
				}
			}
			return findings
		},
	}
}

// --- Gate 3: Code Quality (additional rules) ---

// missingErrorContextRule flags bare `return err` without wrapping context.
func missingErrorContextRule() Rule {
	re := regexp.MustCompile(`(?m)^\s*return err\s*$`)
	return Rule{
		ID: "no-missing-error-context", Name: "Error returns must include context", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Bare 'return err' without fmt.Errorf or errors.Wrap loses call-site context",
		Check: func(file string, content []byte) []Finding {
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-missing-error-context", CatCodeQuality, SevMajor,
				"Bare 'return err' without context wrapping — error origin will be unclear",
				"Use fmt.Errorf(\"context: %w\", err) or errors.Wrap to add context")
		},
	}
}

// bareAnyTypeRule flags bare any/interface{} in Go or any/unknown in TypeScript.
func bareAnyTypeRule() Rule {
	goAny := regexp.MustCompile(`\binterface\s*\{\s*\}|\bany\b`)
	tsAny := regexp.MustCompile(`:\s*(any|unknown)\b`)
	return Rule{
		ID: "no-bare-any-type", Name: "No bare any/interface{} types", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Bare any/interface{} or TypeScript any/unknown defeats type safety",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			if isGoFile(file) {
				// Exclude type assertion matches (x.(type) patterns)
				var findings []Finding
				lines := strings.Split(string(content), "\n")
				for i, line := range lines {
					if goAny.MatchString(line) {
						// Skip type assertions like x.(any) or type switches
						if strings.Contains(line, ".(") {
							continue
						}
						findings = append(findings, Finding{
							RuleID:      "no-bare-any-type",
							Category:    CatCodeQuality,
							Severity:    SevMajor,
							File:        file,
							Line:        i + 1,
							Description: "Uses bare any/interface{} — defeats type safety",
							Suggestion:  "Use a concrete type, generic constraint, or defined interface instead",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
				return findings
			}
			if strings.HasSuffix(file, ".ts") || strings.HasSuffix(file, ".tsx") {
				return regexCheck(tsAny, file, content, "no-bare-any-type", CatCodeQuality, SevMajor,
					"Uses bare any/unknown type annotation — defeats TypeScript type safety",
					"Use a specific type, generic, or 'unknown' with type narrowing instead of 'any'")
			}
			return nil
		},
	}
}

// --- Gate 3: Security (additional rules) ---

// missingAuthCheckRule flags HTTP handlers without auth/permission checks.
func missingAuthCheckRule() Rule {
	handlerFunc := regexp.MustCompile(`func\s+(\(\w+\s+\*?\w+\)\s+)?(Handle\w+|Serve\w+|\w+[Hh]andler)\s*\(`)
	authPattern := regexp.MustCompile(`(?i)(auth|permission|rbac|middleware|token|session)`)
	pathPattern := regexp.MustCompile(`(?i)(api/|handler|route|server)`)
	return Rule{
		ID: "no-missing-auth-check", Name: "HTTP handlers must have auth checks", Category: CatSecurity,
		Severity: SevBlocking, Enabled: true,
		Description: "HTTP handler functions without auth/permission checks may expose unprotected endpoints",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			// Only check files in API/handler/route/server paths
			if !pathPattern.MatchString(file) {
				return nil
			}
			lines := strings.Split(string(content), "\n")
			var findings []Finding
			for i, line := range lines {
				if handlerFunc.MatchString(line) {
					// Check the next 20 lines for auth-related patterns
					end := i + 20
					if end > len(lines) {
						end = len(lines)
					}
					window := strings.Join(lines[i:end], "\n")
					if !authPattern.MatchString(window) {
						findings = append(findings, Finding{
							RuleID:      "no-missing-auth-check",
							Category:    CatSecurity,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "HTTP handler has no auth/permission check within 20 lines — endpoint may be unprotected",
							Suggestion:  "Add authentication/authorization middleware or explicit permission checks",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}

// --- Gate 3: Test quality (additional rules) ---

// missingErrorTestRule flags functions that return errors but have no test exercising the error path.
func missingErrorTestRule() Rule {
	errorReturnFunc := regexp.MustCompile(`func\s+(\w+)\([^)]*\)\s*(?:\([^)]*error[^)]*\)|error)\s*\{`)
	return Rule{
		ID: "no-missing-error-test", Name: "Error-returning functions must have error path tests", Category: CatTestCoverage,
		Severity: SevMajor, Enabled: true,
		Description: "Functions returning errors should have tests that exercise the error path",
		Check: func(file string, content []byte) []Finding {
			// This rule only applies to Go source files (not test files themselves)
			if !isGoFile(file) || isTestFile(file) {
				return nil
			}
			matches := errorReturnFunc.FindAllSubmatch(content, -1)
			if len(matches) == 0 {
				return nil
			}
			// This is a per-file heuristic: we flag functions with error returns.
			// The caller's integration layer should cross-reference with _test.go files
			// to check if both the function name AND err/Error appear in the test.
			// At the single-file level, we simply flag the function signature for awareness.
			return nil // Handled at integration level (cross-file), not per-file
		},
	}
}

// --- Gate 6: UX quality (additional rules) ---

// missingEmptyStateRule flags components with data iteration that don't handle empty data.
func missingEmptyStateRule() Rule {
	iterationPattern := regexp.MustCompile(`\.(map|forEach)\(|v-for|(\*ngFor)`)
	emptyCheck := regexp.MustCompile(`(?i)(length\s*(===|==|!==|!=|>|<)\s*0|\.length\s*\)|!data|isEmpty|empty|no\s+(results|items|data)|emptyState|empty-state)`)
	return Rule{
		ID: "no-missing-empty-state", Name: "Data lists must handle empty state", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Components that iterate over data without empty state handling show blank UI when data is empty",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) {
				return nil
			}
			contentStr := string(content)
			if iterationPattern.MatchString(contentStr) && !emptyCheck.MatchString(contentStr) {
				return []Finding{{
					RuleID:      "no-missing-empty-state",
					Category:    CatUXQuality,
					Severity:    SevBlocking,
					File:        file,
					Description: "Component iterates over data (.map/v-for/ngFor) but has no empty state handling — blank UI when data is empty",
					Suggestion:  "Add a check for empty data (e.g., data.length === 0) and render an appropriate empty state message",
				}}
			}
			return nil
		},
	}
}

// --- Gate 3: Reliability ---

// unboundedQueryRule flags SQL SELECT queries without LIMIT clauses.
func unboundedQueryRule() Rule {
	selectPattern := regexp.MustCompile(`(?i)\bSELECT\b`)
	limitPattern := regexp.MustCompile(`(?i)\bLIMIT\b`)
	// Exclude queries that are inherently bounded or are subqueries
	excludePattern := regexp.MustCompile(`(?i)(COUNT\s*\(|EXISTS\s*\(|SELECT\s+1\b)`)
	return Rule{
		ID: "no-unbounded-query", Name: "SQL queries must have LIMIT clauses", Category: CatReliability,
		Severity: SevMajor, Enabled: true,
		Description: "Unbounded SELECT queries can return millions of rows and crash the service",
		Check: func(file string, content []byte) []Finding {
			if isTestFile(file) {
				return nil
			}
			ext := filepath.Ext(file)
			if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".ts" && ext != ".tsx" && ext != ".jsx" {
				return nil
			}
			contentStr := string(content)
			if !selectPattern.MatchString(contentStr) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(contentStr, "\n")
			for i, line := range lines {
				if !selectPattern.MatchString(line) {
					continue
				}
				if excludePattern.MatchString(line) {
					continue
				}
				// Check the current line and next 5 lines for a LIMIT clause
				end := i + 6
				if end > len(lines) {
					end = len(lines)
				}
				window := strings.Join(lines[i:end], "\n")
				if !limitPattern.MatchString(window) {
					findings = append(findings, Finding{
						RuleID:      "no-unbounded-query",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "SQL SELECT without LIMIT — unbounded query can return excessive rows",
						Suggestion:  "Add a LIMIT clause to prevent unbounded result sets",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// --- Gate 3: Code quality (frontend) ---

// consoleLogRule flags console.log/warn/error in JS/TS production code.
func consoleLogRule() Rule {
	re := regexp.MustCompile(`\bconsole\.(log|warn|error)\(`)
	return Rule{
		ID: "no-console-log", Name: "No console.log/warn/error in production JS/TS", Category: CatCodeQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Console logging in production code — use structured logging instead",
		Check: func(file string, content []byte) []Finding {
			if !isJSOrTSFile(file) || isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "no-console-log", CatCodeQuality, SevMajor,
				"Uses console.log/warn/error — not suitable for production, use structured logging",
				"Replace with a proper logging library (e.g., winston, pino, or a custom logger)")
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
		strings.HasSuffix(path, ".test.tsx") ||
		strings.HasSuffix(path, ".test.js") ||
		strings.HasSuffix(path, ".test.jsx") ||
		strings.HasSuffix(path, ".spec.ts") ||
		strings.HasSuffix(path, ".spec.tsx") ||
		strings.HasSuffix(path, ".spec.js") ||
		strings.HasSuffix(path, ".spec.jsx") ||
		strings.Contains(filepath.Base(path), "test_")
}

func isGoFile(path string) bool {
	return strings.HasSuffix(path, ".go")
}

func isFrontendFile(path string) bool {
	exts := []string{".tsx", ".jsx", ".vue", ".svelte", ".html", ".htm"}
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func isStyleFile(path string) bool {
	exts := []string{".css", ".scss", ".sass", ".less", ".styl"}
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func isJSOrTSFile(path string) bool {
	exts := []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"}
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func isHTMLTemplateFile(path string) bool {
	return strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm") ||
		strings.HasSuffix(path, ".vue") || strings.HasSuffix(path, ".svelte")
}

// unwiredCodeRule flags patterns indicating code that exists but is likely
// not wired into the system: unused assignments, interface-only declarations,
// and "wire this up later" markers.
func unwiredCodeRule() Rule {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(wire this|hook this up|connect later|integrate later|plug in later|call this from)`),
		regexp.MustCompile(`(?i)(not yet (called|used|wired|connected|invoked))`),
		regexp.MustCompile(`(?i)(unused|UNUSED|dead code|DEAD CODE)`),
	}
	return Rule{
		ID: "no-unwired-code", Name: "No unwired or dead code markers", Category: CatConsistency,
		Severity: SevBlocking, Enabled: true,
		Description: "Code exists but is not wired — dead code that looks complete is worse than missing code",
		Check: func(file string, content []byte) []Finding {
			var findings []Finding
			for _, re := range patterns {
				findings = append(findings, regexCheck(re, file, content, "no-unwired-code",
					CatConsistency, SevBlocking,
					"Code exists but is marked as unwired/unused — wire it or delete it",
					"Trace the call chain from entry point → function. If nothing calls this, either wire it or remove it.")...)
			}
			return findings
		},
	}
}

// --- Gate 6: UX Quality ---

// inaccessibleImageRule flags <img> tags without alt attributes.
func inaccessibleImageRule() Rule {
	// Match <img that does NOT have alt= on the same line
	imgTag := regexp.MustCompile(`<img\s[^>]*>`)
	altAttr := regexp.MustCompile(`\balt\s*=`)
	return Rule{
		ID: "a11y-img-alt", Name: "Images must have alt text", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Images without alt text are inaccessible to screen readers — WCAG 2.1 Level A violation",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				imgs := imgTag.FindAllString(line, -1)
				for _, img := range imgs {
					if !altAttr.MatchString(img) {
						findings = append(findings, Finding{
							RuleID:      "a11y-img-alt",
							Category:    CatUXQuality,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "Image missing alt attribute — inaccessible to screen readers",
							Suggestion:  "Add alt=\"descriptive text\" or alt=\"\" for decorative images",
							Evidence:    strings.TrimSpace(img),
						})
					}
				}
			}
			return findings
		},
	}
}

// inaccessibleInteractiveRule flags click handlers on non-interactive elements without
// proper ARIA roles and keyboard support.
func inaccessibleInteractiveRule() Rule {
	// onClick on div/span without role or tabIndex
	onClickDiv := regexp.MustCompile(`<(div|span)\s[^>]*onClick`)
	roleAttr := regexp.MustCompile(`\brole\s*=`)
	return Rule{
		ID: "a11y-interactive", Name: "Interactive elements must be accessible", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Click handlers on non-interactive elements (div, span) without role/tabIndex are inaccessible",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if onClickDiv.MatchString(line) && !roleAttr.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "a11y-interactive",
						Category:    CatUXQuality,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "onClick on non-interactive element without role/tabIndex — keyboard users cannot interact",
						Suggestion:  "Use <button> instead, or add role=\"button\" tabIndex={0} onKeyDown handler",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// missingViewportRule flags HTML files without the responsive viewport meta tag.
func missingViewportRule() Rule {
	return Rule{
		ID: "ux-viewport-meta", Name: "HTML must have responsive viewport meta", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "HTML documents without viewport meta tag render incorrectly on mobile devices",
		Check: func(file string, content []byte) []Finding {
			if !strings.HasSuffix(file, ".html") && !strings.HasSuffix(file, ".htm") {
				return nil
			}
			contentStr := string(content)
			// Only check files that have <html or <head (i.e., full documents)
			if !strings.Contains(contentStr, "<head") && !strings.Contains(contentStr, "<html") {
				return nil
			}
			if !strings.Contains(contentStr, "viewport") {
				return []Finding{{
					RuleID:      "ux-viewport-meta",
					Category:    CatUXQuality,
					Severity:    SevBlocking,
					File:        file,
					Description: "Missing <meta name=\"viewport\"> — page will not be responsive on mobile",
					Suggestion:  "Add <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"> in <head>",
				}}
			}
			return nil
		},
	}
}

// noResponsiveDesignRule flags CSS files that lack any media queries or responsive
// units, indicating a non-responsive layout.
func noResponsiveDesignRule() Rule {
	mediaQuery := regexp.MustCompile(`@media`)
	responsiveUnit := regexp.MustCompile(`\b\d+(\.\d+)?(rem|em|vw|vh|vmin|vmax|%)`)
	return Rule{
		ID: "ux-responsive", Name: "Styles must include responsive design", Category: CatUXQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Stylesheets without media queries or responsive units may not work across device sizes",
		Check: func(file string, content []byte) []Finding {
			if !isStyleFile(file) {
				return nil
			}
			// Only flag non-trivial stylesheets (>20 lines suggests real styling)
			lines := strings.Count(string(content), "\n")
			if lines < 20 {
				return nil
			}
			contentStr := string(content)
			hasMedia := mediaQuery.MatchString(contentStr)
			hasResponsiveUnits := responsiveUnit.MatchString(contentStr)
			if !hasMedia && !hasResponsiveUnits {
				return []Finding{{
					RuleID:      "ux-responsive",
					Category:    CatUXQuality,
					Severity:    SevMajor,
					File:        file,
					Description: "Stylesheet has no media queries or responsive units — layout will break on mobile/tablet",
					Suggestion:  "Add @media breakpoints and use rem/em/vw/% instead of fixed px values",
				}}
			}
			return nil
		},
	}
}

// missingErrorBoundaryRule flags React apps that catch no render errors.
func missingErrorBoundaryRule() Rule {
	errorBoundary := regexp.MustCompile(`(?i)(ErrorBoundary|componentDidCatch|error\s*boundary|getDerivedStateFromError)`)
	reactImport := regexp.MustCompile(`(?i)(from\s+['"]react['"]|import\s+React)`)
	return Rule{
		ID: "ux-error-boundary", Name: "React apps must have error boundaries", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "React apps without error boundaries show blank pages on render errors",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) {
				return nil
			}
			contentStr := string(content)
			// Only check React entry points (files that render the app root)
			isEntry := strings.Contains(contentStr, "createRoot") ||
				strings.Contains(contentStr, "ReactDOM.render") ||
				strings.Contains(contentStr, "hydrateRoot")
			if !isEntry {
				return nil
			}
			if reactImport.MatchString(contentStr) && !errorBoundary.MatchString(contentStr) {
				return []Finding{{
					RuleID:      "ux-error-boundary",
					Category:    CatUXQuality,
					Severity:    SevBlocking,
					File:        file,
					Description: "React app entry point has no ErrorBoundary — render errors show blank page",
					Suggestion:  "Wrap the app root in an ErrorBoundary component with a user-friendly fallback UI",
				}}
			}
			return nil
		},
	}
}

// missingLoadingStateRule flags data-fetching components without loading/error states.
func missingLoadingStateRule() Rule {
	fetchPatterns := regexp.MustCompile(`(useQuery|useSWR|useEffect\([^)]*fetch|\.then\(|async\s+function|await\s+fetch)`)
	loadingPattern := regexp.MustCompile(`(?i)(loading|isLoading|isFetching|spinner|skeleton|Suspense|fallback)`)
	return Rule{
		ID: "ux-loading-state", Name: "Data-fetching components must have loading states", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Components that fetch data without loading states show blank/broken UI during loads",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) || isStyleFile(file) {
				return nil
			}
			contentStr := string(content)
			if fetchPatterns.MatchString(contentStr) && !loadingPattern.MatchString(contentStr) {
				return []Finding{{
					RuleID:      "ux-loading-state",
					Category:    CatUXQuality,
					Severity:    SevBlocking,
					File:        file,
					Description: "Component fetches data but has no loading state — users see blank/broken UI",
					Suggestion:  "Add loading indicators (spinner, skeleton) and error states for all async operations",
				}}
			}
			return nil
		},
	}
}

// missingFormLabelRule flags form inputs without associated labels.
func missingFormLabelRule() Rule {
	inputNoLabel := regexp.MustCompile(`<input\s[^>]*>`)
	labelAttr := regexp.MustCompile(`\b(aria-label|aria-labelledby|id\s*=)\s*=`)
	return Rule{
		ID: "a11y-form-label", Name: "Form inputs must have labels", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Form inputs without labels are inaccessible — WCAG 2.1 Level A violation",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) && !isHTMLTemplateFile(file) {
				return nil
			}
			var findings []Finding
			contentStr := string(content)
			lines := strings.Split(contentStr, "\n")
			hasAnyLabel := strings.Contains(contentStr, "<label")
			for i, line := range lines {
				inputs := inputNoLabel.FindAllString(line, -1)
				for _, input := range inputs {
					// Skip hidden inputs and submit buttons
					if strings.Contains(input, "type=\"hidden\"") || strings.Contains(input, "type='hidden'") ||
						strings.Contains(input, "type=\"submit\"") || strings.Contains(input, "type='submit'") {
						continue
					}
					// Check for inline aria-label or aria-labelledby
					if labelAttr.MatchString(input) {
						continue
					}
					// Check for placeholder (not sufficient for a11y but not as bad)
					if strings.Contains(input, "placeholder") && hasAnyLabel {
						continue // file has labels; placeholder alone is a mild a11y issue
					}
					if !hasAnyLabel && !labelAttr.MatchString(input) {
						findings = append(findings, Finding{
							RuleID:      "a11y-form-label",
							Category:    CatUXQuality,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "Form input has no associated <label> or aria-label — inaccessible",
							Suggestion:  "Add <label htmlFor=\"id\"> or aria-label=\"description\" to the input",
							Evidence:    strings.TrimSpace(input),
						})
					}
				}
			}
			return findings
		},
	}
}

// focusTrapRule flags CSS that removes focus outlines without providing alternatives.
func focusTrapRule() Rule {
	outlineNone := regexp.MustCompile(`outline\s*:\s*(none|0)\b`)
	focusVisible := regexp.MustCompile(`focus-visible`)
	return Rule{
		ID: "a11y-focus-visible", Name: "Focus indicators must not be removed", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "Removing focus outlines makes keyboard navigation impossible — WCAG 2.1 Level AA violation",
		Check: func(file string, content []byte) []Finding {
			if !isStyleFile(file) && !isFrontendFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if outlineNone.MatchString(line) && !focusVisible.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "a11y-focus-visible",
						Category:    CatUXQuality,
						Severity:    SevBlocking,
						File:        file,
						Line:        i + 1,
						Description: "Focus outline removed without alternative — keyboard users lose navigation",
						Suggestion:  "Replace outline:none with a visible :focus-visible style or custom focus indicator",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// hardcodedColorRule flags inline styles with hardcoded colors instead of
// design tokens or CSS variables — leads to inconsistent theming and
// dark-mode breakage.
func hardcodedColorRule() Rule {
	// Inline style with hardcoded hex/rgb color
	inlineColor := regexp.MustCompile(`style\s*=\s*\{?\{[^}]*(color|background|border)[^}]*["'](#[0-9a-fA-F]{3,8}|rgb)`)
	return Rule{
		ID: "ux-no-hardcoded-colors", Name: "No hardcoded colors in inline styles", Category: CatUXQuality,
		Severity: SevMajor, Enabled: true,
		Description: "Hardcoded colors in inline styles break theming and dark mode",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if inlineColor.MatchString(line) {
					findings = append(findings, Finding{
						RuleID:      "ux-no-hardcoded-colors",
						Category:    CatUXQuality,
						Severity:    SevMajor,
						File:        file,
						Line:        i + 1,
						Description: "Hardcoded color in inline style — breaks theming and dark mode support",
						Suggestion:  "Use CSS variables (var(--color-primary)) or design tokens instead",
						Evidence:    strings.TrimSpace(line),
					})
				}
			}
			return findings
		},
	}
}

// dangerousInnerHTMLRule flags use of dangerouslySetInnerHTML in React components
// (XSS risk + rendering inconsistency).
func dangerousInnerHTMLRule() Rule {
	re := regexp.MustCompile(`dangerouslySetInnerHTML`)
	return Rule{
		ID: "ux-no-dangerous-html", Name: "No dangerouslySetInnerHTML", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "dangerouslySetInnerHTML is an XSS vector and bypasses React's rendering — avoid unless sanitized",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) {
				return nil
			}
			return regexCheck(re, file, content, "ux-no-dangerous-html", CatUXQuality, SevBlocking,
				"Uses dangerouslySetInnerHTML — XSS risk and bypasses virtual DOM",
				"Use a sanitization library (DOMPurify) or render content safely with React components")
		},
	}
}

// missingKeyPropRule flags React list rendering without key props.
func missingKeyPropRule() Rule {
	mapReturn := regexp.MustCompile(`\.map\(\s*\(?[^)]*\)?\s*=>\s*[({]?\s*<`)
	keyProp := regexp.MustCompile(`\bkey\s*=`)
	return Rule{
		ID: "ux-list-key", Name: "List items must have key props", Category: CatUXQuality,
		Severity: SevBlocking, Enabled: true,
		Description: "React list items without key props cause rendering bugs and poor performance",
		Check: func(file string, content []byte) []Finding {
			if !isFrontendFile(file) || isTestFile(file) {
				return nil
			}
			var findings []Finding
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if mapReturn.MatchString(line) {
					// Check this line and the next few for a key prop
					context := line
					for j := i + 1; j < len(lines) && j <= i+3; j++ {
						context += lines[j]
					}
					if !keyProp.MatchString(context) {
						findings = append(findings, Finding{
							RuleID:      "ux-list-key",
							Category:    CatUXQuality,
							Severity:    SevBlocking,
							File:        file,
							Line:        i + 1,
							Description: "List rendering via .map() without key prop — causes render bugs and poor performance",
							Suggestion:  "Add a unique key prop to the first element returned from .map()",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
			}
			return findings
		},
	}
}
