// Package server — production WAL adapter for the lanes-protocol replay
// path (TASK-16).
//
// The handler uses the LanesWAL interface so it can be tested with
// in-memory fakes (see fakeLanesWAL in lanes_replay_test.go). This file
// supplies the production wiring that bridges *bus.Bus.Replay() to the
// LanesWAL contract:
//
//   - filters by Pattern.TypePrefix = "lane." so only lane events flow;
//   - filters by Lane.SessionID equality (the bus event has no
//     scope.session field, so we filter on the LaneEvent payload);
//   - reports ErrWALTruncatedError when the requested fromSeq is older
//     than the WAL's earliest retained seq.
package server

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
)

// BusWALAdapter wraps *bus.Bus to satisfy LanesWAL.
//
// MinRetainedSeq is the lowest seq currently held in the WAL. When a
// caller asks for fromSeq < MinRetainedSeq the adapter returns
// ErrWALTruncatedError so the SSE handler emits 404 wal_truncated and
// the WS handler emits JSON-RPC -32004 + close 4404 (per spec §6.3).
//
// Callers are responsible for keeping MinRetainedSeq up to date — the
// bus does not currently truncate (a TODO referenced from spec §6.4 but
// out of scope for this task) so today MinRetainedSeq stays at 0 and
// the truncate path is exercised only by tests via fakeLanesWAL.
type BusWALAdapter struct {
	Bus            *bus.Bus
	MinRetainedSeq uint64
}

// NewBusWALAdapter constructs a BusWALAdapter from a *bus.Bus.
func NewBusWALAdapter(b *bus.Bus) *BusWALAdapter {
	return &BusWALAdapter{Bus: b}
}

// ReplayLane satisfies LanesWAL. It walks the bus WAL from fromSeq
// onwards, deserialises each event's Lane payload from the bus event's
// raw payload bytes, filters by sessionID, and forwards matching events
// to handler. Returns ErrWALTruncatedError if fromSeq predates the
// retained window.
func (a *BusWALAdapter) ReplayLane(_ context.Context, sessionID string, fromSeq uint64, handler func(*hub.Event) error) error {
	if a == nil || a.Bus == nil {
		return nil
	}
	if fromSeq > 0 && fromSeq <= a.MinRetainedSeq {
		return &ErrWALTruncatedError{FromSeq: fromSeq}
	}
	pattern := bus.Pattern{TypePrefix: "lane."}
	return a.Bus.Replay(pattern, fromSeq, func(busEvt bus.Event) {
		var lane hub.LaneEvent
		if len(busEvt.Payload) == 0 {
			return
		}
		if err := json.Unmarshal(busEvt.Payload, &lane); err != nil {
			return
		}
		if lane.SessionID != sessionID {
			return
		}
		_ = handler(&hub.Event{
			ID:        busEvt.ID,
			Type:      hub.EventType(busEvt.Type),
			Timestamp: busEvt.Timestamp,
			Lane:      &lane,
		})
	})
}
