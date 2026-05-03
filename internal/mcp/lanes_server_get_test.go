// Package mcp — tests for r1.lanes.get (specs/lanes-protocol.md §7.3
// / TASK-21).
package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// fakeLanesWAL is an in-memory replay surface for tests.
type fakeLanesWAL struct {
	events []*hub.Event
}

func (f *fakeLanesWAL) ReplayLane(_ context.Context, sessionID string, fromSeq uint64, handler func(*hub.Event) error) error {
	for _, ev := range f.events {
		if ev == nil || ev.Lane == nil {
			continue
		}
		if ev.Lane.SessionID != sessionID {
			continue
		}
		if ev.Lane.Seq < fromSeq {
			continue
		}
		if err := handler(ev); err != nil {
			return err
		}
	}
	return nil
}

// TestLanesGetSnapshotOnly returns the lane snapshot when tail=0.
func TestLanesGetSnapshotOnly(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_get")
	be.lanes["lane_1"] = &cortex.Lane{
		ID:        "lane_1",
		Kind:      hub.LaneKindLobe,
		Label:     "MyLobe",
		ParentID:  "lane_main",
		Status:    hub.LaneStatusRunning,
		Pinned:    true,
		StartedAt: time.Now().UTC(),
		LastSeq:   42,
	}
	srv := NewLanesServer(be, nil)
	out, err := srv.HandleToolCall(context.Background(), "r1.lanes.get", map[string]interface{}{
		"session_id": "sess_get",
		"lane_id":    "lane_1",
	})
	if err != nil {
		t.Fatalf("HandleToolCall: %v", err)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Lane map[string]any   `json:"lane"`
			Tail []map[string]any `json:"tail"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\n%s", jerr, out)
	}
	if !env.OK {
		t.Fatalf("ok=false; body=%s", out)
	}
	if env.Data.Lane["lane_id"] != "lane_1" {
		t.Errorf("lane_id = %v, want lane_1", env.Data.Lane["lane_id"])
	}
	if env.Data.Lane["lobe_name"] != "MyLobe" {
		t.Errorf("lobe_name = %v, want MyLobe", env.Data.Lane["lobe_name"])
	}
	if env.Data.Lane["parent_lane_id"] != "lane_main" {
		t.Errorf("parent_lane_id = %v, want lane_main", env.Data.Lane["parent_lane_id"])
	}
	if env.Data.Lane["pinned"] != true {
		t.Errorf("pinned = %v, want true", env.Data.Lane["pinned"])
	}
	if env.Data.Lane["last_seq"].(float64) != 42 {
		t.Errorf("last_seq = %v, want 42", env.Data.Lane["last_seq"])
	}
	if len(env.Data.Tail) != 0 {
		t.Errorf("tail len = %d, want 0", len(env.Data.Tail))
	}
}

// TestLanesGetWithTail walks the WAL, returns the bounded tail.
func TestLanesGetWithTail(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_x")
	be.lanes["lane_y"] = &cortex.Lane{
		ID:        "lane_y",
		Kind:      hub.LaneKindMain,
		Status:    hub.LaneStatusRunning,
		StartedAt: time.Now().UTC(),
	}
	wal := &fakeLanesWAL{events: []*hub.Event{
		{ID: "e1", Type: hub.EventLaneCreated, Lane: &hub.LaneEvent{LaneID: "lane_y", SessionID: "sess_x", Seq: 1}},
		{ID: "e2", Type: hub.EventLaneStatus, Lane: &hub.LaneEvent{LaneID: "lane_y", SessionID: "sess_x", Seq: 2, Status: hub.LaneStatusRunning}},
		{ID: "e3", Type: hub.EventLaneDelta, Lane: &hub.LaneEvent{LaneID: "lane_y", SessionID: "sess_x", Seq: 3, DeltaSeq: 1}},
		{ID: "e4", Type: hub.EventLaneDelta, Lane: &hub.LaneEvent{LaneID: "lane_y", SessionID: "sess_x", Seq: 4, DeltaSeq: 2}},
		// non-matching lane: must be skipped
		{ID: "other", Type: hub.EventLaneDelta, Lane: &hub.LaneEvent{LaneID: "lane_other", SessionID: "sess_x", Seq: 5, DeltaSeq: 1}},
	}}
	srv := NewLanesServer(be, wal)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.get", map[string]interface{}{
		"session_id": "sess_x",
		"lane_id":    "lane_y",
		"tail":       2.0,
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Tail []map[string]any `json:"tail"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if !env.OK {
		t.Fatalf("ok=false; body=%s", out)
	}
	if len(env.Data.Tail) != 2 {
		t.Fatalf("tail len = %d, want 2", len(env.Data.Tail))
	}
	// Window is the most-recent 2 matching events: e3, e4.
	got := []float64{
		env.Data.Tail[0]["seq"].(float64),
		env.Data.Tail[1]["seq"].(float64),
	}
	if got[0] != 3 || got[1] != 4 {
		t.Errorf("tail seqs = %v, want [3,4]", got)
	}
}

// TestLanesGetNotFound returns the structured not_found envelope.
func TestLanesGetNotFound(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_x")
	srv := NewLanesServer(be, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.get", map[string]interface{}{
		"session_id": "sess_x",
		"lane_id":    "missing",
	})
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	if env["ok"] != false || env["error_code"] != "not_found" {
		t.Errorf("env = %v, want ok=false error_code=not_found", env)
	}
}

// TestLanesGetMissingLaneIDInvalidRequest covers schema-level
// validation: lane_id is required.
func TestLanesGetMissingLaneIDInvalidRequest(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_x")
	srv := NewLanesServer(be, nil)
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.get", map[string]interface{}{
		"session_id": "sess_x",
	})
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	if env["ok"] != false || env["error_code"] != "invalid_request" {
		t.Errorf("env = %v, want ok=false error_code=invalid_request", env)
	}
}

// TestLanesGetTailWithoutWAL returns an empty tail (not an error)
// when no WAL is configured. Spec §7.3 marks tail as optional.
func TestLanesGetTailWithoutWAL(t *testing.T) {
	t.Parallel()
	be := newFakeLanesBackend("sess_x")
	be.lanes["lane_y"] = &cortex.Lane{ID: "lane_y", Kind: hub.LaneKindMain, Status: hub.LaneStatusRunning, StartedAt: time.Now()}
	srv := NewLanesServer(be, nil) // no WAL
	out, _ := srv.HandleToolCall(context.Background(), "r1.lanes.get", map[string]interface{}{
		"session_id": "sess_x",
		"lane_id":    "lane_y",
		"tail":       100.0,
	})
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Tail []map[string]any `json:"tail"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(out), &env)
	if !env.OK {
		t.Fatalf("ok=false")
	}
	if len(env.Data.Tail) != 0 {
		t.Errorf("expected empty tail with no WAL, got %d entries", len(env.Data.Tail))
	}
}
