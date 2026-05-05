package lanes

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// drainSub reads every message currently sitting on m.sub without
// blocking once the channel is empty for at least timeout. Returns the
// drained messages in receive order.
func drainSub(m *Model, timeout time.Duration) []tea.Msg {
	out := []tea.Msg{}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg := <-m.sub:
			out = append(out, msg)
			// Reset the silence window after every message so
			// caller observes "channel idle for `timeout`" rather
			// than "deadline expired".
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(timeout)
		case <-timer.C:
			return out
		}
	}
}

// TestProducer_Coalesce fires 100 upstream tick events for the same
// lane within 50 ms; the spec's coalescer guarantee is that m.sub
// receives ≤1 laneTickMsg per lane in the next 300 ms window.
//
// Per specs/tui-lanes.md §"Behavioral tests" item 5:
//
//	TestProducer_Coalesce — fire 100 upstream events in 50 ms;
//	m.sub receives ≤1 per lane in the next 300 ms window.
//
// And §"Acceptance criteria":
//
//	WHEN a single upstream lane emits 200 events per second THE
//	SYSTEM SHALL emit at most 5 laneTickMsg per second per lane to
//	the model.
func TestProducer_Coalesce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := New("s", &fakeTransport{})
	pumping := &pumpTransport{events: make(chan LaneEvent, 256)}
	m.transport = pumping

	go m.runProducer(ctx)

	// Pump 100 tick events for the same lane, all with the same
	// status so the status-change bypass does NOT fire. They land
	// inside one PRODUCER_TICK_MS=250 window.
	for i := 0; i < 100; i++ {
		pumping.events <- LaneEvent{
			Kind: "tick",
			Snapshot: LaneSnapshot{
				ID:       "L1",
				Status:   StatusRunning,
				Tokens:   i + 1,
				Activity: "x",
			},
		}
	}

	// Wait one full window plus padding so the coalesce ticker
	// flushes at least once. Then drain everything that arrived.
	time.Sleep(time.Duration(PRODUCER_TICK_MS+150) * time.Millisecond)
	drained := drainSub(m, 50*time.Millisecond)

	tickCount := 0
	for _, msg := range drained {
		if _, ok := msg.(laneTickMsg); ok {
			tickCount++
		}
	}
	// ≤1 tick per lane in the next 300 ms window. We allow 1 (the
	// expected coalesced tick); 0 is also valid if the producer
	// hasn't had a chance to flush yet — but the sleep above
	// guarantees at least one window passed.
	if tickCount > 1 {
		t.Errorf("coalesce: got %d laneTickMsg in flush window; want ≤1", tickCount)
	}
}

// TestProducer_StatusBypass confirms a status change bypasses the
// coalescer: an upstream event whose Status differs from the previous
// queued tick fires a flush immediately rather than waiting for the
// 250 ms ticker.
//
// Per spec §"Behavioral tests" item 6:
//
//	TestProducer_StatusBypass — status change bypasses coalescer.
//
// And §"Subscription Wiring":
//
//	Status changes (Pending→Running→Done) bypass the coalescer
//	(sent immediately) so transitions feel snappy.
func TestProducer_StatusBypass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := New("s", &fakeTransport{})
	pumping := &pumpTransport{events: make(chan LaneEvent, 16)}
	m.transport = pumping

	go m.runProducer(ctx)

	// First tick: status=Running. Coalescer queues it; no flush yet.
	pumping.events <- LaneEvent{
		Kind: "tick",
		Snapshot: LaneSnapshot{
			ID: "L1", Status: StatusRunning, Tokens: 1, Activity: "x",
		},
	}
	// Second tick a few ms later: status changes to Blocked. The
	// producer must flush IMMEDIATELY rather than wait for the 250 ms
	// ticker.
	time.Sleep(20 * time.Millisecond)
	pumping.events <- LaneEvent{
		Kind: "tick",
		Snapshot: LaneSnapshot{
			ID: "L1", Status: StatusBlocked, Tokens: 2, Activity: "stalled",
		},
	}

	// Read with a tight deadline — well under PRODUCER_TICK_MS. If
	// the bypass is honored, the message is on m.sub almost
	// immediately.
	deadline := time.After(time.Duration(PRODUCER_TICK_MS-50) * time.Millisecond)
	for {
		select {
		case msg := <-m.sub:
			if tick, ok := msg.(laneTickMsg); ok {
				if tick.Status != StatusBlocked {
					// First tick (StatusRunning) may also
					// arrive in the same flush; keep
					// reading until we see the bypass.
					continue
				}
				// Got the status-change tick before the
				// ticker would have fired — bypass works.
				return
			}
		case <-deadline:
			t.Fatal("status-change bypass did not fire within PRODUCER_TICK_MS-50ms")
		}
	}
}
