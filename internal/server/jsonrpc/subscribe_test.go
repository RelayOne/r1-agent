package jsonrpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReplayer yields a fixed slice of records to ReplaySince, in seq
// order, and optionally blocks on a release channel between yields so
// tests can interleave live Publish calls during replay.
type fakeReplayer struct {
	records []replayRecord
	gate    chan struct{} // optional: blocks before each yield
}

type replayRecord struct {
	seq  uint64
	kind string
	data any
}

func (f *fakeReplayer) ReplaySince(ctx context.Context, sinceSeq uint64, h JournalHandler) error {
	for _, r := range f.records {
		if r.seq <= sinceSeq {
			continue
		}
		if f.gate != nil {
			<-f.gate
		}
		if err := h(r.seq, r.kind, r.data); err != nil {
			return err
		}
	}
	return nil
}

// recorder is a thread-safe slice of events delivered to the sink.
type recorder struct {
	mu sync.Mutex
	ev []*SubscriptionEvent
}

func (r *recorder) sink(ctx context.Context, ev *SubscriptionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, ev)
	return nil
}

func (r *recorder) snapshot() []*SubscriptionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*SubscriptionEvent, len(r.ev))
	copy(out, r.ev)
	return out
}

// TestSubscribe_ReplaysJournalBeforeLive — the core spec invariant:
// after subscribing with since_seq=0, the replay frames arrive BEFORE
// live frames, even when live frames fired during replay.
func TestSubscribe_ReplaysJournalBeforeLive(t *testing.T) {
	rec := &recorder{}
	src := &fakeReplayer{
		records: []replayRecord{
			{seq: 1, kind: "session.delta", data: map[string]string{"text": "r1"}},
			{seq: 2, kind: "session.delta", data: map[string]string{"text": "r2"}},
			{seq: 3, kind: "session.delta", data: map[string]string{"text": "r3"}},
		},
		gate: make(chan struct{}, 4),
	}
	// Pre-fill the gate so the replayer streams without blocking; the
	// test below explicitly schedules the live Publish call AFTER the
	// first replay record lands.
	src.gate <- struct{}{}

	sub := NewSubscription("sub-1", "s-1", rec.sink, nil)

	// Drive the replay in a goroutine so we can interleave a Publish.
	done := make(chan error, 1)
	go func() {
		done <- sub.Replay(context.Background(), 0, src)
	}()

	// Wait for the first replay frame to arrive.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rec.snapshot()) < 1 {
		t.Fatal("first replay record never arrived")
	}

	// Publish a live event WHILE replay is ongoing. It MUST be
	// queued, not delivered yet.
	if err := sub.Publish(context.Background(), "lane.delta", "live-1"); err != nil {
		t.Fatalf("live publish: %v", err)
	}
	// Confirm it has NOT yet been delivered to the sink — only the
	// first replay record should be there.
	snap := rec.snapshot()
	if len(snap) != 1 {
		t.Fatalf("live event leaked into replay: snap=%d", len(snap))
	}

	// Release the rest of the replay.
	src.gate <- struct{}{}
	src.gate <- struct{}{}

	if err := <-done; err != nil {
		t.Fatalf("replay: %v", err)
	}

	// Now everything should be delivered: 3 replay frames, then 1 live.
	final := rec.snapshot()
	if len(final) != 4 {
		t.Fatalf("final count: got %d want 4", len(final))
	}
	for i, ev := range final[:3] {
		if ev.Type != "session.delta" {
			t.Fatalf("replay frame %d type: got %q", i, ev.Type)
		}
	}
	if final[3].Type != "lane.delta" || final[3].Data != "live-1" {
		t.Fatalf("live frame: %+v", final[3])
	}
	// Per-subscription seq is monotonic across the boundary.
	for i := 1; i < len(final); i++ {
		if final[i].Seq <= final[i-1].Seq {
			t.Fatalf("seq not monotonic at i=%d: %d <= %d", i, final[i].Seq, final[i-1].Seq)
		}
	}
}

// TestSubscribe_PerSubscriptionSeqMonotonic verifies seq starts at 1
// and increments by 1 across all delivered events.
func TestSubscribe_PerSubscriptionSeqMonotonic(t *testing.T) {
	rec := &recorder{}
	src := &fakeReplayer{records: []replayRecord{
		{seq: 5, kind: "x", data: nil},
		{seq: 6, kind: "x", data: nil},
	}}
	sub := NewSubscription("sub-2", "s-1", rec.sink, nil)
	if err := sub.Replay(context.Background(), 0, src); err != nil {
		t.Fatalf("replay: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := sub.Publish(context.Background(), "y", i); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	final := rec.snapshot()
	if len(final) != 5 {
		t.Fatalf("count: %d", len(final))
	}
	for i, ev := range final {
		want := uint64(i + 1)
		if ev.Seq != want {
			t.Fatalf("seq[%d]: got %d want %d", i, ev.Seq, want)
		}
	}
}

// TestSubscribe_FilterRestrictsEvents asserts the filter set drops
// non-matching events without bumping seq.
func TestSubscribe_FilterRestrictsEvents(t *testing.T) {
	rec := &recorder{}
	sub := NewSubscription("sub-3", "s-1", rec.sink, []string{"lane.delta"})
	// Replay nothing first.
	if err := sub.Replay(context.Background(), 0, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
	// Publish 3 events, only one of which matches the filter.
	_ = sub.Publish(context.Background(), "cost.tick", "drop-1")
	_ = sub.Publish(context.Background(), "lane.delta", "keep-1")
	_ = sub.Publish(context.Background(), "session.delta", "drop-2")
	final := rec.snapshot()
	if len(final) != 1 {
		t.Fatalf("expected 1 delivered, got %d (%+v)", len(final), final)
	}
	if final[0].Data != "keep-1" || final[0].Seq != 1 {
		t.Fatalf("unexpected: %+v", final[0])
	}
}

// TestSubscribe_LiveBufferOverflowClosesSubscription asserts the
// overflow path: pushing more than LiveBufferCap during replay fails
// the Publish call and closes the subscription.
func TestSubscribe_LiveBufferOverflowClosesSubscription(t *testing.T) {
	rec := &recorder{}
	// Use a replayer that NEVER finishes so we stay in replay state.
	src := &fakeReplayer{
		records: []replayRecord{{seq: 1, kind: "x", data: nil}},
		gate:    make(chan struct{}), // never fed
	}
	sub := NewSubscription("sub-4", "s-1", rec.sink, nil)

	// Start the replay (it will block waiting for the gate).
	go func() {
		_ = sub.Replay(context.Background(), 0, src)
	}()
	// Wait for state to flip to replay.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if sub.state.Load() == subStateReplay {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Fill the buffer to capacity + 1.
	for i := 0; i < LiveBufferCap; i++ {
		if err := sub.Publish(context.Background(), "y", i); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	// One more should overflow.
	err := sub.Publish(context.Background(), "y", "overflow")
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !errors.Is(err, ErrLiveBufferOverflow) {
		t.Fatalf("expected ErrLiveBufferOverflow, got %v", err)
	}
	if !sub.IsClosed() {
		t.Fatal("subscription should be closed after overflow")
	}
}

// TestSubscribe_ClosedDropsSilently asserts that publishing to a
// closed subscription is a no-op.
func TestSubscribe_ClosedDropsSilently(t *testing.T) {
	var called atomic.Int32
	sink := func(ctx context.Context, ev *SubscriptionEvent) error {
		called.Add(1)
		return nil
	}
	sub := NewSubscription("sub-5", "s-1", sink, nil)
	if err := sub.Replay(context.Background(), 0, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
	sub.Close()
	if err := sub.Publish(context.Background(), "x", nil); err != nil {
		t.Fatalf("publish on closed: %v", err)
	}
	if called.Load() != 0 {
		t.Fatalf("sink called %d times on closed subscription", called.Load())
	}
}

// TestSubscribe_PublishBeforeReplayRejected enforces the state machine.
func TestSubscribe_PublishBeforeReplayRejected(t *testing.T) {
	sink := func(ctx context.Context, ev *SubscriptionEvent) error { return nil }
	sub := NewSubscription("sub-6", "s-1", sink, nil)
	err := sub.Publish(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected error publishing before Replay")
	}
}

// TestSubscribe_NilReplayerStillFlips covers the case where there's
// nothing to replay (e.g. since_seq matches the journal head). State
// must still transition to live.
func TestSubscribe_NilReplayerStillFlips(t *testing.T) {
	rec := &recorder{}
	sub := NewSubscription("sub-7", "s-1", rec.sink, nil)
	if err := sub.Replay(context.Background(), 0, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if sub.state.Load() != subStateLive {
		t.Fatalf("expected live state, got %d", sub.state.Load())
	}
	// Now Publish goes straight through.
	if err := sub.Publish(context.Background(), "x", "ok"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	final := rec.snapshot()
	if len(final) != 1 {
		t.Fatalf("expected 1 event, got %d", len(final))
	}
}
