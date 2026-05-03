// Package server — tests for the lanes-protocol WAL replay path
// (TASK-16) per specs/lanes-protocol.md §6.1 and §6.2.
//
// Drives both the HTTP+SSE and WS transports through a fake LanesWAL
// and verifies:
//
//   - replay starts at since_seq+1 (events at since_seq itself are
//     EXCLUDED — the client already has that one);
//   - replayed events flow before live events and in seq order;
//   - on out-of-window replay (ErrWALTruncatedError), the SSE handler
//     responds 404 with structured wal_truncated payload, and the WS
//     handler emits JSON-RPC -32004 plus a 4404 close frame.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// fakeLanesWAL is an in-memory LanesWAL backed by a slice of events.
// Events are stored in seq order; truncate(seq) marks all events with
// seq <= cutoff as removed so subsequent ReplayLane calls with a
// fromSeq <= cutoff return ErrWALTruncatedError.
type fakeLanesWAL struct {
	mu        sync.Mutex
	events    []*hub.Event // by sessionID
	bySession map[string][]*hub.Event
	cutoff    uint64 // events at seq <= cutoff are pruned
}

func newFakeLanesWAL() *fakeLanesWAL {
	return &fakeLanesWAL{bySession: make(map[string][]*hub.Event)}
}

// Append records ev. Events are kept in append order; tests are
// responsible for emitting in the right seq order for replay assertions.
func (w *fakeLanesWAL) Append(ev *hub.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
	if ev.Lane != nil {
		w.bySession[ev.Lane.SessionID] = append(w.bySession[ev.Lane.SessionID], ev)
	}
}

// Truncate sets the retention cutoff: ReplayLane requests with
// fromSeq <= cutoff return ErrWALTruncatedError.
func (w *fakeLanesWAL) Truncate(cutoff uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cutoff = cutoff
}

// ReplayLane satisfies LanesWAL. Returns ErrWALTruncatedError if
// fromSeq <= w.cutoff (i.e. the requested cursor predates retention).
func (w *fakeLanesWAL) ReplayLane(_ context.Context, sessionID string, fromSeq uint64, handler func(*hub.Event) error) error {
	w.mu.Lock()
	cutoff := w.cutoff
	pool := append([]*hub.Event(nil), w.bySession[sessionID]...)
	w.mu.Unlock()

	if fromSeq > 0 && fromSeq <= cutoff {
		return &ErrWALTruncatedError{FromSeq: fromSeq}
	}
	for _, ev := range pool {
		if ev.Lane != nil && ev.Lane.Seq >= fromSeq {
			if err := handler(ev); err != nil {
				return err
			}
		}
	}
	return nil
}

// TestSubscribeReplayFromSinceSeq exercises the HTTP+SSE path:
// Last-Event-ID: 5 should replay events 6, 7, 8 then continue live.
func TestSubscribeReplayFromSinceSeq(t *testing.T) {
	t.Parallel()
	wal := newFakeLanesWAL()
	for i := 1; i <= 8; i++ {
		wal.Append(&hub.Event{
			ID:        "evt-" + itoa(i),
			Type:      hub.EventLaneStatus,
			Timestamp: time.Now(),
			Lane: &hub.LaneEvent{
				LaneID:    "lane_replay",
				SessionID: "sess_replay",
				Seq:       uint64(i),
				Status:    hub.LaneStatusRunning,
			},
		})
	}

	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub, WAL: wal})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=sess_replay", nil)
	req.Header.Set("Last-Event-ID", "5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-R1-Lanes-Version"); got != "1" {
		t.Errorf("X-R1-Lanes-Version = %q, want 1", got)
	}
	br := bufio.NewReader(resp.Body)

	// First record: session.bound at seq=0 (TASK-17 — emitted before
	// the replay batch even when Last-Event-ID is set).
	rec, err := readSSERecord(br)
	if err != nil {
		t.Fatalf("read bound: %v", err)
	}
	if rec["id"] != "0" || rec["event"] != "session.bound" {
		t.Errorf("first record id=%q event=%q, want 0/session.bound", rec["id"], rec["event"])
	}

	// Replay batch: seqs 6, 7, 8.
	for _, want := range []string{"6", "7", "8"} {
		rec, err := readSSERecord(br)
		if err != nil {
			t.Fatalf("read replay seq=%s: %v", want, err)
		}
		if rec["id"] != want {
			t.Errorf("replay record id = %q, want %s", rec["id"], want)
		}
		if rec["event"] != "lane.status" {
			t.Errorf("replay record event = %q, want lane.status", rec["event"])
		}
	}

	// Now fire a live event at seq=9: should arrive after the replay
	// batch.
	time.Sleep(30 * time.Millisecond) // let subscriber register
	fakeHub.Send(&hub.Event{
		ID:   "evt-9",
		Type: hub.EventLaneCreated,
		Lane: &hub.LaneEvent{LaneID: "lane_replay", SessionID: "sess_replay", Seq: 9, Kind: hub.LaneKindMain},
	})
	rec, err = readSSERecord(br)
	if err != nil {
		t.Fatalf("read live: %v", err)
	}
	if rec["id"] != "9" {
		t.Errorf("live record id = %q, want 9", rec["id"])
	}
}

// TestSubscribeReplayWALTruncated exercises the truncate path on HTTP+SSE.
// since_seq=2 with cutoff=4 means the requested cursor predates
// retention; server returns 404 with wal_truncated payload.
func TestSubscribeReplayWALTruncated(t *testing.T) {
	t.Parallel()
	wal := newFakeLanesWAL()
	for i := 5; i <= 8; i++ {
		wal.Append(&hub.Event{
			ID: "evt-" + itoa(i), Type: hub.EventLaneStatus,
			Lane: &hub.LaneEvent{LaneID: "lane_t", SessionID: "sess_t", Seq: uint64(i)},
		})
	}
	wal.Truncate(4)

	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub, WAL: wal})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v1/lanes/events?session_id=sess_t&since_seq=2", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("X-R1-Lanes-Version"); got != "1" {
		t.Errorf("X-R1-Lanes-Version = %q, want 1", got)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %v", body)
	}
	if v, _ := errObj["code"].(float64); v != -32004 {
		t.Errorf("error code = %v, want -32004", v)
	}
	data, _ := errObj["data"].(map[string]any)
	if data["detail"] != "wal_truncated" {
		t.Errorf("data.detail = %v, want wal_truncated", data["detail"])
	}
	if data["stoke_code"] != "not_found" {
		t.Errorf("data.stoke_code = %v, want not_found", data["stoke_code"])
	}
}

// TestSubscribeReplayWS exercises the WS transport. session.subscribe
// with since_seq=5 yields a result, then session.bound, then events
// 6, 7, 8.
func TestSubscribeReplayWS(t *testing.T) {
	t.Parallel()
	wal := newFakeLanesWAL()
	for i := 1; i <= 8; i++ {
		wal.Append(&hub.Event{
			ID: "evt-" + itoa(i), Type: hub.EventLaneDelta,
			Lane: &hub.LaneEvent{
				LaneID:    "lane_ws",
				SessionID: "sess_ws",
				Seq:       uint64(i),
				DeltaSeq:  uint64(i),
			},
		})
	}

	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub, WAL: wal})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session.subscribe",
		"params":  map[string]any{"session_id": "sess_ws", "since_seq": 5},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// Frame 1: result.
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var resMsg map[string]any
	_ = json.Unmarshal(payload, &resMsg)
	if _, ok := resMsg["result"]; !ok {
		t.Fatalf("expected result, got %v", resMsg)
	}
	res, _ := resMsg["result"].(map[string]any)
	if v, _ := res["snapshot_seq"].(float64); v != 8 {
		t.Errorf("snapshot_seq = %v, want 8", v)
	}

	// Frame 2: session.bound.
	_, payload, _ = readWSFrameAsClient(br)
	var boundMsg map[string]any
	_ = json.Unmarshal(payload, &boundMsg)
	bp, _ := boundMsg["params"].(map[string]any)
	if bp["event"] != "session.bound" {
		t.Errorf("expected session.bound, got %v", bp["event"])
	}

	// Frames 3-5: replay seqs 6, 7, 8.
	for _, wantSeq := range []float64{6, 7, 8} {
		_, payload, err := readWSFrameAsClient(br)
		if err != nil {
			t.Fatalf("read seq=%v: %v", wantSeq, err)
		}
		var n map[string]any
		_ = json.Unmarshal(payload, &n)
		if n["method"] != "$/event" {
			t.Errorf("seq=%v method = %v", wantSeq, n["method"])
		}
		params, _ := n["params"].(map[string]any)
		if params["seq"] != wantSeq {
			t.Errorf("seq = %v, want %v", params["seq"], wantSeq)
		}
		if params["event"] != "lane.delta" {
			t.Errorf("event = %v, want lane.delta", params["event"])
		}
	}
}

// TestSubscribeReplayWSWALTruncated: WS transport returns JSON-RPC
// -32004 with data.code="wal_truncated" then closes with code 4404.
func TestSubscribeReplayWSWALTruncated(t *testing.T) {
	t.Parallel()
	wal := newFakeLanesWAL()
	for i := 5; i <= 8; i++ {
		wal.Append(&hub.Event{
			ID: "evt-" + itoa(i), Type: hub.EventLaneDelta,
			Lane: &hub.LaneEvent{LaneID: "lane_t", SessionID: "sess_t", Seq: uint64(i)},
		})
	}
	wal.Truncate(4)

	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub(), WAL: wal})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "session.subscribe",
		"params": map[string]any{"session_id": "sess_t", "since_seq": 2},
	})
	_ = writeWSFrameMasked(conn, 0x1, req)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// Frame 1: error reply.
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	var msg map[string]any
	_ = json.Unmarshal(payload, &msg)
	errObj, ok := msg["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error reply, got %v", msg)
	}
	if v, _ := errObj["code"].(float64); v != -32004 {
		t.Errorf("error code = %v, want -32004", v)
	}
	data, _ := errObj["data"].(map[string]any)
	if data["code"] != "wal_truncated" {
		t.Errorf("data.code = %v, want wal_truncated", data["code"])
	}

	// Frame 2: close with 4404.
	opcode, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read close: %v", err)
	}
	if opcode != 0x8 {
		t.Fatalf("opcode = %x, want close", opcode)
	}
	if len(payload) < 2 {
		t.Fatalf("close payload too short: %v", payload)
	}
	code := uint16(payload[0])<<8 | uint16(payload[1])
	if code != wsCloseWALTruncated {
		t.Errorf("close code = %d, want %d", code, wsCloseWALTruncated)
	}
}

// itoa is an unsigned-only int-to-string for the small loop counters
// used in test fixtures (avoids importing strconv just for this one use).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := make([]byte, 0, 4)
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
