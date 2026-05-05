package lanes

import (
	"strings"
	"testing"
	"time"
)

// TestSnapshot_KillConfirm captures the kill-confirm modal state:
// the user pressed 'x' on the cursor lane and the [y/N] prompt is
// displayed. Snapshot is taken BEFORE pressing 'y' so the modal text
// is on the rendered surface.
//
// Per spec §"teatest snapshot tests" item 6:
//
//	TestSnapshot_KillConfirm — `x` then snapshot before `y`. Golden
//	shows modal.
func TestSnapshot_KillConfirm(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	for i, id := range []string{"alpha", "beta"} {
		m.Update(laneStartMsg{
			LaneID:    id,
			Title:     id,
			StartedAt: t0.Add(time.Duration(i) * time.Millisecond),
		})
	}
	updateWindow(m, 120, 24)
	m.cursor = 0 // alpha
	m.Update(keyPress("x"))

	if m.confirmKill != "alpha" {
		t.Fatalf("confirmKill=%q want alpha after pressing x", m.confirmKill)
	}

	out := m.View().Content
	if !strings.Contains(out, "kill alpha") {
		t.Errorf("kill-confirm snapshot missing target lane prompt;\n%s", out)
	}
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("kill-confirm snapshot missing [y/N] prompt;\n%s", out)
	}
}

// TestSnapshot_KillAllConfirm captures the kill-all-confirm modal:
// the user pressed 'K' and the double-confirm prompt is rendered.
//
// Per spec §"teatest snapshot tests" item 7:
//
//	TestSnapshot_KillAllConfirm — `K` then snapshot. Golden shows
//	double-confirm.
func TestSnapshot_KillAllConfirm(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	for i, id := range []string{"alpha", "beta", "gamma"} {
		m.Update(laneStartMsg{
			LaneID:    id,
			Title:     id,
			StartedAt: t0.Add(time.Duration(i) * time.Millisecond),
		})
	}
	updateWindow(m, 120, 24)
	m.Update(keyPress("K"))

	if !m.confirmAll {
		t.Fatal("expected confirmAll=true after pressing K")
	}

	out := m.View().Content
	if !strings.Contains(out, "kill ALL") {
		t.Errorf("kill-all snapshot missing prompt;\n%s", out)
	}
}

// TestSnapshot_HelpOverlay captures the help overlay rendered by '?'.
// The overlay must surface the canonical bindings from the keyMap
// (cursor up, focus mode, kill, etc.) plus the dismiss hint.
//
// Per spec §"teatest snapshot tests" item 8:
//
//	TestSnapshot_HelpOverlay — `?` toggles. Golden shows help.
func TestSnapshot_HelpOverlay(t *testing.T) {
	m := New("s", &fakeTransport{})
	updateWindow(m, 200, 50)
	m.Update(keyPress("?"))

	if !m.helpOpen {
		t.Fatal("expected helpOpen=true after pressing ?")
	}

	out := m.View().Content
	for _, want := range []string{
		"cursor up",
		"focus mode",
		"kill lane",
		"toggle help",
		"press ? or esc to close",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay snapshot missing %q;\n%s", want, out)
		}
	}
}
