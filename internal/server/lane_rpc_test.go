// Package server — TASK-25 round-trip JSON-RPC test for the lanes
// protocol per specs/lanes-protocol.md §10.2.
//
// The dedicated round-trip:
//
//   1. Stand up an in-memory Server with a fake hub.
//   2. Open a WS connection, advertise the r1.lanes.v1 subprotocol.
//   3. session.subscribe; capture {sub, snapshot_seq}.
//   4. Drain the synthetic session.bound (seq=0) per spec §5.5.
//   5. Drive the cortex through a synthetic Lobe lifecycle: emit
//      lane.created, lane.status(running), lane.delta x3,
//      lane.cost, lane.note, lane.status(done).
//   6. Assert: every emitted EventLane* shows up in the JSON-RPC
//      stream, in order; seq is monotonic and gap-free; the §5.2
//      envelope is canonical (jsonrpc=2.0, method=$/event, params has
//      sub/seq/event/event_id/session_id/lane_id/at/data fields);
//      $/event notifications carry no `id` field; the per-event-type
//      data sub-object carries the spec §4 payload sub-object shape.
package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// TestLaneRPCRoundTripCortexLifecycle drives a single subscribe ->
// drive cortex -> verify the JSON-RPC stream end-to-end.
//
// The "fake cortex Workspace" is the synthetic event sequence below;
// using the real *cortex.Workspace would pull the lobe lifecycle,
// model providers, and round machinery, none of which is needed to
// exercise the wire-level invariants this test owns.
func TestLaneRPCRoundTripCortexLifecycle(t *testing.T) {
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

	const sessionID = "sess_lane_rpc_25"
	const laneID = "lane_lobe_42"

	// 1. session.subscribe.
	subReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session.subscribe",
		"params":  map[string]any{"session_id": sessionID},
	})
	if err := writeWSFrameMasked(conn, 0x1, subReq); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	// Frame 1: the subscribe result.
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var result map[string]any
	if jerr := json.Unmarshal(payload, &result); jerr != nil {
		t.Fatalf("unmarshal result: %v", jerr)
	}
	if result["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", result["jsonrpc"])
	}
	res, _ := result["result"].(map[string]any)
	subID, _ := res["sub"].(float64)
	if subID < 1 {
		t.Fatalf("sub = %v, want >=1", subID)
	}

	// Frame 2: synthetic session.bound at seq=0 per §5.5.
	_, payload, err = readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read bound: %v", err)
	}
	var bound map[string]any
	_ = json.Unmarshal(payload, &bound)
	if bound["method"] != "$/event" {
		t.Errorf("bound method = %v", bound["method"])
	}
	bp, _ := bound["params"].(map[string]any)
	if bp["event"] != "session.bound" {
		t.Errorf("first event = %v, want session.bound", bp["event"])
	}
	if v, _ := bp["seq"].(float64); v != 0 {
		t.Errorf("session.bound seq = %v, want 0", v)
	}

	// 2. Synthetic Lobe lifecycle. Each event carries a monotonic seq
	//    and the §4 payload fields.
	now := time.Now().UTC()
	started := now
	ended := now.Add(150 * time.Millisecond)
	lifecycle := []*hub.Event{
		{
			ID: "evt-created", Type: hub.EventLaneCreated, Timestamp: now,
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 1,
				Kind:      hub.LaneKindLobe,
				LobeName:  "MemoryRecallLobe",
				ParentID:  "lane_main",
				Label:     "recall: cortex workspace",
				StartedAt: &started,
			},
		},
		{
			ID: "evt-status-running", Type: hub.EventLaneStatus, Timestamp: now.Add(time.Millisecond),
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 2,
				Status:     hub.LaneStatusRunning,
				PrevStatus: hub.LaneStatusPending,
				ReasonCode: "started",
				Reason:     "lobe dispatch",
			},
		},
		{
			ID: "evt-delta-1", Type: hub.EventLaneDelta, Timestamp: now.Add(20 * time.Millisecond),
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 3,
				DeltaSeq: 1,
				Block:    &hub.LaneContentBlock{Type: "text_delta", Text: "found "},
			},
		},
		{
			ID: "evt-delta-2", Type: hub.EventLaneDelta, Timestamp: now.Add(30 * time.Millisecond),
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 4,
				DeltaSeq: 2,
				Block:    &hub.LaneContentBlock{Type: "text_delta", Text: "3 matching memories"},
			},
		},
		{
			ID: "evt-cost", Type: hub.EventLaneCost, Timestamp: now.Add(40 * time.Millisecond),
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 5,
				TokensIn: 12480, TokensOut: 312, USD: 0.00184,
			},
		},
		{
			ID: "evt-note", Type: hub.EventLaneNote, Timestamp: now.Add(60 * time.Millisecond),
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 6,
				NoteID:       "note_xyz",
				NoteSeverity: "info",
				NoteKind:     "memory_recall",
				NoteSummary:  "3 prior decisions",
			},
		},
		{
			ID: "evt-status-done", Type: hub.EventLaneStatus, Timestamp: now.Add(150 * time.Millisecond),
			Lane: &hub.LaneEvent{
				LaneID: laneID, SessionID: sessionID, Seq: 7,
				Status:     hub.LaneStatusDone,
				PrevStatus: hub.LaneStatusRunning,
				ReasonCode: "ok",
				EndedAt:    &ended,
			},
		},
	}

	// Wait briefly so the WS subscriber is fully registered against
	// the fake hub before the first Send fires (Register happens after
	// the subscribe ACK and bound emission).
	time.Sleep(20 * time.Millisecond)

	for _, ev := range lifecycle {
		fakeHub.Send(ev)
	}

	// 3. Read seven $/event notifications. Assert envelope shape and
	//    monotonic gap-free seq.
	wantEvents := []string{
		"lane.created",
		"lane.status",
		"lane.delta",
		"lane.delta",
		"lane.cost",
		"lane.note",
		"lane.status",
	}
	var prevSeq float64
	for i, want := range wantEvents {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, err := readWSFrameAsClient(br)
		if err != nil {
			t.Fatalf("read evt %d: %v", i, err)
		}
		var notif map[string]any
		if jerr := json.Unmarshal(payload, &notif); jerr != nil {
			t.Fatalf("unmarshal evt %d: %v", i, jerr)
		}
		if notif["jsonrpc"] != "2.0" {
			t.Errorf("evt %d jsonrpc = %v, want 2.0", i, notif["jsonrpc"])
		}
		if notif["method"] != "$/event" {
			t.Errorf("evt %d method = %v, want $/event", i, notif["method"])
		}
		if _, hasID := notif["id"]; hasID {
			t.Errorf("evt %d carries id field; $/event is a notification", i)
		}
		params, ok := notif["params"].(map[string]any)
		if !ok {
			t.Fatalf("evt %d params not an object", i)
		}
		// Envelope keys per §5.2.
		for _, key := range []string{"sub", "seq", "event", "event_id", "session_id", "lane_id", "at", "data"} {
			if _, ok := params[key]; !ok {
				t.Errorf("evt %d missing envelope key %q", i, key)
			}
		}
		if params["sub"].(float64) != subID {
			t.Errorf("evt %d sub = %v, want %v", i, params["sub"], subID)
		}
		if params["event"] != want {
			t.Errorf("evt %d event = %v, want %v", i, params["event"], want)
		}
		if params["lane_id"] != laneID {
			t.Errorf("evt %d lane_id = %v, want %v", i, params["lane_id"], laneID)
		}
		if params["session_id"] != sessionID {
			t.Errorf("evt %d session_id = %v, want %v", i, params["session_id"], sessionID)
		}
		gotSeq, _ := params["seq"].(float64)
		if gotSeq != float64(i+1) {
			t.Errorf("evt %d seq = %v, want %d", i, gotSeq, i+1)
		}
		if gotSeq <= prevSeq {
			t.Errorf("evt %d seq=%v not monotonic (prev=%v)", i, gotSeq, prevSeq)
		}
		prevSeq = gotSeq

		// Per-event-type sub-object spot-checks — we don't lock byte-
		// for-byte equality (the runtime float64 timestamps are
		// nondeterministic) but we assert the §4 fields land in
		// data.
		data, _ := params["data"].(map[string]any)
		switch want {
		case "lane.created":
			if data["kind"] != "lobe" {
				t.Errorf("created kind = %v, want lobe", data["kind"])
			}
			if data["lobe_name"] != "MemoryRecallLobe" {
				t.Errorf("created lobe_name = %v", data["lobe_name"])
			}
			if data["parent_lane_id"] != "lane_main" {
				t.Errorf("created parent_lane_id = %v", data["parent_lane_id"])
			}
		case "lane.status":
			if data["status"] == nil {
				t.Errorf("status missing")
			}
			if data["reason_code"] == nil {
				t.Errorf("reason_code missing")
			}
		case "lane.delta":
			if data["delta_seq"] == nil {
				t.Errorf("delta_seq missing")
			}
			block, _ := data["content_block"].(map[string]any)
			if block["type"] != "text_delta" {
				t.Errorf("delta block type = %v", block["type"])
			}
		case "lane.cost":
			if data["tokens_in"] == nil || data["tokens_out"] == nil || data["usd"] == nil {
				t.Errorf("cost missing token/usd fields: %v", data)
			}
		case "lane.note":
			if data["note_id"] != "note_xyz" {
				t.Errorf("note_id = %v", data["note_id"])
			}
			if data["severity"] != "info" {
				t.Errorf("severity = %v", data["severity"])
			}
		}
	}
}

// TestLaneRPCRoundTripFiltersOtherSessions ensures the JSON-RPC path
// filters by session_id at the subscriber boundary — events for a
// different session NEVER appear on the subscribed connection.
func TestLaneRPCRoundTripFiltersOtherSessions(t *testing.T) {
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

	subReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session.subscribe",
		"params":  map[string]any{"session_id": "sess_alpha"},
	})
	if err := writeWSFrameMasked(conn, 0x1, subReq); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	// Drain subscribe result + bound.
	if _, _, err := readWSFrameAsClient(br); err != nil {
		t.Fatalf("read result: %v", err)
	}
	if _, _, err := readWSFrameAsClient(br); err != nil {
		t.Fatalf("read bound: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	// Wrong session: must NOT pass.
	fakeHub.Send(&hub.Event{
		ID: "wrong", Type: hub.EventLaneCreated, Timestamp: time.Now(),
		Lane: &hub.LaneEvent{LaneID: "x", SessionID: "sess_other", Seq: 1, Kind: hub.LaneKindMain},
	})
	// Right session: MUST pass.
	fakeHub.Send(&hub.Event{
		ID: "right", Type: hub.EventLaneCreated, Timestamp: time.Now(),
		Lane: &hub.LaneEvent{LaneID: "y", SessionID: "sess_alpha", Seq: 2, Kind: hub.LaneKindMain},
	})

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var notif map[string]any
	_ = json.Unmarshal(payload, &notif)
	params, _ := notif["params"].(map[string]any)
	if params["session_id"] != "sess_alpha" {
		t.Errorf("got session_id %v, want sess_alpha (cross-session leaked)", params["session_id"])
	}
	if v, _ := params["seq"].(float64); v != 2 {
		t.Errorf("got seq %v, want 2", v)
	}
}

// _ context import kept for future cancel-driven assertions.
var _ = context.Background
