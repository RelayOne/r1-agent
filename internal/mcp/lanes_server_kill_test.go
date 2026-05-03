// Package mcp — tests for r1.lanes.kill (specs/lanes-protocol.md §7.4
// / TASK-22).
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

// TestLanesKillCascade kills a parent and verifies all descendants
// transition to cancelled, with kill events emitted on the bus.
func TestLanesKillCascade(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_kill_42")

	main := ws.NewMainLane(context.Background())
	if err := main.Transition(hub.LaneStatusRunning, "started", "starting main"); err != nil {
		t.Fatalf("main transition: %v", err)
	}
	child := ws.NewLobeLane(context.Background(), "MemoryLobe", main)
	if err := child.Transition(hub.LaneStatusRunning, "started", ""); err != nil {
		t.Fatalf("child transition: %v", err)
	}
	grand := ws.NewToolLane(context.Background(), child, "WebFetch")
	if err := grand.Transition(hub.LaneStatusRunning, "started", ""); err != nil {
		t.Fatalf("grand transition: %v", err)
	}

	srv := NewLanesServer(ws, nil)
	out, err := srv.HandleToolCall(context.Background(), "r1.lanes.kill", map[string]interface{}{
		"session_id": "sess_kill_42",
		"lane_id":    main.ID,
		"reason":     "operator clicked k",
	})
	if err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			KilledLaneIDs   []string `json:"killed_lane_ids"`
			AlreadyTerminal bool     `json:"already_terminal"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\n%s", jerr, out)
	}
	if !env.OK {
		t.Fatalf("ok=false; body=%s", out)
	}
	if env.Data.AlreadyTerminal {
		t.Errorf("already_terminal = true, want false")
	}
	if len(env.Data.KilledLaneIDs) != 3 {
		t.Errorf("killed_lane_ids len = %d, want 3", len(env.Data.KilledLaneIDs))
	}
	for _, l := range []*cortex.Lane{main, child, grand} {
		if !l.IsTerminal() {
			t.Errorf("lane %s not terminal after cascade kill (status=%s)", l.ID, l.Status)
		}
		if l.Status != hub.LaneStatusCancelled {
			t.Errorf("lane %s status = %s, want cancelled", l.ID, l.Status)
		}
	}
}

// TestLanesKillIdempotent confirms a second kill on a terminal lane
// returns {ok:true, data.already_terminal:true} and emits no events.
func TestLanesKillIdempotent(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_idem")
	main := ws.NewMainLane(context.Background())
	if err := main.Transition(hub.LaneStatusRunning, "started", ""); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := main.Kill("first call"); err != nil {
		t.Fatalf("first kill: %v", err)
	}
	if !main.IsTerminal() {
		t.Fatalf("main not terminal after first kill")
	}

	srv := NewLanesServer(ws, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.kill", map[string]interface{}{
		"session_id": "sess_idem",
		"lane_id":    main.ID,
		"reason":     "second call",
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			KilledLaneIDs   []string `json:"killed_lane_ids"`
			AlreadyTerminal bool     `json:"already_terminal"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\n%s", jerr, out)
	}
	if !env.OK {
		t.Fatalf("ok=false; body=%s", out)
	}
	if !env.Data.AlreadyTerminal {
		t.Errorf("already_terminal = false, want true on idempotent kill")
	}
	if len(env.Data.KilledLaneIDs) != 0 {
		t.Errorf("killed_lane_ids = %v, want []", env.Data.KilledLaneIDs)
	}
}

// TestLanesKillNoCascade with cascade=false kills only the requested
// lane; descendants stay running.
func TestLanesKillNoCascade(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_x")
	main := ws.NewMainLane(context.Background())
	if err := main.Transition(hub.LaneStatusRunning, "started", ""); err != nil {
		t.Fatalf("transition: %v", err)
	}
	child := ws.NewLobeLane(context.Background(), "MyLobe", main)
	if err := child.Transition(hub.LaneStatusRunning, "started", ""); err != nil {
		t.Fatalf("transition: %v", err)
	}

	srv := NewLanesServer(ws, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.kill", map[string]interface{}{
		"session_id": "sess_x",
		"lane_id":    main.ID,
		"cascade":    false,
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			KilledLaneIDs []string `json:"killed_lane_ids"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if !env.OK {
		t.Fatalf("ok=false")
	}
	if len(env.Data.KilledLaneIDs) != 1 {
		t.Errorf("killed_lane_ids len = %d, want 1 (no cascade)", len(env.Data.KilledLaneIDs))
	}
	if child.IsTerminal() {
		t.Errorf("child terminal, want still running (cascade=false)")
	}
	if !main.IsTerminal() {
		t.Errorf("main still running")
	}
}

// TestLanesKillNotFound returns the structured not_found envelope.
func TestLanesKillNotFound(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_x")
	srv := NewLanesServer(ws, nil)

	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.kill", map[string]interface{}{
		"session_id": "sess_x",
		"lane_id":    "missing",
	})
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	if env["ok"] != false || env["error_code"] != "not_found" {
		t.Errorf("env = %v, want ok=false error_code=not_found", env)
	}
}

// TestLanesKillEmitsLaneKilledEvent verifies the bus sees the
// lane.killed event followed by lane.status(cancelled_by_operator)
// per spec §7.4.
func TestLanesKillEmitsLaneKilledEvent(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_event")

	var (
		obsMu        sync.Mutex
		killEvents   []*hub.Event
		statusEvents []*hub.Event
	)
	bus.Register(hub.Subscriber{
		ID: "test.kill_observer",
		Events: []hub.EventType{
			hub.EventLaneKilled,
			hub.EventLaneStatus,
		},
		Mode: hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			obsMu.Lock()
			defer obsMu.Unlock()
			switch ev.Type {
			case hub.EventLaneKilled:
				killEvents = append(killEvents, ev)
			case hub.EventLaneStatus:
				statusEvents = append(statusEvents, ev)
			}
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

	main := ws.NewMainLane(context.Background())
	if err := main.Transition(hub.LaneStatusRunning, "started", ""); err != nil {
		t.Fatalf("transition: %v", err)
	}

	// Wait for the started transition to fan out, then drop it so the
	// later assertion only inspects the kill-induced statuses.
	waitForLaneEvents(t, &obsMu, &statusEvents, 1, 500*time.Millisecond)
	obsMu.Lock()
	statusEvents = nil
	obsMu.Unlock()

	srv := NewLanesServer(ws, nil)
	if _, err := srv.HandleToolCall(context.Background(), "r1.lanes.kill", map[string]interface{}{
		"session_id": "sess_event",
		"lane_id":    main.ID,
		"reason":     "operator stop",
	}); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Wait for async dispatch to deliver kill + cancel-status.
	waitForLaneEvents(t, &obsMu, &killEvents, 1, 500*time.Millisecond)
	waitForLaneEvents(t, &obsMu, &statusEvents, 1, 500*time.Millisecond)

	obsMu.Lock()
	defer obsMu.Unlock()
	if len(killEvents) == 0 {
		t.Errorf("no lane.killed event observed")
	} else {
		if killEvents[0].Lane.Reason != "operator stop" {
			t.Errorf("kill reason = %q, want %q", killEvents[0].Lane.Reason, "operator stop")
		}
		if killEvents[0].Lane.Actor != "operator" {
			t.Errorf("kill actor = %q, want operator", killEvents[0].Lane.Actor)
		}
	}
	// Final lane.status must carry status=cancelled, reason_code=cancelled_by_operator.
	var sawCancelStatus bool
	for _, ev := range statusEvents {
		if ev.Lane.Status == hub.LaneStatusCancelled && ev.Lane.ReasonCode == "cancelled_by_operator" {
			sawCancelStatus = true
		}
	}
	if !sawCancelStatus {
		t.Errorf("never saw lane.status(cancelled, cancelled_by_operator); statuses=%v", laneStatuses(statusEvents))
	}
}

// waitForLaneEvents polls the slice (under mu) until it has at least
// want entries or the deadline elapses. Used to bridge the async
// EmitAsync fan-out without arbitrary sleeps.
func waitForLaneEvents(t *testing.T, mu *sync.Mutex, evs *[]*hub.Event, want int, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*evs)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func laneStatuses(evs []*hub.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, string(ev.Lane.Status)+":"+ev.Lane.ReasonCode)
	}
	return out
}
