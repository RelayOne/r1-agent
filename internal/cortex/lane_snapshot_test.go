package cortex

import (
	"context"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/hub"
)

// TestLaneSnapshot_ConcurrentWithTransition drives Transition/Kill on
// one goroutine while Snapshot-reading from others. Run under -race
// (the default CI gate) this pins the fix for the unlocked Clone copy
// that the tui-lanes panel activation surfaced.
func TestLaneSnapshot_ConcurrentWithTransition(t *testing.T) {
	bus := hub.New()
	ws := NewWorkspace(bus, nil)
	ws.SetSessionID("snap-race")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		l := ws.NewToolLane(context.Background(), nil, "worker")
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = l.Transition(hub.LaneStatusRunning, "started", "started")
			_ = l.Transition(hub.LaneStatusDone, "ok", "ok")
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				s := l.Snapshot()
				if s.ID != l.ID {
					t.Errorf("snapshot ID mismatch: %q != %q", s.ID, l.ID)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestLaneSnapshot_NilAndUnbound covers the defensive paths.
func TestLaneSnapshot_NilAndUnbound(t *testing.T) {
	var nilLane *Lane
	if got := nilLane.Snapshot(); got.ID != "" {
		t.Fatalf("nil lane Snapshot = %+v, want zero", got)
	}
	unbound := &Lane{ID: "lane_x", Status: hub.LaneStatusPending}
	if got := unbound.Snapshot(); got.ID != "lane_x" || got.Status != hub.LaneStatusPending {
		t.Fatalf("unbound lane Snapshot = %+v", got)
	}
}
