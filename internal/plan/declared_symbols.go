package plan

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ericmacdougall/stoke/internal/symindex"
)

// H-27 — AST-level declared-symbol verification.
//
// Problem: the declared-file-not-created gate (H-8) catches "worker
// didn't produce the file at all", but H1-v2 died yesterday with a
// different failure — all 8 files WERE created, but 7 of them were
// stubs that did not actually implement the deliverables named in
// the SOW prose. LLM reviewers rubber-stamped; hollow-shell regex
// missed because the shells weren't empty, just thin.
//
// Solution: parse SOW prose to extract named deliverables ("the
// acknowledgeAlarm handler", "a Zod schema called AlarmSchema",
// "an AuthContext React component"), build a symbol index of the
// changed files via internal/symindex (covers 11 languages via
// Go stdlib AST + regex extractors), and flag every declared name
// that does NOT appear as a function/class/type/const/interface
// in the code. Language-agnostic matching by name; signature-level
// matching is a planned follow-up once name-level catches the
// bulk of the problem.

// init wires the regex extractor as the fallback for H-28's
// tree-sitter path so non-TS/JS/Python files still contribute
// symbols when tree-sitter is enabled. Avoids an import cycle by
// keeping the extraction helper package-private and exposed via
// ExtractDeclaredSymbolsFallback function-var in the H-28 file.
func init() {
	ExtractDeclaredSymbolsFallback = extractSymbolsViaSymindex
}

// extractSymbolsViaSymindex returns symbol names for files that the
// H-28 tree-sitter path can't handle (Rust, Kotlin, Swift, Ruby,
// etc.). Delegates to symindex which already covers those via regex.
func extractSymbolsViaSymindex(repoRoot string, files []string) []string {
	idx, err := symindex.BuildFromFiles(repoRoot, files)
	if err != nil || idx == nil {
		return nil
	}
	names := make([]string, 0, 64)
	for _, sym := range idx.AllSymbols() {
		if sym.Name != "" {
			names = append(names, sym.Name)
		}
	}
	return names
}

// ScanDeclaredSymbolsNotImplemented walks SOW prose for named
// deliverables and verifies each appears as a defined symbol in
// repoRoot's changed source files.
//
// SOW phrasing patterns matched (case-insensitive):
//
//   - "the acknowledgeAlarm handler"
//   - "a XxxSchema class|struct|type|enum|interface|trait"
//   - "an AuthContext component|provider|hook"
//   - "export|exports ... function|class|type|const XxxFoo"
//   - backtick-quoted identifiers like `doSomething` in code-block prose
//
// Rationale for conservative extraction: false positives in this
// gate stall real completion (worker fixed 4 things, gate insists 5
// are still missing because the regex matched a noun phrase). The
// patterns here only match tokens that LOOK like code identifiers
// (camelCase / PascalCase / snake_case, not normal English words).
//
// Returns blocking findings when the symbol is absent AND the gate
// is enabled via QualityConfig.DeclaredSymbolNotImplemented. Repo
// root is required so symindex can read the changed files; when
// changedFiles is empty the function silently returns nil (no
// incremental scope to check).
func ScanDeclaredSymbolsNotImplemented(repoRoot, sowProse string, changedFiles []string) []QualityFinding {
	if repoRoot == "" || strings.TrimSpace(sowProse) == "" || len(changedFiles) == 0 {
		return nil
	}
	// Only walk source files; symindex ignores everything else but
	// we short-circuit on the filename to avoid building an empty
	// index for a docs-only commit.
	src := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		if looksLikeSource(f) {
			src = append(src, f)
		}
	}
	if len(src) == 0 {
		return nil
	}

	declared := ExtractDeclaredSymbols(sowProse)
	if len(declared) == 0 {
		return nil
	}

	// H-51: scan the WHOLE repo's tracked source files for symbol
	// presence, not just the diff. R09-lenient spammed 5+ false
	// "UserError/UserListQuery/requireAdmin not implemented" gate-
	// hits because those symbols were defined in earlier commits
	// (packages/types/, apps/web/lib/session.ts) that the current
	// commit didn't touch. A symbol is "not implemented" only if
	// it's missing from the REPO, not missing from this commit.
	repoSrc := gatherTrackedSourceFiles(repoRoot)
	if len(repoSrc) == 0 {
		// Fall back to changed-files-only so we still report
		// something when git isn't available.
		repoSrc = src
	}
	idx, err := symindex.BuildFromFiles(repoRoot, repoSrc)
	if err != nil || idx == nil {
		return nil
	}

	// Lower-case the index for case-insensitive match since SOW prose
	// occasionally sentence-cases an identifier ("The Acknowledgealarm
	// handler"). Still require the original symbol to have existed.
	present := make(map[string]bool, 64)
	for _, sym := range idx.AllSymbols() {
		if sym.Name == "" {
			continue
		}
		present[sym.Name] = true
		present[strings.ToLower(sym.Name)] = true
	}

	out := make([]QualityFinding, 0, len(declared))
	for _, d := range declared {
		if present[d] || present[strings.ToLower(d)] {
			continue
		}
		// H-83: downgrade prose-extracted declared-symbol findings to
		// advisory (Major, not Blocking). The extractor heuristic
		// regularly false-positives on natural-language prose phrases
		// like "Worker service", "components", "Zod schemas for Ticket"
		// that look identifier-shaped but are English descriptions
		// (R10-sow-serial today burned a whole session replaying the
		// same 10 bogus "missing symbols" through the repair loop).
		// Prose is NOT the authoritative deliverable list — task.Files
		// and session.AcceptanceCriteria are. When the runner needs a
		// strict gate on symbols, use the declared_files-not-created
		// signal (H-2) which operates on task.Files, not prose.
		// Operator can still escalate by setting
		// STOKE_DECLARED_SYMBOL_BLOCKING=1.
		sev := SevAdvisory
		if os.Getenv("STOKE_DECLARED_SYMBOL_BLOCKING") == "1" {
			sev = SevBlocking
		}
		out = append(out, QualityFinding{
			Severity: sev,
			Kind:     "declared-symbol-not-implemented",
			File:     "(sow)",
			Line:     1,
			Detail: "SOW prose mentions `" + d + "` (extracted as a candidate deliverable) " +
				"but no matching function, class, type, interface, or exported constant with that name exists " +
				"in the changed source files. ADVISORY: prose-extracted symbols can be false positives when the " +
				"SOW uses natural-language descriptions. Verify against the task's declared files before treating " +
				"as a real gap.",
		})
	}
	return out
}

// declaredSymbolExtractors run in order; each returns candidate
// identifier strings from SOW prose. Union feeds ExtractDeclaredSymbols.
var declaredSymbolPatterns = []*regexp.Regexp{
	// "the acknowledgeAlarm handler" / "the fooBar function/endpoint/method"
	regexp.MustCompile(`(?i)\b(?:the|a|an)\s+([A-Za-z_][A-Za-z0-9_]*[a-z][A-Za-z0-9_]*)\s+(?:handler|function|method|endpoint|provider|hook|component|controller|service|middleware|guard|resolver|action|reducer|selector|thunk)\b`),
	// "a FooSchema class|struct|type|enum|interface|trait|schema|record"
	regexp.MustCompile(`(?i)\b(?:a|an|the)\s+([A-Z][A-Za-z0-9_]+)\s+(?:class|struct|type|enum|interface|trait|schema|record|model|entity)\b`),
	// "export function Xyz", "export class Xyz", "export const Xyz"
	regexp.MustCompile(`(?i)\bexport(?:s)?\s+(?:default\s+)?(?:async\s+)?(?:function|class|const|let|interface|type|enum)\s+([A-Za-z_][A-Za-z0-9_]*)\b`),
	// "called XxxYyy" / "named XxxYyy" (PascalCase or camelCase only)
	regexp.MustCompile(`\b(?:called|named)\s+([A-Za-z_][A-Za-z0-9_]*[A-Z][A-Za-z0-9_]*|[a-z][a-z0-9_]*[A-Z][A-Za-z0-9_]*)\b`),
	// Backtick-quoted identifiers: `doSomething` / `AlarmSchema`.
	// Only match code-shape tokens (contains an internal uppercase
	// or underscore; pure lowercase gets rejected to avoid matching
	// backtick English like `hello`).
	regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*(?:[A-Z_][A-Za-z0-9_]*|[_][a-z][A-Za-z0-9_]*))`"),
}

// declaredSymbolListPattern matches the "export/expose A, B, and C as
// handler/function/class" construction that real SOWs use when listing
// multiple deliverables on one line. The captured group is the comma-
// separated list which gets split + re-validated per-identifier.
var declaredSymbolListPattern = regexp.MustCompile(
	`(?i)\b(?:export(?:s)?|expose(?:s)?|provide(?:s)?|implement(?:s)?|must\s+(?:export|expose|provide|implement))\s+([A-Za-z_][A-Za-z0-9_,\s]+?)\s+as\s+(?:handler|function|method|class|component|provider|hook|controller|service|middleware|schema|type)s?\b`)

// ExtractDeclaredSymbols returns deduped declared identifiers from
// SOW prose. Capped at 200 entries to bound downstream work on a
// pathological SOW that quotes an entire module.
//
// Conservative matching by design: false positives here translate
// into blocking findings that stall real completion. We prefer
// false negatives (missing a deliverable the gate should have
// caught) to false positives (blocking on a word that wasn't
// actually meant to be a symbol).
//
// Rejected tokens:
//   - Short tokens (≤ 2 chars): noise.
//   - All-uppercase acronyms (HTTP, SQL): treated as prose, not code.
//   - Reserved English words that happen to camelCase (Handle, Provider,
//     Component, Service, Controller, Schema, Model, Record, Entity):
//     the "the X handler" pattern matched X, which is the actual
//     identifier — but the tail (handler/etc) got captured by the
//     surrounding pattern, not by this extractor.
func ExtractDeclaredSymbols(prose string) []string {
	if strings.TrimSpace(prose) == "" {
		return nil
	}
	seen := make(map[string]struct{}, 64)
	var out []string
	const maxSymbols = 200

	for _, re := range declaredSymbolPatterns {
		for _, m := range re.FindAllStringSubmatch(prose, -1) {
			if len(m) < 2 {
				continue
			}
			sym := strings.TrimSpace(m[1])
			if !validDeclaredSymbol(sym) {
				continue
			}
			if _, dup := seen[sym]; dup {
				continue
			}
			seen[sym] = struct{}{}
			out = append(out, sym)
			if len(out) >= maxSymbols {
				return out
			}
		}
	}

	// List construction: "export A, B, and C as handler functions."
	// Split the captured list on comma/"and"/"or" and validate each.
	for _, m := range declaredSymbolListPattern.FindAllStringSubmatch(prose, -1) {
		if len(m) < 2 {
			continue
		}
		list := m[1]
		for _, token := range splitDeclaredSymbolList(list) {
			if !validDeclaredSymbol(token) {
				continue
			}
			if _, dup := seen[token]; dup {
				continue
			}
			seen[token] = struct{}{}
			out = append(out, token)
			if len(out) >= maxSymbols {
				return out
			}
		}
	}
	return out
}

// splitDeclaredSymbolList splits a list-shaped prose fragment like
// "acknowledgeAlarm, resolveAlarm, and previewAlertRule" into
// individual identifier tokens. Trims whitespace + Oxford "and"/"or".
var declaredSymbolListSplit = regexp.MustCompile(`[,\s]+(?:and|or)\s+|[,\s]+`)

func splitDeclaredSymbolList(list string) []string {
	parts := declaredSymbolListSplit.Split(strings.TrimSpace(list), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "and" || p == "or" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// validDeclaredSymbol rejects tokens that almost-certainly aren't
// code identifiers the worker is expected to produce.
func validDeclaredSymbol(s string) bool {
	if len(s) <= 2 {
		return false
	}
	// All-uppercase tokens (HTTP, SQL, API) are almost always acronyms
	// in prose, not identifiers. Accept only if they contain at least
	// one lowercase letter.
	hasLower := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			hasLower = true
			break
		}
	}
	if !hasLower {
		return false
	}
	// Reject English words that frequently appear in the "the X handler"
	// construction but are actually just nouns (e.g. "the request handler"
	// — "request" is not an identifier).
	lower := strings.ToLower(s)
	if _, bad := declaredSymbolBlocklist[lower]; bad {
		return false
	}
	// Must look like an identifier: camelCase, PascalCase, or snake_case
	// with at least one non-leading uppercase OR underscore OR digit.
	// A pure-lowercase word ("request") is rejected as too English-like.
	hasInternalSignal := false
	for i, r := range s {
		if i == 0 {
			continue
		}
		if (r >= 'A' && r <= 'Z') || r == '_' || (r >= '0' && r <= '9') {
			hasInternalSignal = true
			break
		}
	}
	return hasInternalSignal
}

// declaredSymbolBlocklist is English nouns/adjectives + common
// configuration-key identifiers that look like camelCase/snake_case
// code but aren't deliverable symbols. Populated from observation +
// live-cohort false positives (E5 surfaced the tsconfig-option
// pattern).
var declaredSymbolBlocklist = map[string]struct{}{
	// English nouns / common prose
	"request": {}, "response": {}, "error": {}, "event": {},
	"user": {}, "admin": {}, "client": {}, "server": {},
	"database": {}, "api": {}, "route": {}, "page": {},
	"form": {}, "input": {}, "output": {}, "file": {},
	"folder": {}, "directory": {}, "package": {}, "module": {},
	"library": {}, "framework": {},
	"the": {}, "this": {}, "that": {}, "main": {},
	"test": {}, "example": {}, "sample": {}, "default": {},
	"custom": {}, "standard": {}, "basic": {}, "simple": {}, "advanced": {},
	"login": {}, "logout": {}, "signin": {}, "signup": {}, "auth": {},

	// TypeScript compiler options (strict, module system, tooling).
	// Caught in E5's first 26 min: `strictNullChecks`, `noImplicitAny`,
	// `exactOptionalPropertyTypes` all flagged as missing symbols when
	// the SOW merely asked for them in tsconfig. These are CONFIG KEYS,
	// not code identifiers.
	"strict": {}, "strictnullchecks": {}, "strictfunctiontypes": {},
	"strictbindcallapply": {}, "strictpropertyinitialization": {},
	"noimplicitany": {}, "noimplicitthis": {}, "noimplicitreturns": {},
	"alwaysstrict": {}, "exactoptionalpropertytypes": {},
	"nouncheckedindexedaccess": {}, "noimplicitoverride": {},
	"nopropertyaccessfromindexsignature": {}, "useunknownincatchvariables": {},
	"allowjs": {}, "checkjs": {}, "declaration": {}, "sourcemap": {},
	"esmoduleinterop": {}, "allowsyntheticdefaultimports": {},
	"forceconsistentcasinginfilenames": {}, "isolatedmodules": {},
	"resolvejsonmodule": {}, "jsx": {}, "jsximportsource": {},
	"target": {}, "lib": {}, "moduleresolution": {},
	"baseurl": {}, "paths": {}, "rootdir": {}, "outdir": {},
	"skiplibcheck": {}, "downleveliteration": {},
	"experimentaldecorators": {}, "emitdecoratormetadata": {},

	// ESLint / Prettier / build-tool config keys that trigger PascalCase
	// or camelCase detection but are config, not deliverables.
	"rules": {}, "parser": {}, "parseroptions": {}, "plugins": {},
	"extends": {}, "overrides": {}, "env": {}, "settings": {},
	"ignorepatterns": {}, "root": {}, "globals": {},

	// Package.json + workspace keys ("main" already covered above
	// in the English-prose section).
	"dependencies": {}, "devdependencies": {}, "peerdependencies": {},
	"scripts": {}, "workspaces": {}, "private": {},
	"types": {}, "exports": {}, "imports": {},

	// H-42: React + Next.js imports that are NOT deliverables.
	// Caught in PERFCOMP R05-medium: SOW mentions "useState" as part
	// of describing a React client component, and the symbol check
	// flagged it as a missing declaration — it's an import from
	// react, not something the worker implements. Same class as the
	// tsconfig-option blocklist added earlier.
	"usestate": {}, "useeffect": {}, "usecallback": {}, "usememo": {},
	"useref": {}, "usecontext": {}, "usereducer": {}, "uselayouteffect": {},
	"useimperativehandle": {}, "usedebugvalue": {}, "usetransition": {},
	"usedeferredvalue": {}, "useid": {}, "usesyncexternalstore": {},
	"useinsertioneffect": {}, "useoptimistic": {}, "useformstatus": {},
	"useformstate": {}, "useactionstate": {}, "userouter": {},
	"usepathname": {}, "usesearchparams": {}, "useparams": {},
	"useselectedlayoutsegment": {}, "useselectedlayoutsegments": {},
	"useserverinsertedhtml": {},
	// Next.js server helpers
	"redirect": {}, "notfound": {}, "revalidatepath": {}, "revalidatetag": {},
	"cookies": {}, "headers": {}, "draftmode": {},
	// Common built-in globals that appear in prose
	"fetch": {}, "window": {}, "document": {}, "console": {},
	"process": {}, "buffer": {}, "promise": {}, "array": {},
	"object": {}, "string": {}, "number": {}, "boolean": {}, "date": {},
	"math": {}, "json": {}, "map": {}, "set": {}, "regexp": {},
}

// gatherTrackedSourceFiles returns git-tracked source files in the
// repo, filtered to extensions the symindex understands. Used by
// H-51 to scan the WHOLE repo for symbol presence instead of just
// the commit's diff — avoids false-positive "symbol not implemented"
// gate-hits on symbols defined in earlier commits.
func gatherTrackedSourceFiles(repoRoot string) []string {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" {
			continue
		}
		if looksLikeSource(f) {
			files = append(files, f)
		}
	}
	return files
}

// looksLikeSource returns true when filename has an extension the
// symindex can extract from. Keeps the gate cheap on commits that
// only touch docs/config.
func looksLikeSource(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go",
		".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".py",
		".rs",
		".java",
		".kt", ".kts",
		".swift",
		".rb",
		".php",
		".cs",
		".ex", ".exs",
		".c", ".h", ".cpp", ".cc", ".hpp", ".hxx",
		".scala", ".sc":
		return true
	}
	return false
}
