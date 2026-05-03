// Package server — tests for the lanes-protocol HTTP+SSE handler (TASK-13).
//
// Covers the contract documented in specs/lanes-protocol.md §6.1:
//
//   - GET /v1/lanes/events?session_id=... returns text/event-stream;
//   - X-R1-Lanes-Version: 1 is set on the response;
//   - the synthetic session.bound record (seq=0) is the first SSE
//     record every subscriber sees (TASK-17 sanity check; the dedicated
//     TASK-17 test in lanes_session_bound_test.go drills deeper);
//   - live lane events emitted on the hub appear as SSE records with
//     id=<seq> and event=<lane.kind>;
//   - missing session_id is rejected with 400.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// fakeLanesHub is a minimal in-memory implementation of LanesHub for tests.
// It records subscribers and exposes a Send method so tests can drive lane
// events without instantiating the full hub.Bus.
type fakeLanesHub struct {
	mu          sync.Mutex
	subscribers map[string]hub.Subscriber
}

func newFakeLanesHub() *fakeLanesHub {
	return &fakeLanesHub{subscribers: make(map[string]hub.Subscriber)}
}

func (f *fakeLanesHub) Register(sub hub.Subscriber) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribers[sub.ID] = sub
}

func (f *fakeLanesHub) Unregister(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subscribers, id)
}

// Send fans out ev to every subscriber whose Events list contains ev.Type.
// Synchronous; returns when every handler has been invoked. Tests that
// race the SSE writer should read from the response body before calling
// Send a second time.
func (f *fakeLanesHub) Send(ev *hub.Event) {
	f.mu.Lock()
	subs := make([]hub.Subscriber, 0, len(f.subscribers))
	for _, sub := range f.subscribers {
		for _, et := range sub.Events {
			if et == ev.Type {
				subs = append(subs, sub)
				break
			}
		}
	}
	f.mu.Unlock()

	for _, sub := range subs {
		if sub.Handler != nil {
			sub.Handler(context.Background(), ev)
		}
	}
}

// readSSERecord reads one SSE record (terminated by a blank line) from r.
// Returns the parsed map of fields (id, event, data). Returns nil on EOF.
func readSSERecord(r *bufio.Reader) (map[string]string, error) {
	out := make(map[string]string)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(out) > 0 {
				return out, nil
			}
			continue
		}
		if idx := strings.Index(line, ":"); idx >= 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimPrefix(line[idx+1:], " ")
			out[key] = val
		}
	}
}

func TestHandleLaneEventsHTTPSSE(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	bus := NewEventBus()
	srv := New(0, "", bus).WithLanes(&LanesWiring{Hub: fakeHub})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=sess_test_42", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/lanes/events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("X-R1-Lanes-Version"); got != "1" {
		t.Errorf("X-R1-Lanes-Version = %q, want 1", got)
	}

	br := bufio.NewReader(resp.Body)

	// First record MUST be session.bound at seq=0 (TASK-17).
	rec, err := readSSERecord(br)
	if err != nil {
		t.Fatalf("read session.bound: %v", err)
	}
	if rec["id"] != "0" {
		t.Errorf("session.bound id = %q, want 0", rec["id"])
	}
	if rec["event"] != "session.bound" {
		t.Errorf("first record event = %q, want session.bound", rec["event"])
	}
	var bound map[string]any
	if err := json.Unmarshal([]byte(rec["data"]), &bound); err != nil {
		t.Fatalf("unmarshal session.bound data: %v", err)
	}
	if bound["session_id"] != "sess_test_42" {
		t.Errorf("session.bound session_id = %v, want sess_test_42", bound["session_id"])
	}

	// Now fire a few lane events. Wait briefly so the subscriber is
	// fully registered (the SSE handler registers AFTER emitting bound).
	time.Sleep(50 * time.Millisecond)

	for i, kind := range []hub.EventType{hub.EventLaneCreated, hub.EventLaneStatus, hub.EventLaneDelta} {
		fakeHub.Send(&hub.Event{
			ID:   "evt-" + kindShort(kind),
			Type: kind,
			Lane: &hub.LaneEvent{
				LaneID:    "lane_test",
				SessionID: "sess_test_42",
				Seq:       uint64(i + 1),
				Status:    hub.LaneStatusRunning,
				Kind:      hub.LaneKindMain,
				Label:     "test",
				DeltaSeq:  uint64(i + 1),
			},
		})
	}

	// Read three SSE records, each carrying the matching seq and event.
	for i, expectKind := range []string{"lane.created", "lane.status", "lane.delta"} {
		rec, err := readSSERecord(br)
		if err != nil {
			t.Fatalf("read %s: %v", expectKind, err)
		}
		if rec["event"] != expectKind {
			t.Errorf("rec[%d] event = %q, want %q", i, rec["event"], expectKind)
		}
		expectSeq := []string{"1", "2", "3"}[i]
		if rec["id"] != expectSeq {
			t.Errorf("rec[%d] id = %q, want %s", i, rec["id"], expectSeq)
		}
	}
}

// kindShort returns the last dotted segment of an event type.
func kindShort(t hub.EventType) string {
	s := string(t)
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// TestHandleLaneEventsRejectsMissingSessionID enforces the 400 reply when
// the required session_id query parameter is omitted (spec §6.1).
func TestHandleLaneEventsRejectsMissingSessionID(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/lanes/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandleLaneEventsLanesNotConfigured exercises the 503 reply when the
// server runs without WithLanes.
func TestHandleLaneEventsLanesNotConfigured(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/lanes/events?session_id=sess_x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// TestHandleLaneEventsFiltersOtherSessions verifies that events for a
// different session_id are not delivered to a subscriber bound to ours.
func TestHandleLaneEventsFiltersOtherSessions(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=sess_A", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)

	// Drain session.bound first.
	if _, err := readSSERecord(br); err != nil {
		t.Fatalf("read bound: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	// Emit for sess_B: must NOT appear on the sess_A stream.
	fakeHub.Send(&hub.Event{
		ID:   "should-be-filtered",
		Type: hub.EventLaneCreated,
		Lane: &hub.LaneEvent{LaneID: "lane_x", SessionID: "sess_B", Seq: 1},
	})
	// Emit for sess_A: MUST appear.
	fakeHub.Send(&hub.Event{
		ID:   "should-pass",
		Type: hub.EventLaneCreated,
		Lane: &hub.LaneEvent{LaneID: "lane_y", SessionID: "sess_A", Seq: 2, Kind: hub.LaneKindMain},
	})

	rec, err := readSSERecord(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rec["id"] != "2" {
		t.Errorf("expected first record after bound to be seq=2 (sess_A), got id=%q event=%q", rec["id"], rec["event"])
	}
}
