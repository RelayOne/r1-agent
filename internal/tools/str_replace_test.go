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
