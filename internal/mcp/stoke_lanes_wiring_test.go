// Package mcp — TASK-24: lanes/stoke MCP-server wiring test.
//
// Verifies that StokeServer.WithLanesServer composes the two surfaces:
//   - tools/list returns BOTH the stoke/r1 build tools AND the five
//     r1.lanes.* tools;
//   - tools/call routes r1.lanes.* invocations to the LanesServer
//     (and continues to route stoke_/r1_ invocations to the StokeServer).
package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

func TestStokeServerWithLanesServerListsBothSurfaces(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_wire")
	lanes := NewLanesServer(ws, nil)

	stoke := NewStokeServer("/usr/bin/r1").WithLanesServer(lanes)
	defs := stoke.ToolDefinitions()
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}

	// Stoke build tools (canonical r1_ + legacy stoke_) are still
	// present.
	for _, n := range []string{
		"r1_build_from_sow",
		"stoke_build_from_sow",
		"r1_get_mission_status",
		"stoke_get_mission_status",
	} {
		if !names[n] {
			t.Errorf("missing stoke tool %q in combined surface", n)
		}
	}
	// Lane tools (all five) are appended.
	for _, n := range []string{
		"r1.lanes.list",
		"r1.lanes.subscribe",
		"r1.lanes.get",
		"r1.lanes.kill",
		"r1.lanes.pin",
	} {
		if !names[n] {
			t.Errorf("missing lane tool %q in combined surface", n)
		}
	}
}

func TestStokeServerHandleToolCallRoutesLanesPrefix(t *testing.T) {
	t.Parallel()
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	ws.SetSessionID("sess_wire")
	main := ws.NewMainLane(context.Background())
	lanes := NewLanesServer(ws, nil)

	stoke := NewStokeServer("/usr/bin/r1").WithLanesServer(lanes)

	out, err := stoke.HandleToolCall("r1.lanes.list", map[string]interface{}{
		"session_id": "sess_wire",
	})
	if err != nil {
		t.Fatalf("HandleToolCall(r1.lanes.list): %v", err)
	}
	if !strings.Contains(out, main.ID) {
		t.Errorf("lanes.list result missing main lane id %q: %s", main.ID, out)
	}
}

// TestStokeServerHandleToolCallLanesUnwiredErrors covers the case
// where someone hand-rolls an r1.lanes.* call against a StokeServer
// that has no LanesServer attached: clean error (not panic).
func TestStokeServerHandleToolCallLanesUnwiredErrors(t *testing.T) {
	t.Parallel()
	stoke := NewStokeServer("/usr/bin/r1") // no WithLanesServer
	_, err := stoke.HandleToolCall("r1.lanes.list", map[string]interface{}{
		"session_id": "sess_x",
	})
	if err == nil {
		t.Errorf("expected error for unwired lane tool, got nil")
	}
}
