package lanes

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// updateWindow is a tiny test helper that synthesises a
// tea.WindowSizeMsg and feeds it to the model's Update — which is
// where the spec puts the layout-recompute logic.
func updateWindow(m *Model, w, h int) {
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// TestDecideMode_NarrowWidth covers the spec checklist case where
// width < 2*LANE_MIN_WIDTH should drop to stack mode regardless of
// lane count.
func TestDecideMode_NarrowWidth(t *testing.T) {
	cols, mode := decideMode(60, 24, 4, modeColumns)
	if mode != modeStack {
		t.Errorf("mode=%v want stack", mode)
	}
	if cols != 1 {
		t.Errorf("cols=%d want 1", cols)
	}
}

// TestDecideMode_WideWidth covers a 200-col terminal with 4 lanes:
// every column fits, so we get a 4-col grid.
func TestDecideMode_WideWidth(t *testing.T) {
	cols, mode := decideMode(200, 50, 4, modeColumns)
	if mode != modeColumns {
		t.Errorf("mode=%v want columns", mode)
	}
	if cols != 4 {
		t.Errorf("cols=%d want 4", cols)
	}
}

// TestDecideMode_FocusOverride asserts that focus mode is sticky:
// resize must not knock the user out of focus.
func TestDecideMode_FocusOverride(t *testing.T) {
	cols, mode := decideMode(200, 50, 4, modeFocus)
	if mode != modeFocus {
		t.Errorf("mode=%v want focus", mode)
	}
	if cols != 1 {
		t.Errorf("cols=%d want 1 in focus mode", cols)
	}
}

// TestDecideMode_EmptyZero asserts that n==0 returns modeEmpty.
func TestDecideMode_EmptyZero(t *testing.T) {
	cols, mode := decideMode(200, 50, 0, modeColumns)
	if mode != modeEmpty {
		t.Errorf("mode=%v want empty", mode)
	}
	if cols != 1 {
		t.Errorf("cols=%d want 1", cols)
	}
}

// TestDecideMode_ColsCappedAtMAX confirms COLS_MAX caps the grid even
// when the terminal is wide enough for more cells.
func TestDecideMode_ColsCappedAtMAX(t *testing.T) {
	// width=400 / LANE_MIN_WIDTH(32) = 12; n=8 — cap at COLS_MAX=4.
	cols, mode := decideMode(400, 50, 8, modeColumns)
	if mode != modeColumns {
		t.Errorf("mode=%v want columns", mode)
	}
	if cols != COLS_MAX {
		t.Errorf("cols=%d want %d (COLS_MAX)", cols, COLS_MAX)
	}
}

// TestDecideMode_ColsCappedAtN confirms cols never exceeds the lane
// count even on a wide terminal.
func TestDecideMode_ColsCappedAtN(t *testing.T) {
	// width=400 / 32 = 12 cols can fit; n=2 — cap at n.
	cols, mode := decideMode(400, 50, 2, modeColumns)
	if mode != modeColumns {
		t.Errorf("mode=%v want columns", mode)
	}
	if cols != 2 {
		t.Errorf("cols=%d want 2 (n cap)", cols)
	}
}

// TestRenderCache_FreshGet covers the Put then Get fast path: a
// freshly stored string returns intact at the same width.
func TestRenderCache_FreshGet(t *testing.T) {
	c := newRenderCache()
	c.Put("L1", 40, "rendered")
	got, ok := c.Get("L1", 40)
	if !ok {
		t.Fatal("Get returned miss for freshly-Put entry")
	}
	if got != "rendered" {
		t.Errorf("Get=%q want rendered", got)
	}
}

// TestRenderCache_DirtyInvalidates exercises the explicit Invalidate
// path required by spec §"Render-Cache Contract" item 1.
func TestRenderCache_DirtyInvalidates(t *testing.T) {
	c := newRenderCache()
	c.Put("L1", 40, "rendered")
	c.Invalidate("L1")
	if _, ok := c.Get("L1", 40); ok {
		t.Error("Invalidate should drop the cache entry")
	}
	// Re-Put clears dirty.
	c.Put("L1", 40, "rendered2")
	if got, ok := c.Get("L1", 40); !ok || got != "rendered2" {
		t.Errorf("Get after re-Put = %q,%v want rendered2,true", got, ok)
	}
}

// TestRenderCache_WidthChangeInvalidates exercises spec §"Render-Cache
// Contract" item 2 — a cell width change drops the entry.
func TestRenderCache_WidthChangeInvalidates(t *testing.T) {
	c := newRenderCache()
	c.Put("L1", 40, "narrow")
	if _, ok := c.Get("L1", 50); ok {
		t.Error("width mismatch should miss")
	}
	if _, ok := c.Get("L1", 40); !ok {
		t.Error("matching width should hit")
	}
}

// TestRenderCache_Clear drops the entire cache (spec §"Render-Cache
// Contract" item 6 — WindowSizeMsg with width/cols change).
func TestRenderCache_Clear(t *testing.T) {
	c := newRenderCache()
	c.Put("L1", 40, "a")
	c.Put("L2", 40, "b")
	c.Clear()
	if _, ok := c.Get("L1", 40); ok {
		t.Error("Clear should drop L1")
	}
	if _, ok := c.Get("L2", 40); ok {
		t.Error("Clear should drop L2")
	}
}

// TestUpdate_WindowSizeRecomputesMode confirms WindowSizeMsg drives
// decideMode and updates m.cols / m.mode.
func TestUpdate_WindowSizeRecomputesMode(t *testing.T) {
	m := New("s", &fakeTransport{})
	for _, id := range []string{"A", "B", "C"} {
		m.Update(laneStartMsg{LaneID: id})
	}
	// Wide terminal: columns with 3 cols (capped at n).
	updateWindow(m, 200, 50)
	if m.mode != modeColumns {
		t.Errorf("mode=%v want columns", m.mode)
	}
	if m.cols != 3 {
		t.Errorf("cols=%d want 3", m.cols)
	}
	// Narrow: stack.
	updateWindow(m, 60, 24)
	if m.mode != modeStack {
		t.Errorf("after narrow mode=%v want stack", m.mode)
	}
}

// TestUpdate_WindowSizeClearsCacheOnWidthChange asserts that a width
// change drops the entire cache (spec §"Render-Cache Contract" item 6).
func TestUpdate_WindowSizeClearsCacheOnWidthChange(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "L1"})
	m.cache.Put("L1", 40, "rendered")
	updateWindow(m, 200, 50)
	if _, ok := m.cache.Get("L1", 40); ok {
		t.Error("WindowSizeMsg width change must clear cache")
	}
}
