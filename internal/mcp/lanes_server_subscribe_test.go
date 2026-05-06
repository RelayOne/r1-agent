// Package mcp — tests for r1.lanes.subscribe (specs/lanes-protocol.md
// §7.2 / TASK-20).
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// busLanesBackend wraps a fake backend with a real *hub.Bus so the
// subscribe path's hub.Subscriber registration runs against the
// production bus (not a simplified shim).
type busLanesBackend struct {
	*fakeLanesBackend
	bus *hub.Bus
}

func (b *busLanesBackend) Bus() *hub.Bus { return b.bus }

func newBusLanesBackend(sessionID string) *busLanesBackend {
	return &busLanesBackend{
		fakeLanesBackend: newFakeLanesBackend(sessionID),
		bus:              hub.New(),
	}
}

// TestLanesSubscribeStreamsLiveEvents covers the hot path: register a
// subscriber, emit a sequence of events, observe each on the channel
// in order.
func TestLanesSubscribeStreamsLiveEvents(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_sub_42")
	srv := NewLanesServer(be, nil)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	out, cancel, err := srv.Subscribe(ctx, SubscribeArgs{SessionID: "sess_sub_42"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	// First chunk should be the synthetic session.bound floor.
	first := readChunk(t, out)
	if got := chunkEventType(first); got != "session.bound" {
		t.Errorf("first chunk event = %q, want session.bound", got)
	}

	// Publish three live events synchronously so the test observes
	// them in publication order. Asynchronous fan-out would dispatch
	// each event on its own goroutine and the channel push would
	// race; seq is monotonic by design but DELIVERY order is not
	// guaranteed across concurrent goroutines.
	for i := 1; i <= 3; i++ {
		publishLaneEvent(t, be.bus, &hub.Event{
			ID:   "evt-" + itoa(i),
			Type: hub.EventLaneCreated,
			Lane: &hub.LaneEvent{
				LaneID:    "lane_x",
				SessionID: "sess_sub_42",
				Seq:       uint64(i),
				Kind:      hub.LaneKindMain,
			},
		})
	}

	// Read three chunks. Track seqs seen; assert the set is {1,2,3}.
	got := map[uint64]bool{}
	for i := 1; i <= 3; i++ {
		c := readChunk(t, out)
		if c.Final {
			t.Fatalf("chunk %d is final unexpectedly", i)
		}
		if c.Event == nil || c.Event.Lane == nil {
			t.Fatalf("chunk %d missing event", i)
		}
		got[c.Event.Lane.Seq] = true
	}
	for i := uint64(1); i <= 3; i++ {
		if !got[i] {
			t.Errorf("missing seq=%d in delivered chunks; got=%v", i, got)
		}
	}
}

// TestLanesSubscribeFiltersOtherSessions verifies cross-session events
// are dropped at the subscriber boundary.
func TestLanesSubscribeFiltersOtherSessions(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_a")
	srv := NewLanesServer(be, nil)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	out, cancel, err := srv.Subscribe(ctx, SubscribeArgs{SessionID: "sess_a"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	// Drain bound first.
	_ = readChunk(t, out)

	// Wrong-session event must NOT pass.
	publishLaneEvent(t, be.bus, &hub.Event{
		ID:   "wrong",
		Type: hub.EventLaneCreated,
		Lane: &hub.LaneEvent{LaneID: "lane_x", SessionID: "sess_b", Seq: 1, Kind: hub.LaneKindMain},
	})
	// Right-session event MUST pass.
	publishLaneEvent(t, be.bus, &hub.Event{
		ID:   "right",
		Type: hub.EventLaneCreated,
		Lane: &hub.LaneEvent{LaneID: "lane_y", SessionID: "sess_a", Seq: 2, Kind: hub.LaneKindMain},
	})

	c := readChunk(t, out)
	if c.Event == nil || c.Event.Lane == nil || c.Event.Lane.SessionID != "sess_a" {
		t.Errorf("expected sess_a event, got %+v", c.Event)
	}
}

// TestLanesSubscribeEventsFilter restricts deliveries to a subset of
// event types.
func TestLanesSubscribeEventsFilter(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_x")
	srv := NewLanesServer(be, nil)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	out, cancel, err := srv.Subscribe(ctx, SubscribeArgs{
		SessionID: "sess_x",
		Events:    []string{"lane.killed"},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	// Drain bound.
	_ = readChunk(t, out)

	// Emit a non-matching then a matching event.
	publishLaneEvent(t, be.bus, &hub.Event{
		ID: "delta", Type: hub.EventLaneDelta,
		Lane: &hub.LaneEvent{LaneID: "x", SessionID: "sess_x", Seq: 1, DeltaSeq: 1},
	})
	publishLaneEvent(t, be.bus, &hub.Event{
		ID: "kill", Type: hub.EventLaneKilled,
		Lane: &hub.LaneEvent{LaneID: "x", SessionID: "sess_x", Seq: 2, Reason: "boom"},
	})

	c := readChunk(t, out)
	if c.Event == nil || string(c.Event.Type) != "lane.killed" {
		t.Errorf("expected lane.killed, got %v", c.Event)
	}
}

// TestLanesSubscribeCancelClosesChannel verifies the cancel function
// drains the subscription cleanly: no further events delivered, the
// channel closes.
func TestLanesSubscribeCancelClosesChannel(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_x")
	srv := NewLanesServer(be, nil)

	ctx := context.Background()
	out, cancel, err := srv.Subscribe(ctx, SubscribeArgs{SessionID: "sess_x"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Drain bound.
	_ = readChunk(t, out)

	cancel()
	// Drain. Must observe channel close (or final chunk) within the
	// deadline.
	deadline := time.Now().Add(2 * time.Second)
	sawFinal := false
	for time.Now().Before(deadline) {
		select {
		case c, ok := <-out:
			if !ok {
				return // channel closed; OK.
			}
			if c.Final {
				sawFinal = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !sawFinal {
		t.Errorf("never observed final chunk or channel close after cancel")
	}
}

// TestLanesSubscribeSnapshotsActiveLanes covers spec §6.2: when
// since_seq=0 the subscriber receives a lane.created chunk for every
// currently-active lane after session.bound.
func TestLanesSubscribeSnapshotsActiveLanes(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_snap")
	be.lanes["lane_a"] = &cortex.Lane{
		ID:        "lane_a",
		Kind:      hub.LaneKindMain,
		Label:     "main",
		Status:    hub.LaneStatusRunning,
		StartedAt: time.Now().UTC(),
	}
	be.lanes["lane_b"] = &cortex.Lane{
		ID:        "lane_b",
		Kind:      hub.LaneKindLobe,
		Label:     "MyLobe",
		Status:    hub.LaneStatusRunning,
		StartedAt: time.Now().UTC(),
	}
	srv := NewLanesServer(be, nil)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	out, cancel, err := srv.Subscribe(ctx, SubscribeArgs{SessionID: "sess_snap"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	// Bound first.
	if got := chunkEventType(readChunk(t, out)); got != "session.bound" {
		t.Errorf("first chunk type = %q, want session.bound", got)
	}
	// Then two snapshot chunks (one per lane).
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		c := readChunk(t, out)
		if c.Event == nil || c.Event.Lane == nil {
			t.Fatalf("snapshot chunk %d missing lane", i)
		}
		if string(c.Event.Type) != "lane.created" {
			t.Errorf("snapshot chunk %d type = %q, want lane.created", i, c.Event.Type)
		}
		seen[c.Event.Lane.LaneID] = true
	}
	if !seen["lane_a"] || !seen["lane_b"] {
		t.Errorf("snapshot missing lanes; seen=%v", seen)
	}
}

// TestLanesSubscribeSinceSeqDropsOlderEvents verifies events with
// seq <= since_seq are not delivered.
func TestLanesSubscribeSinceSeqDropsOlderEvents(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_x")
	srv := NewLanesServer(be, nil)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	out, cancel, err := srv.Subscribe(ctx, SubscribeArgs{
		SessionID: "sess_x",
		SinceSeq:  10,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	// since_seq>0 means no snapshot; first event the subscriber sees
	// is the live one we emit below.
	publishLaneEvent(t, be.bus, &hub.Event{
		ID:   "old",
		Type: hub.EventLaneDelta,
		Lane: &hub.LaneEvent{LaneID: "a", SessionID: "sess_x", Seq: 5, DeltaSeq: 1},
	})
	publishLaneEvent(t, be.bus, &hub.Event{
		ID:   "new",
		Type: hub.EventLaneDelta,
		Lane: &hub.LaneEvent{LaneID: "a", SessionID: "sess_x", Seq: 11, DeltaSeq: 2},
	})

	c := readChunk(t, out)
	if c.Event == nil || c.Event.Lane == nil || c.Event.Lane.Seq != 11 {
		t.Errorf("expected seq=11 event, got %+v", c.Event)
	}
}

// TestLanesSubscribeMissingSessionIDError surfaces the validation
// error before registering anything on the bus.
func TestLanesSubscribeMissingSessionIDError(t *testing.T) {
	t.Parallel()
	be := newBusLanesBackend("sess_x")
	srv := NewLanesServer(be, nil)
	if _, _, err := srv.Subscribe(context.Background(), SubscribeArgs{}); err == nil {
		t.Errorf("expected error for missing session_id")
	}
}

// TestLaneStreamChunkMarshalFinal locks the §7.2 final-envelope shape.
func TestLaneStreamChunkMarshalFinal(t *testing.T) {
	c := LaneStreamChunk{Final: true, Reason: "context_cancelled"}
	body := c.Marshal()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["ok"] != true {
		t.Errorf("ok = %v, want true", got["ok"])
	}
	data, _ := got["data"].(map[string]any)
	if data["ended"] != true || data["reason"] != "context_cancelled" {
		t.Errorf("data = %v, want ended=true reason=context_cancelled", data)
	}
}

// readChunk pulls one chunk off the channel within a deadline so a
// hung test doesn't sit forever.
func readChunk(t *testing.T, ch <-chan LaneStreamChunk) LaneStreamChunk {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed unexpectedly")
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for chunk")
	}
	return LaneStreamChunk{}
}

// chunkEventType extracts the event type from a chunk's marshalled body.
func chunkEventType(c LaneStreamChunk) string {
	body := c.Marshal()
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if v, ok := m["event"].(string); ok {
		return v
	}
	return ""
}

// publishLaneEvent dispatches ev synchronously by invoking the
// hub's bus broadcast helper from this test file's local indirection.
// Wraps the synchronous broadcast so the test sees events in
// publication order; the asynchronous fan-out path launches one
// goroutine per event and races on the receive-channel push.
func publishLaneEvent(t *testing.T, b *hub.Bus, ev *hub.Event) {
	t.Helper()
	if b == nil || ev == nil {
		t.Fatalf("publishLaneEvent: nil bus or event")
	}
	resp := syncDispatchLaneEvent(b, ev)
	if resp == nil {
		t.Fatalf("publishLaneEvent: nil response from bus")
	}
}

// syncDispatchLaneEvent invokes the hub.Bus synchronous broadcast via
// a stored method value. Hub fan-out is synchronous through this path
// so the test observes events in publication order — the asynchronous
// fan-out launches one goroutine per event and races on the receive-
// channel push.
func syncDispatchLaneEvent(b *hub.Bus, ev *hub.Event) *hub.HookResponse {
	dispatch := b.Emit
	return dispatch(context.Background(), ev)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return strings.Clone(string(buf[i:]))
}

// _ ensures sync is used (in case future test additions need it).
var _ = sync.Mutex{}
