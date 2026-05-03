// Package mcp — tests for LanesServer (specs/lanes-protocol.md §7).
//
// This file covers the wire-level advertisement contract:
//
//   - five tools are advertised with the canonical r1.lanes.* names;
//   - every tool has a non-empty input_schema and output_schema;
//   - the schemas parse as valid JSON (sanity check that the §7
//     verbatim embedding did not introduce a typo);
//   - dispatch returns a structured error envelope (not a panic) for
//     the streaming r1.lanes.subscribe tool when called via the
//     non-streaming HandleToolCall path (callers must use Subscribe).
//
// Per-tool round-trips are covered in lanes_server_list_test.go,
// lanes_server_subscribe_test.go, lanes_server_get_test.go,
// lanes_server_kill_test.go, lanes_server_pin_test.go.
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// fakeLanesBackend is a minimal in-memory LanesBackend for tests.
type fakeLanesBackend struct {
	sessionID string
	lanes     map[string]*cortex.Lane
	bus       *hub.Bus
}

func newFakeLanesBackend(sessionID string) *fakeLanesBackend {
	return &fakeLanesBackend{
		sessionID: sessionID,
		lanes:     make(map[string]*cortex.Lane),
	}
}

func (f *fakeLanesBackend) Lanes() []*cortex.Lane {
	out := make([]*cortex.Lane, 0, len(f.lanes))
	for _, l := range f.lanes {
		out = append(out, l)
	}
	return out
}

func (f *fakeLanesBackend) GetLane(id string) (*cortex.Lane, bool) {
	l, ok := f.lanes[id]
	return l, ok
}

func (f *fakeLanesBackend) SessionID() string { return f.sessionID }

func (f *fakeLanesBackend) Bus() *hub.Bus { return f.bus }

// TestLanesServerToolDefinitions covers the §7 advertisement contract.
func TestLanesServerToolDefinitions(t *testing.T) {
	t.Parallel()
	srv := NewLanesServer(newFakeLanesBackend("sess_test"), nil)
	tools := srv.ToolDefinitions()

	want := []string{
		"r1.lanes.list",
		"r1.lanes.subscribe",
		"r1.lanes.get",
		"r1.lanes.kill",
		"r1.lanes.pin",
	}
	if len(tools) != len(want) {
		t.Fatalf("len(tools) = %d, want %d", len(tools), len(want))
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Name, name)
		}
		if tools[i].Description == "" {
			t.Errorf("tools[%d] (%s) missing description", i, name)
		}
		if len(tools[i].InputSchema) == 0 {
			t.Errorf("tools[%d] (%s) missing input_schema", i, name)
		}
		if len(tools[i].OutputSchema) == 0 {
			t.Errorf("tools[%d] (%s) missing output_schema", i, name)
		}
		// Schemas must parse as JSON.
		var input, output map[string]any
		if err := json.Unmarshal(tools[i].InputSchema, &input); err != nil {
			t.Errorf("tools[%d] (%s) input_schema not valid JSON: %v", i, name, err)
		}
		if err := json.Unmarshal(tools[i].OutputSchema, &output); err != nil {
			t.Errorf("tools[%d] (%s) output_schema not valid JSON: %v", i, name, err)
		}
		// Draft 2020-12 mandates a $schema declaration on each.
		if got := input["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("tools[%d] (%s) input_schema $schema = %v, want draft 2020-12", i, name, got)
		}
		if got := output["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("tools[%d] (%s) output_schema $schema = %v, want draft 2020-12", i, name, got)
		}
	}
}

// TestLanesServerHandleToolCallSubscribeRejectsNonStreaming verifies
// that calling r1.lanes.subscribe through the non-streaming
// HandleToolCall returns a structured invalid_request envelope. The
// streaming path lives on Subscribe and is exercised by
// lanes_server_subscribe_test.go.
func TestLanesServerHandleToolCallSubscribeRejectsNonStreaming(t *testing.T) {
	t.Parallel()
	srv := NewLanesServer(newFakeLanesBackend("sess_test"), nil)

	for _, name := range []string{
		"r1.lanes.subscribe", // streaming: returns invalid_request envelope
	} {
		out, err := srv.HandleToolCall(context.Background(), name, map[string]interface{}{
			"session_id": "sess_test",
			"lane_id":    "lane_x",
			"pinned":     true,
		})
		if err != nil {
			t.Errorf("HandleToolCall(%s) returned go error: %v", name, err)
			continue
		}
		var env map[string]any
		if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
			t.Errorf("HandleToolCall(%s) returned non-JSON: %s", name, out)
			continue
		}
		if got := env["ok"]; got != false {
			t.Errorf("HandleToolCall(%s) ok = %v, want false (streaming tool rejects non-streaming dispatch)", name, got)
		}
		if env["error_code"] == nil || env["error_code"] == "" {
			t.Errorf("HandleToolCall(%s) missing error_code: %s", name, out)
		}
	}
}

// TestLanesServerHandleUnknownTool covers the explicit unknown-tool
// branch — surfaces a Go error (which the JSON-RPC layer wraps in a
// tool-call isError response).
func TestLanesServerHandleUnknownTool(t *testing.T) {
	t.Parallel()
	srv := NewLanesServer(newFakeLanesBackend("sess_test"), nil)
	_, err := srv.HandleToolCall(context.Background(), "r1.lanes.noop", nil)
	if err == nil {
		t.Errorf("expected error for unknown tool")
	}
}
