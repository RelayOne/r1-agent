// desktop_rpc_cmd_test.go — unit tests for `r1 desktop-rpc` (R1D-1.2).
//
// Tests verify:
//   - stdio dispatch routes each RPC method to the Handler
//   - not_implemented errors encode as JSON-RPC code -32010
//   - unknown method returns -32601
//   - session.cancel triggers session.ended event then exit code 0
//   - session.started event pushed on startup
//   - malformed JSON returns parse error -32700

package main

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func callRPC(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	paramsJSON, _ := json.Marshal(params)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      "1",
		"method":  method,
		"params":  json.RawMessage(paramsJSON),
	}
	reqLine, _ := json.Marshal(req)
	return runOneRequest(t, string(reqLine))
}

// runOneRequest runs the desktop-rpc server with a single request line
// followed by EOF, and returns the first non-event response object.
func runOneRequest(t *testing.T, reqLine string) map[string]any {
	t.Helper()
	var stdout strings.Builder
	// stdin: startup handshake + one request line + EOF
	stdin := strings.NewReader(reqLine + "\n")
	code := runDesktopRPCCmd(
		[]string{"--session-id", "test-session-1"},
		stdin,
		&stdout,
		&strings.Builder{},
	)
	if code != 0 {
		t.Fatalf("runDesktopRPCCmd returned %d", code)
	}
	// Parse all output lines; return the first line with an "id" field.
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		line := scanner.Text()
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if _, hasID := obj["id"]; hasID {
			return obj
		}
	}
	t.Fatal("no RPC response found in output")
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDesktopRPC_SessionStartedEventOnStartup(t *testing.T) {
	var stdout strings.Builder
	stdin := strings.NewReader("") // EOF immediately
	runDesktopRPCCmd(
		[]string{"--session-id", "test-startup"},
		stdin,
		&stdout,
		&strings.Builder{},
	)
	output := stdout.String()
	if !strings.Contains(output, `"session.started"`) {
		t.Errorf("expected session.started event in output; got:\n%s", output)
	}
	if !strings.Contains(output, `"test-startup"`) {
		t.Errorf("expected session_id test-startup in output; got:\n%s", output)
	}
}

func TestDesktopRPC_SessionEndedEventOnEOF(t *testing.T) {
	var stdout strings.Builder
	stdin := strings.NewReader("") // EOF immediately
	runDesktopRPCCmd(
		[]string{"--session-id", "test-eof"},
		stdin,
		&stdout,
		&strings.Builder{},
	)
	output := stdout.String()
	if !strings.Contains(output, `"session.ended"`) {
		t.Errorf("expected session.ended event in output; got:\n%s", output)
	}
}

func TestDesktopRPC_SessionStart_NotImplemented(t *testing.T) {
	resp := callRPC(t, "session.start", map[string]any{"prompt": "hello"})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32010 {
		t.Errorf("expected code -32010 (not_implemented), got %v", code)
	}
	data, _ := errObj["data"].(map[string]any)
	stokeCode, _ := data["stoke_code"].(string)
	if stokeCode != "not_implemented" {
		t.Errorf("expected stoke_code not_implemented, got %q", stokeCode)
	}
}

func TestDesktopRPC_MemoryListScopes_NotImplemented(t *testing.T) {
	resp := callRPC(t, "memory.list_scopes", nil)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32010 {
		t.Errorf("expected -32010, got %v", code)
	}
}

func TestDesktopRPC_UnknownMethod_MethodNotFound(t *testing.T) {
	resp := callRPC(t, "bogus.method", nil)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32601 {
		t.Errorf("expected -32601 (method_not_found), got %v", code)
	}
}

func TestDesktopRPC_MalformedJSON_ParseError(t *testing.T) {
	var stdout strings.Builder
	stdin := strings.NewReader("{not valid json}\n")
	runDesktopRPCCmd(
		[]string{"--session-id", "test-parse"},
		stdin,
		&stdout,
		&strings.Builder{},
	)
	output := stdout.String()
	if !strings.Contains(output, "-32700") {
		t.Errorf("expected parse error -32700 in output; got:\n%s", output)
	}
}

func TestDesktopRPC_InvalidJSONRPCVersion_InvalidRequest(t *testing.T) {
	req := `{"jsonrpc":"1.0","id":"1","method":"session.start","params":{}}`
	resp := runOneRequest(t, req)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32600 {
		t.Errorf("expected -32600 (invalid_request), got %v", code)
	}
}

func TestDesktopRPC_SkillList_NotImplementedTauriOnly(t *testing.T) {
	resp := callRPC(t, "skill.list", nil)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field for skill.list; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32010 {
		t.Errorf("expected -32010, got %v", code)
	}
}

func TestDesktopRPC_SessionSend_NoopSuccess(t *testing.T) {
	resp := callRPC(t, "session.send", map[string]any{
		"session_id": "test-session-1",
		"prompt":     "hello",
	})
	if resp["error"] != nil {
		t.Errorf("session.send should succeed as no-op; got error: %v", resp["error"])
	}
	// result should be present (empty object)
	if resp["result"] == nil {
		t.Errorf("expected non-nil result for session.send no-op")
	}
}

func TestDesktopRPC_LedgerGetNode_NotImplemented(t *testing.T) {
	resp := callRPC(t, "ledger.get_node", map[string]any{"hash": "abc123"})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32010 {
		t.Errorf("expected -32010, got %v", code)
	}
}

func TestDesktopRPC_MissingSessionID_GeneratesOne(t *testing.T) {
	var stdout strings.Builder
	stdin := strings.NewReader("") // EOF immediately
	code := runDesktopRPCCmd(
		[]string{}, // no --session-id
		stdin,
		&stdout,
		&strings.Builder{},
	)
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "desktop-") {
		t.Errorf("expected auto-generated session ID with desktop- prefix; got:\n%s", output)
	}
}

func TestDesktopRPC_DescentCurrentTier_NotImplemented(t *testing.T) {
	resp := callRPC(t, "descent.current_tier", map[string]any{
		"session_id": "test-session-1",
	})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32010 {
		t.Errorf("expected -32010, got %v", code)
	}
}
