// Package plan — deliverable.go
//
// Spec-to-deliverable extractor (deeper anti-stub fix). The
// run 40 diagnosis showed tasks like "scaffold shadcn/ui
// components" produced barrel files because the specific
// deliverable list in the SOW ("data table, date picker,
// multi-select, modal") never reached the worker as a
// file-level checklist. Workers interpreted "scaffolding" as
// "put infrastructure in place" rather than "write the
// specific components."
//
// This extractor runs at prompt-build time: given a task
// description + the relevant SOW excerpt, it pulls out
// enumerated deliverables and returns them as a concrete
// checklist the worker MUST satisfy. The checklist gets
// injected into the worker's system prompt alongside the
// task description so there's no way to interpret "scaffold
// X" as "write a barrel file" — the deliverable list spells
// out each component by name.
//
// Pattern recognition (conservative — false negatives
// preferred over false positives):
//
//   - "components (X, Y, Z, W)" / "tools (A, B, C)"
//   - "including X, Y, and Z"
//   - "such as X, Y"
//   - "implement X, Y, Z"
//   - "must provide X, Y, and Z"
//   - Bulleted lists under a deliverable section
//
// Deliberately NOT extracted (too noisy):
//   - Generic nouns ("errors", "logic", "state")
//   - Single-word items without a list context
//   - Tool/lib names mentioned as dependencies
package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Deliverable is one concrete item the worker must produce.
type Deliverable struct {
	// Name is the human-readable name pulled from the SOW
	// (e.g. "data table", "date picker"). Normalized to
	// title case + space-separated.
	Name string

	// Kind classifies the deliverable for file-name
	// synthesis + worker guidance.
	Kind DeliverableKind

	// Source is the raw SOW fragment the extractor pulled
	// this deliverable from. Included in the checklist so
	// the worker can cross-reference.
	Source string
}

// DeliverableKind is a coarse classification. Callers use
// Kind to suggest file paths + stock guidance for the
// worker.
type DeliverableKind string

const (
	// KindComponent: UI component, typically TSX/JSX.
	KindComponent DeliverableKind = "component"

	// KindFunction: a named function / API helper.
	KindFunction DeliverableKind = "function"

	// KindType: typedef / schema / interface.
	KindType DeliverableKind = "type"

	// KindCommand: CLI command / subcommand.
	KindCommand DeliverableKind = "command"

	// KindUnknown: item extracted but kind uncertain.
	KindUnknown DeliverableKind = "unknown"
)

// Extraction patterns. Ordered from most-specific to
// least-specific so an item that matches multiple patterns
// gets the sharpest kind assignment.
var (
	// Each "list" pattern supports BOTH paren-delimited
	// ("components (X, Y, Z)") AND colon-delimited
	// ("components: X, Y, Z") forms. Colon form closes at
	// the first `.`, `;`, or end-of-line so the regex
	// doesn't greedily swallow the next prose sentence.
	componentListRe = regexp.MustCompile(
		`(?i)(component|widget|control)s?\s*(?:\(([^)]+)\)|:\s*([^.;\n]+))`)

	toolListRe = regexp.MustCompile(
		`(?i)(tool|utility|utilities|api|apis|helper|helpers)s?\s*(?:\(([^)]+)\)|:\s*([^.;\n]+))`)

	typeListRe = regexp.MustCompile(
		`(?i)(type|typedef|schema|interface)s?\s*(?:\(([^)]+)\)|:\s*([^.;\n]+))`)

	// "including X, Y, and Z" / "such as X, Y".
	// The item-body captures up to 2 words per list item
	// (so "data table" survives but "handlers for session
	// management" does not). Requires at least one comma
	// to avoid matching "including login handling" as a
	// single-item list with trailing prose.
	inclusionRe = regexp.MustCompile(
		`(?i)(?:including|such as|like)\s+([a-z][a-z\-]*(?:\s+[a-z][a-z\-]*)?(?:\s*,\s*(?:and\s+)?[a-z][a-z\-]*(?:\s+[a-z][a-z\-]*)?){1,})`)

	// "implement X, Y, and Z" — imperative verbs.
	implementListRe = regexp.MustCompile(
		`(?i)(?:implement|provide|ship|deliver|expose)s?\s+([a-z][a-z\-]*(?:\s+[a-z][a-z\-]*)?(?:\s*,\s*(?:and\s+)?[a-z][a-z\-]*(?:\s+[a-z][a-z\-]*)?){1,})`)
)

// ExtractDeliverables scans text (task description + SOW
// excerpt, concatenated) and returns the identified
// deliverable list. Deduplicated + sorted by Name for
// deterministic output.
func ExtractDeliverables(text string) []Deliverable {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	seen := map[string]Deliverable{}

	addMatches := func(re *regexp.Regexp, kind DeliverableKind) {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			// Paren-form + colon-form share a regex via two
			// alternative capture groups. Pick the first
			// non-empty one as the list body; falls back to
			// the last group for simple single-body regexes
			// (inclusion / implement).
			body := ""
			for i := 1; i < len(m); i++ {
				if strings.TrimSpace(m[i]) != "" {
					body = m[i]
					// For the branched "kind prefix + list"
					// regexes, group 1 is the prefix — skip
					// it when a later group has content.
					if i == 1 && len(m) > 2 {
						continue
					}
					break
				}
			}
			if body == "" {
				body = m[len(m)-1]
			}
			for _, name := range splitList(body) {
				name = normalizeDeliverableName(name)
				if !isValidDeliverable(name) {
					continue
				}
				key := strings.ToLower(name)
				if prev, ok := seen[key]; ok {
					// Prefer the more-specific kind on dup.
					if kindRank(kind) < kindRank(prev.Kind) {
						prev.Kind = kind
						seen[key] = prev
					}
					continue
				}
				seen[key] = Deliverable{
					Name:   name,
					Kind:   kind,
					Source: strings.TrimSpace(m[0]),
				}
			}
		}
	}

	addMatches(componentListRe, KindComponent)
	addMatches(toolListRe, KindFunction)
	addMatches(typeListRe, KindType)
	// Generic lists run last — their kind is unknown unless
	// a more-specific pattern already matched.
	addMatches(inclusionRe, KindUnknown)
	addMatches(implementListRe, KindUnknown)

	out := make([]Deliverable, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// splitList splits a comma/semicolon-separated list,
// handling the " and " conjunction Before the last item.
func splitList(s string) []string {
	// Replace " and " with a comma so the split works
	// uniformly.
	s = strings.ReplaceAll(s, " and ", ", ")
	// Also handle "&".
	s = strings.ReplaceAll(s, " & ", ", ")
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// normalizeDeliverableName cleans up a raw fragment:
// strip surrounding punctuation, collapse whitespace,
// lowercase-then-titlecase the first letter of each word.
func normalizeDeliverableName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ".:;\"'()[]")
	// Collapse whitespace.
	parts := strings.Fields(s)
	s = strings.Join(parts, " ")
	return s
}

// isValidDeliverable rejects generic nouns + obviously-
// wrong tokens that slip through the list patterns.
func isValidDeliverable(s string) bool {
	if s == "" {
		return false
	}
	if len(s) < 3 {
		return false
	}
	// Length cap: very long strings are usually prose
	// that got caught by the regex, not a deliverable.
	if len(s) > 50 {
		return false
	}
	// Reject common noise tokens. These are nouns that
	// frequently appear in SOW prose as list items but don't
	// describe concrete deliverables — flagging them as
	// mandatory files would make the checklist noise-heavy
	// and produce false positives that distract the worker.
	low := strings.ToLower(s)
	noise := map[string]bool{
		"etc": true, "more": true, "others": true, "items": true,
		"things": true, "stuff": true, "everything": true,
		"anything": true, "some": true, "several": true,
		"many": true, "a few": true, "various": true,
		"this": true, "that": true, "these": true, "those": true,
		"which": true, "what": true, "how": true,
		// Abstract category nouns — not concrete deliverables.
		"errors": true, "logic": true, "state": true,
		"state management": true, "data": true, "content": true,
		"behavior": true, "logic management": true,
		"validation": true, "handling": true, "management": true,
		"processing": true, "code": true, "files": true,
		"tests": true, "docs": true, "documentation": true,
	}
	if noise[low] {
		return false
	}
	return true
}

func kindRank(k DeliverableKind) int {
	switch k {
	case KindComponent:
		return 0
	case KindType:
		return 1
	case KindFunction:
		return 2
	case KindCommand:
		return 3
	default:
		return 99
	}
}

// RenderChecklist produces a prompt-ready checklist block
// for the worker. Empty list → empty string so the caller
// can skip injection when nothing was extractable.
//
// Wording is SHAPED BY KIND: component/type items typically
// merit their own file; function/command/unknown items may
// legitimately live together in a router or schema file.
// The anti-barrel language only applies when most items are
// component-class, so callers don't get told to split HTTP
// methods (get/post/put/delete) into four separate files.
func RenderChecklist(ds []Deliverable) string {
	if len(ds) == 0 {
		return ""
	}
	// Count component-class items to decide whether the
	// per-file language is appropriate.
	componentLike := 0
	for _, d := range ds {
		if d.Kind == KindComponent || d.Kind == KindType {
			componentLike++
		}
	}
	oneFilePerItem := componentLike*2 >= len(ds) // majority rule

	var b strings.Builder
	if oneFilePerItem {
		b.WriteString("MANDATORY DELIVERABLES (each must exist as a dedicated source file with real implementation — NOT a single barrel file with a comment promising them later):\n")
	} else {
		b.WriteString("MANDATORY DELIVERABLES (each must be implemented with real, working code — may share files where the SOW's shape suggests it, but all must exist):\n")
	}
	for i, d := range ds {
		fmt.Fprintf(&b, "  %d. %s", i+1, d.Name)
		if d.Kind != KindUnknown {
			fmt.Fprintf(&b, " [%s]", d.Kind)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if oneFilePerItem {
		b.WriteString("A single file that re-exports a stub or contains a comment like 'components will be added later' DOES NOT SATISFY this checklist. Each item above requires its own source file with working implementation code.\n")
	} else {
		b.WriteString("A barrel file that only re-exports a stub or contains placeholders does NOT SATISFY this checklist. Follow the SOW's specified file layout — one file per item when the spec implies that, or shared files when the spec shows them grouped (e.g. HTTP methods sharing a controller, types in one schema module).\n")
	}
	return b.String()
}

// FileMinBytes is the per-file minimum-size guard for the
// post-dispatch check. Files under this threshold that
// appeared in a task's declared-files list fail the
// integrity gate with "file is suspiciously small —
// probably a stub."
//
// Tuned to 256 bytes: a real import + one-line export is
// typically ~80-150 bytes, but a substantive file
// implementing a declared component, type, or function is
// almost always > 256. Operators can override via
// Task.MinBytes if they genuinely want a tiny file.
const FileMinBytes = 256
