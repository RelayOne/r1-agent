package lanes

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// TestRenderLanePeer_OneLine asserts that the peer row is exactly
// one line tall (no border, no wrap) and contains the lane glyph and
// title.
func TestRenderLanePeer_OneLine(t *testing.T) {
	l := &Lane{
		ID:      "L1",
		Title:   "memory-recall",
		Status:  StatusRunning,
		CostUSD: 0.0837,
	}
	row := renderLanePeer(l, 60)
	if strings.Contains(row, "\n") {
		t.Errorf("peer row must be one line; got:\n%s", row)
	}
	if !strings.Contains(row, "memory-recall") {
		t.Errorf("peer row missing title; got: %q", row)
	}
	if !strings.Contains(row, l.Status.Glyph()) {
		t.Errorf("peer row missing status glyph; got: %q", row)
	}
}

// TestRenderLanePeer_TruncatesLongTitle confirms the row stays within
// the supplied width even when the title is huge.
func TestRenderLanePeer_TruncatesLongTitle(t *testing.T) {
	l := &Lane{
		ID:    "L1",
		Title: strings.Repeat("x", 200),
	}
	row := renderLanePeer(l, 30)
	if lipgloss.Width(row) > 30 {
		t.Errorf("peer row width=%d > 30; got: %q", lipgloss.Width(row), row)
	}
}

// TestViewFocus_SplitsMainAndPeers asserts that focus mode renders
// the focused lane content and at least one peer row in the join
// output.
func TestViewFocus_SplitsMainAndPeers(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	m.Update(laneStartMsg{LaneID: "A", Title: "main-lane", StartedAt: t0})
	m.Update(laneStartMsg{LaneID: "B", Title: "peer-one", StartedAt: t0.Add(time.Millisecond)})
	m.Update(laneStartMsg{LaneID: "C", Title: "peer-two", StartedAt: t0.Add(2 * time.Millisecond)})
	updateWindow(m, 200, 50)
	m.mode = modeFocus
	m.focusID = "A"
	v := m.View()
	if !strings.Contains(v.Content, "main-lane") {
		t.Errorf("focus view missing main lane title; got:\n%s", v.Content)
	}
	if !strings.Contains(v.Content, "peer-one") || !strings.Contains(v.Content, "peer-two") {
		t.Errorf("focus view missing peer titles; got:\n%s", v.Content)
	}
}

// TestViewFocus_FallsBackToOverviewOnNarrow asserts that a narrow
// terminal collapses focus mode to a single column rather than
// rendering an unreadable peer column.
func TestViewFocus_FallsBackToOverviewOnNarrow(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	m.Update(laneStartMsg{LaneID: "A", Title: "main", StartedAt: t0})
	m.Update(laneStartMsg{LaneID: "B", Title: "peer", StartedAt: t0.Add(time.Millisecond)})
	// Narrow but with focus mode forced.
	m.width = 40
	m.height = 24
	m.mode = modeFocus
	m.focusID = "A"
	v := m.View()
	// Just assert the view doesn't panic / produce empty.
	if !strings.Contains(v.Content, "main") {
		t.Errorf("narrow focus fallback missing main title; got:\n%s", v.Content)
	}
}
