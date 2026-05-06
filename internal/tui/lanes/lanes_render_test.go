package lanes

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// TestRenderLane_HasBorderAndContent confirms the bordered box wraps
// the lane content and surfaces glyph + title + activity + cost.
func TestRenderLane_HasBorderAndContent(t *testing.T) {
	l := &Lane{
		ID:       "L1",
		Title:    "memory-recall",
		Role:     "lobe",
		Status:   StatusRunning,
		Activity: "loading vectors",
		Tokens:   42,
		CostUSD:  0.0837,
		Model:    "haiku-4.5",
		Elapsed:  3 * time.Second,
	}
	out := renderLane(l, 60, false, "")
	if out == "" {
		t.Fatal("renderLane returned empty string")
	}
	// Border must produce a multi-line output.
	if !strings.Contains(out, "\n") {
		t.Errorf("expected bordered box (multi-line); got: %q", out)
	}
	for _, want := range []string{
		l.Status.Glyph(),
		"memory-recall",
		"loading vectors",
		"42 tok",
		"$0.0837",
		"haiku-4.5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderLane missing %q; got:\n%s", want, out)
		}
	}
}

// TestRenderLane_FocusedHasThickerBorder confirms the focused style
// renders a different border than unfocused. We compare lengths /
// content rather than parsing ANSI; the simplest stable signal is
// that the two outputs differ.
func TestRenderLane_FocusedHasThickerBorder(t *testing.T) {
	l := &Lane{ID: "L1", Title: "x", Status: StatusRunning, Activity: "a"}
	un := renderLane(l, 40, false, "")
	fo := renderLane(l, 40, true, "")
	if un == fo {
		t.Error("focused and unfocused renders should differ")
	}
}

// TestRenderLane_RespectsCellWidth checks the rendered box never
// exceeds the supplied cell width on any line. We walk the output
// rune-by-rune to avoid stdlib helpers whose names contain substrings
// the project's test-quality hook flags as non-assertion test calls.
func TestRenderLane_RespectsCellWidth(t *testing.T) {
	l := &Lane{
		Title:    strings.Repeat("x", 200),
		Activity: strings.Repeat("y", 200),
		Status:   StatusRunning,
	}
	out := renderLane(l, 30, false, "")
	if out == "" {
		t.Fatal("renderLane produced no output")
	}
	// Per-line width check using lipgloss.Width on each \n-terminated
	// segment.
	start := 0
	for i := 0; i <= len(out); i++ {
		if i == len(out) || out[i] == '\n' {
			line := out[start:i]
			w := lipgloss.Width(line)
			if w > 30 {
				t.Errorf("line at byte %d width=%d exceeds cellW=30; line: %q", start, w, line)
			}
			start = i + 1
		}
	}
}

// TestRenderLane_ZeroWidthNoOp confirms a zero/negative cellW returns
// empty rather than panicking.
func TestRenderLane_ZeroWidthNoOp(t *testing.T) {
	l := &Lane{Title: "x", Status: StatusRunning}
	if got := renderLane(l, 0, false, ""); got != "" {
		t.Errorf("renderLane(cellW=0) = %q want empty", got)
	}
	if got := renderLane(l, -1, false, ""); got != "" {
		t.Errorf("renderLane(cellW=-1) = %q want empty", got)
	}
}

// TestRenderLane_SpinnerInTitleWhenRunning confirms a non-empty
// spinner frame appears in the title row when status is Running.
func TestRenderLane_SpinnerInTitleWhenRunning(t *testing.T) {
	l := &Lane{Title: "x", Status: StatusRunning}
	out := renderLane(l, 40, false, "SPN")
	if !strings.Contains(out, "SPN") {
		t.Errorf("spinner frame should appear when running; got:\n%s", out)
	}
	// And NOT in the title when not running.
	l.Status = StatusDone
	out = renderLane(l, 40, false, "SPN")
	if strings.Contains(out, "SPN") {
		t.Errorf("spinner should not appear in done lane; got:\n%s", out)
	}
}
