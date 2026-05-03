package lanes

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
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
		// Replay: install snapshots without dropping any in-flight
		// state we already have. The producer guarantees laneListMsg
		// arrives before any tick / start for the same lane in the
		// same flush window, so a simple "install missing, update
		// existing" pass is correct.
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
		m.width = msg.Width
		m.height = msg.Height
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

	// --- Keys (full bindings land in items 22 + 23; item 12
	// only owns the dispatch shell). The 'q'/'ctrl+c' branch is
	// here so the panel quits cleanly and the producer ctx is
	// cancelled. ---

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		return m, nil
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

// View renders the panel as one vertical join of per-lane summary
// rows followed by a single-line status bar. This is the foundation
// view that satisfies the tea.Model interface end-to-end after item
// 12; checklist items 14–19 (viewEmpty, viewOverview, viewFocus,
// renderLane, renderLanePeer, viewStatusBar) replace this body with
// the layout-aware variants that branch on m.mode and read from the
// renderCache. The behaviour delivered here is the always-correct
// fallback: list every lane in stable order with glyph + title +
// status + tokens + cost, plus an aggregate footer. It honours the
// glyph-pairing accessibility rule from the package doc (every
// status string is preceded by its single-cell glyph).
func (m *Model) View() tea.View {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	if len(m.lanes) == 0 {
		b.WriteString("(no lanes)\n")
	} else {
		for _, l := range m.lanes {
			fmt.Fprintf(&b, "%s %-20s %-9s %5d tok  $%6.4f  %s\n",
				l.Status.Glyph(),
				truncate(l.Title, 20),
				l.Status.String(),
				l.Tokens,
				l.CostUSD,
				truncate(l.Activity, max(0, m.width-60)),
			)
			// Clear Dirty after consuming it (item 12 contract:
			// View resets the bit so the next Update mutation is
			// the only trigger for re-render).
			l.Dirty = false
		}
	}
	// Status bar (always rendered; not cached per spec
	// §"Render-Cache Contract" final paragraph).
	fmt.Fprintf(&b, "\n  r1 lanes  %d lanes  $%.4f / $%.2f  %s\n",
		m.totalLanes, m.totalCost, m.budgetLimit, m.currentModel,
	)
	return tea.NewView(b.String())
}

// truncate returns s clipped to n cells with an ellipsis suffix when
// it had to clip. n<=0 returns the empty string.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

