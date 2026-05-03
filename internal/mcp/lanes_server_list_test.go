// Package mcp — tests for r1.lanes.list (specs/lanes-protocol.md §7.1
// / TASK-19).
package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// TestLanesListBasic covers the happy path: a session with three
// lanes returns three entries with correct projection.
func TestLanesListBasic(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_list_42")
	now := time.Now().UTC()
	be.lanes["lane_1"] = &cortex.Lane{
		ID:        "lane_1",
		Kind:      hub.LaneKindMain,
		Label:     "main",
		Status:    hub.LaneStatusRunning,
		StartedAt: now.Add(-3 * time.Second),
		LastSeq:   5,
	}
	be.lanes["lane_2"] = &cortex.Lane{
		ID:        "lane_2",
		Kind:      hub.LaneKindLobe,
		ParentID:  "lane_1",
		Label:     "MemoryRecallLobe",
		Status:    hub.LaneStatusDone,
		StartedAt: now.Add(-2 * time.Second),
		EndedAt:   now.Add(-1 * time.Second),
		LastSeq:   42,
	}
	be.lanes["lane_3"] = &cortex.Lane{
		ID:        "lane_3",
		Kind:      hub.LaneKindTool,
		ParentID:  "lane_1",
		Label:     "WebFetch",
		Status:    hub.LaneStatusRunning,
		Pinned:    true,
		StartedAt: now.Add(-1 * time.Second),
		LastSeq:   100,
	}

	srv := NewLanesServer(be, nil)
	out, err := srv.HandleToolCall(context.Background(), "r1.lanes.list", map[string]interface{}{
		"session_id": "sess_list_42",
	})
	if err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}

	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Lanes []map[string]any `json:"lanes"`
		} `json:"data"`
		ErrorCode string `json:"error_code"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\n%s", jerr, out)
	}
	if !env.OK {
		t.Fatalf("ok = false, error_code=%s, body=%s", env.ErrorCode, out)
	}
	if len(env.Data.Lanes) != 3 {
		t.Fatalf("lanes len = %d, want 3", len(env.Data.Lanes))
	}
	// Order must be by started_at ascending.
	if env.Data.Lanes[0]["lane_id"] != "lane_1" {
		t.Errorf("lanes[0] = %v, want lane_1", env.Data.Lanes[0]["lane_id"])
	}
	if env.Data.Lanes[2]["lane_id"] != "lane_3" {
		t.Errorf("lanes[2] = %v, want lane_3", env.Data.Lanes[2]["lane_id"])
	}
	// Required fields.
	for i, l := range env.Data.Lanes {
		for _, k := range []string{"lane_id", "kind", "status", "started_at"} {
			if _, ok := l[k]; !ok {
				t.Errorf("lanes[%d] missing required field %q", i, k)
			}
		}
	}
	// Lobe lane has lobe_name set per spec §7.1.
	if env.Data.Lanes[1]["lobe_name"] != "MemoryRecallLobe" {
		t.Errorf("lobe lane lobe_name = %v, want MemoryRecallLobe", env.Data.Lanes[1]["lobe_name"])
	}
	// Pinned flag flows through.
	if env.Data.Lanes[2]["pinned"] != true {
		t.Errorf("pinned lane pinned = %v, want true", env.Data.Lanes[2]["pinned"])
	}
}

// TestLanesListIncludeTerminalFalse drops terminal lanes when the
// caller asks to.
func TestLanesListIncludeTerminalFalse(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_x")
	be.lanes["a"] = &cortex.Lane{ID: "a", Kind: hub.LaneKindMain, Status: hub.LaneStatusRunning, StartedAt: time.Now()}
	be.lanes["b"] = &cortex.Lane{ID: "b", Kind: hub.LaneKindLobe, Status: hub.LaneStatusDone, StartedAt: time.Now()}
	be.lanes["c"] = &cortex.Lane{ID: "c", Kind: hub.LaneKindTool, Status: hub.LaneStatusCancelled, StartedAt: time.Now()}

	srv := NewLanesServer(be, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.list", map[string]interface{}{
		"session_id":       "sess_x",
		"include_terminal": false,
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Lanes []map[string]any `json:"lanes"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if len(env.Data.Lanes) != 1 || env.Data.Lanes[0]["lane_id"] != "a" {
		t.Errorf("lanes = %v, want only [a]", env.Data.Lanes)
	}
}

// TestLanesListKindsFilter restricts the result to specific kinds.
func TestLanesListKindsFilter(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_x")
	be.lanes["a"] = &cortex.Lane{ID: "a", Kind: hub.LaneKindMain, Status: hub.LaneStatusRunning, StartedAt: time.Now()}
	be.lanes["b"] = &cortex.Lane{ID: "b", Kind: hub.LaneKindLobe, Status: hub.LaneStatusRunning, StartedAt: time.Now().Add(time.Millisecond)}
	be.lanes["c"] = &cortex.Lane{ID: "c", Kind: hub.LaneKindTool, Status: hub.LaneStatusRunning, StartedAt: time.Now().Add(2 * time.Millisecond)}

	srv := NewLanesServer(be, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.list", map[string]interface{}{
		"session_id": "sess_x",
		"kinds":      []interface{}{"lobe", "tool"},
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Lanes []map[string]any `json:"lanes"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if len(env.Data.Lanes) != 2 {
		t.Errorf("lanes len = %d, want 2", len(env.Data.Lanes))
	}
	for _, l := range env.Data.Lanes {
		k := l["kind"]
		if k != "lobe" && k != "tool" {
			t.Errorf("unexpected kind: %v", k)
		}
	}
}

// TestLanesListMissingSessionID surfaces a structured error envelope.
func TestLanesListMissingSessionID(t *testing.T) {
	t.Parallel()
	srv := NewLanesServer(newFakeLanesBackend("sess_x"), nil)
	out, err := srv.HandleToolCall(context.Background(), "r1.lanes.list", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	if env["ok"] != false || env["error_code"] != "invalid_request" {
		t.Errorf("env = %v, want ok=false error_code=invalid_request", env)
	}
}

// TestLanesListSessionMismatchEmpty returns an empty result when the
// requested session_id doesn't match the backend's (per the implementation
// note: surfaces poll many sessions, treat mismatches as empty).
func TestLanesListSessionMismatchEmpty(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_a")
	be.lanes["x"] = &cortex.Lane{ID: "x", Kind: hub.LaneKindMain, Status: hub.LaneStatusRunning, StartedAt: time.Now()}
	srv := NewLanesServer(be, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.list", map[string]interface{}{
		"session_id": "sess_b",
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Lanes []map[string]any `json:"lanes"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if !env.OK || len(env.Data.Lanes) != 0 {
		t.Errorf("expected empty lanes for session mismatch, got %v", env)
	}
}

// TestWorkspaceLanesAccessor verifies the new accessor on Workspace
// integrates end-to-end with NewMainLane.
func TestWorkspaceLanesAccessor(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_test_accessor")
	main := ws.NewMainLane(context.Background())
	if main == nil {
		t.Fatal("NewMainLane returned nil")
	}
	got := ws.Lanes()
	if len(got) != 1 || got[0].ID != main.ID {
		t.Errorf("Lanes() = %v, want [main]", got)
	}
	l, ok := ws.GetLane(main.ID)
	if !ok || l.ID != main.ID {
		t.Errorf("GetLane(%s) = (%v,%v), want (%v,true)", main.ID, l, ok, main.ID)
	}
	if _, ok := ws.GetLane("nope"); ok {
		t.Errorf("GetLane(nope) returned ok=true")
	}
	if ws.Bus() != bus {
		t.Errorf("Bus() != bound bus")
	}
}
