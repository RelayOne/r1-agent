// Task-specific spec extraction: the fix for "agent hallucinates
// plausible-but-wrong identifiers because the SOW's authoritative spec
// is 32k characters away in the cached system block."
//
// Strategy: for each task, grep the raw SOW text for identifiers that
// identify this task (file paths, filenames without extensions, crate
// names derived from paths, identifiers from task.Description). Extract
// the surrounding paragraphs. Inject the result INTO the user prompt
// at the point of use so the model reads it the same turn it's about
// to start writing code.
//
// The raw SOW in the system prompt is still useful as the fallback
// "source of truth" for grepping — but the per-task excerpt is what
// the model actually acts on.
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/plan"
)

// toEngineSupervisor translates a specSupervisorSpec into the
// engine.SupervisorConfig the native runner expects. Returns nil if
// the spec is nil so call sites can pass the result directly to
// execNativeTask without a further check.
func toEngineSupervisor(s *specSupervisorSpec) *engine.SupervisorConfig {
	if s == nil {
		return nil
	}
	out := &engine.SupervisorConfig{
		WorkDir:        s.WorkDir,
		WritesPerCheck: s.WritesPerCheck,
	}
	for _, e := range s.Expectations {
		out.Expectations = append(out.Expectations, engine.SpecExpectation{
			File:           e.File,
			MustContain:    e.MustContain,
			MustNotContain: e.MustNotContain,
		})
	}
	return out
}

// specExcerptConfig controls how aggressive the excerpt extraction is.
type specExcerptConfig struct {
	// MaxChars is the cap on the combined excerpt text. Defaults to 4000
	// so it fits comfortably in the user message without ballooning.
	MaxChars int
	// ParagraphRadius is how many paragraphs around a match to include
	// (1 = the matched paragraph + 1 before + 1 after). Defaults to 1.
	ParagraphRadius int
}

// extractTaskSpecExcerpt pulls the SOW paragraphs most relevant to a
// specific task and returns them as a single string suitable for the
// user prompt. Returns "" if no matches are found or if rawSOW is
// empty.
//
// Matching sources (in priority order):
//  1. task.Files — full path matches
//  2. basenames of task.Files (e.g. "concern.rs")
//  3. crate names derived from paths (e.g. "persys-concern" from
//     "crates/persys-concern/src/lib.rs")
//  4. content_match.File / content_match.Pattern from session
//     acceptance criteria that reference files the task will write
//  5. "Identifier-like" tokens from task.Description (CamelCase,
//     snake_case with underscores, words with hyphens)
//
// Paragraphs containing any match are pulled out along with N
// surrounding paragraphs (default 1 on each side). Duplicates are
// removed and the result is capped at MaxChars.
func extractTaskSpecExcerpt(rawSOW string, session plan.Session, task plan.Task, cfg specExcerptConfig) string {
	if strings.TrimSpace(rawSOW) == "" {
		return ""
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = 4000
	}
	if cfg.ParagraphRadius <= 0 {
		cfg.ParagraphRadius = 1
	}

	// Build the search terms.
	terms := collectTaskSearchTerms(session, task)
	if len(terms) == 0 {
		return ""
	}

	// Split into paragraphs. A paragraph boundary is a blank line OR a
	// markdown heading (## or bigger) so we don't run different
	// sections together.
	paragraphs := splitIntoParagraphs(rawSOW)
	if len(paragraphs) == 0 {
		return ""
	}

	// Find matching paragraph indices. Case-insensitive for English
	// words (e.g. a term like "persys-concern" should match "the
	// PERSYS-CONCERN crate" in prose), but case-sensitive for terms
	// that contain characters like `/` or `.` or `_` (those look like
	// code identifiers and case matters).
	lowerParagraphs := make([]string, len(paragraphs))
	for i, p := range paragraphs {
		lowerParagraphs[i] = strings.ToLower(p)
	}
	matches := make(map[int]bool)
	for i := range paragraphs {
		for _, term := range terms {
			if term == "" {
				continue
			}
			if isCodeLikeTerm(term) {
				if strings.Contains(paragraphs[i], term) {
					matches[i] = true
					break
				}
			} else {
				if strings.Contains(lowerParagraphs[i], strings.ToLower(term)) {
					matches[i] = true
					break
				}
			}
		}
	}
	if len(matches) == 0 {
		return ""
	}

	// Expand each match by ParagraphRadius on both sides.
	included := make(map[int]bool)
	for idx := range matches {
		start := idx - cfg.ParagraphRadius
		if start < 0 {
			start = 0
		}
		end := idx + cfg.ParagraphRadius
		if end >= len(paragraphs) {
			end = len(paragraphs) - 1
		}
		for j := start; j <= end; j++ {
			included[j] = true
		}
	}

	// Collect in document order.
	ordered := make([]int, 0, len(included))
	for idx := range included {
		ordered = append(ordered, idx)
	}
	sort.Ints(ordered)

	// Assemble with gap markers so the model knows where we skipped.
	var b strings.Builder
	prev := -2
	for _, idx := range ordered {
		if prev >= 0 && idx != prev+1 {
			b.WriteString("...\n\n")
		}
		b.WriteString(paragraphs[idx])
		b.WriteString("\n\n")
		prev = idx
	}

	out := strings.TrimSpace(b.String())
	if len(out) > cfg.MaxChars {
		out = out[:cfg.MaxChars] + "\n...(excerpt truncated)"
	}
	return out
}

// collectTaskSearchTerms builds the set of strings we'll grep the SOW
// for. Each entry is a verbatim substring match (case-sensitive for
// identifiers, loose for basenames).
func collectTaskSearchTerms(session plan.Session, task plan.Task) []string {
	seen := make(map[string]bool)
	var terms []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(s) < 3 {
			return
		}
		if seen[s] {
			return
		}
		seen[s] = true
		terms = append(terms, s)
	}

	// 1. Task file paths (full)
	for _, f := range task.Files {
		add(f)
		// 2. Basename (e.g. "concern.rs")
		if idx := strings.LastIndex(f, "/"); idx >= 0 && idx < len(f)-1 {
			add(f[idx+1:])
		}
		// 3. Crate name from path: crates/persys-concern/... → persys-concern
		if strings.HasPrefix(f, "crates/") {
			rest := strings.TrimPrefix(f, "crates/")
			if slash := strings.Index(rest, "/"); slash > 0 {
				add(rest[:slash])
			}
		}
		// 4. Directory component just above the file, e.g. "src/auth" → "auth"
		parts := strings.Split(f, "/")
		if len(parts) >= 2 {
			parent := parts[len(parts)-2]
			if parent != "src" && parent != "lib" {
				add(parent)
			}
		}
	}

	// 5. Session outputs
	for _, o := range session.Outputs {
		add(strings.TrimSuffix(o, "/"))
		if idx := strings.LastIndex(o, "/"); idx >= 0 && idx < len(o)-1 {
			add(o[idx+1:])
		}
	}

	// 6. Content-match patterns from session acceptance criteria that
	// reference files the task declares
	taskFileSet := make(map[string]bool)
	for _, f := range task.Files {
		taskFileSet[f] = true
	}
	for _, ac := range session.AcceptanceCriteria {
		if ac.ContentMatch != nil && taskFileSet[ac.ContentMatch.File] {
			add(ac.ContentMatch.Pattern)
		}
		// FileExists ACs: the file path itself is already in terms; no
		// extra tokens to add. Intentionally not an `if` branch so
		// staticcheck SA9003 is happy.
	}

	// 7. Identifier-like tokens from task.Description
	// (CamelCase words, snake_case words ≥ 2 underscores, hyphenated
	// words, paths with slashes, backtick-quoted identifiers)
	identRe := regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]+[A-Z][a-zA-Z0-9]+|[a-z]+_[a-z_]+|[a-z]+-[a-z-]+)\b|` + "`" + `([^` + "`" + `]+)` + "`")
	for _, m := range identRe.FindAllStringSubmatch(task.Description, -1) {
		if m[1] != "" {
			add(m[1])
		}
		if m[2] != "" {
			add(m[2])
		}
	}

	return terms
}

// splitIntoParagraphs breaks raw text into paragraphs. A paragraph
// isCodeLikeTerm decides whether a search term should be matched
// case-sensitively. Terms containing path separators, dots, or
// underscores look like code identifiers (e.g. "crates/foo",
// "concern.rs", "pub struct Concern") and are matched verbatim. All
// other terms are matched case-insensitively so English prose like
// "the persys-concern crate" hits a term like "persys-concern".
func isCodeLikeTerm(term string) bool {
	for _, c := range term {
		if c == '/' || c == '.' || c == '_' || c == '(' || c == ')' || c == '<' || c == '>' || c == ':' {
			return true
		}
	}
	return false
}

// splitIntoParagraphs breaks raw text into paragraphs. A paragraph
// boundary is one or more blank lines OR a markdown heading (#, ##,
// ### etc.) on its own line. Headings are kept with the following
// paragraph so section titles stay attached to their content.
func splitIntoParagraphs(s string) []string {
	lines := strings.Split(s, "\n")
	var paragraphs []string
	var current strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Heading: flush current, start a new paragraph with the heading.
		if strings.HasPrefix(trimmed, "#") && len(trimmed) > 1 && trimmed[1] != '!' {
			if current.Len() > 0 {
				p := strings.TrimSpace(current.String())
				if p != "" {
					paragraphs = append(paragraphs, p)
				}
				current.Reset()
			}
			current.WriteString(line)
			current.WriteString("\n")
			continue
		}
		// Blank line: flush.
		if trimmed == "" {
			if current.Len() > 0 {
				p := strings.TrimSpace(current.String())
				if p != "" {
					paragraphs = append(paragraphs, p)
				}
				current.Reset()
			}
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		p := strings.TrimSpace(current.String())
		if p != "" {
			paragraphs = append(paragraphs, p)
		}
	}
	return paragraphs
}

// buildTaskIdentifierChecklist returns a pre-code checklist the model
// must answer (internally) before writing. Forces the model to
// explicitly match its planned identifiers against what the spec
// actually says. Empty string when there's nothing task-specific to
// check.
func buildTaskIdentifierChecklist(session plan.Session, task plan.Task) string {
	idents := taskCanonicalIdentifiers(session, task)
	if len(idents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("BEFORE YOU WRITE ANY CODE — answer these questions internally:\n")
	b.WriteString("  1. Does every identifier below appear in the SPEC EXCERPT as an exact match?\n")
	b.WriteString("  2. Is my planned module/struct/function naming literally the same (not a paraphrase)?\n")
	b.WriteString("  3. If the spec defines a struct with specific fields, do I have the exact field names and count?\n")
	b.WriteString("\nCANONICAL IDENTIFIERS FOR THIS TASK (use EXACTLY as written):\n")
	for _, ident := range idents {
		fmt.Fprintf(&b, "  • %s\n", ident)
	}
	b.WriteString("\nIf any planned identifier differs from what's here — STOP and re-read the spec excerpt. The spec always wins.\n")
	return b.String()
}

// buildTaskSupervisor assembles the SupervisorConfig the agentloop's
// midturn hook will use to check for spec drift during a task.
// Expectations are built from:
//  1. task.Files — each file gets an expectation entry
//  2. acceptance_criteria.ContentMatch targeting the task's files —
//     the pattern becomes a MustContain string (the verbatim expected
//     substring the spec calls out)
//
// Returns nil when no meaningful expectations can be built (no task
// files OR no content_match patterns), since a supervisor with no
// expectations would just burn turns for nothing.
func buildTaskSupervisor(workDir string, session plan.Session, task plan.Task, writesPerCheck int) *specSupervisorSpec {
	if len(task.Files) == 0 {
		return nil
	}
	taskFileSet := make(map[string]bool)
	for _, f := range task.Files {
		taskFileSet[f] = true
	}

	expectations := make([]taskFileExpectation, 0, len(task.Files))
	for _, f := range task.Files {
		exp := taskFileExpectation{File: f}
		// Pull in content_match patterns that target this file.
		for _, ac := range session.AcceptanceCriteria {
			if ac.ContentMatch == nil {
				continue
			}
			if ac.ContentMatch.File == f && ac.ContentMatch.Pattern != "" {
				exp.MustContain = append(exp.MustContain, ac.ContentMatch.Pattern)
			}
		}
		expectations = append(expectations, exp)
	}

	// If NONE of the expectations have any MustContain entries, the
	// supervisor has nothing to verify — skip it so we don't make
	// useless turn-by-turn no-op calls.
	hasSomething := false
	for _, e := range expectations {
		if len(e.MustContain) > 0 || len(e.MustNotContain) > 0 {
			hasSomething = true
			break
		}
	}
	if !hasSomething {
		return nil
	}
	if writesPerCheck <= 0 {
		writesPerCheck = 3
	}
	return &specSupervisorSpec{
		WorkDir:        workDir,
		Expectations:   expectations,
		WritesPerCheck: writesPerCheck,
	}
}

// specSupervisorSpec is the local analogue of engine.SupervisorConfig
// — we use a local type here so sow_task_spec.go doesn't have to
// import the engine package's internal types, and the caller
// translates to engine.SupervisorConfig at the use site.
type specSupervisorSpec struct {
	WorkDir        string
	Expectations   []taskFileExpectation
	WritesPerCheck int
}

type taskFileExpectation struct {
	File           string
	MustContain    []string
	MustNotContain []string
}

// taskCanonicalIdentifiers returns the identifiers that should be
// considered canonical for THIS specific task. Narrower than
// buildCanonicalNamesBlock which dumps everything for the whole
// session.
func taskCanonicalIdentifiers(session plan.Session, task plan.Task) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	// Files this task will create/modify
	for _, f := range task.Files {
		add(f)
	}
	// Content-match patterns from session acceptance criteria that
	// target THIS task's files (those are verbatim expected strings,
	// often containing exact type/field names)
	taskFileSet := make(map[string]bool)
	for _, f := range task.Files {
		taskFileSet[f] = true
	}
	for _, ac := range session.AcceptanceCriteria {
		if ac.ContentMatch != nil && taskFileSet[ac.ContentMatch.File] {
			add(ac.ContentMatch.Pattern)
		}
	}
	sort.Strings(out)
	return out
}
