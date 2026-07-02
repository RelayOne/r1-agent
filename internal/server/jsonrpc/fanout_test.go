package jsonrpc

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// laneDeltaEvent builds a minimal lane.delta hub event for pipe tests.
func laneDeltaEvent(sessionID, laneID, text string) *hub.Event {
	return &hub.Event{
		Type:      hub.EventLaneDelta,
		Timestamp: time.Now(),
		Lane: &hub.LaneEvent{
			LaneID:    laneID,
			SessionID: sessionID,
			Block:     &hub.LaneContentBlock{Type: "text", Text: text},
		},
	}
}

// recordingSink captures delivered SubscriptionEvents.
type recordingSink struct {
	mu     sync.Mutex
	events []*SubscriptionEvent
}

func (r *recordingSink) sink(_ context.Context, ev *SubscriptionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *ev
	r.events = append(r.events, &cp)
	return nil
}

func (r *recordingSink) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recordingSink) at(i int) *SubscriptionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[i]
}

// startJournaledSession is the shared fixture: sandboxed hub with a
// journal path fn, one started session. Returns handler + session id.
func startJournaledSession(t *testing.T) (*HubHandler, string, func()) {
	t.Helper()
	h, _, cleanup := withSandboxedHub(t)
	jd := t.TempDir()
	h.SetJournalPathFn(func(sessionID string) string {
		return filepath.Join(jd, sessionID+".jsonl")
	})
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: t.TempDir()})
	if err != nil {
		cleanup()
		t.Fatalf("start: %v", err)
	}
	return h, resp.SessionID, cleanup
}

// TestSubscribeSessionWithSink_ReplayThenLive is the load-bearing
// activation proof for audit A069: events dispatched BEFORE subscribe
// arrive via journal replay, events dispatched AFTER arrive via the
// live fanout, in order, on one sink.
func TestSubscribeSessionWithSink_ReplayThenLive(t *testing.T) {
	h, sid, cleanup := startJournaledSession(t)
	defer cleanup()
	ctx := context.Background()

	sess, err := h.Hub.Get(sid)
	if err != nil {
		t.Fatalf("hub.Get: %v", err)
	}
	// Two events pre-subscribe → journal-first pipe persists them.
	if err := sess.DispatchEvent(ctx, laneDeltaEvent(sid, "lane-1", "alpha")); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if err := sess.DispatchEvent(ctx, laneDeltaEvent(sid, "lane-1", "beta")); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}

	rec := &recordingSink{}
	cancel, err := h.SubscribeSessionWithSink(ctx, sid, 0, nil, rec.sink)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	if got := rec.len(); got != 2 {
		t.Fatalf("replayed events = %d, want 2", got)
	}
	if rec.at(0).Type != string(hub.EventLaneDelta) {
		t.Errorf("replay type = %q, want lane.delta", rec.at(0).Type)
	}
	if rec.at(0).Seq != 1 || rec.at(1).Seq != 2 {
		t.Errorf("replay seqs = %d,%d, want 1,2", rec.at(0).Seq, rec.at(1).Seq)
	}

	// Live event post-subscribe → fanout delivers without re-subscribe.
	if err := sess.DispatchEvent(ctx, laneDeltaEvent(sid, "lane-1", "gamma")); err != nil {
		t.Fatalf("dispatch live: %v", err)
	}
	if got := rec.len(); got != 3 {
		t.Fatalf("after live dispatch events = %d, want 3", got)
	}
	if rec.at(2).Seq != 3 {
		t.Errorf("live seq = %d, want 3", rec.at(2).Seq)
	}

	// cancel() unsubscribes: further dispatches are NOT delivered.
	cancel()
	if err := sess.DispatchEvent(ctx, laneDeltaEvent(sid, "lane-1", "delta")); err != nil {
		t.Fatalf("dispatch after cancel: %v", err)
	}
	if got := rec.len(); got != 3 {
		t.Errorf("after cancel events = %d, want 3 (no delivery)", got)
	}
	if n := h.SubscriberCount(sid); n != 0 {
		t.Errorf("subscriber count after cancel = %d, want 0", n)
	}
}

// TestSubscribeSessionWithSink_SinceSeqSkipsReplayed proves the
// resume cursor: since_seq=1 replays only records with seq > 1.
func TestSubscribeSessionWithSink_SinceSeqSkipsReplayed(t *testing.T) {
	h, sid, cleanup := startJournaledSession(t)
	defer cleanup()
	ctx := context.Background()

	sess, _ := h.Hub.Get(sid)
	_ = sess.DispatchEvent(ctx, laneDeltaEvent(sid, "lane-1", "alpha"))
	_ = sess.DispatchEvent(ctx, laneDeltaEvent(sid, "lane-1", "beta"))

	rec := &recordingSink{}
	cancel, err := h.SubscribeSessionWithSink(ctx, sid, 1, nil, rec.sink)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()
	if got := rec.len(); got != 1 {
		t.Fatalf("replayed events = %d, want 1 (since_seq=1)", got)
	}
}

// TestSubscribeSessionWithSink_UnknownSession maps to ErrNotFound.
func TestSubscribeSessionWithSink_UnknownSession(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	rec := &recordingSink{}
	_, err := h.SubscribeSessionWithSink(context.Background(), "s-nope", 0, nil, rec.sink)
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// waitFor polls cond up to 2s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestDaemonSessionSubscribe_DeliversEventsToConn is the WS-path
// activation proof: the subscribe sink is no longer a discard stub —
// replayed and live events arrive as $/event notifications on the
// connection's writer.
func TestDaemonSessionSubscribe_DeliversEventsToConn(t *testing.T) {
	h, sid, cleanup := startJournaledSession(t)
	defer cleanup()
	reg, ctx := withFakeConnRegistry(t)

	sess, _ := h.Hub.Get(sid)
	_ = sess.DispatchEvent(context.Background(), laneDeltaEvent(sid, "lane-1", "alpha"))

	subResp, err := h.DaemonSessionSubscribe(ctx, SessionSubscribeRequest{SessionID: sid})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if subResp.SubID == "" {
		t.Fatal("empty SubID")
	}

	// Async replay delivers the journaled event.
	waitFor(t, "replay notification", func() bool { return reg.notificationCount() >= 1 })

	// Live publish reaches the conn.
	_ = sess.DispatchEvent(context.Background(), laneDeltaEvent(sid, "lane-1", "beta"))
	waitFor(t, "live notification", func() bool { return reg.notificationCount() >= 2 })

	first := reg.notificationAt(0)
	if first.Method != "$/event" {
		t.Errorf("method = %q, want $/event", first.Method)
	}
	ev, ok := first.Params.(*SubscriptionEvent)
	if !ok {
		t.Fatalf("params type = %T, want *SubscriptionEvent", first.Params)
	}
	if ev.Type != string(hub.EventLaneDelta) {
		t.Errorf("event type = %q, want lane.delta", ev.Type)
	}
	if ev.SubID != subResp.SubID {
		t.Errorf("sub id = %q, want %q", ev.SubID, subResp.SubID)
	}

	// Unsubscribe stops delivery and clears the fanout.
	if _, err := h.DaemonSessionUnsubscribe(ctx, SessionUnsubscribeRequest{SubID: subResp.SubID}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if n := h.SubscriberCount(sid); n != 0 {
		t.Errorf("subscriber count after unsubscribe = %d, want 0", n)
	}
	before := reg.notificationCount()
	_ = sess.DispatchEvent(context.Background(), laneDeltaEvent(sid, "lane-1", "gamma"))
	time.Sleep(20 * time.Millisecond)
	if got := reg.notificationCount(); got != before {
		t.Errorf("notifications after unsubscribe grew: %d -> %d", before, got)
	}
}

// TestDaemonSessionInterrupt_IdleSession: interrupting an idle session
// is an idempotent no-op reporting WasRunning=false.
func TestDaemonSessionInterrupt_IdleSession(t *testing.T) {
	h, sid, cleanup := startJournaledSession(t)
	defer cleanup()

	resp, err := h.DaemonSessionInterrupt(context.Background(), SessionInterruptRequest{SessionID: sid, DropPartial: true})
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if resp.WasRunning {
		t.Error("WasRunning = true for idle session, want false")
	}
	if resp.InterruptedAt == "" {
		t.Error("InterruptedAt empty")
	}
}

// TestDaemonSessionInterrupt_UnknownSession maps to ErrNotFound.
func TestDaemonSessionInterrupt_UnknownSession(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()
	_, err := h.DaemonSessionInterrupt(context.Background(), SessionInterruptRequest{SessionID: "s-nope"})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
