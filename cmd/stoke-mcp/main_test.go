package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// rpcCall drives one request through the Server via its
// serve() loop and returns the first response line.
func rpcCall(t *testing.T, srv *Server, req string) map[string]any {
	t.Helper()
	srv.out = &bytes.Buffer{}
	out := srv.out.(*bytes.Buffer)
	if err := srv.serve(strings.NewReader(req + "\n")); err != nil {
		t.Fatalf("serve: %v", err)
	}
	// Grab the first JSON object from output.
	line, _ := readFirstLine(out.Bytes())
	var parsed map[string]any
	if err := json.Unmarshal(line, &parsed); err != nil {
		t.Fatalf("parse response: %v (raw=%q)", err, string(line))
	}
	return parsed
}

func readFirstLine(b []byte) ([]byte, []byte) {
	for i, c := range b {
		if c == '\n' {
			return b[:i], b[i+1:]
		}
	}
	return b, nil
}

func newTestServer() *Server {
	return &Server{out: &bytes.Buffer{}}
}

func TestInitialize_AnnouncesToolsCapability(t *testing.T) {
	srv := newTestServer()
	resp := rpcCall(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("tools capability missing: %+v", caps)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("name=%v want %q", info["name"], serverName)
	}
}

func TestToolsList_Returns4Primitives(t *testing.T) {
	srv := newTestServer()
	resp := rpcCall(t, srv,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	result, _ := resp["result"].(map[string]any)
	arr, _ := result["tools"].([]any)
	if len(arr) != 4 {
		t.Fatalf("tools len=%d want 4", len(arr))
	}
	wantNames := map[string]bool{
		"stoke_invoke": false, "stoke_verify": false,
		"stoke_audit": false, "stoke_delegate": false,
	}
	for _, tt := range arr {
		m, _ := tt.(map[string]any)
		n, _ := m["name"].(string)
		if _, ok := wantNames[n]; ok {
			wantNames[n] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("primitive tool %q not registered", name)
		}
	}
}

func TestToolsCall_Invoke(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"capability":"code-search","input":{"query":"foo"}}}}`
	resp := rpcCall(t, srv, req)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	if result["_stoke.dev/capability"] != "code-search" {
		t.Errorf("capability annotation missing: %+v", result)
	}
}

func TestToolsCall_InvokeMissingCapabilityErrors(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["error"].(map[string]any); !ok {
		t.Fatalf("expected error, got %+v", resp)
	}
}

func TestToolsCall_Verify(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"stoke_verify","arguments":{"task_class":"code","subject":"package main"}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("no result: %+v", resp)
	}
}

func TestToolsCall_Audit(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"stoke_audit","arguments":{"action":"deployed build","evidence_refs":["sha:abc"]}}}`
	resp := rpcCall(t, srv, req)
	result, _ := resp["result"].(map[string]any)
	if result["node_id"] == nil || result["node_id"] == "" {
		t.Errorf("node_id missing: %+v", result)
	}
}

func TestToolsCall_Delegate(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"stoke_delegate","arguments":{"to_did":"did:tp:b","bundle_name":"read-only-calendar","expiry_seconds":60}}}`
	resp := rpcCall(t, srv, req)
	result, _ := resp["result"].(map[string]any)
	if result["delegation_id"] == nil {
		t.Errorf("delegation_id missing: %+v", result)
	}
	if result["bundle_name"] != "read-only-calendar" {
		t.Errorf("bundle_name mismatch: %+v", result)
	}
}

func TestToolsCall_UnknownToolErrors(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"stoke_fabricate","arguments":{}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error")
	}
	if int(err["code"].(float64)) != errMethodMiss {
		t.Errorf("code=%v want %d", err["code"], errMethodMiss)
	}
}

func TestAuth_RejectsWrongKey(t *testing.T) {
	srv := &Server{out: &bytes.Buffer{}, apiKey: "expected-key", requireKey: true}
	req := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"apiKey":"wrong","name":"stoke_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error")
	}
	if int(err["code"].(float64)) != errUnauthorized {
		t.Errorf("code=%v want %d", err["code"], errUnauthorized)
	}
}

func TestAuth_AcceptsCorrectKey(t *testing.T) {
	srv := &Server{out: &bytes.Buffer{}, apiKey: "expected-key", requireKey: true}
	req := `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"apiKey":"expected-key","name":"stoke_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result, got %+v", resp)
	}
}

func TestAuth_AcceptsBearerInMeta(t *testing.T) {
	srv := &Server{out: &bytes.Buffer{}, apiKey: "expected-key", requireKey: true}
	req := `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"_meta":{"authorization":"Bearer expected-key"},"name":"stoke_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result, got %+v", resp)
	}
}

func TestAuth_MissingKeyRejected(t *testing.T) {
	srv := &Server{out: &bytes.Buffer{}, apiKey: "expected-key", requireKey: true}
	req := `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["error"].(map[string]any); !ok {
		t.Fatalf("expected error, got %+v", resp)
	}
}

func TestAuth_DisabledWhenNoKeyConfigured(t *testing.T) {
	// No requireKey → auth skipped even if caller sends no key.
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result, got %+v", resp)
	}
}

func TestParseError(t *testing.T) {
	srv := newTestServer()
	resp := rpcCall(t, srv, `{not valid json`)
	err, _ := resp["error"].(map[string]any)
	if int(err["code"].(float64)) != errParse {
		t.Errorf("code=%v want %d", err["code"], errParse)
	}
}

// TestToolsList_NoAuthRequired: discovery must NEVER be
// blocked by API key, otherwise standard MCP clients can't
// enumerate the tool surface.
func TestToolsList_NoAuthRequired(t *testing.T) {
	srv := &Server{out: nil, apiKey: "secret-key", requireKey: true}
	resp := rpcCall(t, srv,
		`{"jsonrpc":"2.0","id":20,"method":"tools/list","params":{}}`)
	if _, ok := resp["error"].(map[string]any); ok {
		t.Fatalf("tools/list should work without auth, got error: %+v", resp)
	}
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Errorf("expected result, got %+v", resp)
	}
}

// TestInvoke_RejectsMissingInput: P2 fix — input is declared
// required + object in the schema, and a missing or null
// input must produce -32602.
func TestInvoke_RejectsMissingInput(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"capability":"x"}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on missing input")
	}
	if int(err["code"].(float64)) != errInvalidArgs {
		t.Errorf("code=%v want %d", err["code"], errInvalidArgs)
	}
}

func TestInvoke_RejectsNonObjectInput(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":22,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"capability":"x","input":[1,2,3]}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on array input (schema requires object)")
	}
}

// TestVerify_RejectsBadTaskClass: P2 fix — task_class is
// constrained to the 4 enums by the schema; reject anything
// else with -32602.
func TestVerify_RejectsBadTaskClass(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":23,"method":"tools/call","params":{"name":"stoke_verify","arguments":{"task_class":"metaphysics","subject":"x"}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on unknown task_class")
	}
}

func TestVerify_RejectsMissingSubject(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":24,"method":"tools/call","params":{"name":"stoke_verify","arguments":{"task_class":"code"}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on missing subject")
	}
}
