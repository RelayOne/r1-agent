// desktop_rpc_cmd_test.go — unit tests for `r1 desktop-rpc` (R1D-1.2).
//
// Tests verify:
//   - stdio dispatch routes each RPC method to the Handler
//   - not_implemented errors encode as JSON-RPC code -32010
//   - unknown method returns -32601
//   - session.cancel triggers session.ended event then exit code 0
//   - session.started event pushed on startup
//   - malformed JSON returns parse error -32700
//
// Every invocation is hermetic: the desktop-rpc server now materializes a
// real ledger store + memory.db under its --workdir (audit A011), so the
// helpers pin --workdir to a per-test t.TempDir() to keep the source tree
// clean and each run isolated.

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

// hermeticArgs returns desktop-rpc args with a per-test temp workdir so the
// real ledger/memory backends (audit A011) never touch the source tree.
func hermeticArgs(t *testing.T, sessionID string) []string {
	t.Helper()
	return []string{"--session-id", sessionID, "--workdir", t.TempDir()}
}

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
		hermeticArgs(t, "test-session-1"),
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
		hermeticArgs(t, "test-startup"),
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
		hermeticArgs(t, "test-eof"),
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

// memory.list_scopes is a real implemented verb (audit A011): it returns
// the canonical five-scope enumeration, not a -32010 stub.
func TestDesktopRPC_MemoryListScopes_ReturnsScopes(t *testing.T) {
	resp := callRPC(t, "memory.list_scopes", nil)
	if resp["error"] != nil {
		t.Fatalf("memory.list_scopes should succeed; got error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object; got %v", resp["result"])
	}
	scopes, ok := result["scopes"].([]any)
	if !ok {
		t.Fatalf("expected scopes array; got %v", result["scopes"])
	}
	if len(scopes) != 5 {
		t.Errorf("scopes = %d, want 5 (%v)", len(scopes), scopes)
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
		hermeticArgs(t, "test-parse"),
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

// ledger.get_node is a real implemented verb (audit A011): against an
// empty ledger a missing hash returns not_found (-32002), not the
// -32010 stub it used to.
func TestDesktopRPC_LedgerGetNode_MissingIsNotFound(t *testing.T) {
	resp := callRPC(t, "ledger.get_node", map[string]any{"hash": "abc123"})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error field; got %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32002 {
		t.Errorf("expected -32002 (not_found) for a missing node, got %v", code)
	}
	data, _ := errObj["data"].(map[string]any)
	if sc, _ := data["stoke_code"].(string); sc != "not_found" {
		t.Errorf("expected stoke_code not_found, got %q", sc)
	}
}

func TestDesktopRPC_MissingSessionID_GeneratesOne(t *testing.T) {
	var stdout strings.Builder
	stdin := strings.NewReader("") // EOF immediately
	code := runDesktopRPCCmd(
		[]string{"--workdir", t.TempDir()}, // no --session-id
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

// TestDesktopRPC_ContractVerbs_NeverMethodNotFound (audit A029) is the
// drift guard demanded by the contract: every round-trip verb the Tauri
// host sends (desktop/IPC-CONTRACT.md §2.1–§2.8 plus session.set_workdir
// from spec desktop-cortex-augmentation §7) must dispatch to a real
// handler path — success or a typed stokerr-tagged error — and must
// NEVER fall through to -32601 method_not_found. Add new contract verbs
// here so a Rust-side verb addition without a Go-side dispatch case
// fails this test instead of failing silently at runtime.
//
// session.cancel is intentionally absent: its handler path calls
// os.Exit(0) after flushing session.ended, which would kill the test
// process.
func TestDesktopRPC_ContractVerbs_NeverMethodNotFound(t *testing.T) {
	verbs := []string{
		// §2.1 session control
		"session.start", "session.pause", "session.resume",
		// §2.2 ledger query
		"ledger.get_node", "ledger.list_events",
		// §2.3 memory inspection
		"memory.list_scopes", "memory.query",
		// §2.4 cost
		"cost.get_current", "cost.get_history",
		// §2.5 descent state
		"descent.current_tier", "descent.tier_history",
		// §2.7 lane control
		"session.lanes.list", "session.lanes.subscribe",
		"session.lanes.unsubscribe", "session.lanes.kill",
		// spec desktop-cortex-augmentation §7 workdir binding
		"session.set_workdir",
		// §2.8 daemon control
		"daemon.status", "daemon.shutdown",
		// stdin-path no-op ack (§5, tolerated on the RPC path)
		"session.send",
		// Tauri-only skill verbs degrade to not_implemented
		"skill.list", "skill.get",
	}
	for _, verb := range verbs {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			resp := callRPC(t, verb, map[string]any{"session_id": "s-1"})
			errObj, isErr := resp["error"].(map[string]any)
			if !isErr {
				return // success result is a valid non-method_not_found path
			}
			code, _ := errObj["code"].(float64)
			if code == -32601 {
				t.Fatalf("%s: fell through to -32601 method_not_found; "+
					"every contract verb needs a dispatch case", verb)
			}
		})
	}
}

// TestDesktopRPC_LaneAndDaemonVerbs_NotImplemented (audit A029) pins
// the degradation for the routed-but-unimplemented verbs: the handler
// must surface -32010 with data.stoke_code "not_implemented" — the shape
// the Rust host and the panels' truthful-unavailable rendering
// (audit A034/A052) key on.
func TestDesktopRPC_LaneAndDaemonVerbs_NotImplemented(t *testing.T) {
	verbs := []string{
		"session.lanes.list",
		"session.lanes.subscribe",
		"session.lanes.unsubscribe",
		"session.lanes.kill",
		"session.set_workdir",
		"daemon.status",
		"daemon.shutdown",
	}
	for _, verb := range verbs {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			resp := callRPC(t, verb, map[string]any{"session_id": "s-1"})
			errObj, ok := resp["error"].(map[string]any)
			if !ok {
				t.Fatalf("%s: expected error field; got %v", verb, resp)
			}
			code, _ := errObj["code"].(float64)
			if code != -32010 {
				t.Fatalf("%s: expected -32010 (not_implemented), got %v", verb, code)
			}
			data, _ := errObj["data"].(map[string]any)
			stokeCode, _ := data["stoke_code"].(string)
			if stokeCode != "not_implemented" {
				t.Fatalf("%s: expected stoke_code not_implemented, got %q", verb, stokeCode)
			}
		})
	}
}
