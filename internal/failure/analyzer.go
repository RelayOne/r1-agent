package failure

import (
	"fmt"
	"regexp"
	"strings"
)

// Class categorizes why a task failed.
type Class string

const (
	BuildFailed     Class = "BuildFailed"
	TestsFailed     Class = "TestsFailed"
	LintFailed      Class = "LintFailed"
	PolicyViolation Class = "PolicyViolation"
	ReviewRejected  Class = "ReviewRejected"
	Timeout         Class = "Timeout"
	WrongFiles      Class = "WrongFiles"
	Incomplete      Class = "Incomplete"
	Regression      Class = "Regression"
	RateLimited     Class = "RateLimited"
)

// Detail captures one specific issue from a failed attempt.
type Detail struct {
	File    string
	Line    int
	Message string
	Fix     string
}

// Analysis is the complete diagnosis of a failed attempt.
type Analysis struct {
	Class       Class
	Summary     string
	RootCause   string
	Missing     []string
	Specifics   []Detail
	DiffSummary string
}

// Analyze classifies failure from build/test/lint output and extracts specifics.
// Analyze diagnoses a failed attempt from build/test/lint output.
// diffSummary is the actual code diff and is used for policy violation scanning
// (as opposed to build/test/lint output which can false-positive on legitimate
// mentions of forbidden patterns in error messages, test names, etc.).
func Analyze(buildOutput, testOutput, lintOutput string, diffSummary ...string) Analysis {
	// Check for policy violations against the DIFF, not tool output.
	// Tool output can contain legitimate mentions of forbidden patterns
	// (e.g., linter reporting @ts-ignore, test named "TestHandleError").
	policyInput := strings.Join(diffSummary, "\n")
	if policyInput == "" {
		// Fallback: if no diff provided, scan tool output (legacy behavior)
		policyInput = buildOutput + "\n" + testOutput + "\n" + lintOutput
	}
	if violations := scanPolicyViolations(policyInput); len(violations) > 0 {
		return Analysis{
			Class:     PolicyViolation,
			Summary:   fmt.Sprintf("%d policy violation(s) detected", len(violations)),
			RootCause: violations[0].Message,
			Specifics: violations,
		}
	}

	// Build failures
	if isFailing(buildOutput) {
		details := parseBuildErrors(buildOutput)
		summary := "build failed"
		if len(details) > 0 {
			summary = fmt.Sprintf("build failed: %d error(s)", len(details))
		}
		return Analysis{
			Class:     BuildFailed,
			Summary:   summary,
			RootCause: inferRootCause(details, buildOutput),
			Specifics: details,
			Missing:   inferMissing(BuildFailed),
		}
	}

	// Test failures
	if isFailing(testOutput) {
		details := parseTestErrors(testOutput)
		summary := "tests failed"
		if len(details) > 0 {
			summary = fmt.Sprintf("%d test(s) failed", len(details))
		}
		return Analysis{
			Class:     TestsFailed,
			Summary:   summary,
			RootCause: inferRootCause(details, testOutput),
			Specifics: details,
			Missing:   inferMissing(TestsFailed),
		}
	}

	// Lint failures
	if isFailing(lintOutput) {
		details := parseLintErrors(lintOutput)
		summary := "lint violations"
		if len(details) > 0 {
			summary = fmt.Sprintf("%d lint violation(s)", len(details))
		}
		return Analysis{
			Class:     LintFailed,
			Summary:   summary,
			RootCause: inferRootCause(details, lintOutput),
			Specifics: details,
		}
	}

	return Analysis{Class: Incomplete, Summary: "no failure output captured"}
}

func isFailing(output string) bool {
	s := strings.TrimSpace(output)
	if s == "" {
		return false
	}
	lc := strings.ToLower(s)
	return strings.Contains(lc, "error") || strings.Contains(lc, "fail") || strings.Contains(lc, "fatal")
}

// --- Build error parsers ---

var (
	tsErrorRe   = regexp.MustCompile(`(?m)^(.+)\((\d+),\d+\):\s+error TS(\d+):\s+(.+)$`)
	goErrorRe   = regexp.MustCompile(`(?m)^(.+):(\d+):\d+:\s+(.+)$`)
	rustErrorRe = regexp.MustCompile(`(?m)error\[E\d+\]:\s+(.+)\n\s*-->\s+(.+):(\d+):\d+`)
	pyErrorRe   = regexp.MustCompile(`(?m)File "(.+)", line (\d+)(?:.*\n\s+.*\n)?(\w+Error:.+)`)
)

func parseBuildErrors(output string) []Detail {
	var details []Detail

	// TypeScript
	for _, m := range tsErrorRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{
			File: m[1], Line: atoi(m[2]),
			Message: fmt.Sprintf("TS%s: %s", m[3], m[4]),
		})
	}
	if len(details) > 0 {
		return details
	}

	// Go
	for _, m := range goErrorRe.FindAllStringSubmatch(output, 20) {
		if strings.Contains(m[3], "syntax error") || strings.Contains(m[3], "undefined") ||
			strings.Contains(m[3], "cannot") || strings.Contains(m[3], "imported and not used") {
			details = append(details, Detail{
				File: m[1], Line: atoi(m[2]), Message: m[3],
			})
		}
	}
	if len(details) > 0 {
		return details
	}

	// Rust
	for _, m := range rustErrorRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{
			File: m[2], Line: atoi(m[3]), Message: m[1],
		})
	}
	if len(details) > 0 {
		return details
	}

	// Python
	for _, m := range pyErrorRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{
			File: m[1], Line: atoi(m[2]), Message: m[3],
		})
	}

	return details
}

// --- Test error parsers ---

var (
	jestFailRe   = regexp.MustCompile(`(?m)FAIL\s+(.+)$`)
	jestExpectRe = regexp.MustCompile(`(?m)Expected:?\s+(.+)\n\s*Received:?\s+(.+)`)
	goTestFailRe = regexp.MustCompile(`(?m)--- FAIL: (.+) \([\d.]+s\)`)
	pytestFailRe = regexp.MustCompile(`(?m)FAILED (.+?)::(.+)`)
	rustTestRe   = regexp.MustCompile(`(?m)test (.+) \.\.\. FAILED`)
)

func parseTestErrors(output string) []Detail {
	var details []Detail

	// Jest / Vitest
	for _, m := range jestFailRe.FindAllStringSubmatch(output, 20) {
		d := Detail{File: strings.TrimSpace(m[1]), Message: "test suite failed"}
		if exp := jestExpectRe.FindStringSubmatch(output); exp != nil {
			d.Message = fmt.Sprintf("expected %s, received %s", exp[1], exp[2])
		}
		details = append(details, d)
	}
	if len(details) > 0 {
		return details
	}

	// Go test
	for _, m := range goTestFailRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{Message: fmt.Sprintf("FAIL: %s", m[1])})
	}
	if len(details) > 0 {
		return details
	}

	// Pytest
	for _, m := range pytestFailRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{File: m[1], Message: fmt.Sprintf("FAILED: %s", m[2])})
	}
	if len(details) > 0 {
		return details
	}

	// Rust
	for _, m := range rustTestRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{Message: fmt.Sprintf("test %s FAILED", m[1])})
	}

	return details
}

// --- Lint error parsers ---

var (
	eslintRe = regexp.MustCompile(`(?m)^\s*(\d+):(\d+)\s+(error|warning)\s+(.+?)\s+(\S+)$`)
	golintRe = regexp.MustCompile(`(?m)^(.+):(\d+):\d+:\s+(.+)\s+\((.+)\)$`)
	ruffRe   = regexp.MustCompile(`(?m)^(.+):(\d+):\d+:\s+(\w+)\s+(.+)$`)
	clippyRe = regexp.MustCompile(`(?m)warning:\s+(.+)\n\s*-->\s+(.+):(\d+):\d+`)
)

func parseLintErrors(output string) []Detail {
	var details []Detail

	for _, m := range eslintRe.FindAllStringSubmatch(output, 20) {
		if m[3] == "error" {
			details = append(details, Detail{Line: atoi(m[1]), Message: fmt.Sprintf("%s (%s)", m[4], m[5])})
		}
	}
	for _, m := range golintRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{File: m[1], Line: atoi(m[2]), Message: fmt.Sprintf("%s (%s)", m[3], m[4])})
	}
	for _, m := range ruffRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{File: m[1], Line: atoi(m[2]), Message: fmt.Sprintf("%s: %s", m[3], m[4])})
	}
	for _, m := range clippyRe.FindAllStringSubmatch(output, 20) {
		details = append(details, Detail{File: m[2], Line: atoi(m[3]), Message: m[1]})
	}

	return details
}

// --- Policy violation scanner ---

var policyPatterns = []struct {
	re    *regexp.Regexp
	issue string
	fix   string
}{
	{regexp.MustCompile(`@ts-ignore`), "added @ts-ignore", "fix the actual type error"},
	{regexp.MustCompile(`as\s+any`), "used 'as any' assertion", "use a proper type"},
	{regexp.MustCompile(`eslint-disable`), "disabled eslint rule", "fix the lint issue"},
	{regexp.MustCompile(`# type:\s*ignore`), "used Python type: ignore", "fix the type error"},
	{regexp.MustCompile(`#\s*noqa`), "used noqa to suppress lint", "fix the lint issue"},
	{regexp.MustCompile(`#allow\(.*clippy`), "suppressed clippy warning", "fix the clippy issue"},
	{regexp.MustCompile(`\.only\(`), "left .only() on test", "remove .only() so all tests run"},
	{regexp.MustCompile(`console\.log`), "left console.log", "remove debug logging"},
	{regexp.MustCompile(`fmt\.Print`), "left fmt.Print debug output", "remove debug output"},
}

func scanPolicyViolations(combined string) []Detail {
	// Only scan lines that were likely added (heuristic: non-empty, not comments)
	var details []Detail
	for _, line := range strings.Split(combined, "\n") {
		for _, pp := range policyPatterns {
			if pp.re.MatchString(line) {
				details = append(details, Detail{
					Message: pp.issue,
					Fix:     pp.fix,
				})
			}
		}
	}
	return details
}

// --- Inference ---

func inferRootCause(details []Detail, rawOutput string) string {
	if len(details) == 0 {
		return firstLine(rawOutput)
	}
	// Group by message pattern
	counts := map[string]int{}
	for _, d := range details {
		key := d.Message
		if strings.Contains(key, "TS") {
			key = "TypeScript type error"
		}
		counts[key]++
	}
	best, bestN := "", 0
	for k, v := range counts {
		if v > bestN {
			best, bestN = k, v
		}
	}
	if bestN > 1 {
		return fmt.Sprintf("%d instances of: %s", bestN, best)
	}
	return details[0].Message
}

func inferMissing(class Class) []string {
	switch class {
	case BuildFailed:
		return []string{"task should specify import paths and module structure"}
	case TestsFailed:
		return []string{"task should reference how tests are structured in this codebase"}
	case PolicyViolation:
		return []string{"task should explicitly prohibit type bypasses"}
	default:
		return nil
	}
}

// --- Retry decisions ---

// Action is what to do with a failed task.
type Action int

const (
	Retry Action = iota
	Escalate
)

// Decision is the outcome of ShouldRetry.
type Decision struct {
	Action     Action
	Reason     string
	Constraint string
}

// ShouldRetry decides whether to retry or escalate.
func ShouldRetry(analysis *Analysis, attempt int, prior *Analysis) Decision {
	if attempt >= 3 {
		return Decision{Action: Escalate, Reason: "3 attempts exhausted"}
	}
	// Same error twice means the task description is wrong, not the agent
	if prior != nil && analysis.Summary == prior.Summary {
		return Decision{Action: Escalate, Reason: "same failure repeated -- task needs revision"}
	}
	switch analysis.Class {
	case PolicyViolation:
		return Decision{Action: Retry, Constraint: "do not use type bypasses or disable lint rules"}
	case Timeout:
		if attempt >= 2 {
			return Decision{Action: Escalate, Reason: "timed out twice -- task too complex"}
		}
		return Decision{Action: Retry, Constraint: "focus on core change only"}
	case WrongFiles:
		return Decision{Action: Retry, Constraint: "only modify files in the task scope"}
	case RateLimited:
		return Decision{Action: Retry} // pool manager rotates
	default:
		return Decision{Action: Retry}
	}
}

// --- helpers ---

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
