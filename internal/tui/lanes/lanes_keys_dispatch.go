package lanes

import (
	"context"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// handleKey dispatches a tea.KeyPressMsg according to the spec's
// §"Keybinding Map". The dispatch is mode-scoped (overview vs focus
// vs kill-confirm vs help-overlay) so the same key can mean different
// things depending on UI state — most notably 'k' (cursor up in
// overview) vs 'k' as a kill alias.
//
// Per spec checklist items 20 + 23 + 24 + 25.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Quit is unconditional — no ux-confusing edge cases.
	if key.Matches(msg, m.keys.Quit) {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}

	// Modal: kill-confirm consumes EVERY keypress until resolved.
	// This matches the spec's "any other key cancels" rule and
	// avoids accidental cursor moves while the user reads the modal.
	if m.confirmKill != "" || m.confirmAll {
		return m.handleKillConfirmKeyLocked(msg)
	}

	// Help overlay swallows keypresses too — only `?` and `esc`
	// close it.
	if m.helpOpen {
		if key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Esc) {
			m.helpOpen = false
		}
		return m, nil
	}

	// `?` toggles help overlay.
	if key.Matches(msg, m.keys.Help) {
		m.helpOpen = true
		return m, nil
	}

	// `r` forces a full re-render by dropping the cache.
	if key.Matches(msg, m.keys.ForceRender) {
		if m.cache != nil {
			m.cache.Clear()
		}
		return m, nil
	}

	// `K` arms kill-all confirm.
	if key.Matches(msg, m.keys.KillAll) {
		m.confirmAll = true
		return m, nil
	}

	// Mode-scoped: overview vs focus.
	switch m.mode {
	case modeFocus:
		return m.handleFocusKeyLocked(msg)
	default: // modeStack, modeColumns, modeEmpty
		return m.handleOverviewKeyLocked(msg)
	}
}

// handleOverviewKeyLocked covers cursor / jump / enter-focus / kill
// transitions in overview mode. Caller holds m.mu.
func (m *Model) handleOverviewKeyLocked(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	n := len(m.lanes)

	// Jump-to-lane: digit 1..9.
	s := msg.String()
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < n {
			// Spec rule: pressing the digit for the cursor's lane
			// promotes to focus mode.
			if m.cursor == idx {
				m.enterFocusLocked(idx)
			} else {
				m.cursor = idx
				m.invalidateCursorChangeLocked(idx)
			}
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		if n > 0 {
			prev := m.cursor
			m.cursor = (m.cursor - 1 + n) % n
			m.invalidateCursorChangeLockedFromTo(prev, m.cursor)
		}
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		if n > 0 {
			prev := m.cursor
			m.cursor = (m.cursor + 1) % n
			m.invalidateCursorChangeLockedFromTo(prev, m.cursor)
		}
	case key.Matches(msg, m.keys.ShiftTab):
		if n > 0 {
			prev := m.cursor
			m.cursor = (m.cursor - 1 + n) % n
			m.invalidateCursorChangeLockedFromTo(prev, m.cursor)
		}
	case key.Matches(msg, m.keys.Enter):
		if n > 0 && m.cursor >= 0 && m.cursor < n {
			m.enterFocusLocked(m.cursor)
		}
	case key.Matches(msg, m.keys.Kill):
		// 'x' (or 'k' alias when not bound to cursor) arms the
		// kill-confirm modal for the cursor lane. We bind only 'x'
		// in overview to avoid the spec's 'k' collision with
		// cursor-up.
		if n > 0 && m.cursor >= 0 && m.cursor < n {
			m.confirmKill = m.lanes[m.cursor].ID
		}
	case key.Matches(msg, m.keys.Esc):
		// In overview mode esc closes any soft state. Currently
		// only modal/help, both handled above. No-op fall-through.
	}
	return m, nil
}

// handleFocusKeyLocked covers viewport scroll + esc-back + kill
// transitions in focus mode. Caller holds m.mu.
func (m *Model) handleFocusKeyLocked(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	n := len(m.lanes)
	switch {
	case key.Matches(msg, m.keys.Esc):
		// Spec: focus → overview.
		m.exitFocusLocked()
	case key.Matches(msg, m.keys.Up):
		// Scroll the activity viewport up.
		m.vp.ScrollUp(1)
	case key.Matches(msg, m.keys.Down):
		m.vp.ScrollDown(1)
	case key.Matches(msg, m.keys.Kill):
		// 'x' kills the focused lane.
		if m.focusID != "" {
			m.confirmKill = m.focusID
		}
	default:
		// Jump-to-lane in focus mode switches the focused lane.
		s := msg.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			idx := int(s[0] - '1')
			if idx < n {
				prev := m.cursor
				m.cursor = idx
				m.invalidateCursorChangeLockedFromTo(prev, idx)
				m.focusID = m.lanes[idx].ID
				// Invalidate both old and new focused entries.
				if prev >= 0 && prev < n {
					m.cache.Invalidate(m.lanes[prev].ID + "\x00f")
					m.cache.Invalidate(m.lanes[prev].ID)
				}
				m.cache.Invalidate(m.lanes[idx].ID + "\x00f")
				m.cache.Invalidate(m.lanes[idx].ID)
			}
		}
	}
	return m, nil
}

// handleKillConfirmKeyLocked drives the kill-confirm modal. Spec
// rule: 'y' confirms; any other key cancels. KillAll requires double
// 'y' 'y' (the second press while confirmAll is still set).
func (m *Model) handleKillConfirmKeyLocked(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.ConfirmYes) {
		// KillAll path.
		if m.confirmAll {
			t := m.transport
			m.confirmAll = false
			if t == nil {
				return m, nil
			}
			cmd := func() tea.Msg {
				_ = t.KillAll(context.Background())
				return nil
			}
			return m, cmd
		}
		// Single-lane path.
		laneID := m.confirmKill
		t := m.transport
		m.confirmKill = ""
		if t == nil || laneID == "" {
			return m, nil
		}
		cmd := func() tea.Msg {
			_ = t.Kill(context.Background(), laneID)
			return nil
		}
		return m, cmd
	}
	// Anything else cancels.
	m.confirmKill = ""
	m.confirmAll = false
	return m, nil
}

// enterFocusLocked transitions the model from overview → focus on
// the cursor lane. Caller holds m.mu.
func (m *Model) enterFocusLocked(idx int) {
	if idx < 0 || idx >= len(m.lanes) {
		return
	}
	m.cursor = idx
	m.focusID = m.lanes[idx].ID
	m.mode = modeFocus
	// Cache invalidation: focus changes border weight, so the focused
	// lane needs a fresh render.
	m.cache.Invalidate(m.focusID + "\x00f")
	m.cache.Invalidate(m.focusID)
}

// exitFocusLocked transitions back to overview. Caller holds m.mu.
func (m *Model) exitFocusLocked() {
	prevFocus := m.focusID
	m.focusID = ""
	// Recompute the layout mode from the current dimensions and
	// lane count.
	cols, mode := decideMode(m.width, m.height, len(m.lanes), modeStack)
	m.cols = cols
	m.mode = mode
	if prevFocus != "" {
		m.cache.Invalidate(prevFocus + "\x00f")
		m.cache.Invalidate(prevFocus)
	}
}

// invalidateCursorChangeLocked drops the cache entry for the cursor's
// new position so the focused border render is fresh.
func (m *Model) invalidateCursorChangeLocked(idx int) {
	if idx < 0 || idx >= len(m.lanes) {
		return
	}
	id := m.lanes[idx].ID
	m.cache.Invalidate(id)
	m.cache.Invalidate(id + "\x00f")
}

// invalidateCursorChangeLockedFromTo invalidates both the lane the
// cursor left and the lane it landed on. Both need re-render because
// the focused-border style differs from peer.
func (m *Model) invalidateCursorChangeLockedFromTo(from, to int) {
	if from >= 0 && from < len(m.lanes) {
		id := m.lanes[from].ID
		m.cache.Invalidate(id)
		m.cache.Invalidate(id + "\x00f")
	}
	if to >= 0 && to < len(m.lanes) {
		id := m.lanes[to].ID
		m.cache.Invalidate(id)
		m.cache.Invalidate(id + "\x00f")
	}
}
