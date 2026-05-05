package lanes

import (
	tea "charm.land/bubbletea/v2"
)

// waitForLaneTick is the canonical realtime tea.Cmd. It blocks on a
// single receive from m.sub and returns whatever message the producer
// pushed.
//
// Per specs/tui-lanes.md §"waitForLaneTick — the canonical realtime
// cmd":
//
//	func (m *Model) waitForLaneTick() tea.Cmd {
//	    return func() tea.Msg { return <-m.sub }
//	}
//
//	In `Update`: every time a `laneTickMsg`, `laneStartMsg`,
//	`laneEndMsg`, or `laneListMsg` lands, after applying the change,
//	the returned cmd batches `m.waitForLaneTick()` again.
//
// The cmd is re-armed in every Update branch that reads from m.sub
// (see lanes_update.go — item 12). Bubble Tea v2 launches each cmd
// in its own goroutine; the receive blocks until the producer
// flushes.
//
// Sentinel: a zero-value laneTickMsg (LaneID == "") on m.sub means
// the producer has shut down. Update treats that branch as a no-op
// and does NOT re-arm — letting the cmd resolve once and stay
// inert. See lanes_update.go for the no-op branch.
func (m *Model) waitForLaneTick() tea.Cmd {
	return func() tea.Msg {
		return <-m.sub
	}
}
