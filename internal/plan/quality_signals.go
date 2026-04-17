// Package plan — quality_signals.go
//
// Deterministic quality scanners. No LLM calls. No prompt tuning.
// Each scanner answers a yes/no question about a file and either
// flags a concrete issue or doesn't. Designed to fire early and
// often: per-commit, per-task, per-session.
//
// Philosophy: structural signals that LLM judges routinely miss
// because they pattern-match on "it looks done" rather than reading
// bodies. These checks look at the body.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// QualitySeverity classifies a finding's actionability.
type QualitySeverity int

const (
	// SevBlocking means the gate should NOT pass. Hollow shell in a
	// declared file is a clear rubber-stamp signal.
	SevBlocking QualitySeverity = iota
	// SevAdvisory means surface it but don't fail the gate. E.g. mock
	// data appearing in production paths is worth investigating but
	// sometimes legitimate (seed scripts).
	SevAdvisory
)

func (s QualitySeverity) String() string {
	switch s {
	case SevBlocking:
		return "BLOCKING"
	case SevAdvisory:
		return "ADVISORY"
	}
	return "?"
}

// QualityFinding is a single scanner hit. Kind is a short category
// label, File is the relative path, Line is 1-indexed, Detail is a
// one-line human explanation.
type QualityFinding struct {
	Severity QualitySeverity
	Kind     string
	File     string
	Line     int
	Detail   string
}

// QualityReport aggregates scanner output across a set of files.
type QualityReport struct {
	Findings     []QualityFinding
	BlockingN    int
	AdvisoryN    int
	FilesScanned int
}

// Blocking reports whether any scanner emitted a blocking finding.
// Gate integrations should treat this as "the worker's claim of
// complete is not trustworthy; keep the repair loop running".
func (r *QualityReport) Blocking() bool {
	return r != nil && r.BlockingN > 0
}

// Summary renders a one-line count.
func (r *QualityReport) Summary() string {
	if r == nil {
		return "no scan"
	}
	return fmt.Sprintf("%d files scanned → %d blocking, %d advisory",
		r.FilesScanned, r.BlockingN, r.AdvisoryN)
}

// RunQualitySweep runs every deterministic scanner against the given
// files (paths relative to repoRoot). Non-existent / unreadable files
// are silently skipped (not flagged — existence is handled elsewhere).
//
// Each scanner is independent and fast. Expected runtime on a 1000-
// file repo: <200ms.
func RunQualitySweep(repoRoot string, files []string) *QualityReport {
	r := &QualityReport{}
	if len(files) == 0 {
		return r
	}

	type fileBlob struct {
		rel     string
		abs     string
		content string
		lines   []string
	}
	blobs := make([]fileBlob, 0, len(files))
	for _, rel := range files {
		abs := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		content := string(data)
		blobs = append(blobs, fileBlob{
			rel:     rel,
			abs:     abs,
			content: content,
			lines:   strings.Split(content, "\n"),
		})
	}
	r.FilesScanned = len(blobs)

	for _, b := range blobs {
		r.Findings = append(r.Findings, scanHollowShells(b.rel, b.content, b.lines)...)
		r.Findings = append(r.Findings, scanSkippedTests(b.rel, b.lines)...)
		r.Findings = append(r.Findings, scanWeakAssertions(b.rel, b.lines)...)
		r.Findings = append(r.Findings, scanSilentCatches(b.rel, b.lines)...)
		r.Findings = append(r.Findings, scanMockData(b.rel, b.content, b.lines)...)
	}

	// Cross-file: identical function bodies (copy-paste scaffolds).
	pathToContent := make(map[string]string, len(blobs))
	for _, b := range blobs {
		pathToContent[b.rel] = b.content
	}
	r.Findings = append(r.Findings, scanIdenticalBodies(pathToContent)...)

	for _, f := range r.Findings {
		switch f.Severity {
		case SevBlocking:
			r.BlockingN++
		case SevAdvisory:
			r.AdvisoryN++
		}
	}
	return r
}

// FormatQualityReport renders a human-readable report suitable for
// CLI output or inclusion in a repair prompt.
func FormatQualityReport(r *QualityReport) string {
	if r == nil || len(r.Findings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "QUALITY SWEEP: %s\n", r.Summary())
	// Group by severity then file, stable order
	bySev := map[QualitySeverity][]QualityFinding{}
	for _, f := range r.Findings {
		bySev[f.Severity] = append(bySev[f.Severity], f)
	}
	for _, sev := range []QualitySeverity{SevBlocking, SevAdvisory} {
		items := bySev[sev]
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].File != items[j].File {
				return items[i].File < items[j].File
			}
			return items[i].Line < items[j].Line
		})
		fmt.Fprintf(&b, "\n[%s] %d finding(s):\n", sev, len(items))
		for _, f := range items {
			fmt.Fprintf(&b, "  %s:%d  %s — %s\n", f.File, f.Line, f.Kind, f.Detail)
		}
	}
	return b.String()
}

// ───────────────────────── scanners ─────────────────────────

// scanHollowShells: flag functions whose body is trivial (≤ 3 lines
// of real code, excluding braces and comments). Catches the most
// common rubber-stamp pattern: worker declares a file, writes a
// signature, returns `null` or an empty JSX fragment.
//
// Heuristics (JS/TS focus; applies to any .ts/.tsx/.js/.jsx path):
//   - Arrow function assigned to a const with a single `return null`
//     or `return undefined` body.
//   - `function X() { }` or `function X() { return ... }` where the
//     return literal is trivial (null, undefined, empty obj, empty arr,
//     empty string, 0, false, a plain React fragment).
//   - React components that return only `<View/>`, `<></>`, `null`.
//   - API route handlers that only call `res.status(200).send()` or
//     `res.json({})` with no branching.
//
// We skip test files (*.test.*, *.spec.*, __tests__/), type-definition
// files (*.d.ts), and generated files (matching "generated").
func scanHollowShells(rel, content string, lines []string) []QualityFinding {
	if isTestFile(rel) || isTypeDefFile(rel) || isGeneratedFile(rel, content) {
		return nil
	}
	if !looksLikeCode(rel) {
		return nil
	}

	var out []QualityFinding

	// Arrow function: `export const X = (...) => null;` / `=> undefined`
	// / `=> (<></>)` / `=> <View/>` / `=> ({})` / `=> []`.
	// Also the curly-brace variant with a single `return` statement.
	// All of these are rubber-stamp body patterns a reviewer should
	// reject on sight.
	arrowTrivial := regexp.MustCompile(
		`(?m)^\s*(?:export\s+)?(?:default\s+)?const\s+(\w+)\s*(?::\s*[^=]+)?=\s*` +
			`(?:async\s+)?` +
			`\([^)]*\)\s*(?::\s*[^=]+)?=>\s*` +
			`(?:` +
			`null\b|undefined\b|\{\s*\}|\[\s*\]|""|''|\x60\x60|0\b|false\b|true\b` +
			`|\(\s*<\s*>\s*<\/\s*>\s*\)` +
			`|\(?\s*<\s*\w+\s*\/\s*>\s*\)?` +
			`)` +
			`\s*;?`,
	)
	for _, m := range arrowTrivial.FindAllStringSubmatchIndex(content, -1) {
		line := 1 + strings.Count(content[:m[0]], "\n")
		name := content[m[2]:m[3]]
		out = append(out, QualityFinding{
			Severity: SevBlocking,
			Kind:     "hollow-arrow",
			File:     rel,
			Line:     line,
			Detail: fmt.Sprintf(
				"`%s` is an arrow that returns a trivial value (null / empty / fragment). Body has no real logic.",
				name),
		})
	}

	// Curly-body arrow or function with single trivial `return`:
	//   const X = (...) => { return null }
	//   function X() { return null }
	//   async function X() { return null }
	trivialReturn := regexp.MustCompile(
		`(?m)` +
			`(?:^|\W)(?:const\s+(\w+)\s*=\s*(?:async\s+)?\([^)]*\)\s*(?::\s*[^=]+)?=>|function\s+(\w+)\s*\([^)]*\)|(?:async\s+)?function\s+(\w+)\s*\([^)]*\))` +
			`\s*\{\s*return\s+` +
			`(?:null|undefined|\{\s*\}|\[\s*\]|""|''|0|false|true|\(\s*<\s*>\s*<\/\s*>\s*\))` +
			`\s*;?\s*\}`,
	)
	for _, m := range trivialReturn.FindAllStringSubmatchIndex(content, -1) {
		line := 1 + strings.Count(content[:m[0]], "\n")
		name := ""
		for i := 2; i <= 7 && i+1 < len(m); i += 2 {
			if m[i] >= 0 && m[i+1] > m[i] {
				name = content[m[i]:m[i+1]]
				break
			}
		}
		if name == "" {
			name = "(anonymous)"
		}
		out = append(out, QualityFinding{
			Severity: SevBlocking,
			Kind:     "hollow-body",
			File:     rel,
			Line:     line,
			Detail: fmt.Sprintf(
				"`%s` body is a single trivial return. No real logic.", name),
		})
	}

	// React functional component returning only a self-closing
	// single tag: `return <View/>` / `return <></>`.
	componentEmpty := regexp.MustCompile(
		`(?m)return\s*\(?\s*(?:<\s*>\s*<\/\s*>|<\s*\w+\s*\/\s*>)\s*\)?\s*;?`,
	)
	if isReactFile(rel) {
		for _, m := range componentEmpty.FindAllStringIndex(content, -1) {
			line := 1 + strings.Count(content[:m[0]], "\n")
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "empty-jsx",
				File:     rel,
				Line:     line,
				Detail:   "component returns a self-closing tag or empty fragment — no real rendering logic.",
			})
		}
	}

	// Empty route handlers:
	//   res.json({})
	//   res.status(\d+).send()
	//   res.status(\d+).json({})
	//   res.sendStatus(200)
	//   c.json({}) — Hono
	//   return NextResponse.json({})
	routeEmpty := regexp.MustCompile(
		`(?m)\b(?:res|c|ctx)\.(?:json\s*\(\s*\{\s*\}\s*\)|status\s*\(\s*\d+\s*\)\s*\.(?:send|json)\s*\(\s*(?:\{\s*\}\s*)?\)|sendStatus\s*\(\s*\d+\s*\))`,
	)
	if isServerFile(rel) {
		for _, m := range routeEmpty.FindAllStringIndex(content, -1) {
			line := 1 + strings.Count(content[:m[0]], "\n")
			// Only flag if the enclosing function's body has < ~5 lines
			// total — a real handler that also does real work then sends
			// an empty JSON isn't necessarily hollow. For this early
			// heuristic we flag indiscriminately; refine later if FP rate
			// is high.
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "empty-route",
				File:     rel,
				Line:     line,
				Detail:   "route handler returns an empty JSON body or bare status — probably scaffolded, no real behavior.",
			})
		}
	}

	_ = lines // reserved for future line-based checks
	return out
}

// scanSkippedTests: flag disabled/pending tests. If the SOW's AC says
// "tests pass" and the test is marked skipped, the AC is passing on
// nothing.
//
// Triggers:
//   - `.skip(` on it / test / describe / context
//   - `xit(` / `xdescribe(` (Mocha disabled prefix)
//   - `test.todo(` / `it.todo(`
//   - bare `pending()` (Jasmine)
func scanSkippedTests(rel string, lines []string) []QualityFinding {
	if !isTestFile(rel) {
		return nil
	}
	patterns := []struct {
		rx   *regexp.Regexp
		kind string
	}{
		{regexp.MustCompile(`\b(?:it|test|describe|context)\.skip\s*\(`), "skipped-test"},
		{regexp.MustCompile(`\b(?:xit|xdescribe|xcontext|xtest)\s*\(`), "skipped-test"},
		{regexp.MustCompile(`\b(?:it|test)\.todo\s*\(`), "todo-test"},
		{regexp.MustCompile(`(?m)^\s*pending\s*\(\s*\)\s*;?\s*$`), "pending-test"},
	}
	var out []QualityFinding
	for i, ln := range lines {
		for _, p := range patterns {
			if p.rx.MatchString(ln) {
				out = append(out, QualityFinding{
					Severity: SevBlocking,
					Kind:     p.kind,
					File:     rel,
					Line:     i + 1,
					Detail:   "disabled / not-yet-implemented test — AC claim 'tests pass' is invalid against a skipped test.",
				})
				break
			}
		}
	}
	return out
}

// scanWeakAssertions: flag tests that don't actually assert anything
// meaningful. Common rubber-stamp pattern: author adds a test file
// (makes AC "has tests" pass) whose single assertion is `expect(true)
// .toBe(true)`.
//
// Triggers (test files only):
//   - expect(true).toBe(true) / .toEqual(true) / .toBeTruthy()
//   - expect(1).toBe(1) / expect(0).toBe(0)
//   - assert(true) / assert.ok(true)
//   - Empty test body: `it('...', () => {})` / `test('...', () => {})`
func scanWeakAssertions(rel string, lines []string) []QualityFinding {
	if !isTestFile(rel) {
		return nil
	}
	tautology := regexp.MustCompile(
		`\bexpect\s*\(\s*(?:true|false|1|0|null|undefined|""|''|\[\s*\]|\{\s*\})\s*\)\s*\.(?:toBe|toEqual|toStrictEqual)\s*\(\s*(?:true|false|1|0|null|undefined|""|''|\[\s*\]|\{\s*\})\s*\)`,
	)
	trivialTruthy := regexp.MustCompile(
		`\bexpect\s*\(\s*(?:true|1|"[^"]+"|'[^']+')\s*\)\s*\.(?:toBeTruthy|toBeDefined|toBeDefined)\s*\(\s*\)`,
	)
	assertTrue := regexp.MustCompile(
		`\b(?:assert|chai\.assert)(?:\.ok)?\s*\(\s*(?:true|1|"[^"]+"|'[^']+')\s*(?:,.*)?\s*\)`,
	)
	emptyTest := regexp.MustCompile(
		`\b(?:it|test)\s*\(\s*['"][^'"]*['"]\s*,\s*(?:async\s*)?\(\s*\)\s*=>\s*\{\s*\}\s*\)`,
	)
	var out []QualityFinding
	for i, ln := range lines {
		if tautology.MatchString(ln) {
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "tautology-assertion",
				File:     rel,
				Line:     i + 1,
				Detail:   "expect(constant).toBe(same constant) — assertion cannot fail, test is not testing anything.",
			})
		} else if trivialTruthy.MatchString(ln) {
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "trivial-truthy",
				File:     rel,
				Line:     i + 1,
				Detail:   "expect(truthyLiteral).toBeTruthy() — assertion cannot fail.",
			})
		} else if assertTrue.MatchString(ln) {
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "assert-literal",
				File:     rel,
				Line:     i + 1,
				Detail:   "assert(trueLiteral) — assertion cannot fail.",
			})
		} else if emptyTest.MatchString(ln) {
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "empty-test-body",
				File:     rel,
				Line:     i + 1,
				Detail:   "it/test with empty body — test passes unconditionally.",
			})
		}
	}
	return out
}

// scanSilentCatches: flag try/catch blocks that swallow the error
// silently (empty catch body, or catch binding with underscore and
// no handling). A route handler that eats errors will always "work"
// in the AC's happy-path check but fail in production.
//
// This is a later-fire companion to the existing stub-scan catches
// in sow_native.go; the existing one fires at per-file scan time,
// this one is intended to run on per-commit diffs.
func scanSilentCatches(rel string, lines []string) []QualityFinding {
	if isTestFile(rel) || !looksLikeCode(rel) {
		return nil
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\}\s*catch\s*\([^)]*\)\s*\{\s*\}`),
		regexp.MustCompile(`\.catch\s*\(\s*\(?\s*_*\s*\)?\s*=>\s*\{\s*\}\s*\)`),
		regexp.MustCompile(`\.catch\s*\(\s*\(?\s*\)\s*=>\s*null\s*\)`),
		regexp.MustCompile(`\.catch\s*\(\s*\(?\s*\)\s*=>\s*undefined\s*\)`),
	}
	var out []QualityFinding
	for i, ln := range lines {
		for _, rx := range patterns {
			if rx.MatchString(ln) {
				out = append(out, QualityFinding{
					Severity: SevBlocking,
					Kind:     "silent-catch",
					File:     rel,
					Line:     i + 1,
					Detail:   "try/catch or .catch swallows the error with no logging, rethrow, or handling — masks real failures.",
				})
				break
			}
		}
	}
	return out
}

// scanMockData: flag fixture-y names in production paths. Alice/Bob
// /foo/bar/lorem-ipsum/example@example.com/555-prefix phone numbers
// showing up OUTSIDE tests/fixtures usually means the worker wired
// in scaffolded "demo data" where the SOW asked for real logic.
//
// Advisory-severity: legitimate seed scripts / storybook stories use
// these names too; flag for visibility without auto-failing.
func scanMockData(rel, content string, lines []string) []QualityFinding {
	if isTestFile(rel) || isFixtureFile(rel) || isStorybookFile(rel) || !looksLikeCode(rel) {
		return nil
	}
	// We require MULTIPLE distinct mock-data hits in a single file
	// before flagging, to avoid false positives on constants named
	// "foo" in library code.
	hits := 0
	names := regexp.MustCompile(`\b(?:Alice|Bob|Charlie|Eve|Mallory|lorem\s+ipsum)\b`)
	emails := regexp.MustCompile(`['"]\w+@example\.(?:com|org)['"]`)
	phones := regexp.MustCompile(`['"]?\(?\+?1?[-.\s]?\(?555[-.\s)]?\s*\d{3}[-.\s]?\d{4}['"]?`)
	var firstLine int
	for i, ln := range lines {
		if names.MatchString(ln) || emails.MatchString(ln) || phones.MatchString(ln) {
			hits++
			if firstLine == 0 {
				firstLine = i + 1
			}
		}
	}
	if hits >= 3 {
		return []QualityFinding{{
			Severity: SevAdvisory,
			Kind:     "mock-data-in-prod",
			File:     rel,
			Line:     firstLine,
			Detail: fmt.Sprintf(
				"%d instances of fixture-style names / example-domain emails / 555-prefix phones in non-test file — may be scaffolded demo data.",
				hits),
		}}
	}
	_ = content
	return nil
}

// scanIdenticalBodies: cross-file duplicate detection. Hash function
// bodies; groups of size ≥ 4 that share a hash are likely scaffold
// copy-paste. Fires blocking because "5 route handlers with the same
// body" is almost never intentional.
func scanIdenticalBodies(paths map[string]string) []QualityFinding {
	type loc struct {
		file string
		line int
		name string
	}
	// Extract function bodies. Use a lenient regex that captures
	// anything shaped like a named function with a brace body.
	bodyRx := regexp.MustCompile(
		`(?m)(?:function\s+(\w+)|const\s+(\w+)\s*=\s*(?:async\s+)?\([^)]*\)\s*=>)\s*\{((?:[^{}]|\{[^{}]*\})*)\}`,
	)
	bodyMap := make(map[string][]loc)
	for path, content := range paths {
		if !looksLikeCode(path) {
			continue
		}
		for _, m := range bodyRx.FindAllStringSubmatchIndex(content, -1) {
			if len(m) < 8 {
				continue
			}
			// Skip trivial bodies (< 40 bytes after whitespace collapse)
			bodyStart, bodyEnd := m[6], m[7]
			if bodyStart < 0 || bodyEnd <= bodyStart {
				continue
			}
			raw := content[bodyStart:bodyEnd]
			squashed := strings.Join(strings.Fields(raw), " ")
			if len(squashed) < 40 {
				continue
			}
			h := sha256.Sum256([]byte(squashed))
			key := hex.EncodeToString(h[:8])
			name := ""
			if m[2] >= 0 {
				name = content[m[2]:m[3]]
			} else if m[4] >= 0 {
				name = content[m[4]:m[5]]
			}
			line := 1 + strings.Count(content[:m[0]], "\n")
			bodyMap[key] = append(bodyMap[key], loc{file: path, line: line, name: name})
		}
	}
	var out []QualityFinding
	for _, locs := range bodyMap {
		if len(locs) < 4 {
			continue
		}
		// Sort for stable reporting.
		sort.Slice(locs, func(i, j int) bool {
			if locs[i].file != locs[j].file {
				return locs[i].file < locs[j].file
			}
			return locs[i].line < locs[j].line
		})
		var siblings []string
		for _, l := range locs[1:] {
			siblings = append(siblings, fmt.Sprintf("%s:%d(%s)", l.file, l.line, l.name))
		}
		out = append(out, QualityFinding{
			Severity: SevBlocking,
			Kind:     "duplicate-body",
			File:     locs[0].file,
			Line:     locs[0].line,
			Detail: fmt.Sprintf(
				"function `%s` body is duplicated in %d other location(s) — likely scaffold copy-paste: %s",
				locs[0].name, len(locs)-1, strings.Join(siblings, ", ")),
		})
	}
	return out
}

// ───────────────────────── helpers ─────────────────────────

func looksLikeCode(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

func isTestFile(rel string) bool {
	low := strings.ToLower(rel)
	if strings.Contains(low, "/__tests__/") || strings.Contains(low, "__tests__\\") {
		return true
	}
	if strings.Contains(low, "/test/") || strings.Contains(low, "/tests/") {
		return true
	}
	if strings.Contains(low, ".test.") || strings.Contains(low, ".spec.") {
		return true
	}
	return false
}

func isFixtureFile(rel string) bool {
	low := strings.ToLower(rel)
	return strings.Contains(low, "/fixtures/") ||
		strings.Contains(low, "/mocks/") ||
		strings.Contains(low, "/seed") ||
		strings.Contains(low, "/factories/")
}

func isStorybookFile(rel string) bool {
	low := strings.ToLower(rel)
	return strings.Contains(low, ".stories.") ||
		strings.Contains(low, ".story.") ||
		strings.Contains(low, "/stories/")
}

func isTypeDefFile(rel string) bool {
	return strings.HasSuffix(strings.ToLower(rel), ".d.ts")
}

func isReactFile(rel string) bool {
	low := strings.ToLower(rel)
	return strings.HasSuffix(low, ".tsx") || strings.HasSuffix(low, ".jsx")
}

func isServerFile(rel string) bool {
	low := strings.ToLower(rel)
	// Heuristic: anything under /api/, /routes/, /handlers/, /server/,
	// or with "route" / "handler" / "controller" in the filename.
	if strings.Contains(low, "/api/") || strings.Contains(low, "/routes/") ||
		strings.Contains(low, "/handlers/") || strings.Contains(low, "/server/") ||
		strings.Contains(low, "/controllers/") {
		return true
	}
	base := filepath.Base(low)
	return strings.Contains(base, "route") || strings.Contains(base, "handler") ||
		strings.Contains(base, "controller")
}

func isGeneratedFile(rel, content string) bool {
	if strings.Contains(rel, "/generated/") || strings.Contains(rel, "/dist/") ||
		strings.Contains(rel, "/build/") || strings.Contains(rel, ".gen.") {
		return true
	}
	head := content
	if len(head) > 500 {
		head = head[:500]
	}
	low := strings.ToLower(head)
	return strings.Contains(low, "autogenerated") ||
		strings.Contains(low, "auto-generated") ||
		strings.Contains(low, "do not edit")
}
