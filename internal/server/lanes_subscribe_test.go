// Package server — tests for the lanes-protocol session.subscribe
// JSON-RPC method (TASK-15) per specs/lanes-protocol.md §5.2 and §6.2.
//
// Drives the WS endpoint with a real RFC 6455 client (see ws_test.go's
// dialLanesWS) and verifies:
//
//   - session.subscribe returns a result containing {sub, snapshot_seq};
//   - subsequent live lane events arrive as $/event notifications with
//     params.sub matching the subscription id and params.event/seq/
//     session_id/lane_id flattened from §4 verbatim;
//   - session.unsubscribe stops further $/event notifications.
package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// TestSessionSubscribeJSONRPC drives one subscribe + 3 events + unsubscribe
// round trip across the WS transport.
func TestSessionSubscribeJSONRPC(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()
	if resp.StatusCode != 101 {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	// Send session.subscribe.
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session.subscribe",
		"params": map[string]any{
			"session_id": "sess_jsonrpc",
		},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// Frame 1: subscribe result.
	opcode, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if opcode != 0x1 {
		t.Fatalf("opcode = %x, want text", opcode)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", result["jsonrpc"])
	}
	if result["id"] == nil {
		t.Errorf("missing id field")
	}
	res, ok := result["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing or wrong type: %v", result["result"])
	}
	subID, ok := res["sub"].(float64) // JSON numbers decode as float64
	if !ok {
		t.Fatalf("sub missing: %v", res)
	}
	if subID < 1 {
		t.Errorf("sub = %v, want >= 1", subID)
	}
	if _, ok := res["snapshot_seq"]; !ok {
		t.Errorf("snapshot_seq missing from result")
	}

	// Frame 2: session.bound synthetic at seq=0 (TASK-17). Verified
	// here that the JSON-RPC path also honors the floor-marker
	// invariant; lanes_session_bound_test.go drills deeper.
	opcode, payload, err = readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read bound: %v", err)
	}
	if opcode != 0x1 {
		t.Fatalf("bound opcode = %x", opcode)
	}
	var bound map[string]any
	if err := json.Unmarshal(payload, &bound); err != nil {
		t.Fatalf("unmarshal bound: %v", err)
	}
	if bound["method"] != "$/event" {
		t.Errorf("bound method = %v, want $/event", bound["method"])
	}
	bp, _ := bound["params"].(map[string]any)
	if bp["event"] != "session.bound" {
		t.Errorf("first event = %v, want session.bound", bp["event"])
	}
	if v, _ := bp["seq"].(float64); v != 0 {
		t.Errorf("session.bound seq = %v, want 0", v)
	}
	if bp["session_id"] != "sess_jsonrpc" {
		t.Errorf("session.bound session_id = %v, want sess_jsonrpc", bp["session_id"])
	}

	// Now publish 3 lane events.
	events := []hub.EventType{hub.EventLaneCreated, hub.EventLaneStatus, hub.EventLaneDelta}
	for i, et := range events {
		fakeHub.Send(&hub.Event{
			ID:        "evt-" + string(et),
			Type:      et,
			Timestamp: time.Now(),
			Lane: &hub.LaneEvent{
				LaneID:    "lane_jsonrpc",
				SessionID: "sess_jsonrpc",
				Seq:       uint64(i + 1),
				Kind:      hub.LaneKindMain,
				Status:    hub.LaneStatusRunning,
				DeltaSeq:  uint64(i + 1),
			},
		})
	}

	// Read 3 $/event notifications.
	for i, expectKind := range []string{"lane.created", "lane.status", "lane.delta"} {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		opcode, payload, err := readWSFrameAsClient(br)
		if err != nil {
			t.Fatalf("read evt %d: %v", i, err)
		}
		if opcode != 0x1 {
			t.Fatalf("evt %d opcode = %x", i, opcode)
		}
		var notif map[string]any
		if err := json.Unmarshal(payload, &notif); err != nil {
			t.Fatalf("unmarshal evt %d: %v", i, err)
		}
		if notif["method"] != "$/event" {
			t.Errorf("evt %d method = %v, want $/event", i, notif["method"])
		}
		if _, hasID := notif["id"]; hasID {
			t.Errorf("evt %d should be notification (no id), got: %v", i, notif["id"])
		}
		params, _ := notif["params"].(map[string]any)
		if v, _ := params["sub"].(float64); v != subID {
			t.Errorf("evt %d sub = %v, want %v", i, v, subID)
		}
		if v, _ := params["seq"].(float64); v != float64(i+1) {
			t.Errorf("evt %d seq = %v, want %d", i, v, i+1)
		}
		if params["event"] != expectKind {
			t.Errorf("evt %d event = %v, want %s", i, params["event"], expectKind)
		}
		if params["lane_id"] != "lane_jsonrpc" {
			t.Errorf("evt %d lane_id = %v", i, params["lane_id"])
		}
		if params["session_id"] != "sess_jsonrpc" {
			t.Errorf("evt %d session_id = %v", i, params["session_id"])
		}
		if _, ok := params["data"].(map[string]any); !ok {
			t.Errorf("evt %d missing data subobject", i)
		}
	}

	// Send session.unsubscribe.
	unsubReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session.unsubscribe",
		"params":  map[string]any{"sub": subID},
	})
	if err := writeWSFrameMasked(conn, 0x1, unsubReq); err != nil {
		t.Fatalf("write unsub: %v", err)
	}

	// Drain unsubscribe ACK.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := readWSFrameAsClient(br); err != nil {
		t.Fatalf("read unsub ack: %v", err)
	}

	// Subsequent events must NOT arrive. Send one and confirm read
	// times out.
	fakeHub.Send(&hub.Event{
		ID:   "should-be-dropped",
		Type: hub.EventLaneCreated,
		Lane: &hub.LaneEvent{LaneID: "lane_z", SessionID: "sess_jsonrpc", Seq: 99},
	})
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := readWSFrameAsClient(br); err == nil {
		t.Errorf("event arrived after unsubscribe")
	}
}

// TestSessionSubscribeRejectsMissingSessionID verifies the -32602 error
// path when params.session_id is absent.
func TestSessionSubscribeRejectsMissingSessionID(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()
	_ = context.Background

	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "session.subscribe",
		"params":  map[string]any{},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp2 map[string]any
	if err := json.Unmarshal(payload, &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, ok := resp2["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error reply, got %v", resp2)
	}
	if v, _ := errObj["code"].(float64); v != -32602 {
		t.Errorf("error code = %v, want -32602", v)
	}
}

// TestSessionSubscribeUnknownMethod verifies -32601 for unknown methods.
func TestSessionSubscribeUnknownMethod(t *testing.T) {
	t.Parallel()
	fakeHub := newFakeLanesHub()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: fakeHub})
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
		"id":      7,
		"method":  "what.is.this",
		"params":  map[string]any{},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp2 map[string]any
	if err := json.Unmarshal(payload, &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, ok := resp2["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error reply, got %v", resp2)
	}
	if v, _ := errObj["code"].(float64); v != -32601 {
		t.Errorf("error code = %v, want -32601", v)
	}
}
