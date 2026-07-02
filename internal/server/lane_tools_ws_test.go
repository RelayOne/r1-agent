// Package server — WS routing tests for the r1.lanes.* control
// methods (audit A040). Before this wiring, handleClientFrame replied
// -32601 to every lane control call, so the remote lanes TUI
// transport (internal/tui/lanes remoteTransport) could never list,
// kill, or pin a lane over the daemon socket.
package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeLaneTools is a recording LaneToolInvoker returning canned §7
// envelopes per tool name.
type fakeLaneTools struct {
	mu        sync.Mutex
	calls     []laneToolCall
	responses map[string]string
}

type laneToolCall struct {
	Tool string
	Args map[string]interface{}
}

func (f *fakeLaneTools) HandleToolCall(_ context.Context, tool string, args map[string]interface{}) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, laneToolCall{Tool: tool, Args: args})
	if resp, ok := f.responses[tool]; ok {
		return resp, nil
	}
	return `{"ok":true}`, nil
}

func (f *fakeLaneTools) callsFor(tool string) []laneToolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []laneToolCall
	for _, c := range f.calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// laneToolsHarness stands up a lanes WS connection with an active
// subscription (binding the conn to sessionID) plus the fake tool
// invoker, and returns a call helper that sends one JSON-RPC request
// and decodes the reply.
func laneToolsHarness(t *testing.T, tools *fakeLaneTools, sessionID string) func(id int, method string, params map[string]any) map[string]any {
	t.Helper()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub(), Tools: tools})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	t.Cleanup(func() { resp.Body.Close() })

	// Bind the connection to a session via session.subscribe (the
	// remote TUI transport does exactly this before lane calls).
	subReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "session.subscribe",
		"params": map[string]any{"session_id": sessionID},
	})
	if err := writeWSFrameMasked(conn, 0x1, subReq); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	// Drain: subscribe result + session.bound synthetic.
	for i := 0; i < 2; i++ {
		if _, _, err := readWSFrameAsClient(br); err != nil {
			t.Fatalf("drain frame %d: %v", i, err)
		}
	}

	return func(id int, method string, params map[string]any) map[string]any {
		t.Helper()
		req, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": params,
		})
		if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
			t.Fatalf("write %s: %v", method, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, payload, err := readWSFrameAsClient(br)
		if err != nil {
			t.Fatalf("read %s reply: %v", method, err)
		}
		var out map[string]any
		if err := json.Unmarshal(payload, &out); err != nil {
			t.Fatalf("unmarshal %s reply: %v", method, err)
		}
		return out
	}
}

// TestLaneWS_ListRoutesAndUnwraps: r1.lanes.list is routed to the
// invoker with the bound session_id injected, and the §7 envelope is
// unwrapped so result.lanes matches what remoteTransport decodes.
func TestLaneWS_ListRoutesAndUnwraps(t *testing.T) {
	t.Parallel()
	tools := &fakeLaneTools{responses: map[string]string{
		"r1.lanes.list": `{"ok":true,"data":{"lanes":[{"lane_id":"lane-1","kind":"lobe","status":"running","started_at":"2026-07-01T00:00:00Z"}]}}`,
	}}
	call := laneToolsHarness(t, tools, "sess-a40")

	reply := call(2, "r1.lanes.list", map[string]any{})
	if reply["error"] != nil {
		t.Fatalf("unexpected error: %v", reply["error"])
	}
	result, _ := reply["result"].(map[string]any)
	lanes, _ := result["lanes"].([]any)
	if len(lanes) != 1 {
		t.Fatalf("lanes = %v, want 1 entry", result["lanes"])
	}
	lane0, _ := lanes[0].(map[string]any)
	if lane0["lane_id"] != "lane-1" {
		t.Errorf("lane_id = %v", lane0["lane_id"])
	}

	calls := tools.callsFor("r1.lanes.list")
	if len(calls) != 1 {
		t.Fatalf("invoker calls = %d, want 1", len(calls))
	}
	if calls[0].Args["session_id"] != "sess-a40" {
		t.Errorf("session_id injected = %v, want sess-a40 (from subscription)", calls[0].Args["session_id"])
	}
}

// TestLaneWS_KillAndPinRouted: kill/pin params pass through verbatim
// plus the injected session_id; error envelopes surface as JSON-RPC
// -32000 with data.code.
func TestLaneWS_KillAndPinRouted(t *testing.T) {
	t.Parallel()
	tools := &fakeLaneTools{responses: map[string]string{
		"r1.lanes.kill": `{"ok":true,"data":{"killed_lane_ids":["lane-1"],"already_terminal":false}}`,
		"r1.lanes.pin":  `{"ok":false,"error_code":"not_found","error_message":"lane \"lane-x\" not found"}`,
	}}
	call := laneToolsHarness(t, tools, "sess-a40")

	killReply := call(3, "r1.lanes.kill", map[string]any{"lane_id": "lane-1", "reason": "cancelled_by_operator"})
	if killReply["error"] != nil {
		t.Fatalf("kill error: %v", killReply["error"])
	}
	kr, _ := killReply["result"].(map[string]any)
	ids, _ := kr["killed_lane_ids"].([]any)
	if len(ids) != 1 || ids[0] != "lane-1" {
		t.Errorf("killed_lane_ids = %v", kr["killed_lane_ids"])
	}
	kcalls := tools.callsFor("r1.lanes.kill")
	if len(kcalls) != 1 || kcalls[0].Args["lane_id"] != "lane-1" || kcalls[0].Args["reason"] != "cancelled_by_operator" {
		t.Errorf("kill args = %+v", kcalls)
	}

	pinReply := call(4, "r1.lanes.pin", map[string]any{"lane_id": "lane-x", "pinned": true})
	errObj, _ := pinReply["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("pin should surface tool error, got %v", pinReply)
	}
	if code, _ := errObj["code"].(float64); code != -32000 {
		t.Errorf("error code = %v, want -32000", errObj["code"])
	}
	data, _ := errObj["data"].(map[string]any)
	if data["code"] != "not_found" {
		t.Errorf("error data.code = %v, want not_found", data["code"])
	}
}

// TestLaneWS_KillAllIteratesNonTerminalLanes: killAll = list
// (include_terminal=false) + one kill per lane, aggregated ids.
func TestLaneWS_KillAllIteratesNonTerminalLanes(t *testing.T) {
	t.Parallel()
	tools := &fakeLaneTools{responses: map[string]string{
		"r1.lanes.list": `{"ok":true,"data":{"lanes":[{"lane_id":"lane-1"},{"lane_id":"lane-2"}]}}`,
		"r1.lanes.kill": `{"ok":true,"data":{"killed_lane_ids":["killed"],"already_terminal":false}}`,
	}}
	call := laneToolsHarness(t, tools, "sess-a40")

	reply := call(5, "r1.lanes.killAll", map[string]any{"reason": "cancelled_by_operator"})
	if reply["error"] != nil {
		t.Fatalf("killAll error: %v", reply["error"])
	}
	result, _ := reply["result"].(map[string]any)
	ids, _ := result["killed_lane_ids"].([]any)
	if len(ids) != 2 {
		t.Errorf("aggregated killed ids = %v, want 2", ids)
	}

	lcalls := tools.callsFor("r1.lanes.list")
	if len(lcalls) != 1 {
		t.Fatalf("list calls = %d, want 1", len(lcalls))
	}
	if it, ok := lcalls[0].Args["include_terminal"].(bool); !ok || it {
		t.Errorf("include_terminal = %v, want false", lcalls[0].Args["include_terminal"])
	}
	kcalls := tools.callsFor("r1.lanes.kill")
	if len(kcalls) != 2 {
		t.Fatalf("kill calls = %d, want 2 (one per lane)", len(kcalls))
	}
	for i, kc := range kcalls {
		if casc, ok := kc.Args["cascade"].(bool); !ok || casc {
			t.Errorf("kill[%d] cascade = %v, want false", i, kc.Args["cascade"])
		}
		if kc.Args["session_id"] != "sess-a40" {
			t.Errorf("kill[%d] session_id = %v", i, kc.Args["session_id"])
		}
	}
}

// TestLaneWS_ToolsNotConfigured: without Tools wiring the methods
// keep the pre-A040 -32601 behavior.
func TestLaneWS_ToolsNotConfigured(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "r1.lanes.list", "params": map[string]any{},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(payload, &out)
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("want error reply, got %v", out)
	}
	if code, _ := errObj["code"].(float64); code != -32601 {
		t.Errorf("code = %v, want -32601", errObj["code"])
	}
}

// TestLaneWS_MissingSessionBinding: lane calls with no subscription
// AND no session_id param get a -32602 validation error.
func TestLaneWS_MissingSessionBinding(t *testing.T) {
	t.Parallel()
	tools := &fakeLaneTools{}
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub(), Tools: tools})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "r1.lanes.kill",
		"params": map[string]any{"lane_id": "lane-1"},
	})
	if err := writeWSFrameMasked(conn, 0x1, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(payload, &out)
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("want error reply, got %v", out)
	}
	if code, _ := errObj["code"].(float64); code != -32602 {
		t.Errorf("code = %v, want -32602", errObj["code"])
	}
	if len(tools.callsFor("r1.lanes.kill")) != 0 {
		t.Error("invoker should not be reached without a session binding")
	}
}
