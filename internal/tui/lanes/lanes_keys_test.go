package lanes

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// keyPress is a tiny test helper that synthesises a
// tea.KeyPressMsg with a single text rune. Bubble Tea v2 keeps
// `Key.Text` for printable keys and `Key.Code` for non-printable
// ones; the matchers in lanes_keys_dispatch.go go through
// msg.String() and key.Matches.
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "esc", "tab", "shift+tab", "enter", "ctrl+c", "up", "down":
		// Use Text empty + Mod-aware string. Bubble Tea v2 uses
		// Key.Code for these — feed the canonical rune.
		switch s {
		case "esc":
			return tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			return tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "enter":
			return tea.KeyPressMsg{Code: tea.KeyEnter}
		case "ctrl+c":
			return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		case "up":
			return tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			return tea.KeyPressMsg{Code: tea.KeyDown}
		}
	}
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	return tea.KeyPressMsg{}
}

// seedLanes creates n lanes with deterministic IDs (L1..Ln).
func seedLanes(m *Model, n int) {
	t0 := time.Now()
	for i := 0; i < n; i++ {
		id := []byte{'L', byte('0' + i + 1)}
		m.Update(laneStartMsg{LaneID: string(id), StartedAt: t0.Add(time.Duration(i) * time.Millisecond)})
	}
}

// TestKey_JumpToLane confirms digits 1..9 move the cursor.
func TestKey_JumpToLane(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 5)
	m.Update(keyPress("3"))
	if m.cursor != 2 {
		t.Errorf("cursor=%d want 2 after pressing 3", m.cursor)
	}
}

// TestKey_JumpToLaneOutOfRange ignores digits beyond lane count.
func TestKey_JumpToLaneOutOfRange(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	m.Update(keyPress("9"))
	if m.cursor != 0 {
		t.Errorf("cursor=%d want 0 after out-of-range 9", m.cursor)
	}
}

// TestKey_TabCycle wraps around.
func TestKey_TabCycle(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	for i := 0; i < 3; i++ {
		m.Update(keyPress("tab"))
	}
	if m.cursor != 0 {
		t.Errorf("cursor=%d want 0 after 3 tabs (wrap)", m.cursor)
	}
}

// TestKey_ShiftTabBackward wraps backwards.
func TestKey_ShiftTabBackward(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	m.Update(keyPress("shift+tab"))
	if m.cursor != 2 {
		t.Errorf("cursor=%d want 2 after shift+tab from 0 (wrap)", m.cursor)
	}
}

// TestKey_KMovesCursorUp confirms 'k' is cursor-up in overview, NOT
// kill (spec mode-scoping rule).
func TestKey_KMovesCursorUp(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	m.cursor = 1
	m.Update(keyPress("k"))
	if m.cursor != 0 {
		t.Errorf("cursor=%d want 0 after k", m.cursor)
	}
	if m.confirmKill != "" {
		t.Errorf("confirmKill=%q want empty (k must not kill in overview)", m.confirmKill)
	}
}

// TestKey_XArmsKillConfirm confirms 'x' opens the kill modal for the
// cursor lane.
func TestKey_XArmsKillConfirm(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	m.cursor = 1
	m.Update(keyPress("x"))
	if m.confirmKill != "L2" {
		t.Errorf("confirmKill=%q want L2", m.confirmKill)
	}
}

// TestKey_KillFlowYesCallsTransportKill confirms 'x' then 'y' calls
// Transport.Kill exactly once for the cursor lane. The dispatcher
// returns a tea.Cmd; we run it synchronously here.
func TestKey_KillFlowYesCallsTransportKill(t *testing.T) {
	ft := &fakeTransport{}
	m := New("s", ft)
	seedLanes(m, 3)
	m.cursor = 1
	m.Update(keyPress("x"))
	_, cmd := m.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("y on confirmKill should return a transport-kill cmd")
	}
	cmd() // run the dispatched call synchronously
	if len(ft.killed) != 1 {
		t.Fatalf("transport.Kill calls=%d want 1", len(ft.killed))
	}
	if ft.killed[0] != "L2" {
		t.Errorf("killed=%q want L2", ft.killed[0])
	}
	if m.confirmKill != "" {
		t.Errorf("confirmKill=%q want empty after y", m.confirmKill)
	}
}

// TestKey_KillFlowCancelOnAnyOtherKey confirms 'x' then any non-y
// cancels.
func TestKey_KillFlowCancelOnAnyOtherKey(t *testing.T) {
	ft := &fakeTransport{}
	m := New("s", ft)
	seedLanes(m, 3)
	m.cursor = 1
	m.Update(keyPress("x"))
	m.Update(keyPress("n"))
	if m.confirmKill != "" {
		t.Errorf("confirmKill=%q want empty after cancel", m.confirmKill)
	}
	if len(ft.killed) != 0 {
		t.Errorf("transport.Kill should not be called on cancel; got %v", ft.killed)
	}
}

// TestKey_KillAllArmAndConfirm confirms 'K' arms and 'y' confirms,
// calling Transport.KillAll.
func TestKey_KillAllArmAndConfirm(t *testing.T) {
	ft := &fakeTransport{}
	m := New("s", ft)
	seedLanes(m, 3)
	m.Update(keyPress("K"))
	if !m.confirmAll {
		t.Fatal("K must arm confirmAll")
	}
	_, cmd := m.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("y on confirmAll should return a kill-all cmd")
	}
	cmd()
	if ft.killAll != 1 {
		t.Errorf("KillAll calls=%d want 1", ft.killAll)
	}
	if m.confirmAll {
		t.Error("confirmAll must clear after y")
	}
}

// TestKey_EnterEntersFocusMode confirms 'enter' on cursor lane
// transitions the model into modeFocus and stamps focusID.
func TestKey_EnterEntersFocusMode(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	m.cursor = 2
	m.Update(keyPress("enter"))
	if m.mode != modeFocus {
		t.Errorf("mode=%v want focus", m.mode)
	}
	if m.focusID != "L3" {
		t.Errorf("focusID=%q want L3", m.focusID)
	}
}

// TestKey_EscReturnsFromFocus confirms 'esc' in focus mode returns
// to overview.
func TestKey_EscReturnsFromFocus(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	updateWindow(m, 200, 50)
	m.Update(keyPress("enter"))
	if m.mode != modeFocus {
		t.Fatalf("expected focus mode after enter, got %v", m.mode)
	}
	m.Update(keyPress("esc"))
	if m.mode == modeFocus {
		t.Errorf("esc should leave focus mode; mode=%v", m.mode)
	}
	if m.focusID != "" {
		t.Errorf("focusID=%q want empty after esc", m.focusID)
	}
}

// TestKey_HelpToggle confirms '?' opens and closes the help overlay.
func TestKey_HelpToggle(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(keyPress("?"))
	if !m.helpOpen {
		t.Error("? must open help overlay")
	}
	m.Update(keyPress("?"))
	if m.helpOpen {
		t.Error("? must close help overlay on second press")
	}
}

// TestKey_HelpEscClose confirms esc closes the help overlay.
func TestKey_HelpEscClose(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(keyPress("?"))
	m.Update(keyPress("esc"))
	if m.helpOpen {
		t.Error("esc must close help overlay")
	}
}

// TestKey_RForceRendersDropsCache confirms 'r' clears the cache.
func TestKey_RForceRendersDropsCache(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 2)
	updateWindow(m, 200, 50)
	_ = m.View()
	if len(m.cache.s) == 0 {
		t.Fatal("expected cache to be populated before r")
	}
	m.Update(keyPress("r"))
	if len(m.cache.s) != 0 {
		t.Error("r must clear cache")
	}
}

// TestKey_DigitOnCursorPromotesToFocus confirms pressing the digit
// for the lane the cursor is already on enters focus mode.
func TestKey_DigitOnCursorPromotesToFocus(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	m.cursor = 1 // L2
	m.Update(keyPress("2"))
	if m.mode != modeFocus {
		t.Errorf("mode=%v want focus", m.mode)
	}
	if m.focusID != "L2" {
		t.Errorf("focusID=%q want L2", m.focusID)
	}
}
