// Mount() integration tests — the A073 activation proof.
//
// These tests exercise the FULL production event path the chat REPL
// wiring uses:
//
//	cortex.Workspace lane lifecycle (NewMainLane / NewToolLane /
//	Transition) → hub.Bus EventLane* → localTransport.Subscribe →
//	runProducer coalescer → Model.Update
//
// with the panel hosted by the real bubbletea/v2 Program that Mount
// constructs, running headless via tea.WithInput(nil) +
// tea.WithOutput(io.Discard).
package lanes

import (
	"context"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// headlessOpts returns tea program options that run the panel without
// a TTY: input disabled, output discarded, signals off, and a fixed
// window size so decideMode gets a real WindowSizeMsg.
func headlessOpts() []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutSignals(),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(100, 30),
	}
}

// waitForModel polls cond (called under m.mu) until it returns true or
// the deadline expires.
func waitForModel(t *testing.T, m *Model, deadline time.Duration, cond func() bool, what string) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		m.mu.Lock()
		ok := cond()
		m.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestMount_LocalTransport_RendersCortexLanes drives real cortex lane
// lifecycle events through a hub bus into a Mounted panel and asserts
// the Model materializes the lanes with their terminal statuses.
func TestMount_LocalTransport_RendersCortexLanes(t *testing.T) {
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("it-mount-1")

	ctx := context.Background()

	// Production lane lifecycle: main lane + a promoted tool lane that
	// runs to completion (the exact shape the chat-interactive wiring
	// produces per workflow phase). Created BEFORE the panel starts so
	// this test deterministically exercises the Transport contract's
	// list-replay-on-subscribe path.
	main := ws.NewMainLane(ctx)
	if err := main.Transition(hub.LaneStatusRunning, "started", "session started"); err != nil {
		t.Fatalf("main Transition: %v", err)
	}
	tool := ws.NewToolLane(ctx, main, "plan")
	if err := tool.Transition(hub.LaneStatusRunning, "started", "plan started"); err != nil {
		t.Fatalf("tool Transition running: %v", err)
	}
	if err := tool.Transition(hub.LaneStatusDone, "ok", "plan complete"); err != nil {
		t.Fatalf("tool Transition done: %v", err)
	}

	p, m, cleanup := Mount("it-mount-1", NewLocalTransport(ws), headlessOpts())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Run(); err != nil {
			t.Errorf("panel Run: %v", err)
		}
	}()
	defer func() {
		cleanup()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			p.Kill()
			t.Fatal("panel did not exit after cleanup()")
		}
	}()

	// Both lanes must materialize via the list replay, and the tool
	// lane must carry its terminal StatusDone.
	waitForModel(t, m, 5*time.Second, func() bool {
		if len(m.lanes) < 2 {
			return false
		}
		idx, ok := m.laneIndex[tool.ID]
		if !ok {
			return false
		}
		return m.lanes[idx].Status == StatusDone
	}, "2 lanes with tool lane Done")

	// The main lane must be present and non-terminal.
	m.mu.Lock()
	idx, ok := m.laneIndex[main.ID]
	var mainStatus LaneStatus
	if ok {
		mainStatus = m.lanes[idx].Status
	}
	m.mu.Unlock()
	if !ok {
		t.Fatalf("main lane %s never reached the panel", main.ID)
	}
	if mainStatus.IsTerminal() {
		t.Fatalf("main lane unexpectedly terminal: %v", mainStatus)
	}
}

// TestMount_KilledLaneShowsCancelled proves the lane.killed event path
// end-to-end: killing a cortex lane surfaces StatusCancelled in the
// panel.
func TestMount_KilledLaneShowsCancelled(t *testing.T) {
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("it-mount-2")

	p, m, cleanup := Mount("it-mount-2", NewLocalTransport(ws), headlessOpts())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()
	defer func() {
		cleanup()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			p.Kill()
			t.Fatal("panel did not exit after cleanup()")
		}
	}()

	ctx := context.Background()
	lane := ws.NewToolLane(ctx, nil, "execute")
	if err := lane.Transition(hub.LaneStatusRunning, "started", "execute started"); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Wait for the lane to appear (list replay) before killing so the
	// kill is observed as a STREAMED hub event, not replayed state.
	waitForModel(t, m, 5*time.Second, func() bool {
		_, ok := m.laneIndex[lane.ID]
		return ok
	}, "lane to appear")

	// Settle window: localTransport.Subscribe registers its hub
	// subscriber immediately after pushing the list replay; give that
	// registration a beat so the killed event below cannot fall into
	// the (microsecond-scale) replay→register gap.
	time.Sleep(100 * time.Millisecond)

	if err := lane.Kill("cancelled_by_operator"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	waitForModel(t, m, 5*time.Second, func() bool {
		idx, ok := m.laneIndex[lane.ID]
		if !ok {
			return false
		}
		return m.lanes[idx].Status == StatusCancelled
	}, "lane to show StatusCancelled")
}

// TestMount_CleanupStopsProducerAndQuits asserts the returned cleanup
// is idempotent, stops the producer (m.cancel niled), and unblocks
// p.Run.
func TestMount_CleanupStopsProducerAndQuits(t *testing.T) {
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("it-mount-3")

	p, m, cleanup := Mount("it-mount-3", NewLocalTransport(ws), headlessOpts())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()

	// Wait for Init to have installed the producer cancel func.
	waitForModel(t, m, 5*time.Second, func() bool {
		return m.cancel != nil
	}, "producer to start")

	cleanup()
	cleanup() // idempotent — must not panic or double-cancel

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("p.Run did not return after cleanup()")
	}

	m.mu.Lock()
	c := m.cancel
	m.mu.Unlock()
	if c != nil {
		t.Fatal("m.cancel not niled by cleanup/stopProducer")
	}
}
