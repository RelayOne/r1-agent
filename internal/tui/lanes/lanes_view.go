package lanes

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	tea "charm.land/bubbletea/v2"
)

// View is the canonical Bubble Tea v2 View entry point. It dispatches
// on the current layout mode and ALWAYS appends the status bar.
// Modal overlays (kill-confirm, help) are placed via lipgloss.Place
// over the base view.
//
// Per specs/tui-lanes.md §"Implementation Checklist" item 14.
func (m *Model) View() tea.View {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Width fallback: tests construct a Model without sending a
	// WindowSizeMsg. Use the default 80×24 from New() so the renderer
	// produces something useful.
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h <= 0 {
		h = 24
	}

	var body string
	switch m.mode {
	case modeEmpty:
		body = m.viewEmpty(w, h)
	case modeFocus:
		body = m.viewFocus(w, h)
	default: // modeStack, modeColumns
		body = m.viewOverview(w, h)
	}

	// Status bar always last; never cached (spec §Render-Cache
	// Contract final paragraph).
	status := m.viewStatusBar(w)
	combined := lipgloss.JoinVertical(lipgloss.Left, body, status)

	// Modal overlay: kill-confirm or help, in that priority order.
	// Help wins on tie because the user can pop help over a kill
	// confirm (the help text describes 'y' / any-other-key flow).
	if m.helpOpen {
		combined = m.placeOverlay(combined, m.renderHelpOverlay(w, h), w, h)
	} else if m.confirmKill != "" || m.confirmAll {
		combined = m.placeOverlay(combined, m.renderKillConfirm(w), w, h)
	}

	return tea.NewView(combined)
}

// viewEmpty is the no-lanes body. Kept dead-simple; the spec shows
// the status bar still rendering on its own.
func (m *Model) viewEmpty(w, _ int) string {
	hint := "(no lanes)"
	return lipgloss.PlaceHorizontal(w, lipgloss.Center, hint)
}

// viewOverview renders every lane in stable order (createdAt asc,
// laneID tiebreak), reading the per-lane string from the cache when
// present and falling through to renderLane on miss. Layout mode
// (stack vs columns) determines the join geometry.
//
// Per spec checklist item 15.
func (m *Model) viewOverview(w, h int) string {
	lanes := m.sortedLanesLocked()
	if len(lanes) == 0 {
		return m.viewEmpty(w, h)
	}

	cellW := m.cellWidthLocked(w)

	// Render every lane (cached or fresh) into rows of strings.
	rendered := make([]string, len(lanes))
	for i, l := range lanes {
		focused := (i == m.cursor)
		rendered[i] = m.renderLaneCachedLocked(l, cellW, focused)
	}

	if m.mode == modeStack || m.cols <= 1 {
		return lipgloss.JoinVertical(lipgloss.Left, rendered...)
	}

	// Columns: pack rendered cells into cols-wide rows then stack.
	rows := []string{}
	for i := 0; i < len(rendered); i += m.cols {
		end := i + m.cols
		if end > len(rendered) {
			end = len(rendered)
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, rendered[i:end]...)
		rows = append(rows, row)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// sortedLanesLocked returns the lanes slice sorted by StartedAt then
// ID. Caller must hold m.mu. Allocates a fresh slice so a sort here
// does not perturb the model's storage order (laneIndex bookkeeping
// is the only authoritative ordering for keyed lookups).
func (m *Model) sortedLanesLocked() []*Lane {
	out := make([]*Lane, len(m.lanes))
	copy(out, m.lanes)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		return a.ID < b.ID
	})
	return out
}

// cellWidthLocked returns the per-cell width for the current layout.
// Stack mode uses the full width; columns mode divides evenly. The
// extra accounts for the spacing between adjacent column joins (none
// today; reserved for future inter-cell gutters).
func (m *Model) cellWidthLocked(w int) int {
	if m.mode == modeStack || m.cols <= 1 {
		return w
	}
	return w / m.cols
}

// renderLaneCachedLocked is the cache-aware renderer. Caller holds
// m.mu. Honors the spec's §"Render-Cache Contract" — Dirty bit miss,
// width miss, focus miss all force a fresh render via renderLane.
func (m *Model) renderLaneCachedLocked(l *Lane, cellW int, focused bool) string {
	// Cache key combines laneID and focus state via a synthesised
	// suffix so the same lane in focused vs unfocused borders does
	// not clobber each other's cache entry.
	id := l.ID
	if focused {
		id = l.ID + "\x00f"
	}

	// Spec rule #1: Dirty bit forces miss.
	if l.Dirty {
		m.cache.Invalidate(id)
		// Also invalidate the opposite-focus variant, since the
		// underlying data changed.
		other := l.ID
		if focused {
			other = l.ID + "\x00f"
			m.cache.Invalidate(l.ID)
		} else {
			m.cache.Invalidate(l.ID + "\x00f")
		}
		_ = other
	}

	if s, ok := m.cache.Get(id, cellW); ok {
		return s
	}
	s := renderLane(l, cellW, focused, m.spinner.View())
	m.cache.Put(id, cellW, s)
	// Clear dirty AFTER storing the fresh render — the contract
	// (§Render-Cache Contract) explicitly says Dirty resets when
	// the render observes the latest state.
	l.Dirty = false
	return s
}

// placeOverlay composites overlay over base at the centre of the
// terminal. Both are pre-styled strings; lipgloss.Place handles the
// padding / centring.
func (m *Model) placeOverlay(base, overlay string, w, h int) string {
	// We can't do true compositing with v2's basic JoinX/Place; what
	// we ship is "swap the centred middle slice with the overlay".
	// For a small overlay, lipgloss.Place produces a single string
	// with the overlay centred over a same-size canvas; we then
	// concatenate base above and below would lose alignment, so we
	// keep the simple approach: render the overlay as a stand-alone
	// view that REPLACES the body. The status bar stays glued to the
	// bottom via JoinVertical in the caller.
	_ = base
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, overlay)
}

// viewStatusBar, renderLane, renderHelpOverlay, and renderKillConfirm
// are minimal stand-ins; the per-item commits (17, 19, 24, 25) replace
// each one with its full layout. The stand-ins type-check end-to-end
// and produce a readable view, so View() always returns something
// valid even before the later commits land.

// viewFocus is the hand-rolled 65/35 horizontal split renderer.
//
// Per spec §"Layout algorithm" final paragraph and §"Implementation
// Checklist" item 16:
//
//	modeFocus does NOT use bubblelayout; it's a hand-rolled
//	lipgloss.JoinHorizontal(Top, focusedBox, peerStack) because
//	the 65/35 split is fixed and changes only on resize.
//
// Layout: focused lane on the left at 65% width, peers stacked on
// the right at 35% width using renderLanePeer for the one-liner
// summary rows. The focused lane uses renderLane with focused=true
// so the spec's heavier border weight surfaces.
func (m *Model) viewFocus(w, h int) string {
	lanes := m.sortedLanesLocked()
	if len(lanes) == 0 {
		return m.viewEmpty(w, h)
	}

	// Locate the focused lane. Fall back to cursor if the focusID
	// reference disappeared (lane completed and was reaped between
	// the user pressing 'enter' and View running).
	focusedIdx := -1
	for i, l := range lanes {
		if l.ID == m.focusID {
			focusedIdx = i
			break
		}
	}
	if focusedIdx < 0 {
		if m.cursor >= 0 && m.cursor < len(lanes) {
			focusedIdx = m.cursor
		} else {
			focusedIdx = 0
		}
	}

	// 65/35 split with one column gap. The gap is implicit in the
	// rounding (mainW + peerW + 1 == w) so adjacent borders don't
	// touch.
	mainW := (w * 65) / 100
	if mainW < LANE_MIN_WIDTH {
		mainW = LANE_MIN_WIDTH
	}
	peerW := w - mainW - 1
	if peerW < 8 {
		// Terminal too narrow for a useful peer column; collapse
		// back to overview-style single-column rendering.
		return m.viewOverview(w, h)
	}

	main := m.renderLaneCachedLocked(lanes[focusedIdx], mainW, true)

	peerLines := make([]string, 0, len(lanes)-1)
	for i, l := range lanes {
		if i == focusedIdx {
			continue
		}
		peerLines = append(peerLines, renderLanePeer(l, peerW))
	}
	peerStack := strings.Join(peerLines, "\n")

	// Pad peerStack to the same height as main if needed so the
	// horizontal join doesn't tear when one column is taller. We
	// don't pad main because the focused box already encodes a
	// fixed border + body height.
	mainH := lipgloss.Height(main)
	peerH := lipgloss.Height(peerStack)
	if peerH < mainH && peerH > 0 {
		peerStack += strings.Repeat("\n", mainH-peerH)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, main, " ", peerStack)
}

// renderLanePeer is the 1-line, no-border peer row used in focus mode.
//
// Per spec §"Implementation Checklist" item 18:
//
//	1-line row, no border, used in focus mode peer column.
//
// Layout: glyph, name, ellipsised activity, cost — all on one line,
// truncated to fit width.
func renderLanePeer(l *Lane, width int) string {
	if width <= 0 {
		return ""
	}
	// Reserve room for glyph (1) + spacing (1) + cost suffix (~10).
	costStr := fmt.Sprintf("$%6.4f", l.CostUSD)
	costW := lipgloss.Width(costStr)
	// 4 = glyph + space + space + space-before-cost.
	bodyW := width - costW - 4
	if bodyW < 4 {
		bodyW = 4
	}
	body := l.Title
	if l.Activity != "" {
		body = l.Title + " · " + l.Activity
	}
	body = truncate(body, bodyW)
	// Pad body to fixed width so cost right-aligns.
	pad := bodyW - lipgloss.Width(body)
	if pad < 0 {
		pad = 0
	}
	row := fmt.Sprintf("%s %s%s  %s",
		l.Status.Glyph(),
		body,
		strings.Repeat(" ", pad),
		costStr,
	)
	if lipgloss.Width(row) > width {
		row = row[:width]
	}
	return row
}

// viewStatusBar is replaced in item 19 with the 3-segment layout.
func (m *Model) viewStatusBar(w int) string {
	var b strings.Builder
	fmt.Fprintf(&b, " r1 lanes  %d lanes  $%.4f", m.totalLanes, m.totalCost)
	if m.budgetLimit > 0 {
		fmt.Fprintf(&b, " / $%.2f", m.budgetLimit)
	}
	if m.currentModel != "" {
		fmt.Fprintf(&b, "  %s", m.currentModel)
	}
	s := b.String()
	if lipgloss.Width(s) > w {
		s = s[:w]
	}
	return statusBarStyle.Render(s)
}

// renderLane is replaced in item 17 with the bordered three-row box.
// The shim version preserves the glyph-pairing accessibility rule.
func renderLane(l *Lane, cellW int, focused bool, spinnerFrame string) string {
	_ = spinnerFrame
	prefix := " "
	if focused {
		prefix = ">"
	}
	line := fmt.Sprintf("%s %s %-16s %-9s %5d tok  $%6.4f",
		prefix,
		l.Status.Glyph(),
		truncate(l.Title, 16),
		l.Status.String(),
		l.Tokens,
		l.CostUSD,
	)
	if cellW > 0 && lipgloss.Width(line) > cellW {
		line = line[:cellW]
	}
	return line
}

// renderHelpOverlay is replaced in item 25 with the help.Model
// FullHelpView. Stand-in is a one-line hint.
func (m *Model) renderHelpOverlay(w, _ int) string {
	hint := "press ? to close help"
	return modalStyle.Render(hint)
}

// renderKillConfirm is replaced in item 24 with the styled modal
// (single lane vs all lanes branch).
func (m *Model) renderKillConfirm(_ int) string {
	if m.confirmAll {
		return modalStyle.Render("kill ALL lanes? [y/N]")
	}
	return modalStyle.Render(fmt.Sprintf("kill %s? [y/N]", m.confirmKill))
}
