package diffcomp

import (
	"strings"
	"testing"
)

func TestDiffIdentical(t *testing.T) {
	d := Diff("hello\nworld", "hello\nworld")
	if len(d.Hunks) != 0 {
		t.Errorf("identical content should have no hunks, got %d", len(d.Hunks))
	}
}

func TestDiffSimpleAdd(t *testing.T) {
	d := Diff("line1\nline2", "line1\nnewline\nline2")
	if d.Added != 1 {
		t.Errorf("expected 1 added, got %d", d.Added)
	}
	if d.Removed != 0 {
		t.Errorf("expected 0 removed, got %d", d.Removed)
	}
}

func TestDiffSimpleRemove(t *testing.T) {
	d := Diff("line1\nline2\nline3", "line1\nline3")
	if d.Removed != 1 {
		t.Errorf("expected 1 removed, got %d", d.Removed)
	}
}

func TestDiffModify(t *testing.T) {
	d := Diff("line1\nold\nline3", "line1\nnew\nline3")
	if d.Added == 0 || d.Removed == 0 {
		t.Error("modification should show add+remove")
	}
}

func TestRender(t *testing.T) {
	d := Diff("a\nb\nc", "a\nx\nc")
	d.Path = "test.go"
	out := Render(d)

	if !strings.Contains(out, "--- a/test.go") {
		t.Error("should contain file header")
	}
	if !strings.Contains(out, "@@") {
		t.Error("should contain hunk header")
	}
}

func TestSummarize(t *testing.T) {
	d := FileDiff{Path: "main.go", Added: 5, Removed: 2, Hunks: make([]Hunk, 1)}
	s := Summarize(d)
	if !strings.Contains(s, "main.go") {
		t.Error("should contain filename")
	}
	if !strings.Contains(s, "+5") {
		t.Error("should contain added count")
	}
}

func TestSummarizeNoChanges(t *testing.T) {
	d := FileDiff{Path: "empty.go"}
	s := Summarize(d)
	if !strings.Contains(s, "no changes") {
		t.Errorf("expected 'no changes', got %q", s)
	}
}

func TestSummarizeBinary(t *testing.T) {
	d := FileDiff{Path: "image.png", Binary: true}
	s := Summarize(d)
	if !strings.Contains(s, "binary") {
		t.Error("should indicate binary")
	}
}

func TestCompressSkipWhitespace(t *testing.T) {
	d := FileDiff{
		Hunks: []Hunk{{
			Lines: []Line{
				{Op: OpRemove, Content: "  "},
				{Op: OpAdd, Content: "    "},
				{Op: OpRemove, Content: "real change"},
				{Op: OpAdd, Content: "real fix"},
			},
		}},
	}

	compressed := Compress(d, CompressOpts{SkipWhitespace: true})
	if len(compressed.Hunks) == 0 {
		t.Fatal("should keep non-whitespace hunks")
	}
	for _, l := range compressed.Hunks[0].Lines {
		if l.Op != OpContext && strings.TrimSpace(l.Content) == "" {
			t.Error("whitespace-only changes should be removed")
		}
	}
}

func TestCompressSkipComments(t *testing.T) {
	d := FileDiff{
		Hunks: []Hunk{{
			Lines: []Line{
				{Op: OpAdd, Content: "// added comment"},
				{Op: OpRemove, Content: "code removed"},
				{Op: OpAdd, Content: "code added"},
			},
		}},
	}

	compressed := Compress(d, CompressOpts{SkipComments: true})
	for _, l := range compressed.Hunks[0].Lines {
		if l.Op != OpContext && strings.HasPrefix(strings.TrimSpace(l.Content), "//") {
			t.Error("comment changes should be removed")
		}
	}
}

func TestTruncateToTokens(t *testing.T) {
	// Create a large diff
	var hunks []Hunk
	for i := 0; i < 20; i++ {
		hunks = append(hunks, Hunk{
			Lines: []Line{
				{Op: OpRemove, Content: strings.Repeat("old ", 50)},
				{Op: OpAdd, Content: strings.Repeat("new ", 50)},
			},
		})
	}
	d := FileDiff{Hunks: hunks}

	truncated := TruncateToTokens(d, 50)
	if len(truncated.Hunks) >= len(d.Hunks) {
		t.Error("should truncate hunks")
	}
}

func TestStats(t *testing.T) {
	diffs := []FileDiff{
		{Added: 10, Removed: 5},
		{Added: 3, Removed: 1},
	}

	files, added, removed := Stats(diffs)
	if files != 2 || added != 13 || removed != 6 {
		t.Errorf("expected 2/13/6, got %d/%d/%d", files, added, removed)
	}
}

func TestIsComment(t *testing.T) {
	if !isComment("// go comment") {
		t.Error("should detect go comment")
	}
	if !isComment("# python comment") {
		t.Error("should detect python comment")
	}
	if isComment("real code") {
		t.Error("should not flag real code")
	}
}
