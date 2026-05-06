package streamjson

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// BenchmarkLaneEventEndToEnd measures the per-event cost from hub publish
// through the streamjson adapter to a subscriber-observed line.
//
// Spec: specs/lanes-protocol.md item 39.
//
// Performance budget per spec §"Performance budget":
//
//	At 100 events/sec sustained for 60 s, MCP r1.lanes.subscribe consumer
//	end-to-end p99 latency <= 150 ms; CPU overhead of the lane bridge
//	<= 3% of one core. This benchmark establishes the per-event hot-path cost.
//
// Targets (observability, NOT a CI gate):
//
//	BenchmarkLaneEventEndToEnd: target <= 50 us/event (single goroutine)
//	BenchmarkLaneEventFanOut:   target <= 100 us/event (5 subscribers)
//
// Run locally:
//
//	go test -bench=BenchmarkLane -benchtime=10s ./internal/streamjson/
//
// Compare against the targets; if drift > 2x on consistent hardware, audit
// the lane adapter (mutex granularity, JSON marshal cost, hub fan-out).
func BenchmarkLaneEventEndToEnd(b *testing.B) {
	bus := hub.New()

	var sink bytes.Buffer
	tl := NewTwoLane(&sink, true)
	_ = RegisterLaneEvents(bus, tl)

	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("bench-session")
	lane := ws.NewMainLane(context.Background())
	_ = lane.Transition(hub.LaneStatusRunning, "started", "started")

	block := agentloop.ContentBlock{Type: "text_delta", Text: "x"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lane.EmitDelta(block)
	}
	b.StopTimer()

	time.Sleep(5 * time.Millisecond)
}

// BenchmarkLaneEventFanOut measures cost when 5 hub subscribers observe each
// lane event (mimics: TUI + web + desktop + MCP + WAL keeper).
func BenchmarkLaneEventFanOut(b *testing.B) {
	bus := hub.New()

	var observed atomic.Uint64
	for i := 0; i < 5; i++ {
		id := "bench-sub-" + string(rune('a'+i))
		bus.Register(hub.Subscriber{
			ID:     id,
			Events: []hub.EventType{hub.EventLaneDelta},
			Mode:   hub.ModeObserve,
			Handler: func(ctx context.Context, evt *hub.Event) *hub.HookResponse {
				observed.Add(1)
				return nil
			},
		})
	}

	var sink bytes.Buffer
	tl := NewTwoLane(&sink, true)
	_ = RegisterLaneEvents(bus, tl)

	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("bench-fanout")
	lane := ws.NewMainLane(context.Background())
	_ = lane.Transition(hub.LaneStatusRunning, "started", "started")

	block := agentloop.ContentBlock{Type: "text_delta", Text: "x"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lane.EmitDelta(block)
	}
	b.StopTimer()

	time.Sleep(20 * time.Millisecond)
}
