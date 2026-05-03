// Package server — focused tests for the session.bound synthetic
// (TASK-17) per specs/lanes-protocol.md §5.5 and §6.2.
//
// Earlier tests in this suite (TestHandleLaneEventsHTTPSSE,
// TestSessionSubscribeJSONRPC, TestSubscribeReplayFromSinceSeq) all
// confirm that session.bound is the first record. This file drills
// deeper:
//
//   - the session.bound record has seq=0 verbatim;
//   - the embedded session_id matches the request session_id (mismatch
//     detection);
//   - it appears even when no lanes are active and no replay is
//     requested (the floor marker invariant);
//   - it appears BEFORE any replay batch on Last-Event-ID reconnect;
//   - the JSON-RPC variant carries method="$/event", params.event=
//     "session.bound", params.seq=0, params.session_id=<request>;
//   - SSE id="0", event="session.bound";
//   - data is a JSON object (per spec §5.2 the data subobject is
//     present even when empty).
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// TestSessionBoundSynthetic exercises the SSE path: the very first
// record after a fresh subscribe is session.bound at seq=0 carrying
// the requested session_id.
func TestSessionBoundSynthetic(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=sess_bound_42", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	rec, err := readSSERecord(br)
	if err != nil {
		t.Fatalf("read first record: %v", err)
	}

	// SSE-level invariants.
	if rec["id"] != "0" {
		t.Errorf("id = %q, want 0 (spec §5.5: seq=0 reserved for session.bound)", rec["id"])
	}
	if rec["event"] != "session.bound" {
		t.Errorf("event = %q, want session.bound", rec["event"])
	}

	// JSON body invariants.
	var body map[string]any
	if err := json.Unmarshal([]byte(rec["data"]), &body); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if body["event"] != "session.bound" {
		t.Errorf("data.event = %v, want session.bound", body["event"])
	}
	if body["session_id"] != "sess_bound_42" {
		t.Errorf("data.session_id = %v, want sess_bound_42", body["session_id"])
	}
	// Numbers from JSON decode as float64.
	if v, ok := body["seq"].(float64); !ok || v != 0 {
		t.Errorf("data.seq = %v (type %T), want 0", body["seq"], body["seq"])
	}
}

// TestSessionBoundFiresWithoutLanes verifies the floor-marker invariant:
// even when no lane events are firing, the synthetic still appears so
// the client has a known "I am bound to session N" signal.
func TestSessionBoundFiresWithoutLanes(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=quiet", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	// Set a deadline so the test fails fast if no record arrives.
	type sseResult struct {
		rec map[string]string
		err error
	}
	ch := make(chan sseResult, 1)
	go func() {
		rec, err := readSSERecord(br)
		ch <- sseResult{rec, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read: %v", r.err)
		}
		if r.rec["event"] != "session.bound" {
			t.Errorf("event = %q, want session.bound", r.rec["event"])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for session.bound")
	}
}

// TestSessionBoundPrecedesReplayBatch confirms the §6.2 ordering:
// session.bound (seq=0) ALWAYS arrives before any replayed lane events,
// even when Last-Event-ID asks for a slice of history.
func TestSessionBoundPrecedesReplayBatch(t *testing.T) {
	t.Parallel()
	wal := newFakeLanesWAL()
	for i := 1; i <= 3; i++ {
		wal.Append(&hub.Event{
			ID: "evt-" + itoa(i), Type: hub.EventLaneStatus,
			Lane: &hub.LaneEvent{
				LaneID:    "lane_x",
				SessionID: "sess_y",
				Seq:       uint64(i),
				Status:    hub.LaneStatusRunning,
			},
		})
	}

	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub(), WAL: wal})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=sess_y&since_seq=0", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)

	// Record 1 MUST be session.bound.
	rec, err := readSSERecord(br)
	if err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if rec["event"] != "session.bound" || rec["id"] != "0" {
		t.Fatalf("first record id=%q event=%q, want 0/session.bound", rec["id"], rec["event"])
	}

	// since_seq=0 means "no replay" per the handler contract; drive
	// another connection at since_seq=0 with the seq>0 since_seq=1
	// equivalent and verify ordering.

	// Second connection: since_seq=1. Records: bound (seq=0), then
	// replay seqs 2, 3.
	req2, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id=sess_y&since_seq=1", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET 2: %v", err)
	}
	defer resp2.Body.Close()
	br2 := bufio.NewReader(resp2.Body)

	rec, err = readSSERecord(br2)
	if err != nil {
		t.Fatalf("read 2/1: %v", err)
	}
	if rec["event"] != "session.bound" || rec["id"] != "0" {
		t.Fatalf("conn2 first record id=%q event=%q, want 0/session.bound", rec["id"], rec["event"])
	}

	// Then replay seqs 2 and 3.
	for _, want := range []string{"2", "3"} {
		rec, err := readSSERecord(br2)
		if err != nil {
			t.Fatalf("read replay seq=%s: %v", want, err)
		}
		if rec["id"] != want {
			t.Errorf("replay id = %q, want %s (must come AFTER session.bound)", rec["id"], want)
		}
	}
}

// TestSessionBoundOverWS confirms the same floor-marker invariant on
// the WS+JSON-RPC transport: the first $/event after session.subscribe
// is session.bound{seq:0}.
func TestSessionBoundOverWS(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
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
		"params": map[string]any{"session_id": "sess_ws_bound"},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// Frame 1: subscribe result (skip).
	if _, _, err := readWSFrameAsClient(br); err != nil {
		t.Fatalf("read result: %v", err)
	}

	// Frame 2: session.bound notification — primary assertion.
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read bound: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", msg["jsonrpc"])
	}
	if msg["method"] != "$/event" {
		t.Errorf("method = %v, want $/event", msg["method"])
	}
	if _, hasID := msg["id"]; hasID {
		t.Errorf("session.bound should be a notification (no id), got id=%v", msg["id"])
	}
	params, ok := msg["params"].(map[string]any)
	if !ok {
		t.Fatalf("missing params: %v", msg)
	}
	if params["event"] != "session.bound" {
		t.Errorf("params.event = %v, want session.bound", params["event"])
	}
	if v, _ := params["seq"].(float64); v != 0 {
		t.Errorf("params.seq = %v, want 0", v)
	}
	if params["session_id"] != "sess_ws_bound" {
		t.Errorf("params.session_id = %v, want sess_ws_bound", params["session_id"])
	}
	if _, ok := params["data"].(map[string]any); !ok {
		t.Errorf("params.data missing or wrong type: %T", params["data"])
	}
}

// TestSessionBoundDistinguishesSessions verifies the mismatch detection
// use case: two concurrent subscriptions for different sessions each
// receive a session.bound carrying their own session_id, not the other's.
func TestSessionBoundDistinguishesSessions(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	openOne := func(sessionID string) map[string]any {
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/lanes/events?session_id="+sessionID, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", sessionID, err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		br := bufio.NewReader(resp.Body)
		rec, err := readSSERecord(br)
		if err != nil {
			t.Fatalf("read %s: %v", sessionID, err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(rec["data"]), &body); err != nil {
			t.Fatalf("unmarshal %s: %v", sessionID, err)
		}
		return body
	}

	a := openOne("sess_alpha")
	b := openOne("sess_beta")

	if a["session_id"] != "sess_alpha" {
		t.Errorf("alpha bound session_id = %v, want sess_alpha", a["session_id"])
	}
	if b["session_id"] != "sess_beta" {
		t.Errorf("beta bound session_id = %v, want sess_beta", b["session_id"])
	}
	if a["session_id"] == b["session_id"] {
		t.Errorf("bound records carried the same session_id %v across two sessions", a["session_id"])
	}
}
