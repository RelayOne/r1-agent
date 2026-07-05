package lanes

import (
	"sort"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Update is the canonical Bubble Tea v2 Update method. It dispatches on
// every message type the panel cares about, mutates Model state under
// m.mu, marks affected lanes Dirty (so the renderer cache invalidates
// per spec §"Render-Cache Contract"), and re-arms waitForLaneTick on
// every branch that consumed from m.sub.
//
// Per specs/tui-lanes.md §"Implementation Checklist" item 12:
//
//	Implement Update for every msg type; mutate state; set Dirty;
//	invalidate cache; return re-armed cmds.
//
// Keys, kill modal interaction, layout recalc on WindowSizeMsg, and
// the View() body live in subsequent checklist items (13–18). This
// file owns ONLY the message → state → cmd dispatch shell so item 12
// is buildable on its own.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// --- Streaming events from runProducer ---

	case laneTickMsg:
		// Sentinel: zero LaneID means runProducer has shut down.
		// Do NOT re-arm — let the cmd resolve once and stay inert.
		if msg.LaneID == "" {
			return m, nil
		}
		m.mu.Lock()
		idx, ok := m.laneIndex[msg.LaneID]
		if ok {
			l := m.lanes[idx]
			l.SetActivity(msg.Activity)
			l.SetTokens(msg.Tokens)
			l.SetCost(msg.CostUSD)
			l.SetStatus(msg.Status)
			l.SetModel(msg.Model)
			l.SetElapsed(msg.Elapsed)
			l.SetErr(msg.Err)
		}
		// Aggregate counters: sum cost across all lanes; track most
		// recent model; refresh totalLanes.
		m.recalcAggregates()
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	case laneStartMsg:
		m.mu.Lock()
		if _, exists := m.laneIndex[msg.LaneID]; !exists {
			l := &Lane{
				ID:        msg.LaneID,
				Title:     msg.Title,
				Role:      msg.Role,
				Status:    StatusPending,
				StartedAt: msg.StartedAt,
				Dirty:     true,
			}
			m.lanes = append(m.lanes, l)
			m.laneIndex[msg.LaneID] = len(m.lanes) - 1
			m.totalLanes = len(m.lanes)
		}
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	case laneEndMsg:
		m.mu.Lock()
		if idx, ok := m.laneIndex[msg.LaneID]; ok {
			l := m.lanes[idx]
			l.SetStatus(msg.Final)
			l.SetCost(msg.CostUSD)
			l.SetTokens(msg.Tokens)
		}
		m.recalcAggregates()
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	case laneListMsg:
		m.mu.Lock()
		// Replay: a laneListMsg is always a FULL authoritative snapshot
		// (emitted once on subscribe and re-emitted on every reconnect),
		// so it is the reconciliation source of truth. Install missing,
		// update existing, and — critically — prune any lane we hold that
		// is absent from the snapshot. Without the prune, a lane the
		// daemon reaped while the TUI was disconnected would render
		// forever and keep inflating status-bar counts / aggregate cost
		// after a reconnect resubscribe.
		snapIDs := make(map[string]struct{}, len(msg.Lanes))
		for _, snap := range msg.Lanes {
			snapIDs[snap.ID] = struct{}{}
		}
		if len(m.lanes) > 0 {
			kept := m.lanes[:0]
			for _, l := range m.lanes {
				if _, present := snapIDs[l.ID]; present {
					kept = append(kept, l)
					continue
				}
				// Dropping this lane: clear any pending kill-confirm that
				// targeted it so the modal can't dangle on a gone lane.
				if m.confirmKill == l.ID {
					m.confirmKill = ""
				}
			}
			m.lanes = kept
			// laneIndex is rebuilt below after the install/sort pass;
			// stale entries are harmless until then because the install
			// loop re-derives indices via the map lookup on live lanes
			// only. Rebuild it now so the update-existing lookups below
			// don't hit indices past the trimmed slice.
			m.laneIndex = make(map[string]int, len(m.lanes))
			for i, l := range m.lanes {
				m.laneIndex[l.ID] = i
			}
		}
		for _, snap := range msg.Lanes {
			if idx, ok := m.laneIndex[snap.ID]; ok {
				l := m.lanes[idx]
				l.SetStatus(snap.Status)
				l.SetActivity(snap.Activity)
				l.SetTokens(snap.Tokens)
				l.SetCost(snap.CostUSD)
				l.SetModel(snap.Model)
				l.SetElapsed(snap.Elapsed)
				l.SetErr(snap.Err)
				l.SetTitle(snap.Title)
				l.SetRole(snap.Role)
				l.SetEndedAt(snap.EndedAt)
				continue
			}
			l := &Lane{
				ID:        snap.ID,
				Title:     snap.Title,
				Role:      snap.Role,
				Status:    snap.Status,
				Activity:  snap.Activity,
				Tokens:    snap.Tokens,
				CostUSD:   snap.CostUSD,
				Model:     snap.Model,
				Elapsed:   snap.Elapsed,
				Err:       snap.Err,
				StartedAt: snap.StartedAt,
				EndedAt:   snap.EndedAt,
				Dirty:     true,
			}
			m.lanes = append(m.lanes, l)
			m.laneIndex[snap.ID] = len(m.lanes) - 1
		}
		// Re-sort lanes by createdAt then ID so the iteration order
		// matches the spec contract.
		sort.SliceStable(m.lanes, func(i, j int) bool {
			a, b := m.lanes[i], m.lanes[j]
			if !a.StartedAt.Equal(b.StartedAt) {
				return a.StartedAt.Before(b.StartedAt)
			}
			return a.ID < b.ID
		})
		// Rebuild laneIndex after sort.
		for i, l := range m.lanes {
			m.laneIndex[l.ID] = i
		}
		m.totalLanes = len(m.lanes)
		m.recalcAggregates()
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	case killAckMsg:
		m.mu.Lock()
		// Clear the kill-confirm modal regardless of success.
		if m.confirmKill == msg.LaneID {
			m.confirmKill = ""
		}
		// Annotate the lane with the err if present so a future
		// View can surface it. Spec checklist item 12 only requires
		// state mutation; modal rendering lands in item 23.
		if msg.Err != "" {
			if idx, ok := m.laneIndex[msg.LaneID]; ok {
				m.lanes[idx].SetErr(msg.Err)
			}
		}
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	case budgetMsg:
		m.mu.Lock()
		m.totalCost = msg.SpentUSD
		if msg.LimitUSD > 0 {
			m.budgetLimit = msg.LimitUSD
		}
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	// --- Internal layout / window events (item 13 owns the full
	// decideMode integration; this branch stores width/height so
	// the model is correct even before that lands). ---

	case tea.WindowSizeMsg:
		m.mu.Lock()
		prevWidth, prevCols, prevMode := m.width, m.cols, m.mode
		m.width = msg.Width
		m.height = msg.Height
		cols, mode := decideMode(msg.Width, msg.Height, len(m.lanes), m.mode)
		m.cols = cols
		m.mode = mode
		// Cache invalidation rule §6: drop entire cache when width or
		// cols (or mode) changed. We just stored a fresh width/cols/
		// mode; compare against the snapshot we took above.
		if m.cache != nil && (prevWidth != msg.Width || prevCols != cols || prevMode != mode) {
			m.cache.Clear()
		}
		m.mu.Unlock()
		// Window-size messages do NOT come from m.sub — no re-arm.
		return m, nil

	case windowChangedMsg:
		m.mu.Lock()
		m.width = msg.Width
		m.height = msg.Height
		m.mu.Unlock()
		return m, m.waitForLaneTick()

	// --- Spinner ---

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	// --- Keys (item 20 + 23 + 24): cursor / mode / jump-to-lane /
	// focus / kill confirm / help / quit. Matching follows the spec
	// §"Keybinding Map" mode-scoped table.

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// recalcAggregates sums cost across every lane, captures the most
// recent non-empty model name, and refreshes totalLanes. Called by
// every laneTickMsg / laneEndMsg / laneListMsg branch with m.mu held.
func (m *Model) recalcAggregates() {
	var sum float64
	var latestModel string
	for _, l := range m.lanes {
		sum += l.CostUSD
		if l.Model != "" {
			latestModel = l.Model
		}
	}
	m.totalCost = sum
	if latestModel != "" {
		m.currentModel = latestModel
	}
	m.totalLanes = len(m.lanes)
}

// truncate returns s clipped to n display cells with an ellipsis suffix
// when clipping was needed. n<=0 returns the empty string.
//
// Lane titles/roles/models and especially Activity come straight from
// streamed LLM output and routinely contain non-ASCII text (em dashes,
// curly quotes, emoji, CJK). ansi.Truncate measures East-Asian / emoji
// display width and cuts on grapheme (rune) boundaries, so a multibyte
// rune is never sliced in half into invalid UTF-8. The ellipsis counts
// toward the n-cell budget, matching the old n-1 + "…" contract.
//
// Used by lanes_view.go renderers; lives here so tests in any sibling
// file pick it up without import cycles.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

