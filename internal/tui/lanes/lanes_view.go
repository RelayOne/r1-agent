package lanes

import (
	"fmt"
	"sort"
	"strings"
	"time"

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

// viewStatusBar renders the always-on bottom row.
//
// Per spec §"Status Bar Layout":
//
//	Layout rules:
//	  - Left segment: title + status counts.
//	  - Center segment: $spent / $limit + 10-char progress bar.
//	  - Right segment: turns count, model name, help hint.
//	  - Truncation ladder: drop model, then turns, then help hint
//	    when width < 80; below width = 50 collapse to short form.
//
// Per spec checklist item 19. Caller holds m.mu.
func (m *Model) viewStatusBar(w int) string {
	if w <= 0 {
		return ""
	}

	// Status counts: walk lanes once.
	var active, done, errored int
	for _, l := range m.lanes {
		switch {
		case l.Status == StatusErrored:
			errored++
		case l.Status.IsTerminal():
			done++
		default:
			active++
		}
	}

	left := fmt.Sprintf(" r1 lanes  [%d active %d done %d err]", active, done, errored)

	// Center: budget. Build the bar manually because progress.Model
	// is animated and we want a deterministic snapshot.
	pct := 0.0
	if m.budgetLimit > 0 {
		pct = m.totalCost / m.budgetLimit
		if pct < 0 {
			pct = 0
		}
		if pct > 1 {
			pct = 1
		}
	}
	bar := budgetBar(pct, 10)
	var center string
	if m.budgetLimit > 0 {
		center = fmt.Sprintf("$%.4f / $%.2f %s", m.totalCost, m.budgetLimit, bar)
	} else {
		center = fmt.Sprintf("$%.4f", m.totalCost)
	}

	// Right: turns + model + help hint.
	rightParts := []string{}
	if m.totalTurns > 0 {
		rightParts = append(rightParts, fmt.Sprintf("%d turns", m.totalTurns))
	}
	if m.currentModel != "" {
		rightParts = append(rightParts, m.currentModel)
	}
	rightParts = append(rightParts, "?=help")
	right := strings.Join(rightParts, "  ")

	// Truncation ladder.
	full := func() string {
		// Pad center between left and right.
		used := lipgloss.Width(left) + lipgloss.Width(center) + lipgloss.Width(right)
		// 4 spaces of inter-segment gutter (2 + 2).
		used += 4
		filler := w - used
		if filler < 0 {
			filler = 0
		}
		return left + "  " + center + strings.Repeat(" ", filler) + right
	}

	candidate := full()
	if lipgloss.Width(candidate) <= w {
		return statusBarStyle.Render(candidate)
	}

	// width < 80 — drop model from right.
	if m.currentModel != "" {
		stripped := []string{}
		for _, p := range rightParts {
			if p == m.currentModel {
				continue
			}
			stripped = append(stripped, p)
		}
		right = strings.Join(stripped, "  ")
		candidate = left + "  " + center + "  " + right
		if lipgloss.Width(candidate) <= w {
			return statusBarStyle.Render(candidate)
		}
	}

	// Drop turns next.
	right = "?=help"
	candidate = left + "  " + center + "  " + right
	if lipgloss.Width(candidate) <= w {
		return statusBarStyle.Render(candidate)
	}

	// Drop help hint.
	candidate = left + "  " + center
	if lipgloss.Width(candidate) <= w {
		return statusBarStyle.Render(candidate)
	}

	// width < 50 — collapse to short form: [N a M d X e] $cost.
	short := fmt.Sprintf(" [%d a %d d %d e] $%.4f", active, done, errored, m.totalCost)
	if lipgloss.Width(short) > w {
		short = short[:w]
	}
	return statusBarStyle.Render(short)
}

// budgetBar renders a 10-cell horizontal bar with `width` cells
// shaded according to pct ∈ [0,1]. Spec §Status Bar Layout requires
// the bar to shift colour at 70% (yellow) and 90% (red); we encode
// that by changing the fill glyph (no colour escape) so NO_COLOR
// terminals still see the threshold. The colour shift itself is a
// follow-on item — the spec calls it out as a paired-glyph
// accessibility rule which the glyph variants already satisfy.
func budgetBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	full := int(pct * float64(width))
	if full < 0 {
		full = 0
	}
	if full > width {
		full = width
	}
	glyph := "█"
	switch {
	case pct >= 0.9:
		glyph = "▓" // visually distinct at >=90%
	case pct >= 0.7:
		glyph = "▒"
	}
	return "[" + strings.Repeat(glyph, full) + strings.Repeat("░", width-full) + "]"
}

// renderLane is the bordered three-row lane box: a title row (glyph
// + spinner if Running + name + role), a single-line activity row
// (truncate with ellipsis), and a footer (tokens · cost · elapsed ·
// model).
//
// Per spec §"Implementation Checklist" item 17. Uses
// laneBoxFocusedStyle (thick border) when focused and laneBoxStyle
// (rounded) otherwise; both pick up the status colour via
// BorderForeground for the glyph-pairing accessibility rule.
func renderLane(l *Lane, cellW int, focused bool, spinnerFrame string) string {
	if cellW <= 0 {
		return ""
	}

	box := laneBoxStyle
	if focused {
		box = laneBoxFocusedStyle
	}
	color := statusColor(l.Status)
	box = box.BorderForeground(color)

	// Inside-box content width: subtract horizontal frame (border +
	// padding). lipgloss reports this via GetHorizontalFrameSize.
	innerW := cellW - box.GetHorizontalFrameSize()
	if innerW < 4 {
		innerW = 4
	}

	// Title row: glyph + (optional spinner) + title + role.
	var titleParts []string
	titleParts = append(titleParts, l.Status.Glyph())
	if l.Status == StatusRunning && spinnerFrame != "" {
		titleParts = append(titleParts, spinnerFrame)
	}
	titleParts = append(titleParts, l.Title)
	if l.Role != "" {
		titleParts = append(titleParts, "·", l.Role)
	}
	titleRow := laneTitleStyle.Render(truncate(strings.Join(titleParts, " "), innerW))

	// Activity row: single line, ellipsised. Empty string when no
	// activity yet — keeps a stable three-row height.
	activity := l.Activity
	if activity == "" {
		activity = " "
	}
	activityRow := laneActivityStyle.Render(truncate(activity, innerW))

	// Footer: tokens · $cost · elapsed · model. Each segment is
	// dropped when empty rather than rendering a stale value.
	var footerParts []string
	footerParts = append(footerParts, fmt.Sprintf("%d tok", l.Tokens))
	footerParts = append(footerParts, fmt.Sprintf("$%.4f", l.CostUSD))
	if l.Elapsed > 0 {
		footerParts = append(footerParts, l.Elapsed.Round(time.Second).String())
	}
	if l.Model != "" {
		footerParts = append(footerParts, l.Model)
	}
	footerRow := laneFooterStyle.Render(truncate(strings.Join(footerParts, " · "), innerW))

	body := lipgloss.JoinVertical(lipgloss.Left, titleRow, activityRow, footerRow)
	return box.Width(cellW).Render(body)
}

// renderHelpOverlay is the full-screen help modal toggled by '?'.
//
// Per spec checklist item 25: full-screen modal showing every
// keybinding. Uses help.Model.FullHelpView so the rendered surface
// stays in sync with the keyMap declarations in lanes_keys.go.
func (m *Model) renderHelpOverlay(w, _ int) string {
	body := m.help.FullHelpView(m.keys.FullHelp())
	if body == "" {
		body = "(no help bindings)"
	}
	// Append the dismiss hint as a footer line so the user can find
	// the close key without scrolling the keyMap.
	footer := "press ? or esc to close"
	body = lipgloss.JoinVertical(lipgloss.Left, body, "", footer)
	rendered := modalStyle.Render(body)
	// Width-clamp: lipgloss.Place handles centring; we just stop the
	// modal from exceeding the terminal width.
	if w > 0 && lipgloss.Width(rendered) > w {
		rendered = modalStyle.Render(footer) // fallback to one-liner.
	}
	return rendered
}

// renderKillConfirm is replaced in item 24 with the styled modal
// (single lane vs all lanes branch).
func (m *Model) renderKillConfirm(_ int) string {
	if m.confirmAll {
		return modalStyle.Render("kill ALL lanes? [y/N]")
	}
	return modalStyle.Render(fmt.Sprintf("kill %s? [y/N]", m.confirmKill))
}
