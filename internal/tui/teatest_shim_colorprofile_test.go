package tui

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestNewShim_SetsASCIIColorProfile is the regression guard for spec 8
// §12 item 13 ("Wire lipgloss.SetColorProfile(termenv.Ascii) in NewShim
// for deterministic snapshots"). The §10a "Snapshot drift" mitigation
// depends on this — if a future refactor removes the SetColorProfile
// call, snapshots become flaky across CI runners and the test
// harness's value is destroyed.
func TestNewShim_SetsASCIIColorProfile(t *testing.T) {
	// Reset to a non-ASCII profile so we can detect that NewShim
	// changed it back. lipgloss.ColorProfile is global state; we
	// restore the prior value at the end of the test.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	if got := lipgloss.ColorProfile(); got == termenv.Ascii {
		t.Fatal("test setup invalid: lipgloss profile should NOT be ASCII before NewShim")
	}

	_ = NewShim(&bytes.Buffer{})

	if got := lipgloss.ColorProfile(); got != termenv.Ascii {
		t.Errorf("after NewShim, lipgloss profile = %v, want ASCII (%v)", got, termenv.Ascii)
	}
}

// TestNewShim_RenderProducesASCIIOnly asserts that a styled lipgloss
// string rendered AFTER NewShim contains no ANSI escape sequences.
// This is the end-to-end consequence of the prior unit test.
func TestNewShim_RenderProducesASCIIOnly(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	_ = NewShim(&bytes.Buffer{})

	styled := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ff00ff")).
		Background(lipgloss.Color("#00ff00")).
		Render("hello")

	// In ASCII profile lipgloss strips colors; the rendered string is
	// just "hello" (possibly with whitespace) and contains no ESC.
	for _, b := range []byte(styled) {
		if b == 0x1b {
			t.Errorf("rendered string contains ESC byte (0x1b); ASCII profile not effective: %q", styled)
			break
		}
	}
}
