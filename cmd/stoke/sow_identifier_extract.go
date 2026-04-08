// Automatic identifier extraction from raw SOW prose.
//
// When the SOW author declares content_match patterns for every file,
// the supervisor has plenty to verify against. When they don't (the
// common case — most SOWs describe the spec in prose and list files
// without exact identifiers), the supervisor would have no teeth.
//
// This file bridges that gap: it scans the raw SOW for code blocks
// (```rust ... ```), Rust/Go/Python declarations (pub struct X,
// fn foo, type T struct, class Y), and CamelCase/snake_case
// identifiers that look like code references, then associates them
// with the files the SOW mentions near those identifiers.
//
// The result is fed into the supervisor as MustContain expectations
// so "crates/persys-concern/src/concern.rs" automatically gets
// expected identifiers like "pub struct Concern" or "fn new_concern"
// without the SOW author having to spell them out.
package main

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// sowIdentifierExtractor scans raw SOW text and produces a map from
// file path → list of identifiers expected in that file.
type sowIdentifierExtractor struct {
	// MinLength is the shortest identifier we'll accept. Defaults
	// to 4 so we don't flood the supervisor with noise like "fn a".
	MinLength int
	// MaxPerFile caps how many identifiers we associate with a
	// single file. Defaults to 8 so a verbose SOW section doesn't
	// over-constrain a single file.
	MaxPerFile int
}

// extract walks the raw SOW and returns the file → identifiers map.
// Safe to call with an empty SOW (returns empty map).
func (e *sowIdentifierExtractor) extract(rawSOW string) map[string][]string {
	if e.MinLength == 0 {
		e.MinLength = 4
	}
	if e.MaxPerFile == 0 {
		e.MaxPerFile = 8
	}
	out := make(map[string][]string)
	if strings.TrimSpace(rawSOW) == "" {
		return out
	}

	// Pass 1: find code blocks and their enclosing section titles.
	// Each code block gets any identifiers it declares, tied to
	// the file path hints found in the surrounding prose.
	blocks := findCodeBlocks(rawSOW)
	for _, block := range blocks {
		fileHints := collectFileHintsAround(rawSOW, block.Start)
		idents := extractIdentifiersFromCode(block.Content, block.Lang, e.MinLength)
		for _, file := range fileHints {
			out[file] = appendUnique(out[file], idents, e.MaxPerFile)
		}
	}

	// Pass 2: inline identifiers in prose. A line that mentions a
	// filename like "concern.rs" AND a declaration-shaped token
	// like "Concern" or "pub struct Concern" associates them.
	paragraphs := splitIntoParagraphs(rawSOW)
	for _, p := range paragraphs {
		fileHints := extractFileHintsFromParagraph(p)
		idents := extractInlineIdentifiers(p, e.MinLength)
		for _, file := range fileHints {
			out[file] = appendUnique(out[file], idents, e.MaxPerFile)
		}
	}

	// Sort each file's identifiers for stable output.
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// codeBlock represents one fenced code block inside the SOW.
type codeBlock struct {
	Lang    string
	Content string
	Start   int // char offset in the original SOW (for locating context)
}

var codeBlockRe = regexp.MustCompile("(?s)```([a-zA-Z0-9_+-]*)\\s*\n(.*?)\n```")

// findCodeBlocks returns every fenced code block in s, with the
// char offset of the opening fence so callers can locate enclosing
// prose.
func findCodeBlocks(s string) []codeBlock {
	var out []codeBlock
	matches := codeBlockRe.FindAllStringSubmatchIndex(s, -1)
	for _, m := range matches {
		if len(m) < 6 {
			continue
		}
		lang := strings.ToLower(s[m[2]:m[3]])
		content := s[m[4]:m[5]]
		out = append(out, codeBlock{
			Lang:    lang,
			Content: content,
			Start:   m[0],
		})
	}
	return out
}

// collectFileHintsAround looks up ~400 chars of text before a code
// block start offset and returns any file paths mentioned there.
// The "near-block" heuristic assumes the SOW says "here's the code
// for foo.rs:" right before the code block.
func collectFileHintsAround(s string, blockStart int) []string {
	const lookBack = 400
	start := blockStart - lookBack
	if start < 0 {
		start = 0
	}
	context := s[start:blockStart]
	return extractFileHintsFromParagraph(context)
}

// fileHintRe matches common ways a SOW references a file path:
//   - crates/foo/src/lib.rs
//   - src/main.go
//   - Cargo.toml
//   - `foo.rs`
//   - (quoted: "pkg/bar.go")
var fileHintRe = regexp.MustCompile(
	"(?:[a-zA-Z0-9_./-]*/)?[a-zA-Z0-9_.-]+\\.(?:rs|go|py|ts|tsx|js|jsx|java|kt|swift|c|cpp|h|hpp|cs|rb|ex|toml|yaml|yml|json|md)\\b",
)

// extractFileHintsFromParagraph pulls every file-path-looking token
// out of a chunk of text. Returns unique paths in stable order.
func extractFileHintsFromParagraph(text string) []string {
	matches := fileHintRe.FindAllString(text, -1)
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		m = strings.Trim(m, "`\"'()[].,;")
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// Rust-specific declaration patterns. We match pub items more
// aggressively than private ones because supervisors care about what
// the SOW promised to expose.
var (
	rustStructRe = regexp.MustCompile(`(?m)^\s*pub\s+struct\s+([A-Z][A-Za-z0-9_]+)`)
	rustEnumRe   = regexp.MustCompile(`(?m)^\s*pub\s+enum\s+([A-Z][A-Za-z0-9_]+)`)
	rustTraitRe  = regexp.MustCompile(`(?m)^\s*pub\s+trait\s+([A-Z][A-Za-z0-9_]+)`)
	rustFnRe     = regexp.MustCompile(`(?m)^\s*pub\s+fn\s+([a-z_][a-z0-9_]+)\s*\(`)
	rustModRe    = regexp.MustCompile(`(?m)^\s*pub\s+mod\s+([a-z_][a-z0-9_]+)`)

	goTypeRe = regexp.MustCompile(`(?m)^\s*type\s+([A-Z][A-Za-z0-9_]+)\s+(?:struct|interface)\b`)
	goFuncRe = regexp.MustCompile(`(?m)^\s*func\s+(?:\([^)]+\)\s+)?([A-Z][A-Za-z0-9_]+)\s*\(`)

	pyClassRe = regexp.MustCompile(`(?m)^\s*class\s+([A-Z][A-Za-z0-9_]+)`)
	pyFuncRe  = regexp.MustCompile(`(?m)^\s*def\s+([a-z_][a-z0-9_]+)\s*\(`)

	tsClassRe = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:default\s+)?class\s+([A-Z][A-Za-z0-9_]+)`)
	tsIfaceRe = regexp.MustCompile(`(?m)^\s*(?:export\s+)?interface\s+([A-Z][A-Za-z0-9_]+)`)
	tsFuncRe  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?function\s+([a-zA-Z_][a-zA-Z0-9_]+)\s*\(`)
)

// extractIdentifiersFromCode pulls declaration names out of a code
// block. Returns verbatim substrings suitable for a MustContain
// check — e.g. "pub struct Concern", "fn new_concern(" (with the
// opening paren so we don't false-match "new_concern_internal").
func extractIdentifiersFromCode(code, lang string, minLen int) []string {
	lang = strings.ToLower(lang)
	var patterns []*regexp.Regexp
	var prefixes []string

	switch lang {
	case "rust", "rs":
		patterns = []*regexp.Regexp{rustStructRe, rustEnumRe, rustTraitRe, rustFnRe, rustModRe}
		prefixes = []string{"pub struct ", "pub enum ", "pub trait ", "pub fn ", "pub mod "}
	case "go":
		patterns = []*regexp.Regexp{goTypeRe, goFuncRe}
		prefixes = []string{"type ", "func "}
	case "python", "py":
		patterns = []*regexp.Regexp{pyClassRe, pyFuncRe}
		prefixes = []string{"class ", "def "}
	case "typescript", "ts", "tsx", "javascript", "js", "jsx":
		patterns = []*regexp.Regexp{tsClassRe, tsIfaceRe, tsFuncRe}
		prefixes = []string{"class ", "interface ", "function "}
	default:
		// No explicit lang — try all patterns. More matches is
		// fine because we're producing hints, not truth.
		patterns = []*regexp.Regexp{
			rustStructRe, rustEnumRe, rustTraitRe, rustFnRe, rustModRe,
			goTypeRe, goFuncRe,
			pyClassRe, pyFuncRe,
			tsClassRe, tsIfaceRe, tsFuncRe,
		}
		prefixes = []string{
			"pub struct ", "pub enum ", "pub trait ", "pub fn ", "pub mod ",
			"type ", "func ",
			"class ", "def ",
			"class ", "interface ", "function ",
		}
	}

	seen := make(map[string]bool)
	var out []string
	for i, re := range patterns {
		matches := re.FindAllStringSubmatch(code, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			name := m[1]
			if len(name) < minLen {
				continue
			}
			full := prefixes[i] + name
			if seen[full] {
				continue
			}
			seen[full] = true
			out = append(out, full)
		}
	}
	return out
}

// extractInlineIdentifiers handles the prose-only case: a paragraph
// that mentions "the Concern struct" or "implement `persys_memory::Store`"
// without a full code block. Pulls backticked tokens and CamelCase
// words that look like type names.
var (
	backtickRe = regexp.MustCompile("`([^`\\s]+)`")
	camelRe    = regexp.MustCompile(`\b([A-Z][a-z0-9]+(?:[A-Z][a-z0-9]*){1,})\b`)
)

func extractInlineIdentifiers(text string, minLen int) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) < minLen || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range backtickRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range camelRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	return out
}

// appendUnique merges identifiers into an existing slice, dedupes,
// and caps at max. Ordering is stable (first-seen wins on dedup).
func appendUnique(existing, add []string, max int) []string {
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e] = true
	}
	out := existing
	for _, a := range add {
		if seen[a] {
			continue
		}
		if len(out) >= max {
			return out
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// autoExtractTaskExpectations builds a supervisor spec for a task
// by:
//  1. Running the existing content_match extraction (buildTaskSupervisor)
//  2. If that returns nil (no explicit criteria), fall back to the
//     auto-extractor that scans the raw SOW for declarations tied to
//     the task's files
//  3. Merging any extracted identifiers into the expectation list
//
// Returns nil only when BOTH sources produce nothing — in which case
// there's genuinely nothing to supervise and the agent runs unchecked
// for this task.
func autoExtractTaskSupervisor(workDir, rawSOW string, session plan.Session, task plan.Task, writesPerCheck int) *specSupervisorSpec {
	// Start with whatever the structured content_match extraction
	// gives us.
	sup := buildTaskSupervisor(workDir, session, task, writesPerCheck)

	// Auto-extract from raw SOW — always run this, even if sup is
	// already non-nil, to merge in additional identifiers the SOW
	// author didn't declare explicitly.
	if strings.TrimSpace(rawSOW) == "" || len(task.Files) == 0 {
		return sup
	}
	extractor := &sowIdentifierExtractor{}
	fileIdents := extractor.extract(rawSOW)

	// Build/augment a spec.
	if sup == nil {
		sup = &specSupervisorSpec{
			WorkDir:        workDir,
			WritesPerCheck: writesPerCheck,
		}
		if sup.WritesPerCheck <= 0 {
			sup.WritesPerCheck = 3
		}
		for _, f := range task.Files {
			sup.Expectations = append(sup.Expectations, taskFileExpectation{File: f})
		}
	}

	// Merge identifiers into matching expectations.
	for i := range sup.Expectations {
		exp := &sup.Expectations[i]
		// Try exact file match first.
		if idents, ok := fileIdents[exp.File]; ok {
			exp.MustContain = mergeStringSet(exp.MustContain, idents)
			continue
		}
		// Try basename match (SOW might say "concern.rs" instead of
		// the full path).
		base := exp.File
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		if idents, ok := fileIdents[base]; ok {
			exp.MustContain = mergeStringSet(exp.MustContain, idents)
		}
	}

	// If STILL nothing to verify after auto-extraction, drop the
	// supervisor so we don't emit empty-scan no-ops.
	hasSomething := false
	for _, e := range sup.Expectations {
		if len(e.MustContain) > 0 || len(e.MustNotContain) > 0 {
			hasSomething = true
			break
		}
	}
	if !hasSomething {
		return nil
	}
	return sup
}

// mergeStringSet merges add into existing with dedup, returning the
// result. Order is stable: existing first, then new entries in the
// order they appeared in add.
func mergeStringSet(existing, add []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e] = true
	}
	for _, a := range add {
		if seen[a] {
			continue
		}
		seen[a] = true
		existing = append(existing, a)
	}
	return existing
}
