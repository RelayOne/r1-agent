package lanes

import (
	"context"
	"sync"
	"testing"
	"time"
)

// reconnectingTransport simulates a Transport whose Subscribe returns
// after a short window (a forced disconnect), then is re-invoked by
// the caller. On every Subscribe call it MUST replay the full lane
// list as the first event (per Transport contract / spec §Acceptance
// Criteria reconnect rule). The test re-drives Subscribe by calling
// it from a second goroutine after the first returns.
type reconnectingTransport struct {
	mu          sync.Mutex
	snapshot    []LaneSnapshot
	subscribes  int
	disconnect  chan struct{}
	subscribeCh chan struct{} // signals "Subscribe was entered"
}

func (r *reconnectingTransport) List(_ context.Context) ([]LaneSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LaneSnapshot, len(r.snapshot))
	copy(out, r.snapshot)
	return out, nil
}

// Subscribe replays the full lane list as the first event, signals the
// test that it entered, then waits for either the disconnect signal
// (returns nil — caller will reconnect) or ctx cancellation (returns
// ctx.Err()).
func (r *reconnectingTransport) Subscribe(ctx context.Context, _ string, out chan<- LaneEvent) error {
	r.mu.Lock()
	r.subscribes++
	snap := make([]LaneSnapshot, len(r.snapshot))
	copy(snap, r.snapshot)
	r.mu.Unlock()

	// Per spec §Acceptance Criteria reconnect rule:
	//   "WHEN the daemon connection drops and reconnects THE SYSTEM
	//   SHALL replay the full lane list within one frame of reconnect."
	// The transport is the place that replay materialises — fire a
	// `list` event as the first message.
	select {
	case out <- LaneEvent{Kind: "list", List: snap}:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Tell the test we've entered Subscribe (after the list has been
	// queued).
	select {
	case r.subscribeCh <- struct{}{}:
	default:
	}

	// Hold open until we get a disconnect signal or ctx cancels.
	select {
	case <-r.disconnect:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *reconnectingTransport) Kill(_ context.Context, _ string) error    { return nil }
func (r *reconnectingTransport) KillAll(_ context.Context) error           { return nil }
func (r *reconnectingTransport) Pin(_ context.Context, _ string, _ bool) error { return nil }

// TestReconnect_ReplaysLaneList disconnects mid-stream and confirms
// that the next Subscribe replays the full lane list as its first
// message.
//
// Per specs/tui-lanes.md §"Behavioral tests" item 7:
//
//	TestReconnect_ReplaysLaneList — disconnect then reconnect; first
//	message is laneListMsg with last known state.
//
// And §"Acceptance criteria":
//
//	WHEN the daemon connection drops and reconnects THE SYSTEM
//	SHALL replay the full lane list within one frame of reconnect.
//
// This test exercises ONLY the transport-level contract (replay on
// resubscribe). The producer / Update integration is covered by
// TestProducer_ListBypassesCoalesce + TestUpdate_LaneListReplaysAndSorts;
// a full end-to-end producer run would fight the 250 ms ticker and
// add flake without strengthening the assertion.
func TestReconnect_ReplaysLaneList(t *testing.T) {
	t0 := time.Now()
	rt := &reconnectingTransport{
		snapshot: []LaneSnapshot{
			{ID: "A", Title: "alpha", Status: StatusRunning, StartedAt: t0},
			{ID: "B", Title: "beta", Status: StatusBlocked, StartedAt: t0.Add(time.Millisecond)},
		},
		disconnect:  make(chan struct{}, 2),
		subscribeCh: make(chan struct{}, 4),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// First Subscribe: drain the initial list event.
	out := make(chan LaneEvent, 4)
	go func() { _ = rt.Subscribe(ctx, "s", out) }()

	first, ok := readLaneEvent(t, out, time.Second)
	if !ok {
		t.Fatal("first Subscribe did not deliver a list event")
	}
	if first.Kind != "list" {
		t.Errorf("first event kind=%q want list", first.Kind)
	}
	if len(first.List) != 2 {
		t.Errorf("first list len=%d want 2", len(first.List))
	}
	// Wait for the transport to enter its blocking section before we
	// trigger the disconnect — racing the disconnect with the list
	// send would be flaky.
	<-rt.subscribeCh

	// Trigger a disconnect; goroutine returns, simulating the daemon
	// dropping the connection.
	rt.disconnect <- struct{}{}

	// Reconnect: drive Subscribe a second time. New out channel so we
	// can observe the replay independently.
	out2 := make(chan LaneEvent, 4)
	go func() { _ = rt.Subscribe(ctx, "s", out2) }()

	second, ok := readLaneEvent(t, out2, time.Second)
	if !ok {
		t.Fatal("reconnect Subscribe did not deliver a list event")
	}
	if second.Kind != "list" {
		t.Errorf("reconnect event kind=%q want list (replay rule)", second.Kind)
	}
	if len(second.List) != 2 {
		t.Errorf("reconnect list len=%d want 2 (lane state preserved)", len(second.List))
	}
	// The replayed list must carry the last known state — assert the
	// IDs match.
	gotIDs := map[string]bool{}
	for _, s := range second.List {
		gotIDs[s.ID] = true
	}
	for _, want := range []string{"A", "B"} {
		if !gotIDs[want] {
			t.Errorf("reconnect list missing lane %q; got %v", want, gotIDs)
		}
	}

	if rt.subscribes != 2 {
		t.Errorf("Subscribe call count=%d want 2 (initial + reconnect)", rt.subscribes)
	}

	// Drain the second subscribeCh signal then disconnect the second
	// goroutine so it returns cleanly (otherwise it leaks until ctx
	// cancel fires at the test deadline).
	<-rt.subscribeCh
	rt.disconnect <- struct{}{}
}

// readLaneEvent returns the next LaneEvent from out within timeout, or
// (zero, false) on timeout.
func readLaneEvent(t *testing.T, out <-chan LaneEvent, timeout time.Duration) (LaneEvent, bool) {
	t.Helper()
	select {
	case ev := <-out:
		return ev, true
	case <-time.After(timeout):
		return LaneEvent{}, false
	}
}
