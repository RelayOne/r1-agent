// Package server — integration test for BusWALAdapter (TASK-16).
//
// Drives a real *bus.Bus + WAL through the LanesWAL contract to confirm:
//
//   - lane events written to the bus are read back through ReplayLane;
//   - filtering by sessionID works (events for other sessions are not
//     delivered);
//   - fromSeq below MinRetainedSeq returns ErrWALTruncatedError.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
)

func TestBusWALAdapterRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "wal")
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer b.Close()

	// Append four lane events for two different sessions.
	for i := 1; i <= 4; i++ {
		sid := "sess_a"
		if i%2 == 0 {
			sid = "sess_b"
		}
		lane := hub.LaneEvent{
			LaneID:    "lane_" + itoa(i),
			SessionID: sid,
			Seq:       uint64(i),
			Status:    hub.LaneStatusRunning,
		}
		body, _ := json.Marshal(lane)
		if err := b.Publish(bus.Event{
			Type:      bus.EventType("lane.status"),
			Timestamp: time.Now(),
			EmitterID: "test",
			Payload:   body,
		}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	a := NewBusWALAdapter(b)

	// Replay sess_a from seq=1: should get the two odd-numbered events
	// (i=1 and i=3).
	var got []*hub.Event
	if err := a.ReplayLane(context.Background(), "sess_a", 1, func(ev *hub.Event) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatalf("ReplayLane: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	for i, ev := range got {
		if ev.Lane == nil {
			t.Fatalf("event %d has nil Lane", i)
		}
		if ev.Lane.SessionID != "sess_a" {
			t.Errorf("event %d session = %q, want sess_a", i, ev.Lane.SessionID)
		}
		if ev.Type != hub.EventType("lane.status") {
			t.Errorf("event %d type = %q, want lane.status", i, ev.Type)
		}
	}

	// Truncate test: bump MinRetainedSeq past 1, request from 1.
	a.MinRetainedSeq = 2
	err = a.ReplayLane(context.Background(), "sess_a", 1, func(*hub.Event) error { return nil })
	var trunc *ErrWALTruncatedError
	if !errors.As(err, &trunc) {
		t.Fatalf("expected ErrWALTruncatedError, got %v", err)
	}
	if trunc.FromSeq != 1 {
		t.Errorf("trunc.FromSeq = %d, want 1", trunc.FromSeq)
	}
}
