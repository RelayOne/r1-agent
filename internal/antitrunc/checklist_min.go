// checklist_min.go — minimal checkbox counter shipped alongside the
// Gate so gate.go compiles standalone. Item 6 of the spec replaces
// this with a full scopecheck.go that also reports per-section
// counts and exposes CheckedItems / UncheckedItems lookups. Until
// then, this file owns the symbol.
//
// The regex is intentionally permissive ([ xX]) so user-edited plans
// using uppercase X or stray whitespace are still detected. Lines
// must start with `*`, `-`, or whitespace + one of those, mirroring
// markdown bullet conventions.

package antitrunc

import "regexp"

// checklistLineRE matches a markdown checkbox bullet:
//
//	-[ ] item
//	- [x] item
//	*  [X] item
//
// Capture group 1 is the box contents (` `, `x`, or `X`).
var checklistLineRE = regexp.MustCompile(`(?m)^\s*[*-]\s*\[([ xX])\]`)

// CountChecklist returns (done, total) over every checkbox in the
// markdown text. A box containing `x` or `X` is done; ` ` is not.
//
// Empty text returns (0, 0). Callers should treat total == 0 as "no
// checklist found" and skip the gate signal.
func CountChecklist(text string) (done, total int) {
	matches := checklistLineRE.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		total++
		if m[1] == "x" || m[1] == "X" {
			done++
		}
	}
	return done, total
}
