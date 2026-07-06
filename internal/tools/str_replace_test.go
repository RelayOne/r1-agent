package tools

import (
	"strings"
	"testing"
)

func TestStrReplaceExact(t *testing.T) {
	content := "hello world\ngoodbye world\n"
	r, err := StrReplace(content, "hello", "hi", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "exact" {
		t.Errorf("method=%s, want exact", r.Method)
	}
	if !strings.Contains(r.NewContent, "hi world") {
		t.Error("expected 'hi world'")
	}
	if r.Confidence != 1.0 {
		t.Errorf("confidence=%f, want 1.0", r.Confidence)
	}
}

func TestStrReplaceExactMultiple(t *testing.T) {
	content := "foo bar\nfoo baz\n"
	_, err := StrReplace(content, "foo", "qux", false)
	if err == nil {
		t.Error("should error on multiple matches without replace_all")
	}
	if !strings.Contains(err.Error(), "2 times") {
		t.Errorf("error=%q, should mention count", err.Error())
	}
}

func TestStrReplaceAll(t *testing.T) {
	content := "foo bar\nfoo baz\n"
	r, err := StrReplace(content, "foo", "qux", true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Replacements != 2 {
		t.Errorf("replacements=%d, want 2", r.Replacements)
	}
	if strings.Contains(r.NewContent, "foo") {
		t.Error("should replace all foo")
	}
}

func TestStrReplaceWhitespace(t *testing.T) {
	// Extra spaces around tokens, but same line structure
	content := "func   hello()   {\n   return\n}\n"
	old := "func hello() {\n   return\n}"
	r, err := StrReplace(content, old, "func hello() {\n   return 42\n}", false)
	if err != nil {
		t.Fatal(err)
	}
	// May match via whitespace or fuzzy — both are valid cascade results
	if r.Method != "whitespace" && r.Method != "fuzzy" {
		t.Errorf("method=%s, want whitespace or fuzzy", r.Method)
	}
}

func TestStrReplaceEllipsis(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	old := "line1...line5"
	r, err := StrReplace(content, old, "replaced", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "ellipsis" {
		t.Errorf("method=%s, want ellipsis", r.Method)
	}
	if !strings.Contains(r.NewContent, "replaced") {
		t.Error("expected replacement content")
	}
}

func TestStrReplaceFuzzy(t *testing.T) {
	// Content has tabs, old_string has spaces — should match via cascade
	content := "func hello() {\n\treturn 1\n}\n"
	old := "func hello() {\n    return 1\n}"
	r, err := StrReplace(content, old, "func hello() {\n\treturn 2\n}", false)
	if err != nil {
		t.Fatal(err)
	}
	// May match via whitespace or fuzzy depending on normalization
	if r.Method != "whitespace" && r.Method != "fuzzy" {
		t.Errorf("method=%s, want whitespace or fuzzy", r.Method)
	}
}

func TestStrReplaceNotFound(t *testing.T) {
	content := "hello world\n"
	_, err := StrReplace(content, "not here", "x", false)
	if err == nil {
		t.Error("should error on not found")
	}
}

func TestStrReplaceEmptyOld(t *testing.T) {
	_, err := StrReplace("content", "", "x", false)
	if err == nil {
		t.Error("should error on empty old_string")
	}
}

// TestStrReplaceWhitespaceAmbiguous covers the gap where the whitespace tier
// anchored on the FIRST strings.Index match with no uniqueness check, silently
// editing the wrong occurrence. An oldStr that whitespace-normalizes to two
// distinct locations must error (like the exact tier), not edit.
func TestStrReplaceWhitespaceAmbiguous(t *testing.T) {
	// Two blocks that are identical after whitespace normalization but differ
	// in raw spacing (extra spaces at the same token boundaries), so the exact
	// tier finds neither and defers to whitespace.
	content := "x  =  foo(a,  b)\ny = 1\nx = foo(a,   b)\n"
	old := "x = foo(a, b)"
	_, err := StrReplace(content, old, "REPLACED", false)
	if err == nil {
		t.Fatal("expected ambiguity error for whitespace-normalized double match")
	}
	if !strings.Contains(err.Error(), "2 locations") {
		t.Errorf("error=%q, should report the location count", err.Error())
	}
}

// TestStrReplaceWhitespaceAmbiguousReplaceAll shows replace_all=true resolves
// the ambiguity by editing every matching block.
func TestStrReplaceWhitespaceAmbiguousReplaceAll(t *testing.T) {
	content := "x  =  foo(a,  b)\ny = 1\nx = foo(a,   b)\n"
	old := "x = foo(a, b)"
	r, err := StrReplace(content, old, "REPLACED", true)
	if err != nil {
		t.Fatalf("replace_all should succeed: %v", err)
	}
	if r.Replacements != 2 {
		t.Errorf("replacements=%d, want 2", r.Replacements)
	}
	if strings.Contains(r.NewContent, "foo(") {
		t.Errorf("both blocks should be replaced: %q", r.NewContent)
	}
}

// TestStrReplaceWhitespaceUnique confirms a single whitespace-normalized match
// still edits correctly (no false ambiguity).
func TestStrReplaceWhitespaceUnique(t *testing.T) {
	content := "func   hello()   {\n   return\n}\n"
	old := "func hello() {\n   return\n}"
	r, err := StrReplace(content, old, "func hello() {\n   return 42\n}", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "whitespace" && r.Method != "fuzzy" {
		t.Errorf("method=%s, want whitespace or fuzzy", r.Method)
	}
}

// TestStrReplaceEllipsisAmbiguous covers the same gap for the ellipsis tier:
// when the `first` anchor occurs at multiple locations (each with a following
// `last`), the edit target is ambiguous and must error.
func TestStrReplaceEllipsisAmbiguous(t *testing.T) {
	// "start" ... "end" — two disjoint start/end regions.
	content := "start\nA\nend\nmiddle\nstart\nB\nend\n"
	old := "start...end"
	_, err := StrReplace(content, old, "REPLACED", false)
	if err == nil {
		t.Fatal("expected ambiguity error for ellipsis with two anchor regions")
	}
	if !strings.Contains(err.Error(), "2 locations") {
		t.Errorf("error=%q, should report the anchor count", err.Error())
	}
}

// TestStrReplaceUnicodeTier covers SOTA gap #10: LLMs and copy-paste from
// rendered docs emit smart quotes / em-dashes / non-breaking spaces (or an
// NFD form) where the file has ASCII/NFC. The exact tier fails on
// visually-identical bytes; the unicode tier folds and matches, splicing
// into the original file bytes.
func TestStrReplaceUnicodeTier(t *testing.T) {
	// File has ASCII apostrophe + straight quotes + hyphen; the model's
	// old_string uses curly quotes, em-dash, and a non-breaking space.
	content := "msg := \"it's fine\" // co-op\nnext := 1\n"
	oldStr := "msg := “it’s fine” // co—op" // curly quotes + em-dash
	newStr := "msg := \"it is fine\" // coop"

	r, err := StrReplace(content, oldStr, newStr, false)
	if err != nil {
		t.Fatalf("unicode-tier StrReplace failed: %v", err)
	}
	if r.Method != "unicode" {
		t.Errorf("Method = %q, want unicode", r.Method)
	}
	if !strings.Contains(r.NewContent, "it is fine") || strings.Contains(r.NewContent, "it's fine") {
		t.Errorf("replacement not applied: %q", r.NewContent)
	}
	// The untouched second line must survive verbatim.
	if !strings.Contains(r.NewContent, "next := 1") {
		t.Errorf("untouched content lost: %q", r.NewContent)
	}
}

// TestStrReplaceUnicodeNBSP covers non-breaking-space folding specifically.
func TestStrReplaceUnicodeNBSP(t *testing.T) {
	content := "func f(a int) {}\n"          // regular spaces
	oldStr := "func f(a int) {}" // non-breaking spaces
	r, err := StrReplace(content, oldStr, "func g(a int) {}", false)
	if err != nil {
		t.Fatalf("nbsp fold failed: %v", err)
	}
	if r.Method != "unicode" || !strings.Contains(r.NewContent, "func g") {
		t.Errorf("nbsp not folded: method=%q content=%q", r.Method, r.NewContent)
	}
}

// TestStrReplaceExactStillWins ensures the unicode tier does not shadow an
// exact match (no false "unicode" attribution for clean ASCII).
func TestStrReplaceExactStillWins(t *testing.T) {
	r, err := StrReplace("a := 1\n", "a := 1", "a := 2", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "exact" {
		t.Errorf("Method = %q, want exact", r.Method)
	}
}

// TestFoldUnicodeIsConservative ensures we only fold the intended
// confusables and leave other Unicode intact.
func TestFoldUnicodeIsConservative(t *testing.T) {
	if got := foldUnicode("café — “x”"); got != "cafe - \"x\"" {
		// NFC leaves café composed; em-dash and curly quotes fold.
		if got != "café - \"x\"" {
			t.Errorf("foldUnicode = %q, want ASCII punctuation with letters preserved", got)
		}
	}
	// A genuinely distinct CJK char must not be folded away.
	if foldUnicode("日本語") != "日本語" {
		t.Error("foldUnicode altered CJK content")
	}
}
