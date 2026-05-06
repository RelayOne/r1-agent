package lanes

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// Init satisfies tea.Model. It spawns the runProducer goroutine so the
// transport subscription starts immediately, and returns a tea.Batch
// covering the spinner ticker plus the first waitForLaneTick re-arm.
//
// Per specs/tui-lanes.md §"waitForLaneTick — the canonical realtime
// cmd":
//
//	func (m *Model) Init() tea.Cmd {
//	    go m.runProducer()                       // coalescer
//	    return tea.Batch(
//	        m.spinner.Tick,
//	        m.waitForLaneTick(),                 // re-armed in Update
//	    )
//	}
//
// Spawning runProducer here (not in New) means the panel can be
// constructed in tests without the goroutine being launched — the
// teatest harness controls when Init runs. The goroutine's lifetime
// is bounded by m.cancel which is invoked from a tea.Quit branch in
// Update / from the parent program when the panel is unmounted.
//
// If Init is called twice (rare — Bubble Tea v2 calls it exactly
// once per program), the second call replaces the cancel func and
// leaks the previous goroutine. The spec's checklist item 11 doesn't
// mandate idempotency; we still guard against the obvious leak by
// cancelling any previous context first.
func (m *Model) Init() tea.Cmd {
	// Cancel any previous producer goroutine. Defensive — Bubble Tea
	// v2 calls Init once per program in normal operation.
	if m.cancel != nil {
		m.cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	go m.runProducer(ctx)

	return tea.Batch(
		m.spinner.Tick,
		m.waitForLaneTick(),
	)
}
