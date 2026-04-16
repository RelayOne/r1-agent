// Package critic implements an adversarial pre-commit critic.
// Inspired by Google Jules' adversarial critic agent:
//
// Unlike cross-model review (which happens after task completion),
// the critic runs WITHIN the generation loop, catching issues before
// they're committed. This is a same-turn quality gate:
// - Analyzes proposed changes for bugs, security issues, style violations
// - Classifies findings by severity (block, warn, info)
// - Produces structured verdicts that feed back into the generation loop
// - Supports configurable rule sets per project
// - Can request specific fixes before allowing commit
package critic

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Severity classifies a finding.
type Severity string

const (
	SeverityBlock Severity = "block" // must fix before commit
	SeverityWarn  Severity = "warn"  // should fix, doesn't block
	SeverityInfo  Severity = "info"  // informational
)

// Finding is a single issue found by the critic.
type Finding struct {
	Severity    Severity `json:"severity"`
	Category    string   `json:"category"`    // bug, security, style, performance, correctness
	File        string   `json:"file"`
	Line        int      `json:"line,omitempty"`
	Message     string   `json:"message"`
	Suggestion  string   `json:"suggestion,omitempty"`
	Rule        string   `json:"rule,omitempty"`

	// EvidenceRefs are content-addressed pointers to the artifacts the
	// critic actually examined when producing this finding. Replay
	// auditors use these to fetch the exact same bytes the critic saw
	// and verify the finding is reproducible. Optional but strongly
	// encouraged for new code paths — without refs a replay sees only
	// prose, which is non-verifiable. See docs/anti-deception-matrix.md
	// row "critic evidence."
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
}

// EvidenceRef is a content-addressed pointer into the ledger / log /
// diff store. Kind discriminates the ref space; Hash is the lookup key.
//
//	Kind        Hash format                   Resolves to
//	"artifact"  "sha256:<hex>"                file content in ledger
//	"log"       "log:<span-id>"               structured log span
//	"diff"      "sha256:<hex>"                unified-diff blob
//	"ledger_node" "ledger:<node-id>"          named ledger graph node
type EvidenceRef struct {
	Kind string `json:"kind"`
	Hash string `json:"hash"`
}

// Verdict is the critic's overall assessment.
//
// EvidenceRefs on the Verdict carry the aggregate pointer
// set the critic examined across all findings — a replay
// auditor can reconstruct exactly what the critic saw
// without walking each Finding individually. Per-finding
// EvidenceRefs still live on Finding for fine-grained
// attribution; this top-level field covers the "what did
// the whole verdict look at?" question directly.
type Verdict struct {
	Pass         bool          `json:"pass"`       // true if no blocking findings
	Findings     []Finding     `json:"findings"`
	Score        float64       `json:"score"`      // 0-1 quality score
	Summary      string        `json:"summary"`
	Duration     time.Duration `json:"duration"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
}

// AggregateEvidence walks the verdict's Findings and
// returns a deduplicated union of their EvidenceRefs.
// Used to populate Verdict.EvidenceRefs at emit time
// without callers manually reconciling across findings.
func (v Verdict) AggregateEvidence() []EvidenceRef {
	seen := map[string]struct{}{}
	var out []EvidenceRef
	add := func(r EvidenceRef) {
		key := r.Kind + "|" + r.Hash
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	for _, r := range v.EvidenceRefs {
		add(r)
	}
	for _, f := range v.Findings {
		for _, r := range f.EvidenceRefs {
			add(r)
		}
	}
	return out
}

// Rule is a configurable check.
type Rule struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Severity Severity `json:"severity"`
	Pattern  *regexp.Regexp
	Check    func(file, content string) []Finding
}

// Config configures the critic.
type Config struct {
	Rules         []Rule
	BlockOnWarn   bool    // treat warnings as blockers
	MinScore      float64 // minimum quality score to pass (0-1)
	MaxFindings   int     // max findings before auto-block
}

// Critic performs adversarial review of code changes.
type Critic struct {
	config Config
	rules  []Rule
}

// New creates a critic with default rules.
func New(cfg Config) *Critic {
	c := &Critic{config: cfg}
	if len(cfg.Rules) > 0 {
		c.rules = cfg.Rules
	} else {
		c.rules = DefaultRules()
	}
	return c
}

// Review analyzes a set of file changes and produces a verdict.
func (c *Critic) Review(changes map[string]string) *Verdict {
	start := time.Now()
	v := &Verdict{Pass: true}

	for file, content := range changes {
		for _, rule := range c.rules {
			var findings []Finding
			if rule.Check != nil {
				findings = rule.Check(file, content)
			} else if rule.Pattern != nil {
				findings = patternCheck(rule, file, content)
			}
			v.Findings = append(v.Findings, findings...)
		}
	}

	// Determine pass/fail
	blocks := 0
	warns := 0
	for _, f := range v.Findings {
		switch f.Severity {
		case SeverityBlock:
			blocks++
		case SeverityWarn:
			warns++
		}
	}

	if blocks > 0 {
		v.Pass = false
	}
	if c.config.BlockOnWarn && warns > 0 {
		v.Pass = false
	}
	if c.config.MaxFindings > 0 && len(v.Findings) > c.config.MaxFindings {
		v.Pass = false
	}

	// Score: start at 1.0, deduct for findings
	v.Score = 1.0
	v.Score -= float64(blocks) * 0.3
	v.Score -= float64(warns) * 0.1
	v.Score -= float64(len(v.Findings)-blocks-warns) * 0.02
	if v.Score < 0 {
		v.Score = 0
	}

	if c.config.MinScore > 0 && v.Score < c.config.MinScore {
		v.Pass = false
	}

	v.Duration = time.Since(start)
	v.Summary = buildSummary(v)
	return v
}

// ReviewFile analyzes a single file.
func (c *Critic) ReviewFile(file, content string) *Verdict {
	return c.Review(map[string]string{file: content})
}

// FormatFindings produces a structured report for LLM feedback.
func FormatFindings(v *Verdict) string {
	if len(v.Findings) == 0 {
		return "No issues found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Critic Review (%d findings)\n\n", len(v.Findings))

	// Group by severity
	for _, sev := range []Severity{SeverityBlock, SeverityWarn, SeverityInfo} {
		var group []Finding
		for _, f := range v.Findings {
			if f.Severity == sev {
				group = append(group, f)
			}
		}
		if len(group) == 0 {
			continue
		}

		fmt.Fprintf(&b, "### %s\n", strings.ToUpper(string(sev)))
		for _, f := range group {
			if f.Line > 0 {
				fmt.Fprintf(&b, "- **%s:%d** [%s] %s", f.File, f.Line, f.Category, f.Message)
			} else {
				fmt.Fprintf(&b, "- **%s** [%s] %s", f.File, f.Category, f.Message)
			}
			if f.Suggestion != "" {
				fmt.Fprintf(&b, "\n  Fix: %s", f.Suggestion)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if !v.Pass {
		b.WriteString("**BLOCKED**: Fix all blocking issues before committing.\n")
	}

	return b.String()
}

// DefaultRules returns the standard rule set.
func DefaultRules() []Rule {
	return []Rule{
		{
			ID: "no-todo-fixme", Name: "TODO/FIXME in new code", Severity: SeverityInfo,
			Pattern: regexp.MustCompile(`(?i)(TODO|FIXME|HACK|XXX)\b`),
		},
		{
			ID: "no-hardcoded-secrets", Name: "Hardcoded secrets", Severity: SeverityBlock,
			Check: checkSecrets,
		},
		{
			ID: "no-fmt-print", Name: "Debug print statements", Severity: SeverityWarn,
			Check: checkDebugPrints,
		},
		{
			ID: "no-panic", Name: "Panic in library code", Severity: SeverityWarn,
			Check: checkPanics,
		},
		{
			ID: "error-handling", Name: "Unchecked errors", Severity: SeverityWarn,
			Check: checkUncheckedErrors,
		},
		{
			ID: "no-unsafe", Name: "Unsafe operations", Severity: SeverityWarn,
			Pattern: regexp.MustCompile(`unsafe\.Pointer`),
		},
		{
			ID: "sql-injection", Name: "SQL injection risk", Severity: SeverityBlock,
			Check: checkSQLInjection,
		},
		{
			ID: "large-function", Name: "Function too large", Severity: SeverityInfo,
			Check: checkLargeFunction,
		},
		{
			ID: "goroutine-recover", Name: "Goroutine without recover", Severity: SeverityWarn,
			Check: checkGoroutineRecover,
		},
		{
			ID: "http-no-timeout", Name: "HTTP client without timeout", Severity: SeverityWarn,
			Pattern: regexp.MustCompile(`http\.Client\{\s*\}`),
		},
		{
			ID: "destructive-migration", Name: "Destructive SQL migration", Severity: SeverityBlock,
			Pattern: regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|COLUMN|DATABASE)|TRUNCATE\s+TABLE)\b`),
		},
	}
}

func patternCheck(rule Rule, file, content string) []Finding {
	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if rule.Pattern.MatchString(line) {
			findings = append(findings, Finding{
				Severity: rule.Severity,
				Category: "pattern",
				File:     file,
				Line:     i + 1,
				Message:  fmt.Sprintf("Matches rule %q: %s", rule.ID, rule.Name),
				Rule:     rule.ID,
			})
		}
	}
	return findings
}

var secretPatterns = []*regexp.Regexp{
	// Assignment operators: `=`, `:=`, `:`. Previous regex
	// only caught `=` / `:`; Go short-declaration `:=` slipped
	// through, so `apiKey := "ghp_.........."` wasn't flagged
	// even though the tagged OpenAI/GitHub patterns below
	// would still match that specific token.
	regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|password|token)\s*(?::=|:|=)\s*["'][^"']{8,}["']`),
	regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),                            // AWS access key
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),                             // OpenAI key
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),                             // GitHub token
	regexp.MustCompile(`-----BEGIN (RSA |EC )?PRIVATE KEY-----`),
}

func checkSecrets(file, content string) []Finding {
	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		for _, pat := range secretPatterns {
			if pat.MatchString(line) {
				findings = append(findings, Finding{
					Severity:   SeverityBlock,
					Category:   "security",
					File:       file,
					Line:       i + 1,
					Message:    "Possible hardcoded secret or credential",
					Suggestion: "Use environment variables or a secrets manager",
					Rule:       "no-hardcoded-secrets",
				})
				break
			}
		}
	}
	return findings
}

var debugPrintRe = regexp.MustCompile(`^\s*fmt\.Print(ln|f)?\(`)

func checkDebugPrints(file, content string) []Finding {
	if !strings.HasSuffix(file, ".go") {
		return nil
	}
	if strings.HasSuffix(file, "_test.go") {
		return nil
	}

	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if debugPrintRe.MatchString(line) {
			findings = append(findings, Finding{
				Severity:   SeverityWarn,
				Category:   "style",
				File:       file,
				Line:       i + 1,
				Message:    "Debug print statement in production code",
				Suggestion: "Use structured logging instead",
				Rule:       "no-fmt-print",
			})
		}
	}
	return findings
}

func checkPanics(file, content string) []Finding {
	// Exact-basename check so files like domain.go and
	// subdomain.go aren't exempted by the previous broader
	// HasSuffix("main.go") match, which let real library
	// panics slip through in those filenames.
	base := filepath.Base(file)
	if strings.HasSuffix(file, "_test.go") || base == "main.go" {
		return nil
	}

	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "panic(") {
			findings = append(findings, Finding{
				Severity:   SeverityWarn,
				Category:   "correctness",
				File:       file,
				Line:       i + 1,
				Message:    "panic() in library code — prefer returning errors",
				Suggestion: "Return an error instead of panicking",
				Rule:       "no-panic",
			})
		}
	}
	return findings
}

// uncheckedErrRe matches bare receiver.Method(...) calls
// for commonly-error-returning I/O methods on a statement
// line. Previously this only matched zero-arg calls like
// `f.Close()`, so `dst.Write(buf)` and `enc.Encode(x)`
// slipped through. Now matches any arg list (balanced
// parens) followed by end-of-line, and expands the method
// set to include the rest of the io.Writer/Reader/Closer +
// json.Encoder/Decoder surface the codebase uses.
var uncheckedErrRe = regexp.MustCompile(`^\s*[a-zA-Z_]\w*\.(Close|Write|WriteString|Read|ReadAll|Flush|Sync|Encode|Decode)\([^)]*\)$`)

func checkUncheckedErrors(file, content string) []Finding {
	if !strings.HasSuffix(file, ".go") {
		return nil
	}

	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if uncheckedErrRe.MatchString(strings.TrimSpace(line)) {
			findings = append(findings, Finding{
				Severity:   SeverityWarn,
				Category:   "correctness",
				File:       file,
				Line:       i + 1,
				Message:    "Unchecked error return value",
				Suggestion: "Handle or explicitly ignore the error with _ =",
				Rule:       "error-handling",
			})
		}
	}
	return findings
}

var sqlConcatRe = regexp.MustCompile(`(?i)(exec|query|prepare)\w*\([^)]*\+\s*[a-zA-Z]`)

func checkSQLInjection(file, content string) []Finding {
	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if sqlConcatRe.MatchString(line) {
			findings = append(findings, Finding{
				Severity:   SeverityBlock,
				Category:   "security",
				File:       file,
				Line:       i + 1,
				Message:    "Possible SQL injection: string concatenation in query",
				Suggestion: "Use parameterized queries ($1, ?) instead",
				Rule:       "sql-injection",
			})
		}
	}
	return findings
}

func checkLargeFunction(file, content string) []Finding {
	if !strings.HasSuffix(file, ".go") {
		return nil
	}
	if findings := checkLargeFunctionAST(file, content); findings != nil {
		return findings
	}
	return checkLargeFunctionRegex(file, content)
}

// checkLargeFunctionAST uses go/parser for precise function boundaries.
func checkLargeFunctionAST(file, content string) []Finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, content, 0)
	if err != nil {
		return nil
	}

	var findings []Finding
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		startLine := fset.Position(fn.Pos()).Line
		endLine := fset.Position(fn.End()).Line
		length := endLine - startLine
		if length > 100 {
			findings = append(findings, Finding{
				Severity:   SeverityInfo,
				Category:   "complexity",
				File:       file,
				Line:       startLine,
				Message:    fmt.Sprintf("Function %s is %d lines long", fn.Name.Name, length),
				Suggestion: "Consider breaking into smaller functions",
				Rule:       "large-function",
			})
		}
	}
	return findings
}

// checkLargeFunctionRegex is the fallback for unparseable Go source.
func checkLargeFunctionRegex(file, content string) []Finding {
	var findings []Finding
	lines := strings.Split(content, "\n")
	funcStart := -1
	funcName := ""
	depth := 0

	funcRe := regexp.MustCompile(`^func\s+(?:\(\w+\s+\*?\w+\)\s+)?(\w+)\s*\(`)

	for i, line := range lines {
		if m := funcRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			funcStart = i
			funcName = m[1]
			depth = 0
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if funcStart >= 0 && depth <= 0 && i > funcStart {
			length := i - funcStart
			if length > 100 {
				findings = append(findings, Finding{
					Severity:   SeverityInfo,
					Category:   "complexity",
					File:       file,
					Line:       funcStart + 1,
					Message:    fmt.Sprintf("Function %s is %d lines long", funcName, length),
					Suggestion: "Consider breaking into smaller functions",
					Rule:       "large-function",
				})
			}
			funcStart = -1
		}
	}
	return findings
}

var goStmtRe = regexp.MustCompile(`go\s+func\s*\(`)
var recoverRe = regexp.MustCompile(`recover\(\)`)

func checkGoroutineRecover(file, content string) []Finding {
	if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
		return nil
	}
	var findings []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if goStmtRe.MatchString(line) {
			found := false
			end := i + 6
			if end > len(lines) {
				end = len(lines)
			}
			for j := i; j < end; j++ {
				if recoverRe.MatchString(lines[j]) {
					found = true
					break
				}
			}
			if !found {
				findings = append(findings, Finding{
					Severity:   SeverityWarn,
					Category:   "correctness",
					File:       file,
					Line:       i + 1,
					Message:    "Goroutine launched without defer/recover — panics will crash the process",
					Suggestion: "Add defer func() { if r := recover(); r != nil { ... } }()",
					Rule:       "goroutine-recover",
				})
			}
		}
	}
	return findings
}

func buildSummary(v *Verdict) string {
	blocks, warns, infos := 0, 0, 0
	for _, f := range v.Findings {
		switch f.Severity {
		case SeverityBlock:
			blocks++
		case SeverityWarn:
			warns++
		case SeverityInfo:
			infos++
		}
	}

	if len(v.Findings) == 0 {
		return "Clean: no issues found"
	}

	parts := []string{}
	if blocks > 0 {
		parts = append(parts, fmt.Sprintf("%d blocking", blocks))
	}
	if warns > 0 {
		parts = append(parts, fmt.Sprintf("%d warnings", warns))
	}
	if infos > 0 {
		parts = append(parts, fmt.Sprintf("%d info", infos))
	}

	verdict := "PASS"
	if !v.Pass {
		verdict = "BLOCKED"
	}
	return fmt.Sprintf("%s: %s (score: %.2f)", verdict, strings.Join(parts, ", "), v.Score)
}
