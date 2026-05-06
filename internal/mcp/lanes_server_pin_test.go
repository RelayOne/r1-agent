// Package mcp — tests for r1.lanes.pin (specs/lanes-protocol.md §7.5
// / TASK-23).
package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// TestLanesPinSetsFlag verifies r1.lanes.pin toggles Lane.Pinned.
func TestLanesPinSetsFlag(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_pin")
	main := ws.NewMainLane(context.Background())
	if main.Pinned {
		t.Fatalf("new lane should not be pinned")
	}

	srv := NewLanesServer(ws, nil)
	out, err := srv.HandleToolCall(context.Background(), "r1.lanes.pin", map[string]interface{}{
		"session_id": "sess_pin",
		"lane_id":    main.ID,
		"pinned":     true,
	})
	if err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			LaneID string `json:"lane_id"`
			Pinned bool   `json:"pinned"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\n%s", jerr, out)
	}
	if !env.OK {
		t.Fatalf("ok = false; body=%s", out)
	}
	if env.Data.LaneID != main.ID || env.Data.Pinned != true {
		t.Errorf("data = %+v, want lane_id=%s pinned=true", env.Data, main.ID)
	}
	got, _ := ws.GetLane(main.ID)
	if !got.Pinned {
		t.Errorf("Lane.Pinned not set on canonical record")
	}
}

// TestLanesPinClearsFlag flips a pinned lane back to unpinned.
func TestLanesPinClearsFlag(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_pin")
	main := ws.NewMainLane(context.Background())
	main.SetPinned(true)

	srv := NewLanesServer(ws, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.pin", map[string]interface{}{
		"session_id": "sess_pin",
		"lane_id":    main.ID,
		"pinned":     false,
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Pinned bool `json:"pinned"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if !env.OK || env.Data.Pinned != false {
		t.Errorf("env=%+v body=%s", env, out)
	}
	got, _ := ws.GetLane(main.ID)
	if got.Pinned {
		t.Errorf("Lane.Pinned still true after clear")
	}
}

// TestLanesPinIdempotent setting pinned to its current value is a
// no-op (no error, no event, response still ok:true).
func TestLanesPinIdempotent(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_pin")
	main := ws.NewMainLane(context.Background())
	main.SetPinned(true)

	srv := NewLanesServer(ws, nil)
	for i := 0; i < 3; i++ {
		out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.pin", map[string]interface{}{
			"session_id": "sess_pin",
			"lane_id":    main.ID,
			"pinned":     true,
		})
		var env map[string]any
		_ = json.Unmarshal([]byte(out), &env)
		if env["ok"] != true {
			t.Errorf("iteration %d: ok=%v, want true", i, env["ok"])
		}
	}
	got, _ := ws.GetLane(main.ID)
	if !got.Pinned {
		t.Errorf("Lane.Pinned changed across idempotent calls")
	}
}

// TestLanesPinEmitsNoEvent verifies spec §7.5: pin MUST NOT emit a
// hub event. Surfaces re-fetch via lanes.list.
func TestLanesPinEmitsNoEvent(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_pin")
	main := ws.NewMainLane(context.Background())

	var (
		mu     sync.Mutex
		events []*hub.Event
	)
	bus.Register(hub.Subscriber{
		ID: "test.pin_observer",
		Events: []hub.EventType{
			hub.EventLaneCreated,
			hub.EventLaneStatus,
			hub.EventLaneDelta,
			hub.EventLaneCost,
			hub.EventLaneNote,
			hub.EventLaneKilled,
		},
		Mode: hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

	// Allow the lane.created from NewMainLane to settle, then drop it
	// so we observe only the events the pin call produces (which
	// should be ZERO).
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	events = nil
	mu.Unlock()

	srv := NewLanesServer(ws, nil)
	if _, err := srv.HandleToolCall(context.Background(), "r1.lanes.pin", map[string]interface{}{
		"session_id": "sess_pin",
		"lane_id":    main.ID,
		"pinned":     true,
	}); err != nil {
		t.Fatalf("pin: %v", err)
	}

	// Wait long enough to be confident no async fan-out fires.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Errorf("pin emitted %d events, want 0; events=%+v", len(events), events)
	}
}

// TestLanesPinNotFound returns the spec §7.5 error_code enum value
// "not_found" when the lane is missing.
func TestLanesPinNotFound(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_pin")
	srv := NewLanesServer(ws, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.pin", map[string]interface{}{
		"session_id": "sess_pin",
		"lane_id":    "missing",
		"pinned":     true,
	})
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	if env["ok"] != false || env["error_code"] != "not_found" {
		t.Errorf("env = %v, want ok=false error_code=not_found", env)
	}
}

// TestLanesPinValidationError surfaces "internal" (the spec §7.5
// error_code enum is [not_found, internal] so validation issues fall
// under internal).
func TestLanesPinValidationError(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_pin")
	main := ws.NewMainLane(context.Background())
	srv := NewLanesServer(ws, nil)
	// Missing pinned field.
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.pin", map[string]interface{}{
		"session_id": "sess_pin",
		"lane_id":    main.ID,
	})
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	if env["ok"] != false {
		t.Errorf("ok = %v, want false", env["ok"])
	}
	if env["error_code"] != "internal" && env["error_code"] != "not_found" {
		t.Errorf("error_code = %v, want one of [internal, not_found]", env["error_code"])
	}
}
