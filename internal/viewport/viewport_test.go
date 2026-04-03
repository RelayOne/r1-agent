package viewport

import (
	"fmt"
	"strings"
	"testing"
)

func makeContent(n int) string {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("line %d content", i))
	}
	return strings.Join(lines, "\n")
}

func TestViewBasic(t *testing.T) {
	v := FromString(makeContent(200), Config{Height: 10, Overlap: 2})

	start, end := v.VisibleLines()
	if start != 1 || end != 10 {
		t.Errorf("expected 1-10, got %d-%d", start, end)
	}

	view := v.View()
	if !strings.Contains(view, "   1 | line 1 content") {
		t.Error("expected line 1 in view")
	}
	if !strings.Contains(view, "  10 | line 10 content") {
		t.Error("expected line 10 in view")
	}
}

func TestScrollDown(t *testing.T) {
	v := FromString(makeContent(50), Config{Height: 10, Overlap: 2})

	ok := v.ScrollDown()
	if !ok {
		t.Error("should scroll down")
	}

	start, _ := v.VisibleLines()
	if start != 9 { // moved by 8 (height - overlap)
		t.Errorf("expected start 9, got %d", start)
	}
}

func TestScrollUp(t *testing.T) {
	v := FromString(makeContent(50), Config{Height: 10, Overlap: 2})

	if v.ScrollUp() {
		t.Error("should not scroll up from top")
	}

	v.ScrollDown()
	ok := v.ScrollUp()
	if !ok {
		t.Error("should scroll up")
	}
	if !v.AtTop() {
		t.Error("should be at top after scroll up")
	}
}

func TestGotoLine(t *testing.T) {
	v := FromString(makeContent(200), Config{Height: 10, Overlap: 2})

	v.GotoLine(100)
	start, end := v.VisibleLines()
	if start > 100 || end < 100 {
		t.Errorf("line 100 should be visible, got %d-%d", start, end)
	}
}

func TestSearch(t *testing.T) {
	v := FromString(makeContent(200), Config{Height: 10, Overlap: 2})

	line := v.Search("line 150")
	if line != 150 {
		t.Errorf("expected line 150, got %d", line)
	}

	start, end := v.VisibleLines()
	if start > 150 || end < 150 {
		t.Errorf("line 150 should be visible after search, got %d-%d", start, end)
	}
}

func TestSearchNotFound(t *testing.T) {
	v := FromString(makeContent(50), Config{Height: 10, Overlap: 2})
	line := v.Search("nonexistent")
	if line != 0 {
		t.Errorf("expected 0 for not found, got %d", line)
	}
}

func TestAtTopBottom(t *testing.T) {
	v := FromString(makeContent(20), Config{Height: 10, Overlap: 2})

	if !v.AtTop() {
		t.Error("should start at top")
	}
	if v.AtBottom() {
		t.Error("should not be at bottom initially")
	}

	v.ScrollDown()
	v.ScrollDown()
	if !v.AtBottom() {
		t.Error("should be at bottom after scrolling")
	}
}

func TestContext(t *testing.T) {
	v := FromString(makeContent(200), Config{Height: 10, Overlap: 2})
	ctx := v.Context()
	if !strings.Contains(ctx, "lines 1-10 of 200") {
		t.Errorf("unexpected context: %s", ctx)
	}
}

func TestGetLine(t *testing.T) {
	v := FromString(makeContent(50), Config{Height: 10})

	line, ok := v.GetLine(25)
	if !ok || line != "line 25 content" {
		t.Errorf("expected line 25 content, got %q ok=%v", line, ok)
	}

	_, ok = v.GetLine(0)
	if ok {
		t.Error("line 0 should not exist")
	}
}

func TestGetRange(t *testing.T) {
	v := FromString(makeContent(50), Config{Height: 10})
	lines := v.GetRange(10, 12)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "line 10 content" {
		t.Errorf("expected line 10, got %q", lines[0])
	}
}

func TestSmallFile(t *testing.T) {
	v := FromString(makeContent(5), Config{Height: 100})
	if !v.AtTop() || !v.AtBottom() {
		t.Error("small file should be both at top and bottom")
	}
	if v.TotalLines() != 5 {
		t.Errorf("expected 5 lines, got %d", v.TotalLines())
	}
}

func TestSearchNext(t *testing.T) {
	content := "apple\nbanana\napple\ncherry\napple"
	v := FromString(content, Config{Height: 3, Overlap: 1})

	// First search finds line 1
	line := v.Search("apple")
	if line != 1 {
		t.Errorf("expected line 1, got %d", line)
	}

	// SearchNext from after line 1 finds line 3
	line = v.SearchNext("apple")
	if line != 3 {
		t.Errorf("expected line 3, got %d", line)
	}
}
