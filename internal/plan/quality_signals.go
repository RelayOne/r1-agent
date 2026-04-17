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

// QualityConfig is the feature-gate for the deterministic scanners.
// Each bool toggles one scanner. This exists so operators can A/B
// individual gates and switch to whatever shape works best against
// the observed cohort — "all gates on" is the default but may be
// too noisy for some SOWs.
//
// Overlay via env var STOKE_QS_DISABLE="orphan-file,duplicate-body"
// (comma-separated gate names) or STOKE_QS_ENABLE_ONLY="hollow-shell,
// skipped-test" (exclusive list — everything else off).
type QualityConfig struct {
	HollowShells     bool // hollow-arrow / hollow-body / empty-jsx / empty-route
	SkippedTests     bool // skipped-test / todo-test / pending-test
	WeakAssertions   bool // tautology-assertion / trivial-truthy / assert-literal / empty-test-body
	SilentCatches    bool // silent-catch
	MockData         bool // mock-data-in-prod (advisory)
	IdenticalBodies  bool // duplicate-body
	NoExports        bool // no-exports
	GitActivity      bool // git-no-real-activity
	OrphanReferences bool // orphan-file

	// Experimental — default off until validated. Opt-in via env.
	SOWEndpoints   bool // sow-endpoint-missing
	SOWStructural  bool // sow-claim-missing
	PackageScripts bool // package-script-missing
	RuntimeSmoke   bool // runtime-smoke (requires subprocess, expensive)
}

// DefaultQualityConfig returns the production default: all validated
// scanners on, experimentals off. Callers that want everything-on
// should explicitly set the experimental fields.
func DefaultQualityConfig() QualityConfig {
	return QualityConfig{
		HollowShells:     true,
		SkippedTests:     true,
		WeakAssertions:   true,
		SilentCatches:    true,
		MockData:         true,
		IdenticalBodies:  true,
		NoExports:        true,
		GitActivity:      true,
		OrphanReferences: true,
		// Experimental — off by default:
		SOWEndpoints:   false,
		SOWStructural:  false,
		PackageScripts: false,
		RuntimeSmoke:   false,
	}
}

// gateNameMap maps canonical gate IDs (stable, documented) to the
// pointer in QualityConfig that controls them. Used by env-var
// overlay and CLI flag parsing.
func gateNameMap(cfg *QualityConfig) map[string]*bool {
	return map[string]*bool{
		"hollow-shell":    &cfg.HollowShells,
		"skipped-test":    &cfg.SkippedTests,
		"weak-assertion":  &cfg.WeakAssertions,
		"silent-catch":    &cfg.SilentCatches,
		"mock-data":       &cfg.MockData,
		"duplicate-body":  &cfg.IdenticalBodies,
		"no-exports":      &cfg.NoExports,
		"git-activity":    &cfg.GitActivity,
		"orphan-file":     &cfg.OrphanReferences,
		"sow-endpoints":   &cfg.SOWEndpoints,
		"sow-structural":  &cfg.SOWStructural,
		"package-scripts": &cfg.PackageScripts,
		"runtime-smoke":   &cfg.RuntimeSmoke,
	}
}

// GateNames returns all known gate IDs in stable order. Useful for
// CLI help text and observability logs.
func GateNames() []string {
	dummy := QualityConfig{}
	m := gateNameMap(&dummy)
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// LoadQualityConfigFromEnv returns the default config overlaid with
// env-var switches. Precedence:
//  1. STOKE_QS_ENABLE_ONLY="a,b,c" — everything off except the named
//     gates. Highest-priority override.
//  2. STOKE_QS_DISABLE="a,b" — start from default, turn off named
//     gates.
//  3. STOKE_QS_ENABLE="a,b" — start from default, turn ON named gates
//     (useful for opting into experimentals).
//
// Unknown names in any of these lists are logged to stderr and
// ignored (not fatal — we don't want a typo in an env var to break
// the harness).
func LoadQualityConfigFromEnv() QualityConfig {
	cfg := DefaultQualityConfig()
	m := gateNameMap(&cfg)

	apply := func(raw string, val bool, onUnknown string) {
		if raw == "" {
			return
		}
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			p, ok := m[name]
			if !ok {
				fmt.Fprintf(os.Stderr, "quality-signals: %s: unknown gate %q (known: %s)\n",
					onUnknown, name, strings.Join(GateNames(), ", "))
				continue
			}
			*p = val
		}
	}

	if only := os.Getenv("STOKE_QS_ENABLE_ONLY"); only != "" {
		for _, p := range m {
			*p = false
		}
		apply(only, true, "STOKE_QS_ENABLE_ONLY")
		return cfg
	}
	apply(os.Getenv("STOKE_QS_DISABLE"), false, "STOKE_QS_DISABLE")
	apply(os.Getenv("STOKE_QS_ENABLE"), true, "STOKE_QS_ENABLE")
	return cfg
}

// Enabled returns the list of gate IDs that are currently on, for
// logging.
func (c QualityConfig) Enabled() []string {
	// Copy c so we can iterate with the name map without mutating.
	cc := c
	m := gateNameMap(&cc)
	var out []string
	for name, p := range m {
		if *p {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

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

// RunQualitySweep runs every scanner enabled by the env-loaded config.
// Equivalent to RunQualitySweepWithConfig(repoRoot, files, nil, LoadQualityConfigFromEnv()).
// Callers with explicit config should use the With variant.
func RunQualitySweep(repoRoot string, files []string) *QualityReport {
	return RunQualitySweepWithConfig(repoRoot, files, nil, LoadQualityConfigFromEnv())
}

// RunQualitySweepForSOW is the full-power entry point: runs all enabled
// scanners INCLUDING the SOW-scoped ones (endpoints, structural claims,
// package scripts) that require the SOW prose to operate. When sow is
// nil, behaves like RunQualitySweep (no SOW-scoped scanners fire).
func RunQualitySweepForSOW(repoRoot string, files []string, sow *SOW) *QualityReport {
	return RunQualitySweepWithConfig(repoRoot, files, sow, LoadQualityConfigFromEnv())
}

// RunQualitySweepWithConfig is the explicit-config entry point. Each
// scanner only fires if its gate is on in cfg. This is where A/B
// testing different gate combinations happens — write the QualityConfig,
// pass it in, compare results across runs.
//
// Non-existent / unreadable files are silently skipped.
// Expected runtime on a 1000-file repo: <500ms including SOW scanners.
func RunQualitySweepWithConfig(repoRoot string, files []string, sow *SOW, cfg QualityConfig) *QualityReport {
	r := &QualityReport{}
	if len(files) == 0 && sow == nil {
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
		if cfg.HollowShells {
			r.Findings = append(r.Findings, scanHollowShells(b.rel, b.content, b.lines)...)
		}
		if cfg.SkippedTests {
			r.Findings = append(r.Findings, scanSkippedTests(b.rel, b.lines)...)
		}
		if cfg.WeakAssertions {
			r.Findings = append(r.Findings, scanWeakAssertions(b.rel, b.lines)...)
		}
		if cfg.SilentCatches {
			r.Findings = append(r.Findings, scanSilentCatches(b.rel, b.lines)...)
		}
		if cfg.MockData {
			r.Findings = append(r.Findings, scanMockData(b.rel, b.content, b.lines)...)
		}
		if cfg.NoExports {
			r.Findings = append(r.Findings, scanNoExports(b.rel, b.content)...)
		}
		if cfg.GitActivity {
			r.Findings = append(r.Findings, scanGitActivity(repoRoot, b.rel)...)
		}
	}

	if cfg.IdenticalBodies {
		pathToContent := make(map[string]string, len(blobs))
		for _, b := range blobs {
			pathToContent[b.rel] = b.content
		}
		r.Findings = append(r.Findings, scanIdenticalBodies(pathToContent)...)
	}

	if cfg.OrphanReferences {
		declaredPaths := make([]string, 0, len(blobs))
		for _, b := range blobs {
			declaredPaths = append(declaredPaths, b.rel)
		}
		r.Findings = append(r.Findings, scanOrphanReferences(repoRoot, declaredPaths)...)
	}

	// SOW-scoped scanners (endpoint contracts, structural claims,
	// package scripts). These read the SOW prose and require it to
	// be non-empty. Experimental — off by default.
	if sow != nil {
		sowText := collectSOWText(sow)
		if cfg.SOWEndpoints && sowText != "" {
			r.Findings = append(r.Findings, scanSOWEndpointContracts(repoRoot, sowText)...)
		}
		if cfg.SOWStructural && sowText != "" {
			r.Findings = append(r.Findings, scanSOWStructuralClaims(repoRoot, sowText)...)
		}
		if cfg.PackageScripts && sowText != "" {
			r.Findings = append(r.Findings, scanPackageScripts(repoRoot, sowText)...)
		}
	}

	// Cap findings at 50 per scanner+file combination to avoid
	// spamming review prompts when a single file has dozens of
	// hits of the same kind (e.g. a test file with 40 `.skip()`
	// calls). Downstream consumers can still see the count but
	// not the full payload for the overflow.
	const maxFindingsPerKind = 50
	counter := map[string]int{}
	var capped []QualityFinding
	var overflow int
	for _, f := range r.Findings {
		key := f.Kind + "|" + f.File
		counter[key]++
		if counter[key] <= maxFindingsPerKind {
			capped = append(capped, f)
		} else {
			overflow++
		}
	}
	if overflow > 0 {
		capped = append(capped, QualityFinding{
			Severity: SevAdvisory,
			Kind:     "findings-truncated",
			File:     "(cap)",
			Line:     0,
			Detail: fmt.Sprintf(
				"%d additional findings suppressed (cap: %d per kind+file). Full results available by re-running with STOKE_QS_DISABLE specific kinds.",
				overflow, maxFindingsPerKind),
		})
	}
	r.Findings = capped
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

// scanNoExports: flag declared production files whose body has code
// but no named/default export at all. A "file exists" check is
// satisfied but anything importing the declared name will fail.
// This is the "worker forgot to actually export the thing" pattern.
//
// Skips files that are entry points (index.ts at app root, route.tsx
// handler conventions in Next.js which allow HTTP-method exports
// only), test files, fixtures, and type-defs.
func scanNoExports(rel, content string) []QualityFinding {
	if !looksLikeCode(rel) || isTestFile(rel) || isFixtureFile(rel) ||
		isStorybookFile(rel) || isTypeDefFile(rel) || isGeneratedFile(rel, content) {
		return nil
	}
	// File must have real content (>80 bytes excluding whitespace)
	// to warrant an export check — tiny files are caught elsewhere.
	dense := strings.Join(strings.Fields(content), "")
	if len(dense) < 80 {
		return nil
	}
	// Accept any form of export: `export foo`, `export { ... }`,
	// `export default`, `module.exports =`, `exports.foo =`,
	// `export * from`, CommonJS patterns.
	exportRx := regexp.MustCompile(
		`(?m)(?:^|\s)(?:export\s+(?:default|const|let|var|function|async\s+function|class|type|interface|enum|\*|\{)|module\.exports\s*=|exports\.\w+\s*=)`,
	)
	if exportRx.MatchString(content) {
		return nil
	}
	return []QualityFinding{{
		Severity: SevBlocking,
		Kind:     "no-exports",
		File:     rel,
		Line:     1,
		Detail: "file has real code (>80 non-whitespace bytes) but no named/default/CommonJS export — nothing can import from this file. Likely a declared-but-never-wired scaffold.",
	}}
}

// scanGitActivity: flag declared files that exist on disk but have
// never been touched in a real commit (only appear in an initial
// bulk-scaffold commit). A file whose only git history is "add
// scaffold" has zero real implementation investment.
//
// Rule: file must have at least 2 commits touching it OR 1 commit
// that isn't the very first commit in the repo. Skips test files
// (tests legitimately land once and never change).
func scanGitActivity(repoRoot, rel string) []QualityFinding {
	if isTestFile(rel) || isFixtureFile(rel) || isStorybookFile(rel) ||
		isTypeDefFile(rel) || !looksLikeCode(rel) {
		return nil
	}
	// `git log --format=%H -- <path>` gives one line per commit.
	out, err := runGit(repoRoot, "log", "--format=%H", "--", rel)
	if err != nil {
		return nil
	}
	commits := strings.Split(strings.TrimSpace(out), "\n")
	if len(commits) == 0 || commits[0] == "" {
		// File is untracked — can't judge activity yet; likely the
		// worker just wrote it in this session. Don't flag.
		return nil
	}
	if len(commits) >= 2 {
		return nil // multiple touches → real activity
	}
	// Only one commit touched this file. Check whether that commit
	// is the repo's very first commit (scaffold seed). If so, flag.
	firstCommit, err := runGit(repoRoot, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return nil
	}
	firstCommit = strings.TrimSpace(firstCommit)
	if strings.TrimSpace(commits[0]) == firstCommit {
		return []QualityFinding{{
			Severity: SevBlocking,
			Kind:     "git-no-real-activity",
			File:     rel,
			Line:     1,
			Detail:   "file's only commit is the initial scaffold. No subsequent real work has edited it — worker declared it and moved on.",
		}}
	}
	return nil
}

// scanOrphanReferences: flag declared files whose default/named
// exports are never imported or referenced anywhere else in the
// repo. Uses a cheap grep heuristic:
//
//  1. For each declared file, extract its exported identifier names.
//  2. Grep the rest of the repo for any of those names.
//  3. If zero hits across all non-declared .ts/.tsx/.js/.jsx files,
//     the file is orphaned (declared but never wired).
//
// Skips index files, main/entry files, .d.ts, test files.
func scanOrphanReferences(repoRoot string, declaredFiles []string) []QualityFinding {
	if len(declaredFiles) == 0 {
		return nil
	}
	declaredSet := make(map[string]bool, len(declaredFiles))
	for _, d := range declaredFiles {
		declaredSet[d] = true
	}
	var out []QualityFinding
	for _, rel := range declaredFiles {
		if !looksLikeCode(rel) || isTestFile(rel) || isFixtureFile(rel) ||
			isStorybookFile(rel) || isTypeDefFile(rel) || isEntryFile(rel) {
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		names := extractExportNames(string(data))
		if len(names) == 0 {
			// no-exports handles this separately; nothing to grep for.
			continue
		}
		// Grep each name across the repo (excluding the file itself
		// and excluding node_modules / build / dist / .git).
		referenced := false
		for _, name := range names {
			if len(name) < 3 {
				continue // too generic to safely grep
			}
			if hasExternalReference(repoRoot, name, rel) {
				referenced = true
				break
			}
		}
		if !referenced {
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "orphan-file",
				File:     rel,
				Line:     1,
				Detail: fmt.Sprintf(
					"exports (%s) are not imported or referenced anywhere else in the repo. Declared but never wired in.",
					strings.Join(names, ", ")),
			})
		}
	}
	return out
}

// extractExportNames: pull named/default/const/function export
// identifiers from a TS/JS source. Heuristic — not a real parser.
// Returns unique names, trimmed.
func extractExportNames(content string) []string {
	named := regexp.MustCompile(
		`(?m)export\s+(?:default\s+)?(?:async\s+)?(?:const|let|var|function|class|type|interface|enum)\s+(\w+)`)
	defaultFunc := regexp.MustCompile(
		`(?m)export\s+default\s+(?:async\s+)?function\s*(\w+)?`)
	braceExport := regexp.MustCompile(
		`(?m)export\s*\{\s*([^}]+)\}`)
	set := map[string]bool{}
	for _, m := range named.FindAllStringSubmatch(content, -1) {
		if len(m) >= 2 && m[1] != "" {
			set[m[1]] = true
		}
	}
	for _, m := range defaultFunc.FindAllStringSubmatch(content, -1) {
		if len(m) >= 2 && m[1] != "" {
			set[m[1]] = true
		}
	}
	for _, m := range braceExport.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		for _, part := range strings.Split(m[1], ",") {
			// Handle "foo as bar" — the external name is "bar".
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if idx := strings.Index(part, " as "); idx >= 0 {
				part = strings.TrimSpace(part[idx+4:])
			}
			if part != "" {
				set[part] = true
			}
		}
	}
	var out []string
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// hasExternalReference: grep repo for `name`, excluding the file
// itself, node_modules, build output, git dir. Uses `git grep`
// which is MUCH faster than `grep -r` and respects .gitignore.
func hasExternalReference(repoRoot, name, selfPath string) bool {
	// git grep -l -F -w "name" -- ':(exclude)selfPath'
	// The exclusion pathspec avoids matching the declaration line
	// in the file's own source.
	out, _ := runGit(repoRoot, "grep", "-l", "-F", "-w", "--", name,
		":(exclude)"+selfPath)
	hits := strings.TrimSpace(out)
	if hits == "" {
		return false
	}
	// Filter out node_modules / dist / build hits — git grep should
	// already have ignored them, but double-check for repos that
	// accidentally track node_modules.
	for _, line := range strings.Split(hits, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "node_modules/") ||
			strings.Contains(line, "/dist/") ||
			strings.Contains(line, "/build/") {
			continue
		}
		return true
	}
	return false
}

// isEntryFile: filenames that are conventionally entry points and
// should NOT be flagged as orphans (they're imported by tooling, not
// by other source files).
func isEntryFile(rel string) bool {
	base := filepath.Base(rel)
	low := strings.ToLower(base)
	switch low {
	case "index.ts", "index.tsx", "index.js", "index.jsx",
		"main.ts", "main.tsx", "main.js", "main.jsx",
		"_app.tsx", "_app.js", "_document.tsx", "app.tsx",
		"layout.tsx", "page.tsx", "route.ts", "route.tsx",
		"middleware.ts", "next.config.js", "next.config.mjs",
		"vite.config.ts", "vite.config.js",
		"tailwind.config.js", "tailwind.config.ts",
		"postcss.config.js", "metro.config.js", "babel.config.js",
		"jest.config.js", "jest.config.ts",
		"vitest.config.ts", "vitest.config.js",
		"playwright.config.ts", "playwright.config.js":
		return true
	}
	// Next.js app-router convention: any file named route.ts / page.tsx
	// / layout.tsx / loading.tsx / not-found.tsx at any depth.
	if strings.HasSuffix(low, "/page.tsx") || strings.HasSuffix(low, "/page.ts") ||
		strings.HasSuffix(low, "/layout.tsx") || strings.HasSuffix(low, "/route.ts") ||
		strings.HasSuffix(low, "/route.tsx") || strings.HasSuffix(low, "/loading.tsx") ||
		strings.HasSuffix(low, "/not-found.tsx") || strings.HasSuffix(low, "/error.tsx") {
		return true
	}
	return false
}

// (runGit is provided by gitcontext.go in this package.)

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

// ───────────────── experimental SOW-scoped scanners ─────────────────

// collectSOWText concatenates every prose field of a SOW into a
// single haystack for regex scanning.
func collectSOWText(sow *SOW) string {
	if sow == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(sow.Description)
	b.WriteString("\n")
	for _, s := range sow.Sessions {
		b.WriteString(s.Title)
		b.WriteString("\n")
		b.WriteString(s.Description)
		b.WriteString("\n")
		for _, t := range s.Tasks {
			b.WriteString(t.Description)
			b.WriteString("\n")
		}
		for _, ac := range s.AcceptanceCriteria {
			b.WriteString(ac.Description)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// scanSOWEndpointContracts: extract HTTP endpoint declarations from
// the SOW prose (patterns like "POST /api/login", "GET /api/residents"),
// then verify that a route file exists for each at a plausible path.
//
// Matches Next.js app-router (`app/api/<path>/route.ts`), Next.js
// pages-router (`pages/api/<path>.ts`), Express-ish (`routes/<path>.ts`,
// `handlers/<path>.ts`), and Hono/Fastify (files mentioning the path
// as a route literal).
//
// Experimental — default off. False-positive risk: SOW prose uses
// "GET /api/X" in a figurative sense, or the project uses a
// framework with non-standard routing.
func scanSOWEndpointContracts(repoRoot, sowText string) []QualityFinding {
	// Pattern: word-boundary HTTP verb, whitespace, /path
	// Verbs: GET|POST|PUT|PATCH|DELETE
	endpointRx := regexp.MustCompile(
		`\b(GET|POST|PUT|PATCH|DELETE)\s+(/\S+)`)
	matches := endpointRx.FindAllStringSubmatch(sowText, -1)
	if len(matches) == 0 {
		return nil
	}
	// De-dup: endpoint = METHOD + path, strip trailing punctuation.
	type ep struct{ method, path string }
	seen := map[ep]bool{}
	endpoints := make([]ep, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		path := strings.TrimRight(m[2], ".,;:`)*)\"'")
		// Skip obvious non-endpoints: /usr/, /tmp/, /home/, /etc/.
		if strings.HasPrefix(path, "/usr/") || strings.HasPrefix(path, "/tmp/") ||
			strings.HasPrefix(path, "/home/") || strings.HasPrefix(path, "/etc/") ||
			strings.HasPrefix(path, "/bin/") {
			continue
		}
		key := ep{method: m[1], path: path}
		if seen[key] {
			continue
		}
		seen[key] = true
		endpoints = append(endpoints, key)
	}

	var out []QualityFinding
	for _, e := range endpoints {
		if routeFileExists(repoRoot, e.path) {
			continue
		}
		out = append(out, QualityFinding{
			Severity: SevBlocking,
			Kind:     "sow-endpoint-missing",
			File:     inferEndpointFile(e.path),
			Line:     1,
			Detail: fmt.Sprintf(
				"SOW declares %s %s — no route file found at app/api%s/route.*, pages/api%s.*, or equivalent. Endpoint is promised but not implemented.",
				e.method, e.path, e.path, e.path),
		})
	}
	return out
}

// routeFileExists: best-effort check for a route file at any of
// the conventional framework locations. Also checks whether the
// path literal appears in any file whose name contains "route",
// "handler", or "server" (catches Express/Fastify centralized
// router files).
func routeFileExists(repoRoot, httpPath string) bool {
	// Normalize: strip leading slash; collapse trailing slash.
	p := strings.TrimPrefix(httpPath, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		p = "index"
	}
	// Strip dynamic segments: /api/users/[id] → /api/users for dir check.
	cleanPath := regexp.MustCompile(`\[[^\]]+\]`).ReplaceAllString(p, "X")
	cleanPath = regexp.MustCompile(`:[a-zA-Z_][a-zA-Z0-9_]*`).ReplaceAllString(cleanPath, "X")

	// Walk possible prefixes (monorepo or single-app layout) and check
	// for a file existing there.
	prefixes := []string{
		"", "apps/web/", "apps/api/", "apps/server/",
		"packages/api/", "packages/server/", "server/", "api/",
	}
	// Candidate patterns:
	//   <prefix>app/<path>/route.(ts|tsx|js)
	//   <prefix>app/(anything)/<path>/route.*  -- skip (too loose)
	//   <prefix>pages/<path>.(ts|tsx|js)
	//   <prefix>src/app/<path>/route.*
	//   <prefix>src/routes/<path>.*
	for _, pfx := range prefixes {
		candidates := []string{
			pfx + "app/" + cleanPath + "/route.ts",
			pfx + "app/" + cleanPath + "/route.tsx",
			pfx + "app/" + cleanPath + "/route.js",
			pfx + "pages/" + cleanPath + ".ts",
			pfx + "pages/" + cleanPath + ".tsx",
			pfx + "pages/" + cleanPath + ".js",
			pfx + "src/app/" + cleanPath + "/route.ts",
			pfx + "src/routes/" + cleanPath + ".ts",
			pfx + "src/routes/" + cleanPath + ".tsx",
			pfx + "routes/" + cleanPath + ".ts",
			pfx + "handlers/" + cleanPath + ".ts",
		}
		for _, c := range candidates {
			if _, err := os.Stat(filepath.Join(repoRoot, c)); err == nil {
				return true
			}
		}
	}
	// Last resort: grep for the literal path in any route/handler/server
	// file. Catches centralized routers (Express app.get(...)).
	literalPath := httpPath
	// Git grep the literal, filter to route-ish files.
	out, _ := runGit(repoRoot, "grep", "-l", "-F", "--", literalPath)
	for _, line := range strings.Split(out, "\n") {
		lineL := strings.ToLower(line)
		if strings.Contains(lineL, "route") || strings.Contains(lineL, "handler") ||
			strings.Contains(lineL, "server") || strings.Contains(lineL, "/api/") {
			return true
		}
	}
	return false
}

func inferEndpointFile(httpPath string) string {
	p := strings.TrimPrefix(httpPath, "/")
	return "app/" + p + "/route.ts"
}

// scanSOWStructuralClaims: extract structural tuple claims from SOW
// prose and verify the named items appear somewhere plausible in
// the repo. Patterns recognized:
//
//   "columns: a, b, c"            — data-table column list
//   "fields: a, b, c"             — schema / form fields
//   "exports { a, b, c }"         — module exports
//   "props: a, b, c"              — component props
//
// For each tuple, we try to find a file whose name relates to the
// noun (e.g. a claim near "AlarmTable" expects to find AlarmTable.*
// or alarm-table.*), then grep for every listed item in that file.
// Missing items become findings.
//
// Experimental — default off. Heuristic noun-to-file matching is
// imperfect; expect some false positives.
func scanSOWStructuralClaims(repoRoot, sowText string) []QualityFinding {
	// Reduce noise: only operate on sentences that look like declarations.
	// Pattern: <noun-capitalized> ... (columns|fields|props|exports):? (bracketed or listed items)
	claimRx := regexp.MustCompile(
		`(?i)\b([A-Z]\w{2,}(?:Table|List|Card|Panel|Editor|Form|View|Page|Screen|Component|Modal)?)\b[^.\n]{0,80}?\b(columns?|fields?|props?|exports?|methods?|items?)\s*[:=]?\s*[\[\{]?([^.\n\]\}]+)[\]\}]?`)
	matches := claimRx.FindAllStringSubmatch(sowText, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []QualityFinding
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		noun := strings.TrimSpace(m[1])
		kind := strings.ToLower(strings.TrimSpace(m[2]))
		listRaw := m[3]
		// Split on commas / "and" / newlines
		items := splitClaimList(listRaw)
		if len(items) < 2 {
			continue
		}
		// Heuristic: require at least 3 items to avoid matching
		// generic English ("The page fields are name and age").
		if len(items) < 3 {
			continue
		}
		file := findCandidateFile(repoRoot, noun)
		if file == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(repoRoot, file))
		if err != nil {
			continue
		}
		body := string(content)
		var missing []string
		for _, it := range items {
			// Normalize: lowercase compare, alphanumeric only.
			needle := strings.TrimSpace(it)
			if needle == "" {
				continue
			}
			if !strings.Contains(strings.ToLower(body), strings.ToLower(needle)) {
				missing = append(missing, it)
			}
		}
		if len(missing) > 0 && len(missing) < len(items) {
			// At least one item found → the file is the right target,
			// but some items are missing. Flag the specific misses.
			out = append(out, QualityFinding{
				Severity: SevBlocking,
				Kind:     "sow-claim-missing",
				File:     file,
				Line:     1,
				Detail: fmt.Sprintf(
					"SOW declares %s %s=[%s]; %s missing from %s — claim is partially unfulfilled.",
					noun, kind, strings.Join(items, ", "),
					strings.Join(missing, ", "), file),
			})
		}
	}
	return out
}

// splitClaimList: extract identifier-like tokens from a prose list.
// Handles comma-separated, "and"-joined, bracketed, or tagged lists.
func splitClaimList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Normalize " and " → ", "
	raw = regexp.MustCompile(`\s+and\s+`).ReplaceAllString(raw, ", ")
	// Split on comma / pipe / newline.
	parts := regexp.MustCompile(`[,|\n]+`).Split(raw, -1)
	identRx := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	var out []string
	for _, p := range parts {
		p = strings.Trim(p, " \t\"'`()[]{}*")
		if p == "" || len(p) > 40 {
			continue
		}
		// Accept single-word identifiers or simple kebab/snake case.
		first := strings.Fields(p)
		if len(first) == 0 {
			continue
		}
		cand := first[0]
		if identRx.MatchString(cand) {
			out = append(out, cand)
		}
	}
	return out
}

// findCandidateFile: search the repo for a source file whose name
// matches a noun, case-insensitively. Returns the first hit. Cheap
// git grep on filenames.
func findCandidateFile(repoRoot, noun string) string {
	if noun == "" {
		return ""
	}
	// Try PascalCase exact match (AlarmTable.tsx), then kebab-case
	// (alarm-table.tsx), then component-folder index.
	patterns := []string{
		noun + ".tsx", noun + ".ts", noun + ".jsx", noun + ".js",
		pascalToKebab(noun) + ".tsx",
		pascalToKebab(noun) + ".ts",
	}
	for _, pat := range patterns {
		out, _ := runGit(repoRoot, "ls-files", "--", "**/"+pat, pat)
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.Contains(line, "node_modules") ||
				strings.Contains(line, "/dist/") {
				continue
			}
			return line
		}
	}
	return ""
}

func pascalToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 32) // lowercase
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// scanPackageScripts: if the SOW promises any of {test, build,
// typecheck, dev, start, lint}, verify each app's package.json has
// a script entry for it. Catches the "the SOW said we'd have tests
// but nothing is wired into `npm test`" failure mode.
//
// Default on once validated; shipping default-off as experimental.
func scanPackageScripts(repoRoot, sowText string) []QualityFinding {
	// Detect which scripts the SOW promises. We look for literal
	// mentions of command names — this is coarse but deterministic.
	promised := map[string]bool{}
	textLow := strings.ToLower(sowText)
	for _, name := range []string{"test", "build", "typecheck", "type-check",
		"dev", "start", "lint", "format"} {
		// Need whole-word boundary to avoid "restart" matching "start".
		rx := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		if rx.MatchString(textLow) {
			promised[name] = true
		}
	}
	if len(promised) == 0 {
		return nil
	}
	// Find every package.json in the repo (excluding node_modules).
	out, err := runGit(repoRoot, "ls-files", "--", "**/package.json", "package.json")
	if err != nil {
		return nil
	}
	pkgFiles := strings.Split(strings.TrimSpace(out), "\n")
	if len(pkgFiles) == 0 || pkgFiles[0] == "" {
		return nil
	}
	// Top-level package.json only for now (monorepo root + app roots).
	// Avoid inspecting nested package.jsons that are artifacts of
	// vendored tooling.
	var findings []QualityFinding
	for _, pkg := range pkgFiles {
		if strings.Count(pkg, "/") > 3 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, pkg))
		if err != nil {
			continue
		}
		body := strings.ToLower(string(data))
		// Parse scripts block cheaply: look for `"scripts": { ... }`
		// and grep for each promised name inside it.
		scriptsIdx := strings.Index(body, `"scripts"`)
		if scriptsIdx < 0 {
			continue
		}
		// Take the next 2000 bytes as the scripts scope (imprecise
		// but fine for presence checks).
		scope := body[scriptsIdx:]
		if len(scope) > 2000 {
			scope = scope[:2000]
		}
		for name := range promised {
			// Typecheck / type-check / tsc variants
			variants := []string{`"` + name + `"`}
			if name == "typecheck" {
				variants = append(variants, `"type-check"`, `"tsc"`)
			}
			if name == "type-check" {
				variants = append(variants, `"typecheck"`, `"tsc"`)
			}
			found := false
			for _, v := range variants {
				if strings.Contains(scope, v) {
					found = true
					break
				}
			}
			if !found {
				findings = append(findings, QualityFinding{
					Severity: SevBlocking,
					Kind:     "package-script-missing",
					File:     pkg,
					Line:     1,
					Detail: fmt.Sprintf(
						"SOW promises a %q script; %s scripts block has no such entry. Anything invoking `npm run %s` will fail.",
						name, pkg, name),
				})
			}
		}
	}
	return findings
}
