package lanes

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
)

// TestSnapshot_NoColor confirms the rendered surface contains zero
// ANSI color escape sequences after the panel's output is piped
// through the canonical colorprofile.Writer with NO_COLOR=1 in the
// environment — which is the codepath every Bubble Tea program uses
// to honor the spec.
//
// Per specs/tui-lanes.md acceptance criterion:
//
//	WHEN `NO_COLOR=1` THE SYSTEM SHALL render zero ANSI color escape
//	sequences, and status SHALL still be unambiguous via glyph.
//
// And §"teatest snapshot tests" item 9:
//
//	TestSnapshot_NoColor — env NO_COLOR=1. Golden has zero ANSI
//	escapes.
//
// And §"Boundaries — What NOT To Do":
//
//	Do not branch on `os.Getenv("NO_COLOR")` directly — lipgloss
//	handles it.
//
// We pipe the rendered tea.View.Content through colorprofile.NewWriter
// (which is what lipgloss.Writer uses under the hood when writing to
// stdout) — that writer detects NO_COLOR and strips ANSI before
// forwarding to its underlying io.Writer. The test asserts:
//
//   1. After the colorprofile writer pass, the output buffer has no
//      ESC \x1b byte.
//   2. The status glyphs still surface, so a NO_COLOR user still sees
//      unambiguous lifecycle state. This is the spec's paired-glyph
//      accessibility rule.
func TestSnapshot_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	m := New("s", &fakeTransport{})
	t0 := time.Now()
	m.Update(laneStartMsg{LaneID: "A", Title: "alpha", StartedAt: t0})
	m.Update(laneTickMsg{LaneID: "A", Status: StatusRunning, Activity: "thinking"})
	m.Update(laneStartMsg{LaneID: "B", Title: "beta", StartedAt: t0.Add(time.Millisecond)})
	m.Update(laneEndMsg{LaneID: "B", Final: StatusDone})
	rendered := renderSnapshot(t, m, 120, 24)

	// Pipe through colorprofile.NewWriter — same path lipgloss.Writer
	// takes when writing to a real terminal. With NO_COLOR=1 the
	// detected profile is NoTTY, which strips every ANSI sequence.
	var buf bytes.Buffer
	w := colorprofile.NewWriter(&buf, []string{"NO_COLOR=1", "TERM=dumb"})
	if _, err := w.Write([]byte(rendered)); err != nil {
		t.Fatalf("colorprofile.Writer write: %v", err)
	}
	out := buf.String()

	if strings.ContainsRune(out, '\x1b') {
		idx := strings.IndexRune(out, '\x1b')
		end := idx + 16
		if end > len(out) {
			end = len(out)
		}
		t.Errorf("NO_COLOR=1 surface still contains ANSI escape at byte %d: %q", idx, out[idx:end])
	}
	for _, want := range []string{
		StatusRunning.Glyph(),
		StatusDone.Glyph(),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("NO_COLOR snapshot missing glyph %q (paired-glyph rule);\n%s", want, out)
		}
	}
}

// TestSnapshot_StatusGlyphs verifies that every one of the six lifecycle
// statuses surfaces its glyph in a single rendered view. We seed one
// lane per status so all six glyphs land on the same surface.
//
// Per spec §"teatest snapshot tests" item 10:
//
//	TestSnapshot_StatusGlyphs — each of 6 statuses present at least
//	once.
func TestSnapshot_StatusGlyphs(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	statuses := []struct {
		id     string
		title  string
		status LaneStatus
	}{
		{"L1", "p", StatusPending},
		{"L2", "r", StatusRunning},
		{"L3", "b", StatusBlocked},
		{"L4", "d", StatusDone},
		{"L5", "e", StatusErrored},
		{"L6", "c", StatusCancelled},
	}
	for i, s := range statuses {
		m.Update(laneStartMsg{
			LaneID:    s.id,
			Title:     s.title,
			StartedAt: t0.Add(time.Duration(i) * time.Millisecond),
		})
		// laneStartMsg installs StatusPending; force the desired
		// status via a tick (or end for terminal statuses) so each
		// lane reports the requested glyph.
		switch {
		case s.status == StatusPending:
			// already pending after start; nothing to do.
		case s.status.IsTerminal():
			m.Update(laneEndMsg{LaneID: s.id, Final: s.status})
		default:
			m.Update(laneTickMsg{LaneID: s.id, Status: s.status, Activity: "x"})
		}
	}
	out := renderSnapshot(t, m, 200, 50)

	for _, s := range statuses {
		if !strings.Contains(out, s.status.Glyph()) {
			t.Errorf("status-glyph snapshot missing glyph %q for status %v;\n%s",
				s.status.Glyph(), s.status, out)
		}
	}
}
