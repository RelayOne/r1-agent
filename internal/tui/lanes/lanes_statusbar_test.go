package lanes

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestStatusBar_ContainsAllSegments at width=120 must contain title,
// counts, cost, model, and help hint.
func TestStatusBar_ContainsAllSegments(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "A"})
	m.Update(laneStartMsg{LaneID: "B"})
	m.Update(laneEndMsg{LaneID: "B", Final: StatusDone})
	m.Update(laneStartMsg{LaneID: "C"})
	m.Update(laneEndMsg{LaneID: "C", Final: StatusErrored})
	m.totalCost = 0.0837
	m.budgetLimit = 1.0
	m.currentModel = "haiku-4.5"
	m.totalTurns = 42
	out := m.viewStatusBar(120)
	for _, want := range []string{
		"r1 lanes",
		"1 active",
		"1 done",
		"1 err",
		"$0.0837",
		"$1.00",
		"haiku-4.5",
		"42 turns",
		"?=help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar missing %q; got: %q", want, out)
		}
	}
}

// TestStatusBar_TruncatesAt60 confirms model + turns drop when there
// isn't enough room.
func TestStatusBar_TruncatesAt60(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "A"})
	m.totalCost = 0.0837
	m.budgetLimit = 1.0
	m.currentModel = "claude-haiku-4-5-20251020"
	m.totalTurns = 42
	out := m.viewStatusBar(60)
	if w := lipgloss.Width(out); w > 60 {
		t.Errorf("status bar width=%d exceeds 60; got: %q", w, out)
	}
}

// TestStatusBar_CollapsesAt40 confirms the short form fires when
// width is below the truncation ladder's last rung.
func TestStatusBar_CollapsesAt40(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "A"})
	m.totalCost = 0.0837
	m.budgetLimit = 1.0
	m.currentModel = "haiku-4.5"
	m.totalTurns = 42
	out := m.viewStatusBar(40)
	if w := lipgloss.Width(out); w > 40 {
		t.Errorf("collapsed status bar width=%d exceeds 40; got: %q", w, out)
	}
	// Short form drops the "?=help" hint; assert the cost is still
	// surfaced.
	if !strings.Contains(out, "$0.0837") {
		t.Errorf("collapsed status bar missing cost; got: %q", out)
	}
}

// TestBudgetBar_FillsProportional confirms the bar's fill cell count
// scales with pct.
func TestBudgetBar_FillsProportional(t *testing.T) {
	for _, c := range []struct {
		pct  float64
		want int // number of fill chars
	}{
		{0.0, 0},
		{0.5, 5},
		{1.0, 10},
		{1.5, 10}, // clamp
		{-0.1, 0}, // clamp
	} {
		bar := budgetBar(c.pct, 10)
		// Fill is whichever char is NOT the empty char "░".
		fill := 0
		for _, r := range bar {
			if r == '█' || r == '▓' || r == '▒' {
				fill++
			}
		}
		if fill != c.want {
			t.Errorf("budgetBar(%v): fill=%d want %d (bar=%q)", c.pct, fill, c.want, bar)
		}
	}
}

// TestBudgetBar_GlyphChangesAtThreshold confirms the spec's 70% /
// 90% paired-glyph thresholds shift the fill character so NO_COLOR
// terminals see the alert level.
func TestBudgetBar_GlyphChangesAtThreshold(t *testing.T) {
	low := budgetBar(0.5, 10)
	mid := budgetBar(0.75, 10)
	high := budgetBar(0.95, 10)
	if !strings.ContainsRune(low, '█') {
		t.Errorf("low bar should use █; got %q", low)
	}
	if !strings.ContainsRune(mid, '▒') {
		t.Errorf("mid bar should use ▒; got %q", mid)
	}
	if !strings.ContainsRune(high, '▓') {
		t.Errorf("high bar should use ▓; got %q", high)
	}
}
