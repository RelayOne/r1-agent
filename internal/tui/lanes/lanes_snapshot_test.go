package lanes

import (
	"strings"
	"testing"
	"time"
)

// renderSnapshot is the hand-rolled "buffer-and-compare" helper that
// stands in for charmbracelet/x/exp/teatest/v2 (not in go.mod). It
// drives the model through the supplied window size and returns the
// rendered tea.View content as a string.
//
// Per specs/tui-lanes.md §"Testing" stack note:
//
//	teatest v2 ... if it's in go.mod, otherwise hand-build a buffer-
//	and-compare pattern.
//
// Callers compare the returned string against substring assertions
// rather than committing golden files: golden ANSI bytes vary across
// terminal width assumptions in lipgloss v2 betas (per spec
// §"Risks & Mitigations" first row), so substring-level assertions
// keep the test stable while still exercising the same render path
// teatest would walk.
func renderSnapshot(t *testing.T, m *Model, w, h int) string {
	t.Helper()
	updateWindow(m, w, h)
	view := m.View()
	return view.Content
}

// TestSnapshot_Empty exercises the zero-lanes / status-bar-only
// rendering path. Width=120, no lanes — output must contain the
// "(no lanes)" hint and the always-on status bar header.
//
// Per spec §"teatest snapshot tests" item 1:
//
//	TestSnapshot_Empty — no lanes, width=120. Golden = empty status
//	bar only.
func TestSnapshot_Empty(t *testing.T) {
	m := New("s", &fakeTransport{})
	out := renderSnapshot(t, m, 120, 24)

	if !strings.Contains(out, "(no lanes)") {
		t.Errorf("empty snapshot must include the (no lanes) hint;\n%s", out)
	}
	if !strings.Contains(out, "r1 lanes") {
		t.Errorf("empty snapshot must include status bar title;\n%s", out)
	}
}

// TestSnapshot_StackMode exercises the narrow-terminal branch:
// width=60 forces a single-column vertical stack regardless of lane
// count. All lane titles must surface in the rendered view.
//
// Per spec §"teatest snapshot tests" item 2:
//
//	TestSnapshot_StackMode — 3 lanes, width=60. Golden = vertical
//	stack.
func TestSnapshot_StackMode(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	for i, id := range []string{"alpha", "beta", "gamma"} {
		m.Update(laneStartMsg{
			LaneID:    id,
			Title:     id,
			Role:      "lobe",
			StartedAt: t0.Add(time.Duration(i) * time.Millisecond),
		})
	}
	out := renderSnapshot(t, m, 60, 24)

	if m.mode != modeStack {
		t.Fatalf("expected modeStack at width=60; got %v", m.mode)
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("stack snapshot missing lane %q;\n%s", want, out)
		}
	}
}

// TestSnapshot_ColumnsMode_2 exercises the 2-column grid: width=80
// is wide enough for 2 cells of LANE_MIN_WIDTH=32 plus padding but
// not 3.
//
// Per spec §"teatest snapshot tests" item 3:
//
//	TestSnapshot_ColumnsMode_2 — 2 lanes, width=80. Golden = 2-col
//	grid.
func TestSnapshot_ColumnsMode_2(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	m.Update(laneStartMsg{LaneID: "A", Title: "alpha", StartedAt: t0})
	m.Update(laneStartMsg{LaneID: "B", Title: "beta", StartedAt: t0.Add(time.Millisecond)})
	out := renderSnapshot(t, m, 80, 24)

	if m.mode != modeColumns {
		t.Fatalf("expected modeColumns at width=80; got %v", m.mode)
	}
	if m.cols != 2 {
		t.Fatalf("expected cols=2 at width=80 with 2 lanes; got cols=%d", m.cols)
	}
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("2-col snapshot missing lane %q;\n%s", want, out)
		}
	}
}

// TestSnapshot_ColumnsMode_4 exercises the 4-column grid: width=160
// fits 4 cells of LANE_MIN_WIDTH=32 (= 128 + padding).
//
// Per spec §"teatest snapshot tests" item 4:
//
//	TestSnapshot_ColumnsMode_4 — 4 lanes, width=160. Golden = 4-col
//	grid.
func TestSnapshot_ColumnsMode_4(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	titles := []string{"alpha", "beta", "gamma", "delta"}
	for i, id := range titles {
		m.Update(laneStartMsg{
			LaneID:    id,
			Title:     id,
			StartedAt: t0.Add(time.Duration(i) * time.Millisecond),
		})
	}
	out := renderSnapshot(t, m, 160, 24)

	if m.mode != modeColumns {
		t.Fatalf("expected modeColumns at width=160; got %v", m.mode)
	}
	if m.cols != 4 {
		t.Fatalf("expected cols=4 at width=160 with 4 lanes; got cols=%d", m.cols)
	}
	for _, want := range titles {
		if !strings.Contains(out, want) {
			t.Errorf("4-col snapshot missing lane %q;\n%s", want, out)
		}
	}
}

// TestSnapshot_FocusMode exercises the 65/35 horizontal split: 4 lanes,
// 'enter' on lane index 1 (the spec wording says "lane 2", which is
// 1-indexed → cursor=1).
//
// Per spec §"teatest snapshot tests" item 5:
//
//	TestSnapshot_FocusMode — 4 lanes, enter on lane 2, width=140.
//	Golden = 65/35.
func TestSnapshot_FocusMode(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	titles := []string{"alpha", "beta", "gamma", "delta"}
	for i, id := range titles {
		m.Update(laneStartMsg{
			LaneID:    id,
			Title:     id,
			StartedAt: t0.Add(time.Duration(i) * time.Millisecond),
		})
	}
	updateWindow(m, 140, 24)
	// Move cursor to index 1 (lane 2 in 1-indexed parlance) and
	// press 'enter' to enter focus mode.
	m.cursor = 1
	m.Update(keyPress("enter"))

	if m.mode != modeFocus {
		t.Fatalf("expected modeFocus after enter; got %v", m.mode)
	}
	if m.focusID != "beta" {
		t.Fatalf("expected focusID=beta; got %q", m.focusID)
	}

	out := m.View().Content
	// The focused lane title must appear; peers render as one-line
	// summaries so they appear too.
	for _, want := range titles {
		if !strings.Contains(out, want) {
			t.Errorf("focus snapshot missing lane %q;\n%s", want, out)
		}
	}
}
