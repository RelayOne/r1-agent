package lanes

import (
	"strings"
	"testing"
)

// TestHelp_OverlayShowsBindings confirms the help overlay rendered
// content includes the canonical bindings from the keyMap.
func TestHelp_OverlayShowsBindings(t *testing.T) {
	m := New("s", &fakeTransport{})
	updateWindow(m, 200, 50)
	m.helpOpen = true
	v := m.View()
	for _, want := range []string{
		"cursor up", "focus mode", "kill lane", "toggle help",
	} {
		if !strings.Contains(v.Content, want) {
			t.Errorf("help overlay missing %q; got:\n%s", want, v.Content)
		}
	}
}

// TestHelp_OverlayDismissHint confirms the close-help footer hint
// is rendered.
func TestHelp_OverlayDismissHint(t *testing.T) {
	m := New("s", &fakeTransport{})
	updateWindow(m, 200, 50)
	m.helpOpen = true
	v := m.View()
	if !strings.Contains(v.Content, "press ? or esc to close") {
		t.Errorf("help overlay missing dismiss hint; got:\n%s", v.Content)
	}
}

// TestHelp_OverlayEnterDoesNotChangeFocus confirms keypresses are
// swallowed by the help overlay (spec rule: only ? and esc dismiss).
func TestHelp_OverlayEnterDoesNotChangeFocus(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 2)
	m.cursor = 1
	m.helpOpen = true
	m.Update(keyPress("enter"))
	if m.mode == modeFocus {
		t.Error("enter must not enter focus mode while help is open")
	}
	// help must still be open
	if !m.helpOpen {
		t.Error("help overlay must persist through irrelevant keypresses")
	}
}

// TestHelp_OverlayKillNotArmed confirms x is swallowed by help.
func TestHelp_OverlayKillNotArmed(t *testing.T) {
	m := New("s", &fakeTransport{})
	seedLanes(m, 2)
	m.helpOpen = true
	m.Update(keyPress("x"))
	if m.confirmKill != "" {
		t.Errorf("x must not arm kill while help is open; confirmKill=%q", m.confirmKill)
	}
}

// TestKillConfirm_ModalRenders confirms the kill-confirm modal text
// surfaces the lane id when armed.
func TestKillConfirm_ModalRenders(t *testing.T) {
	m := New("s", &fakeTransport{})
	updateWindow(m, 200, 50)
	seedLanes(m, 2)
	m.confirmKill = "L1"
	v := m.View()
	if !strings.Contains(v.Content, "kill L1") {
		t.Errorf("kill modal missing target lane id; got:\n%s", v.Content)
	}
	if !strings.Contains(v.Content, "[y/N]") {
		t.Errorf("kill modal missing prompt; got:\n%s", v.Content)
	}
}

// TestKillConfirm_AllModalRenders confirms the kill-all variant
// surfaces a distinct prompt.
func TestKillConfirm_AllModalRenders(t *testing.T) {
	m := New("s", &fakeTransport{})
	updateWindow(m, 200, 50)
	seedLanes(m, 2)
	m.confirmAll = true
	v := m.View()
	if !strings.Contains(v.Content, "kill ALL") {
		t.Errorf("kill-all modal missing prompt; got:\n%s", v.Content)
	}
}
