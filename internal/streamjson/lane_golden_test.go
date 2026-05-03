// Package streamjson — lane_golden_test.go (TASK-12 of
// specs/lanes-protocol.md §11).
//
// Golden replay assertions for the six lane events in
// internal/streamjson/testdata/lanes/. Each fixture file is one §4
// event in canonical JSON form; this file asserts:
//
//   1. Each fixture loads + parses cleanly as a JSON object.
//   2. Round-trip fidelity: fixture → struct → marshal → equal-by-key
//      to original (json.RawMessage round-trip + DeepEqual on the
//      decoded map).
//   3. Critical classification: each critical-variant fixture (per
//      spec §5.3) produces isCriticalLaneEvent=true; non-critical
//      fixtures produce false.
//   4. Hub→stream emit: feed a synthetic hub.Event matching each
//      fixture; assert the streamjson NDJSON output carries the same
//      type / lane_id / event_id / session_id / seq.
package streamjson

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// laneFixture pairs a fixture file path with the synthetic hub.Event
// the streamjson subscriber should produce when it sees the matching
// LaneEvent payload.
type laneFixture struct {
	file       string
	eventType  hub.EventType
	wantCrit   bool // expected isCriticalLaneEvent result
	buildEvent func(t *testing.T, parsed map[string]any) *hub.Event
}

// loadFixture reads the JSON document at testdata/lanes/<name> and
// parses it into a map. Failures fail the test fatally so callers do
// not have to nil-check.
func loadFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	p := filepath.Join("testdata", "lanes", name)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %q: %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse fixture %q: %v", p, err)
	}
	return m
}

// fixtures returns the table of golden fixtures + the builder that
// turns each into a synthetic hub.Event for the emit-path test.
func fixtures() []laneFixture {
	return []laneFixture{
		{
			file: "lane.created.json", eventType: hub.EventLaneCreated, wantCrit: false,
			buildEvent: func(t *testing.T, p map[string]any) *hub.Event {
				return &hub.Event{
					ID:        p["event_id"].(string),
					Type:      hub.EventLaneCreated,
					Timestamp: parseAt(t, p["at"].(string)),
					Lane: &hub.LaneEvent{
						LaneID:    p["lane_id"].(string),
						SessionID: p["session_id"].(string),
						Seq:       uint64(p["seq"].(float64)),
						Kind:      hub.LaneKindLobe,
						LobeName:  "MemoryRecallLobe",
						ParentID:  "lane_01J0K3M3",
						Label:     "Recalling memories matching: 'cortex workspace'",
					},
				}
			},
		},
		{
			file: "lane.status.json", eventType: hub.EventLaneStatus, wantCrit: false,
			buildEvent: func(t *testing.T, p map[string]any) *hub.Event {
				return &hub.Event{
					ID:        p["event_id"].(string),
					Type:      hub.EventLaneStatus,
					Timestamp: parseAt(t, p["at"].(string)),
					Lane: &hub.LaneEvent{
						LaneID:     p["lane_id"].(string),
						SessionID:  p["session_id"].(string),
						Seq:        uint64(p["seq"].(float64)),
						Status:     hub.LaneStatusRunning,
						PrevStatus: hub.LaneStatusPending,
						Reason:     "started",
						ReasonCode: "started",
					},
				}
			},
		},
		{
			file: "lane.delta.json", eventType: hub.EventLaneDelta, wantCrit: false,
			buildEvent: func(t *testing.T, p map[string]any) *hub.Event {
				return &hub.Event{
					ID:        p["event_id"].(string),
					Type:      hub.EventLaneDelta,
					Timestamp: parseAt(t, p["at"].(string)),
					Lane: &hub.LaneEvent{
						LaneID:    p["lane_id"].(string),
						SessionID: p["session_id"].(string),
						Seq:       uint64(p["seq"].(float64)),
						DeltaSeq:  7,
						Block: &hub.LaneContentBlock{
							Type: "text_delta",
							Text: "found 3 matching memories: ",
						},
					},
				}
			},
		},
		{
			file: "lane.cost.json", eventType: hub.EventLaneCost, wantCrit: false,
			buildEvent: func(t *testing.T, p map[string]any) *hub.Event {
				return &hub.Event{
					ID:        p["event_id"].(string),
					Type:      hub.EventLaneCost,
					Timestamp: parseAt(t, p["at"].(string)),
					Lane: &hub.LaneEvent{
						LaneID:        p["lane_id"].(string),
						SessionID:     p["session_id"].(string),
						Seq:           uint64(p["seq"].(float64)),
						TokensIn:      12480,
						TokensOut:     312,
						CachedTokens:  11200,
						USD:           0.00184,
						CumulativeUSD: 0.00521,
					},
				}
			},
		},
		{
			file: "lane.note.json", eventType: hub.EventLaneNote, wantCrit: true,
			buildEvent: func(t *testing.T, p map[string]any) *hub.Event {
				return &hub.Event{
					ID:        p["event_id"].(string),
					Type:      hub.EventLaneNote,
					Timestamp: parseAt(t, p["at"].(string)),
					Lane: &hub.LaneEvent{
						LaneID:       p["lane_id"].(string),
						SessionID:    p["session_id"].(string),
						Seq:          uint64(p["seq"].(float64)),
						NoteID:       "note_01J0K3M4PX",
						NoteSeverity: "critical",
						NoteKind:     "memory_recall",
						NoteSummary:  "3 prior decisions referenced this Workspace shape",
					},
				}
			},
		},
		{
			file: "lane.killed.json", eventType: hub.EventLaneKilled, wantCrit: true,
			buildEvent: func(t *testing.T, p map[string]any) *hub.Event {
				return &hub.Event{
					ID:        p["event_id"].(string),
					Type:      hub.EventLaneKilled,
					Timestamp: parseAt(t, p["at"].(string)),
					Lane: &hub.LaneEvent{
						LaneID:    p["lane_id"].(string),
						SessionID: p["session_id"].(string),
						Seq:       uint64(p["seq"].(float64)),
						Reason:    "cancelled_by_operator",
						Actor:     "operator",
						ActorID:   "user_01J0K",
					},
				}
			},
		},
	}
}

// parseAt parses an RFC 3339 timestamp from a fixture; failures fail
// the test fatally because we control the fixture contents.
func parseAt(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse at %q: %v", s, err)
	}
	return tt
}

// TestLaneGoldenFixturesParse asserts every fixture loads + parses as a
// JSON object with the six required top-level keys (event, event_id,
// session_id, seq, at, lane_id, data).
func TestLaneGoldenFixturesParse(t *testing.T) {
	t.Parallel()
	required := []string{"event", "event_id", "session_id", "seq", "at", "lane_id", "data"}
	for _, fx := range fixtures() {
		t.Run(fx.file, func(t *testing.T) {
			m := loadFixture(t, fx.file)
			for _, key := range required {
				if _, ok := m[key]; !ok {
					t.Errorf("fixture %q missing required key %q", fx.file, key)
				}
			}
			if got := m["event"]; got != string(fx.eventType) {
				t.Errorf("fixture %q event = %v, want %q", fx.file, got, string(fx.eventType))
			}
		})
	}
}

// TestLaneGoldenRoundTrip asserts each fixture can be remarshaled and
// the round-trip preserves shape: parse → reparse from re-emitted JSON
// → equal-by-key. This catches accidental field drift in the fixtures
// themselves.
func TestLaneGoldenRoundTrip(t *testing.T) {
	t.Parallel()
	for _, fx := range fixtures() {
		t.Run(fx.file, func(t *testing.T) {
			original := loadFixture(t, fx.file)
			re, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			var roundtrip map[string]any
			if err := json.Unmarshal(re, &roundtrip); err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			if !reflect.DeepEqual(original, roundtrip) {
				t.Errorf("round-trip diff:\noriginal: %#v\nroundtrip: %#v", original, roundtrip)
			}
		})
	}
}

// TestLaneGoldenCriticalClassification asserts each fixture's
// synthesized hub.Event yields the correct isCriticalLaneEvent verdict
// per spec §5.3. lane.killed and lane.note(severity=critical) must be
// critical; the rest must be observability.
func TestLaneGoldenCriticalClassification(t *testing.T) {
	t.Parallel()
	for _, fx := range fixtures() {
		t.Run(fx.file, func(t *testing.T) {
			parsed := loadFixture(t, fx.file)
			ev := fx.buildEvent(t, parsed)
			got := isCriticalLaneEvent(ev)
			if got != fx.wantCrit {
				t.Errorf("fixture %q: isCriticalLaneEvent = %v, want %v", fx.file, got, fx.wantCrit)
			}
		})
	}
}

// TestLaneGoldenHubToStream feeds each fixture's synthesized hub.Event
// into RegisterLaneEvents-attached TwoLane and asserts the NDJSON line
// emitted carries the expected type, lane_id, event_id, session_id,
// and seq. This is the end-to-end emit-path golden replay assertion.
func TestLaneGoldenHubToStream(t *testing.T) {
	t.Parallel()
	for _, fx := range fixtures() {
		t.Run(fx.file, func(t *testing.T) {
			parsed := loadFixture(t, fx.file)
			ev := fx.buildEvent(t, parsed)

			buf := &safeBuffer{}
			tl := NewTwoLane(buf, true)
			defer tl.Drain(2 * time.Second)

			bus := hub.New()
			RegisterLaneEvents(bus, tl)
			bus.EmitAsync(ev)

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if bytes.Contains([]byte(buf.String()), []byte(`"type":"`+string(fx.eventType)+`"`)) {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			tl.Drain(2 * time.Second)

			lines := splitNewline(strings.TrimRight(buf.String(), "\n"))
			var matched map[string]any
			for _, line := range lines {
				if line == "" {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					t.Errorf("parse emitted line: %v\n%s", err, line)
					continue
				}
				if m["type"] == string(fx.eventType) {
					matched = m
					break
				}
			}
			if matched == nil {
				t.Fatalf("no NDJSON line of type %q emitted; got:\n%s", fx.eventType, buf.String())
			}
			if matched["lane_id"] != parsed["lane_id"] {
				t.Errorf("lane_id mismatch: got %v, want %v", matched["lane_id"], parsed["lane_id"])
			}
			if matched["event_id"] != parsed["event_id"] {
				t.Errorf("event_id mismatch: got %v, want %v", matched["event_id"], parsed["event_id"])
			}
			if matched["session_id"] != parsed["session_id"] {
				t.Errorf("session_id mismatch: got %v, want %v", matched["session_id"], parsed["session_id"])
			}
			if matched["seq"] != parsed["seq"] {
				t.Errorf("seq mismatch: got %v, want %v", matched["seq"], parsed["seq"])
			}
		})
	}
}
