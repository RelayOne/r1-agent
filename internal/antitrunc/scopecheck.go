// scopecheck.go — markdown checkbox parsing for plans and specs.
//
// Used by the Gate to detect unchecked items, and by the
// `r1 antitrunc verify` CLI to cross-reference commit "done"
// claims against the actual checklist state. Also exported for
// supervisor rules and the cortex Lobe.
//
// The regex `^[*-]\s*\[([ xX])\]` follows GFM-style task list
// syntax with relaxed whitespace handling. We deliberately accept
// upper-case X so a contributor who hand-edits a plan in vim with
// caps-lock active is still counted as "checked".
//
// Functions exposed:
//
//   - CountChecklist(text)        — total / done counts.
//                                   (defined in checklist_min.go;
//                                   re-exported for clarity here)
//   - ChecklistItems(text)        — every line plus its checked state.
//   - UncheckedItems(text)        — the subset of items with [ ].
//   - CheckedItems(text)          — the subset with [x] or [X].
//   - ScopeReport(path)           — read file + return structured report.
//   - SpecStatus(text)            — extract STATUS from header comment.
package antitrunc

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// checklistFullRE captures both the box state AND the item text so
// ChecklistItems can return human-readable labels for use in
// findings. The line is the entire matched line minus the bullet
// marker.
var checklistFullRE = regexp.MustCompile(`(?m)^\s*[*-]\s*\[([ xX])\]\s*(.*)$`)

// statusHeaderRE pulls STATUS from <!-- STATUS: <value> --> or bare
// `STATUS: <value>` lines. Values are returned lowercase.
var statusHeaderRE = regexp.MustCompile(`(?im)<!--\s*STATUS:\s*([a-zA-Z_-]+)\s*-->|^STATUS:\s*([a-zA-Z_-]+)`)

// ChecklistItem is a single parsed checkbox line. Index is
// 1-indexed (matching how humans count items in plans).
type ChecklistItem struct {
	Index   int
	Checked bool
	Text    string
}

// ChecklistItems parses every checkbox line in markdown.
//
// Empty input returns nil. Order is preserved (file order).
func ChecklistItems(text string) []ChecklistItem {
	matches := checklistFullRE.FindAllStringSubmatch(text, -1)
	out := make([]ChecklistItem, 0, len(matches))
	for i, m := range matches {
		out = append(out, ChecklistItem{
			Index:   i + 1,
			Checked: m[1] == "x" || m[1] == "X",
			Text:    strings.TrimSpace(m[2]),
		})
	}
	return out
}

// UncheckedItems returns only the unchecked subset.
func UncheckedItems(text string) []ChecklistItem {
	all := ChecklistItems(text)
	var out []ChecklistItem
	for _, it := range all {
		if !it.Checked {
			out = append(out, it)
		}
	}
	return out
}

// CheckedItems returns only the checked subset.
func CheckedItems(text string) []ChecklistItem {
	all := ChecklistItems(text)
	var out []ChecklistItem
	for _, it := range all {
		if it.Checked {
			out = append(out, it)
		}
	}
	return out
}

// ScopeReport summarises a plan or spec file.
type ScopeReport struct {
	Path      string
	Status    string // lowercase, e.g. "in-progress" or "" if absent.
	Total     int
	Done      int
	Unchecked []ChecklistItem
}

// IsComplete reports whether the report is fully checked. Files
// with zero checklist items return false (no claim either way).
func (r ScopeReport) IsComplete() bool {
	return r.Total > 0 && r.Done == r.Total
}

// PercentDone returns the completion ratio as a float in [0, 1].
// Files with no checklist return 0.
func (r ScopeReport) PercentDone() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Done) / float64(r.Total)
}

// ScopeReportFromText parses scope state from raw markdown.
func ScopeReportFromText(path, text string) ScopeReport {
	done, total := CountChecklist(text)
	return ScopeReport{
		Path:      path,
		Status:    SpecStatus(text),
		Total:     total,
		Done:      done,
		Unchecked: UncheckedItems(text),
	}
}

// ScopeReportFromFile reads path and parses scope state. Returns
// (zero-value-with-Path, err) on read error.
func ScopeReportFromFile(path string) (ScopeReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScopeReport{Path: path}, fmt.Errorf("read %s: %w", path, err)
	}
	return ScopeReportFromText(path, string(data)), nil
}

// SpecStatus returns the lowercase STATUS value from a spec header,
// or "" if no STATUS marker is present. Recognises both
// `<!-- STATUS: in-progress -->` and bare `STATUS: in-progress`.
func SpecStatus(text string) string {
	m := statusHeaderRE.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	// One of the two capture groups will be set.
	for _, g := range m[1:] {
		if g != "" {
			return strings.ToLower(strings.TrimSpace(g))
		}
	}
	return ""
}
