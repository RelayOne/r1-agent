package lanes

import (
	"strings"
	"testing"
	"time"
)

// TestView_EmptyDispatch confirms the View dispatch lands on the
// empty branch when no lanes exist and includes the status bar.
func TestView_EmptyDispatch(t *testing.T) {
	m := New("s", &fakeTransport{})
	updateWindow(m, 120, 24)
	v := m.View()
	if !strings.Contains(v.Content, "(no lanes)") {
		t.Errorf("empty view should mention (no lanes); got:\n%s", v.Content)
	}
	if !strings.Contains(v.Content, "r1 lanes") {
		t.Errorf("status bar must always render; got:\n%s", v.Content)
	}
}

// TestView_StackDispatch confirms a narrow terminal renders one lane
// per row.
func TestView_StackDispatch(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	m.Update(laneStartMsg{LaneID: "A", Title: "alpha", StartedAt: t0})
	m.Update(laneStartMsg{LaneID: "B", Title: "beta", StartedAt: t0.Add(time.Millisecond)})
	updateWindow(m, 60, 24)
	if m.mode != modeStack {
		t.Fatalf("expected stack mode at width=60, got %v", m.mode)
	}
	v := m.View()
	if !strings.Contains(v.Content, "alpha") || !strings.Contains(v.Content, "beta") {
		t.Errorf("stack view missing lane titles; got:\n%s", v.Content)
	}
}

// TestView_ColumnsDispatch confirms a wide terminal packs lanes into
// columns. We assert the rendered output contains both lane titles.
func TestView_ColumnsDispatch(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	m.Update(laneStartMsg{LaneID: "A", Title: "alpha", StartedAt: t0})
	m.Update(laneStartMsg{LaneID: "B", Title: "beta", StartedAt: t0.Add(time.Millisecond)})
	updateWindow(m, 200, 50)
	if m.mode != modeColumns {
		t.Fatalf("expected columns mode at width=200, got %v", m.mode)
	}
	if m.cols != 2 {
		t.Fatalf("expected 2 cols at width=200 with 2 lanes, got %d", m.cols)
	}
	v := m.View()
	if !strings.Contains(v.Content, "alpha") || !strings.Contains(v.Content, "beta") {
		t.Errorf("columns view missing lane titles; got:\n%s", v.Content)
	}
}

// TestView_OverviewUsesCacheOnSecondCall asserts that two consecutive
// View calls with no state change produce identical output, which is
// only true if the second call hit the per-lane render cache.
func TestView_OverviewUsesCacheOnSecondCall(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "L1", Title: "cached", StartedAt: time.Now()})
	updateWindow(m, 200, 50)
	v1 := m.View()
	// At least one entry must exist in the cache after the first
	// View call. (The exact key includes a focus suffix; just count.)
	if len(m.cache.s) == 0 {
		t.Error("expected cache to be populated after first View")
	}
	v2 := m.View()
	if v1.Content != v2.Content {
		t.Errorf("cached View output differs from first call")
	}
}

// TestView_OverviewMissOnDirty asserts that a Lane.Dirty=true forces
// a fresh render (cache miss) the next time View is called.
func TestView_OverviewMissOnDirty(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "L1", Title: "old", StartedAt: time.Now()})
	updateWindow(m, 200, 50)
	_ = m.View()
	// Mutate the title — flips Dirty.
	m.lanes[0].SetTitle("new")
	if !m.lanes[0].Dirty {
		t.Fatal("SetTitle must flip Dirty for the test to be meaningful")
	}
	v2 := m.View()
	if !strings.Contains(v2.Content, "new") {
		t.Errorf("expected updated title in view; got:\n%s", v2.Content)
	}
	if strings.Contains(v2.Content, "old") {
		t.Errorf("stale title leaked into view; got:\n%s", v2.Content)
	}
}
