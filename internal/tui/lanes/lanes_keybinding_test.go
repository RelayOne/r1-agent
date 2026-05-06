package lanes

import (
	"testing"
)

// The four TestKeybinding_* tests in this file mirror the spec's
// §"Behavioral tests" item 1 list verbatim:
//
//	TestKeybinding_JumpToLane — `3` moves cursor to lane index 2.
//	TestKeybinding_TabCycle — `tab` past last wraps to first.
//	TestKeybinding_KillFlow — `x` then `y` calls transport.Kill(laneID).
//	TestKeybinding_KillCancel — `x` then any-other-key cancels modal.
//
// They overlap with the existing TestKey_* set (which uses a different
// naming convention) but the spec's "verbatim test names" rule wants
// these exact identifiers visible in `go test -v`.

// TestKeybinding_JumpToLane confirms pressing '3' moves the cursor to
// lane index 2 (digit 3 in 1-indexed parlance → cursor=2 in 0-indexed
// storage).
func TestKeybinding_JumpToLane(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 5)
	m.Update(keyPress("3"))
	if m.cursor != 2 {
		t.Errorf("cursor=%d want 2 after pressing 3", m.cursor)
	}
}

// TestKeybinding_TabCycle confirms tab past the last lane wraps back
// to the first (cursor=0).
func TestKeybinding_TabCycle(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 3)
	// Tab three times: 0 → 1 → 2 → 0 (wrap).
	for i := 0; i < 3; i++ {
		m.Update(keyPress("tab"))
	}
	if m.cursor != 0 {
		t.Errorf("cursor=%d want 0 after 3 tabs (wrap)", m.cursor)
	}
}

// TestKeybinding_KillFlow confirms 'x' then 'y' on the cursor lane
// invokes transport.Kill exactly once with the cursor lane's ID.
func TestKeybinding_KillFlow(t *testing.T) {
	ft := &fakeTransport{}
	m := New("s", ft)
	seedLanes(m, 3)
	m.cursor = 1 // L2

	// 'x' arms the kill-confirm modal for the cursor lane.
	m.Update(keyPress("x"))
	if m.confirmKill != "L2" {
		t.Fatalf("confirmKill=%q want L2 after pressing x", m.confirmKill)
	}

	// 'y' confirms — Update returns a tea.Cmd that runs Transport.Kill
	// asynchronously. Run the cmd synchronously so the assertion can
	// observe the side effect.
	_, cmd := m.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("y on confirmKill must return a transport.Kill cmd")
	}
	cmd()

	if len(ft.killed) != 1 {
		t.Fatalf("Transport.Kill calls=%d want 1", len(ft.killed))
	}
	if ft.killed[0] != "L2" {
		t.Errorf("killed[0]=%q want L2", ft.killed[0])
	}
	if m.confirmKill != "" {
		t.Errorf("confirmKill=%q want empty after y", m.confirmKill)
	}
}

// TestKeybinding_KillCancel confirms 'x' then any non-y key cancels
// the kill-confirm modal without invoking Transport.Kill.
func TestKeybinding_KillCancel(t *testing.T) {
	ft := &fakeTransport{}
	m := New("s", ft)
	seedLanes(m, 3)
	m.cursor = 1 // L2

	m.Update(keyPress("x"))
	if m.confirmKill != "L2" {
		t.Fatalf("confirmKill=%q want L2 before cancel", m.confirmKill)
	}

	// 'n' is the canonical "no" — but the spec says "any other key
	// cancels". Use 'n' for symmetry with the [y/N] prompt.
	m.Update(keyPress("n"))

	if m.confirmKill != "" {
		t.Errorf("confirmKill=%q want empty after cancel", m.confirmKill)
	}
	if len(ft.killed) != 0 {
		t.Errorf("Transport.Kill must not be called on cancel; got %v", ft.killed)
	}
}
