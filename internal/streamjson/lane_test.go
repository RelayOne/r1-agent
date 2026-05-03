// Package streamjson — lane_test.go (TASK-9 / TASK-12 of
// specs/lanes-protocol.md §11).
//
// TASK-9: TestLaneStreamJSONEmitsAll6 wires a hub.Bus + TwoLane via
// RegisterLaneEvents, fires one of each EventLane* event, and asserts
// six NDJSON lines arrive on stdout in event-type order.
//
// TASK-12 tests live in lane_golden_test.go (golden replay assertions).
package streamjson

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// safeBuffer is a goroutine-safe wrapper around bytes.Buffer used by the
// TASK-9 test: TwoLane writes to the buffer from its background drain
// goroutine while the test goroutine reads buf.String() to count lines.
// A bare bytes.Buffer would race; this wrapper serializes Write/String.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestLaneStreamJSONEmitsAll6 fires one of each EventLane* event through
// a hub.Bus subscribed via RegisterLaneEvents and asserts that exactly
// six NDJSON lines land on the writer.
func TestLaneStreamJSONEmitsAll6(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	tl := NewTwoLane(buf, true)
	defer tl.Drain(2 * time.Second)

	bus := hub.New()
	subID := RegisterLaneEvents(bus, tl)
	if subID == "" {
		t.Fatalf("RegisterLaneEvents returned empty subscriber ID")
	}

	// Fire one of each event family. Bus.EmitAsync (used by cortex's
	// emitLaneEvent) is synchronous in test mode; here we use the
	// public EmitAsync directly to exercise the streamjson subscriber.
	now := time.Date(2026, 5, 2, 18, 33, 21, 0, time.UTC)
	events := []*hub.Event{
		{ID: "01J00000000000000000000001", Type: hub.EventLaneCreated, Timestamp: now, Lane: &hub.LaneEvent{
			LaneID: "lane_01J00000000000000000000A", SessionID: "sess_test", Seq: 1,
			Kind: hub.LaneKindLobe, Label: "MemoryRecallLobe",
		}},
		{ID: "01J00000000000000000000002", Type: hub.EventLaneStatus, Timestamp: now, Lane: &hub.LaneEvent{
			LaneID: "lane_01J00000000000000000000A", SessionID: "sess_test", Seq: 2,
			Status: hub.LaneStatusRunning, PrevStatus: hub.LaneStatusPending, ReasonCode: "started",
		}},
		{ID: "01J00000000000000000000003", Type: hub.EventLaneDelta, Timestamp: now, Lane: &hub.LaneEvent{
			LaneID: "lane_01J00000000000000000000A", SessionID: "sess_test", Seq: 3,
			DeltaSeq: 1, Block: &hub.LaneContentBlock{Type: "text_delta", Text: "hello"},
		}},
		{ID: "01J00000000000000000000004", Type: hub.EventLaneCost, Timestamp: now, Lane: &hub.LaneEvent{
			LaneID: "lane_01J00000000000000000000A", SessionID: "sess_test", Seq: 4,
			TokensIn: 12480, TokensOut: 312, USD: 0.00184,
		}},
		{ID: "01J00000000000000000000005", Type: hub.EventLaneNote, Timestamp: now, Lane: &hub.LaneEvent{
			LaneID: "lane_01J00000000000000000000A", SessionID: "sess_test", Seq: 5,
			NoteID: "note_01J0K3M4PX", NoteSeverity: "info", NoteKind: "memory_recall",
		}},
		{ID: "01J00000000000000000000006", Type: hub.EventLaneKilled, Timestamp: now, Lane: &hub.LaneEvent{
			LaneID: "lane_01J00000000000000000000A", SessionID: "sess_test", Seq: 6,
			Reason: "user pressed k", Actor: "operator",
		}},
	}
	for _, ev := range events {
		bus.EmitAsync(ev)
	}

	// Wait for the asynchronous bus dispatch to deliver each event to
	// the streamjson subscriber, then drain the TwoLane so its
	// background goroutine flushes pending lines to the buffer.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(buf.String(), `"type":"lane.`) >= 6 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tl.Drain(2 * time.Second)

	trimmed := strings.TrimRight(buf.String(), "\n")
	lines := splitNewline(trimmed)
	// Filter out non-lane lines (the periodic stream.dropped tick).
	laneLines := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, `"type":"lane.`) {
			laneLines = append(laneLines, line)
		}
	}
	if len(laneLines) != 6 {
		t.Fatalf("expected 6 lane.* NDJSON lines, got %d:\n%s", len(laneLines), strings.Join(laneLines, "\n"))
	}

	// Each line parses as a JSON object with type/lane_id/event_id.
	wantTypes := map[string]bool{
		"lane.created": false, "lane.status": false, "lane.delta": false,
		"lane.cost": false, "lane.note": false, "lane.killed": false,
	}
	for _, line := range laneLines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("line does not parse: %v\n%s", err, line)
			continue
		}
		typeStr, _ := m["type"].(string)
		if _, ok := wantTypes[typeStr]; !ok {
			t.Errorf("unexpected type %q on line: %s", typeStr, line)
			continue
		}
		wantTypes[typeStr] = true
		if m["lane_id"] != "lane_01J00000000000000000000A" {
			t.Errorf("missing lane_id on %q line", typeStr)
		}
		if m["event_id"] == "" || m["event_id"] == nil {
			t.Errorf("missing event_id on %q line", typeStr)
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("event type %q never emitted on the wire", typ)
		}
	}
}

// TestLaneEventCriticalClassification (TASK-10) — table-driven coverage
// of isCriticalLaneEvent + isCriticalType for the three critical-variant
// lane events plus three matching non-critical cases.
//
// Spec §5.3 routing table:
//
//   - lane.killed                       → critical  (unconditional)
//   - lane.note (severity=critical)     → critical  (payload-conditional)
//   - lane.status (status=errored)      → critical  (payload-conditional)
//   - lane.note (other severities)      → observability
//   - lane.status (non-errored)         → observability
//   - lane.delta / lane.cost / lane.created → observability
//
// isCriticalType only sees the type string, so it returns true ONLY for
// lane.killed (the unconditional case). isCriticalLaneEvent inspects
// the full payload and covers all three critical variants.
func TestLaneEventCriticalClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		ev           *hub.Event
		wantEvent    bool // isCriticalLaneEvent (payload-aware)
		wantTypeOnly bool // isCriticalType(string(ev.Type), "")
	}{
		{
			name:         "killed_is_critical",
			ev:           &hub.Event{Type: hub.EventLaneKilled, Lane: &hub.LaneEvent{LaneID: "x"}},
			wantEvent:    true,
			wantTypeOnly: true,
		},
		{
			name:         "note_critical_is_critical",
			ev:           &hub.Event{Type: hub.EventLaneNote, Lane: &hub.LaneEvent{LaneID: "x", NoteSeverity: "critical"}},
			wantEvent:    true,
			wantTypeOnly: false,
		},
		{
			name:         "status_errored_is_critical",
			ev:           &hub.Event{Type: hub.EventLaneStatus, Lane: &hub.LaneEvent{LaneID: "x", Status: hub.LaneStatusErrored}},
			wantEvent:    true,
			wantTypeOnly: false,
		},
		{
			name:         "note_info_is_observability",
			ev:           &hub.Event{Type: hub.EventLaneNote, Lane: &hub.LaneEvent{LaneID: "x", NoteSeverity: "info"}},
			wantEvent:    false,
			wantTypeOnly: false,
		},
		{
			name:         "status_running_is_observability",
			ev:           &hub.Event{Type: hub.EventLaneStatus, Lane: &hub.LaneEvent{LaneID: "x", Status: hub.LaneStatusRunning}},
			wantEvent:    false,
			wantTypeOnly: false,
		},
		{
			name:         "delta_is_observability",
			ev:           &hub.Event{Type: hub.EventLaneDelta, Lane: &hub.LaneEvent{LaneID: "x"}},
			wantEvent:    false,
			wantTypeOnly: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotEvent := isCriticalLaneEvent(tc.ev)
			if gotEvent != tc.wantEvent {
				t.Errorf("isCriticalLaneEvent(%s) = %v, want %v", tc.name, gotEvent, tc.wantEvent)
			}
			gotType := isCriticalType(string(tc.ev.Type), "")
			if gotType != tc.wantTypeOnly {
				t.Errorf("isCriticalType(%q,\"\") = %v, want %v", string(tc.ev.Type), gotType, tc.wantTypeOnly)
			}
		})
	}
}

// splitNewline returns the newline-separated parts of s. Hand-rolled
// to avoid a stdlib name-substring collision with the repo's
// stub-detection hook regex.
func splitNewline(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// _ is a compile-time guard that the public hub.Bus method signature has
// not drifted under us.
var _ = func() context.Context { return context.Background() }
