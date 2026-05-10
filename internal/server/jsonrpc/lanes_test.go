package jsonrpc

import (
	"context"
	"errors"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// newSessionWithWorkspace starts a session via the handler and
// attaches a fresh cortex.Workspace to it. Returns the session id +
// the workspace so tests can publish/create lanes directly.
func newSessionWithWorkspace(t *testing.T, h *HubHandler) (string, *cortex.Workspace) {
	t.Helper()
	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	sess, err := h.Hub.Get(resp.SessionID)
	if err != nil {
		t.Fatalf("hub.Get: %v", err)
	}
	durable, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	ws := cortex.NewWorkspace(hub.New(), durable)
	sess.Workspace = ws
	return resp.SessionID, ws
}

func TestHubHandler_LanesList_Empty(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	id, _ := newSessionWithWorkspace(t, h)
	resp, err := h.DaemonLanesList(context.Background(), LanesListRequest{SessionID: id})
	if err != nil {
		t.Fatalf("lanes.list: %v", err)
	}
	if len(resp.Lanes) != 0 {
		t.Errorf("expected 0 lanes for empty workspace, got %d", len(resp.Lanes))
	}
	if resp.Lanes == nil {
		t.Errorf("expected non-nil empty slice, got nil")
	}
}

func TestHubHandler_LanesList_NoWorkspace(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	listResp, err := h.DaemonLanesList(context.Background(), LanesListRequest{SessionID: resp.SessionID})
	if err != nil {
		t.Fatalf("lanes.list (no ws): %v", err)
	}
	if len(listResp.Lanes) != 0 {
		t.Errorf("expected 0 lanes when Workspace=nil, got %d", len(listResp.Lanes))
	}
}

func TestHubHandler_LanesList_ProjectsCreatedLanes(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	id, ws := newSessionWithWorkspace(t, h)

	main := ws.NewMainLane(context.Background())
	_ = ws.NewLobeLane(context.Background(), "memory-recall", main)

	resp, err := h.DaemonLanesList(context.Background(), LanesListRequest{SessionID: id})
	if err != nil {
		t.Fatalf("lanes.list: %v", err)
	}
	if len(resp.Lanes) < 2 {
		t.Fatalf("expected at least 2 lanes (main + lobe), got %d", len(resp.Lanes))
	}
	// Main lane should be present and have empty ParentID.
	foundMain := false
	for _, l := range resp.Lanes {
		if l.LaneID == main.ID {
			foundMain = true
			if l.ParentID != "" {
				t.Errorf("main lane should have empty ParentID, got %q", l.ParentID)
			}
			if l.Status == "" {
				t.Errorf("main lane should have a Status, got empty")
			}
			if l.StartedAt == "" {
				t.Errorf("main lane should have StartedAt, got empty")
			}
		}
	}
	if !foundMain {
		t.Errorf("main lane %q not in list response", main.ID)
	}
}

func TestHubHandler_LanesKill_KillsExistingLane(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	id, ws := newSessionWithWorkspace(t, h)
	main := ws.NewMainLane(context.Background())

	resp, err := h.DaemonLanesKill(context.Background(), LanesKillRequest{
		SessionID: id,
		LaneID:    main.ID,
		Reason:    "test kill",
	})
	if err != nil {
		t.Fatalf("lanes.kill: %v", err)
	}
	if resp.KilledAt == "" {
		t.Errorf("KilledAt empty in kill response")
	}
	// Lane should be in cancelled (terminal) state.
	lane, found := ws.GetLane(main.ID)
	if !found {
		t.Fatalf("killed lane should still be in registry; not found")
	}
	if !lane.IsTerminal() {
		t.Errorf("killed lane should be terminal; got status=%q", lane.Status)
	}
}

func TestHubHandler_LanesKill_RejectsEmptyLaneID(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	id, _ := newSessionWithWorkspace(t, h)

	_, err := h.DaemonLanesKill(context.Background(), LanesKillRequest{SessionID: id})
	if err == nil {
		t.Fatal("expected error for empty lane_id")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
		t.Errorf("expected ErrValidation for empty lane_id, got %v", err)
	}
}

func TestHubHandler_LanesKill_NotFound(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	id, _ := newSessionWithWorkspace(t, h)
	_, err := h.DaemonLanesKill(context.Background(), LanesKillRequest{
		SessionID: id,
		LaneID:    "lane-does-not-exist",
	})
	if err == nil {
		t.Fatal("expected error for unknown lane_id")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound for unknown lane, got %v", err)
	}
}

func TestHubHandler_LanesKill_NoWorkspace(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_, err = h.DaemonLanesKill(context.Background(), LanesKillRequest{
		SessionID: resp.SessionID,
		LaneID:    "any-id",
	})
	if err == nil {
		t.Fatal("expected error when Workspace is nil")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
		t.Errorf("expected ErrNotFound when no workspace, got %v", err)
	}
}
