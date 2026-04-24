// Package conflictres provides smart merge conflict resolution.
// Goes beyond text-level three-way merge to understand code semantics:
//
// - Parses conflict markers from git merge output
// - Classifies conflicts (import ordering, whitespace, real semantic)
// - Auto-resolves trivial conflicts (import dedup, formatting)
// - For semantic conflicts, generates structured descriptions for LLM resolution
// - Validates resolved files compile/parse correctly
package conflictres

import (
	"fmt"
	"sort"
	"strings"
)

// ConflictKind classifies a merge conflict.
type ConflictKind string

const (
	KindImport     ConflictKind = "import"     // import list ordering/additions
	KindWhitespace ConflictKind = "whitespace"  // formatting only
	KindTrivial    ConflictKind = "trivial"     // both sides add non-overlapping code
	KindSemantic   ConflictKind = "semantic"    // real logic conflict
	KindDuplicate  ConflictKind = "duplicate"   // both sides added same thing
)

// Conflict represents a single merge conflict in a file.
type Conflict struct {
	File       string       `json:"file"`
	Kind       ConflictKind `json:"kind"`
	StartLine  int          `json:"start_line"`
	Ours       string       `json:"ours"`       // our version
	Theirs     string       `json:"theirs"`     // their version
	Base       string       `json:"base"`       // common ancestor (if available)
	Resolved   string       `json:"resolved,omitempty"`
	AutoResolved bool       `json:"auto_resolved"`
	Confidence float64      `json:"confidence"` // 0-1 for auto-resolved
}

// Resolution is the result of conflict resolution.
type Resolution struct {
	File       string     `json:"file"`
	Conflicts  []Conflict `json:"conflicts"`
	Resolved   string     `json:"resolved_content"`
	AllAuto    bool       `json:"all_auto"`
	NeedReview []int      `json:"need_review,omitempty"` // indices of conflicts needing human/LLM review
}

// Parse extracts conflicts from a file with git conflict markers.
func Parse(content, file string) []Conflict {
	lines := strings.Split(content, "\n")
	var conflicts []Conflict

	i := 0
	for i < len(lines) {
		if strings.HasPrefix(lines[i], "<<<<<<<") {
			c := parseConflict(lines, i, file)
			if c != nil {
				conflicts = append(conflicts, *c)
			}
			// Skip past the conflict
			for i < len(lines) && !strings.HasPrefix(lines[i], ">>>>>>>") {
				i++
			}
		}
		i++
	}

	// Classify each conflict
	for idx := range conflicts {
		conflicts[idx].Kind = classify(conflicts[idx])
	}

	return conflicts
}

// AutoResolve attempts to automatically resolve conflicts.
func AutoResolve(conflicts []Conflict) []Conflict {
	for i := range conflicts {
		switch conflicts[i].Kind {
		case KindWhitespace:
			// Take ours (or theirs, doesn't matter)
			conflicts[i].Resolved = conflicts[i].Ours
			conflicts[i].AutoResolved = true
			conflicts[i].Confidence = 1.0

		case KindDuplicate:
			// Both added the same thing, take either
			conflicts[i].Resolved = conflicts[i].Ours
			conflicts[i].AutoResolved = true
			conflicts[i].Confidence = 1.0

		case KindImport:
			// Merge imports: union of both, sorted, deduped
			resolved := mergeImports(conflicts[i].Ours, conflicts[i].Theirs)
			conflicts[i].Resolved = resolved
			conflicts[i].AutoResolved = true
			conflicts[i].Confidence = 0.95

		case KindTrivial:
			// Both sides added non-overlapping content, combine
			conflicts[i].Resolved = conflicts[i].Ours + "\n" + conflicts[i].Theirs
			conflicts[i].AutoResolved = true
			conflicts[i].Confidence = 0.8
		case KindSemantic:
			// Semantic conflicts cannot be auto-resolved; leave for manual review.
		}
	}
	return conflicts
}

// Resolve applies resolved conflicts to produce the final file content.
func Resolve(content string, conflicts []Conflict) *Resolution {
	lines := strings.Split(content, "\n")
	var result []string
	var needReview []int

	i := 0
	conflictIdx := 0
	for i < len(lines) {
		if strings.HasPrefix(lines[i], "<<<<<<<") && conflictIdx < len(conflicts) {
			c := conflicts[conflictIdx]
			if c.Resolved != "" {
				result = append(result, strings.Split(c.Resolved, "\n")...)
			} else {
				needReview = append(needReview, conflictIdx)
				// Keep conflict markers for manual resolution
				for i < len(lines) {
					result = append(result, lines[i])
					if strings.HasPrefix(lines[i], ">>>>>>>") {
						break
					}
					i++
				}
			}
			// Skip past conflict markers
			for i < len(lines) && !strings.HasPrefix(lines[i], ">>>>>>>") {
				i++
			}
			conflictIdx++
		} else {
			result = append(result, lines[i])
		}
		i++
	}

	return &Resolution{
		File:       conflicts[0].File,
		Conflicts:  conflicts,
		Resolved:   strings.Join(result, "\n"),
		AllAuto:    len(needReview) == 0,
		NeedReview: needReview,
	}
}

// FormatForLLM produces a structured description of unresolved conflicts
// suitable for LLM-based resolution.
func FormatForLLM(conflicts []Conflict) string {
	var b strings.Builder
	unresolved := 0

	for i, c := range conflicts {
		if c.AutoResolved {
			continue
		}
		unresolved++
		fmt.Fprintf(&b, "### Conflict %d (in %s, line %d, kind: %s)\n\n", i+1, c.File, c.StartLine, c.Kind)
		fmt.Fprintf(&b, "**Ours (current branch):**\n```\n%s\n```\n\n", c.Ours)
		fmt.Fprintf(&b, "**Theirs (incoming):**\n```\n%s\n```\n\n", c.Theirs)
		if c.Base != "" {
			fmt.Fprintf(&b, "**Common ancestor:**\n```\n%s\n```\n\n", c.Base)
		}
		b.WriteString("Resolve this conflict by producing the correct merged code.\n\n")
	}

	if unresolved == 0 {
		return "All conflicts were auto-resolved."
	}
	return b.String()
}

// Stats returns summary statistics.
func Stats(conflicts []Conflict) (total, autoResolved, needReview int) {
	for _, c := range conflicts {
		total++
		if c.AutoResolved {
			autoResolved++
		} else {
			needReview++
		}
	}
	return
}

func parseConflict(lines []string, start int, file string) *Conflict {
	c := &Conflict{File: file, StartLine: start + 1}
	var ours, theirs []string
	section := "ours" // ours, base, theirs

	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "|||||||") {
			section = "base"
			continue
		}
		if strings.HasPrefix(lines[i], "=======") {
			section = "theirs"
			continue
		}
		if strings.HasPrefix(lines[i], ">>>>>>>") {
			break
		}

		switch section {
		case "ours":
			ours = append(ours, lines[i])
		case "base":
			c.Base += lines[i] + "\n"
		case "theirs":
			theirs = append(theirs, lines[i])
		}
	}

	c.Ours = strings.Join(ours, "\n")
	c.Theirs = strings.Join(theirs, "\n")
	c.Base = strings.TrimSuffix(c.Base, "\n")
	return c
}

func classify(c Conflict) ConflictKind {
	ours := strings.TrimSpace(c.Ours)
	theirs := strings.TrimSpace(c.Theirs)

	// Identical content = duplicate
	if ours == theirs {
		return KindDuplicate
	}

	// Whitespace-only difference
	if normalizeWhitespace(ours) == normalizeWhitespace(theirs) {
		return KindWhitespace
	}

	// Import conflict detection
	if looksLikeImports(ours) && looksLikeImports(theirs) {
		return KindImport
	}

	// Trivial: one side is a subset of the other or non-overlapping additions
	if strings.Contains(theirs, ours) || strings.Contains(ours, theirs) {
		return KindTrivial
	}

	return KindSemantic
}

func normalizeWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	var normalized []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	return strings.Join(normalized, "\n")
}

func looksLikeImports(s string) bool {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	importCount := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		// An import line is a quoted path optionally with an alias, or import keyword, or parens
		if trimmed == "(" || trimmed == ")" || strings.HasPrefix(trimmed, "import") {
			importCount++
		} else if strings.HasPrefix(trimmed, `"`) && strings.HasSuffix(trimmed, `"`) {
			importCount++
		} else if len(trimmed) > 0 {
			// Check for aliased import: alias "path"
			parts := strings.Fields(trimmed)
			if len(parts) == 2 && strings.HasPrefix(parts[1], `"`) && strings.HasSuffix(parts[1], `"`) {
				importCount++
			}
		}
	}
	return len(lines) > 0 && importCount == len(lines)
}

func mergeImports(ours, theirs string) string {
	oursImports := extractImportPaths(ours)
	theirsImports := extractImportPaths(theirs)

	// Union + dedup
	seen := make(map[string]bool)
	var all []string
	for _, imp := range append(oursImports, theirsImports...) {
		if !seen[imp] {
			seen[imp] = true
			all = append(all, imp)
		}
	}
	sort.Strings(all)

	// Rebuild import block
	var b strings.Builder
	for _, imp := range all {
		fmt.Fprintf(&b, "\t%s\n", imp)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func extractImportPaths(s string) []string {
	var imports []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, `"`) {
			imports = append(imports, trimmed)
		}
	}
	return imports
}
