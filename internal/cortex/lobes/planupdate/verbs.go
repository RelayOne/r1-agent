// Verb-scan helper for PlanUpdateLobe trigger logic (spec item 17).
//
// The spec says:
//
//	"every 3rd assistant turn boundary, OR on user message containing any
//	 verb from cortex.ActionVerbs (the cortex-core verb-scan helper)"
//
// API adaptation: cortex.ActionVerbs does NOT exist in cortex-core. The
// verb-scan logic lives here as a local actionVerbs slice and a
// case-insensitive whole-word matcher. The vocabulary is intentionally
// narrow — common plan-mutating verbs only — so the trigger does not
// fire on every chat-style message.
package planupdate

import (
	"strings"
	"unicode"
)

// actionVerbs is the canonical vocabulary for the PlanUpdate verb-scan
// trigger. Every term is lower-case and matched as a whole word against
// the user message. The list mirrors the spec's intent: words that
// signal plan mutations (add/remove/edit) without firing on chit-chat.
var actionVerbs = []string{
	"plan",
	"add",
	"remove",
	"delete",
	"rename",
	"split",
	"merge",
	"drop",
	"include",
	"replace",
	"reorder",
}

// scanVerbs reports whether s contains any of the supplied verbs as a
// whole word, ignoring case. Whole-word matching uses Unicode word
// boundaries so "padding" does not match "add" and "merger" does not
// match "merge". The empty string never matches.
func scanVerbs(s string, verbs []string) bool {
	if s == "" || len(verbs) == 0 {
		return false
	}
	lower := strings.ToLower(s)
	for _, v := range verbs {
		if v == "" {
			continue
		}
		if containsWord(lower, v) {
			return true
		}
	}
	return false
}

// containsWord reports whether s (assumed lower-case) contains word as
// a whole word — i.e. the surrounding characters on both sides are
// either start/end-of-string or a non-letter, non-digit rune.
func containsWord(s, word string) bool {
	if word == "" {
		return false
	}
	idx := 0
	for {
		off := strings.Index(s[idx:], word)
		if off < 0 {
			return false
		}
		start := idx + off
		end := start + len(word)
		if isWordBoundary(s, start, end) {
			return true
		}
		// Advance one rune past the current match to keep scanning.
		idx = start + 1
		if idx >= len(s) {
			return false
		}
	}
}

// isWordBoundary reports whether the substring s[start:end] is a
// stand-alone word: the rune immediately before start is not a letter
// or digit (or there is no such rune), and the rune immediately after
// end is not a letter or digit (or there is no such rune).
func isWordBoundary(s string, start, end int) bool {
	if start > 0 {
		prev := rune(s[start-1])
		if unicode.IsLetter(prev) || unicode.IsDigit(prev) {
			return false
		}
	}
	if end < len(s) {
		next := rune(s[end])
		if unicode.IsLetter(next) || unicode.IsDigit(next) {
			return false
		}
	}
	return true
}
