// Spec-faithfulness guards for the SOW native loop.
//
// Three mechanisms that together fix the "agent hallucinates plausible
// names that don't match the spec" failure mode the user hit during
// real builds:
//
//  1. buildCanonicalNamesBlock — extract every specific identifier the
//     SOW declares (crate names, file paths, content_match patterns)
//     and render them as a "USE EXACTLY THESE NAMES" block in the
//     cached system prompt. The agent sees "you must create
//     crates/persys-concern/Cargo.toml" instead of guessing from a
//     session title.
//
//  2. scanPlaceholderStubs — before declaring a session done, grep
//     the changed files for placeholder patterns (pub fn placeholder,
//     unimplemented!(), todo!(), TODO:, FIXME:, ...unimplemented).
//     If any are found, fail the session into the repair loop with
//     specific line numbers so the agent has to replace them with
//     real implementations.
//
//  3. checkSpecFaithfulness — after a session, walk every declared
//     file in task.Files / session.Outputs and verify it exists AND
//     isn't a 0-byte or placeholder-only file. Catches "task said
//     it created crates/foo/Cargo.toml but actually created
//     crates/bar/Cargo.toml".
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// buildCanonicalNamesBlock returns a "USE EXACTLY THESE NAMES" block
// for the system prompt, extracted from the SOW's structured fields.
// Returns "" when there's nothing specific enough to reinforce.
//
// Sources of canonical names, in priority order:
//  1. task.Files — file paths the task must create/modify
//  2. session.Outputs — artifact names the session produces
//  3. acceptance_criteria.FileExists — files required to exist
//  4. acceptance_criteria.ContentMatch.File + .Pattern — the pattern
//     is the authoritative expected string, which frequently contains
//     the crate name / module name / error variant
//  5. Stack.Infra names — service identifiers that leak into config
func buildCanonicalNamesBlock(sowDoc *plan.SOW, session plan.Session, task plan.Task) string {
	if sowDoc == nil {
		return ""
	}

	// Collect unique entries with a short source tag.
	type entry struct {
		value  string
		origin string
	}
	seen := make(map[string]entry)
	add := func(value, origin string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; !ok {
			seen[value] = entry{value: value, origin: origin}
		}
	}

	// 1. Task files
	for _, f := range task.Files {
		add(f, "task.files")
	}
	// 2. Session outputs
	for _, o := range session.Outputs {
		add(o, "session.outputs")
	}
	// 3/4. Session acceptance criteria
	for _, ac := range session.AcceptanceCriteria {
		if ac.FileExists != "" {
			add(ac.FileExists, "acceptance."+ac.ID+".file_exists")
		}
		if ac.ContentMatch != nil {
			add(ac.ContentMatch.File, "acceptance."+ac.ID+".content_match.file")
			// The pattern itself is the verbatim expected string —
			// reinforce it so the agent doesn't paraphrase.
			add(ac.ContentMatch.Pattern, "acceptance."+ac.ID+".content_match.pattern")
		}
	}
	// 5. Infra names (service identifiers)
	for _, inf := range sowDoc.Stack.Infra {
		add(inf.Name, "stack.infra")
	}

	if len(seen) == 0 {
		return ""
	}

	// Sort for stable prompt output (cache-friendly).
	entries := make([]entry, 0, len(seen))
	for _, e := range seen {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].origin != entries[j].origin {
			return entries[i].origin < entries[j].origin
		}
		return entries[i].value < entries[j].value
	})

	var b strings.Builder
	b.WriteString("CANONICAL IDENTIFIERS — USE THESE EXACTLY AS WRITTEN (no synonyms, no abbreviations, no plausible alternatives):\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  %q  (from %s)\n", e.value, e.origin)
	}
	b.WriteString("\n")
	b.WriteString("If you're about to type a name that differs from one above — STOP. Re-read the spec. The canonical form always wins.\n")
	return b.String()
}

// placeholderPatterns are per-language regexes the quality gate uses to
// detect unfinished stub code. Each pattern pairs a human-readable
// description with the actual regex. Cheap, deterministic, runs on the
// content of files that appear in `git status --porcelain`.
//
// The patterns are deliberately narrow to avoid false positives:
//   - "pub fn placeholder()" literally
//   - unimplemented!() / todo!() as standalone bodies
//   - "TODO: implement" comments (not general TODOs)
//   - Python's "pass" as the ONLY statement in a function body
//   - Generic "NotImplemented" / "NotImplementedError"
var placeholderPatterns = []struct {
	Lang    string
	Name    string
	Pattern *regexp.Regexp
}{
	// NOTE: use [ \t]* for leading whitespace — [\s]* matches newlines
	// too, which in (?m) mode lets the match start on a blank line above
	// the real pattern and reports the wrong line number.
	{"rust", "pub fn placeholder", regexp.MustCompile(`(?m)^[ \t]*pub\s+fn\s+placeholder\s*\(`)},
	{"rust", "unimplemented!()", regexp.MustCompile(`\bunimplemented!\s*\(\s*\)`)},
	{"rust", "todo!()", regexp.MustCompile(`\btodo!\s*\(\s*\)`)},
	{"rust", "fn placeholder", regexp.MustCompile(`(?m)^[ \t]*fn\s+placeholder\s*\(`)},
	{"go", "panic(\"not implemented\")", regexp.MustCompile(`panic\(\s*"not\s+implemented"\s*\)`)},
	{"go", "panic(\"TODO", regexp.MustCompile(`panic\(\s*"TODO`)},
	{"go", "// TODO: implement", regexp.MustCompile(`(?m)^[ \t]*//\s*TODO:\s*implement`)},
	{"python", "raise NotImplementedError", regexp.MustCompile(`\braise\s+NotImplementedError`)},
	{"python", "# TODO: implement", regexp.MustCompile(`(?m)^[ \t]*#\s*TODO:\s*implement`)},
	{"typescript", "throw new Error('Not implemented')", regexp.MustCompile(`throw\s+new\s+Error\s*\(\s*['"]Not\s+implemented['"]\s*\)`)},
	{"any", "FIXME: implement", regexp.MustCompile(`FIXME:\s*implement`)},
	{"any", "placeholder function", regexp.MustCompile(`(?i)\bplaceholder\s+function\b`)},
}

// PlaceholderFinding is a single stub detection.
type PlaceholderFinding struct {
	File    string
	Line    int
	Pattern string
	Snippet string
}

// scanPlaceholderStubs walks the given paths (relative to repoRoot) and
// returns any placeholder/stub patterns found. Non-text files and files
// that don't exist are silently skipped. Only common source extensions
// are scanned so random binary diffs don't produce noise.
func scanPlaceholderStubs(repoRoot string, paths []string) []PlaceholderFinding {
	var findings []PlaceholderFinding
	for _, rel := range paths {
		if !isSourceFile(rel) {
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNo := 0
		var fileLines []string
		for scanner.Scan() {
			fileLines = append(fileLines, scanner.Text())
			lineNo++
			// Cap per-file scan at 5000 lines to avoid pathological
			// generated files.
			if lineNo > 5000 {
				break
			}
		}
		f.Close()

		content := strings.Join(fileLines, "\n")
		for _, p := range placeholderPatterns {
			loc := p.Pattern.FindStringIndex(content)
			if loc == nil {
				continue
			}
			// Find which line the match starts on.
			line := 1 + strings.Count(content[:loc[0]], "\n")
			snippet := ""
			if line-1 < len(fileLines) {
				snippet = strings.TrimSpace(fileLines[line-1])
				if len(snippet) > 120 {
					snippet = snippet[:120] + "..."
				}
			}
			findings = append(findings, PlaceholderFinding{
				File:    rel,
				Line:    line,
				Pattern: p.Name,
				Snippet: snippet,
			})
		}
	}
	return findings
}

// isSourceFile returns true for extensions we consider code. Avoids
// scanning binary files and large generated artifacts.
func isSourceFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".rs", ".go", ".py", ".ts", ".tsx", ".js", ".jsx",
		".java", ".kt", ".swift", ".c", ".cc", ".cpp", ".h", ".hpp",
		".cs", ".rb", ".php", ".ex", ".exs", ".scala", ".clj",
		".toml", ".yaml", ".yml", ".json":
		return true
	}
	return false
}

// formatPlaceholderFindings renders findings as a repair-prompt block
// the agent can act on directly. One line per finding with file:line
// and a trimmed snippet.
func formatPlaceholderFindings(findings []PlaceholderFinding) string {
	var b strings.Builder
	b.WriteString("STUB/PLACEHOLDER CODE DETECTED — these are all blocking issues:\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "  %s:%d (%s)", f.File, f.Line, f.Pattern)
		if f.Snippet != "" {
			fmt.Fprintf(&b, ": %s", f.Snippet)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nReplace every placeholder/stub with a real implementation that actually does what the task requires. Do not leave TODO comments in place of working code.\n")
	return b.String()
}

// checkSpecFaithfulness walks every declared file in task.Files +
// session.Outputs and returns the missing/suspicious ones. A file is
// suspicious if it's 0 bytes, contains ONLY whitespace, or matches a
// placeholder pattern for the detected language. Used as an extra
// pre-acceptance gate so "task claimed success but created an empty
// stub" gets caught before the acceptance run.
func checkSpecFaithfulness(repoRoot string, session plan.Session) (missing []string, suspicious []PlaceholderFinding) {
	declaredFiles := make(map[string]bool)
	for _, t := range session.Tasks {
		for _, f := range t.Files {
			declaredFiles[f] = true
		}
	}
	// Only check files that were explicitly declared; we don't want to
	// enforce file-by-file spec faithfulness on unrelated project
	// files.
	for f := range declaredFiles {
		abs := filepath.Join(repoRoot, f)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, f)
			}
			continue
		}
		if info.IsDir() {
			continue
		}
		if info.Size() == 0 {
			missing = append(missing, f+" (0 bytes)")
			continue
		}
	}
	// Run the placeholder scan across declared files (deduplicated).
	declaredList := make([]string, 0, len(declaredFiles))
	for f := range declaredFiles {
		declaredList = append(declaredList, f)
	}
	sort.Strings(declaredList)
	suspicious = scanPlaceholderStubs(repoRoot, declaredList)
	sort.Strings(missing)
	return missing, suspicious
}

// formatSpecFaithfulnessBlob renders missing-file + placeholder findings
// into a repair prompt block. Missing files come first because they're
// the more fundamental issue — no point scanning a file that doesn't
// exist.
func formatSpecFaithfulnessBlob(missing []string, suspicious []PlaceholderFinding) string {
	var b strings.Builder
	if len(missing) > 0 {
		b.WriteString("MISSING OR EMPTY FILES (the SOW declared these but they don't exist or are 0 bytes):\n")
		for _, f := range missing {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
		b.WriteString("\nCreate each missing file with actual content — not an empty stub.\n\n")
	}
	if len(suspicious) > 0 {
		b.WriteString(formatPlaceholderFindings(suspicious))
	}
	return b.String()
}

// lenToStr is a small helper mirroring engine.lenStr so we can render
// truncation notices without importing strconv everywhere.
func lenToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(digits[i:])
	if neg {
		return "-" + s
	}
	return s
}
