package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestLaneEventTypesDeclared locks the wire-format spelling of every
// EventLane* constant so a future refactor cannot silently rename one.
// Surfaces (TUI, web UI, Tauri host, MCP clients) match on these
// strings; renaming a literal is a wire-protocol break per spec §5.6.
func TestLaneEventTypesDeclared(t *testing.T) {
	t.Parallel()
	cases := map[EventType]string{
		EventLaneCreated: "lane.created",
		EventLaneStatus:  "lane.status",
		EventLaneDelta:   "lane.delta",
		EventLaneCost:    "lane.cost",
		EventLaneNote:    "lane.note",
		EventLaneKilled:  "lane.killed",
	}
	for et, want := range cases {
		if string(et) != want {
			t.Errorf("EventType %q does not match spec literal %q", string(et), want)
		}
	}
}

// TestLaneEventJSONShape exercises the LaneEvent payload through encoding/json
// and asserts that omitempty-tagged fields drop out when zero, and that the
// JSON keys match specs/lanes-protocol.md §4 verbatim.
func TestLaneEventJSONShape(t *testing.T) {
	t.Parallel()

	// Minimal lane.created. Only required fields plus a kind/parent/label.
	startedAt := time.Date(2026, 5, 2, 18, 33, 21, 482000000, time.UTC)
	created := &LaneEvent{
		LaneID:    "lane_01J0K3M4",
		SessionID: "sess_01J0K3M4",
		Seq:       142,
		Kind:      LaneKindLobe,
		ParentID:  "lane_01J0K3M3",
		Label:     "Recalling memories",
		LobeName:  "MemoryRecallLobe",
		StartedAt: &startedAt,
	}
	b, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal lane.created: %v", err)
	}
	got := string(b)

	// Required fields appear.
	for _, key := range []string{
		`"lane_id":"lane_01J0K3M4"`,
		`"session_id":"sess_01J0K3M4"`,
		`"seq":142`,
		`"kind":"lobe"`,
		`"parent_id":"lane_01J0K3M3"`,
		`"label":"Recalling memories"`,
		`"lobe_name":"MemoryRecallLobe"`,
		`"started_at":"2026-05-02T18:33:21`,
	} {
		if !strings.Contains(got, key) {
			t.Errorf("expected key %s in JSON; got: %s", key, got)
		}
	}
	// Zero-valued optional fields must NOT appear.
	for _, key := range []string{
		`"status"`,
		`"reason"`,
		`"reason_code"`,
		`"prev_status"`,
		`"pinned"`,
		`"delta_seq"`,
		`"content_block"`,
		`"tokens_in"`,
		`"tokens_out"`,
		`"usd"`,
		`"note_id"`,
		`"actor"`,
	} {
		if strings.Contains(got, key) {
			t.Errorf("did not expect key %s in zero-field JSON; got: %s", key, got)
		}
	}
}

// TestEventLaneFieldRoundtrip asserts the new Lane pointer field on Event
// round-trips through json.Marshal/Unmarshal. This locks in the wire
// envelope for all six EventLane* events (item 2 of the implementation
// checklist).
func TestEventLaneFieldRoundtrip(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 2, 18, 33, 21, 0, time.UTC)
	in := &Event{
		ID:        "evt_1",
		Type:      EventLaneStatus,
		Timestamp: now,
		Lane: &LaneEvent{
			LaneID:     "lane_1",
			SessionID:  "sess_1",
			Seq:        7,
			Status:     LaneStatusRunning,
			PrevStatus: LaneStatusPending,
			Reason:     "started",
			ReasonCode: "started",
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if !strings.Contains(string(b), `"lane":{`) {
		t.Fatalf("expected nested lane object in JSON; got: %s", string(b))
	}
	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if out.Lane == nil {
		t.Fatalf("Lane pointer dropped in roundtrip")
	}
	if out.Lane.LaneID != in.Lane.LaneID || out.Lane.Status != in.Lane.Status || out.Lane.Seq != in.Lane.Seq {
		t.Errorf("LaneEvent payload mismatch after roundtrip: got %+v want %+v", *out.Lane, *in.Lane)
	}
}

// TestLaneEventDeltaBlock asserts the LaneContentBlock pointer and its JSON
// key (`content_block`) match spec §4.3 verbatim.
func TestLaneEventDeltaBlock(t *testing.T) {
	t.Parallel()
	ev := &LaneEvent{
		LaneID:   "lane_1",
		DeltaSeq: 7,
		Block: &LaneContentBlock{
			Type: "text_delta",
			Text: "hello world",
		},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"content_block":{"type":"text_delta","text":"hello world"}`) {
		t.Errorf("content_block shape mismatch; got: %s", got)
	}
	if !strings.Contains(got, `"delta_seq":7`) {
		t.Errorf("delta_seq missing; got: %s", got)
	}
}
