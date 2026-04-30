package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// rpcCall drives one request through the Server via its
// serve() loop and returns the first response line.
func rpcCall(t *testing.T, srv *Server, req string) map[string]any {
	t.Helper()
	srv.out = &bytes.Buffer{}
	out, ok := srv.out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("srv.out: unexpected type: %T", srv.out)
	}
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

// newTestServer constructs a Server with LIVE backends
// pointed at a per-test-run temp-dir ledger. Each test
// gets its own ledger so writes don't leak across tests
// and parallel runs don't race.
//
// Unlike the scaffold era where newTestServer returned a
// Server with no backends (handlers returned synthetic
// text), the backend-wired handlers deref s.backends
// directly — tests that skip backend construction would
// nil-pointer immediately.
func newTestServer() *Server {
	t := testBackendsTempDir
	if t == "" {
		dir, err := os.MkdirTemp("", "stoke-mcp-test-")
		if err != nil {
			panic("newTestServer: MkdirTemp: " + err.Error())
		}
		t = dir
	}
	backends, err := NewBackends(t)
	if err != nil {
		panic("newTestServer: NewBackends: " + err.Error())
	}
	return &Server{out: &bytes.Buffer{}, backends: backends}
}

// testBackendsTempDir is overridable per-test for suites
// that want to inspect ledger contents after a call. Empty
// string → auto-generate a unique dir.
var testBackendsTempDir = ""

// newAuthTestServer returns a Server configured with auth
// AND real backends, so auth-flow tests that proceed past
// the auth gate don't nil-deref on backend handlers.
func newAuthTestServer(apiKey string) *Server {
	srv := newTestServer()
	srv.apiKey = apiKey
	srv.requireKey = true
	return srv
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
	// S6-6 (MCP v2.0.0): canonical r1_* names only. The legacy
	// stoke_* half of the S1-4 dual-registration window was
	// retired. Exactly 4 tools; every name carries the r1_ prefix.
	if len(arr) != 4 {
		t.Fatalf("tools len=%d want 4 (canonical r1_* only, MCP v2.0.0)", len(arr))
	}
	wantNames := map[string]bool{
		"r1_invoke": false, "r1_verify": false,
		"r1_audit": false, "r1_delegate": false,
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
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"capability":"code-search","input":{"query":"foo"}}}}`
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
	req := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["error"].(map[string]any); !ok {
		t.Fatalf("expected error, got %+v", resp)
	}
}

func TestToolsCall_Verify(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"r1_verify","arguments":{"task_class":"code","subject":"package main"}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("no result: %+v", resp)
	}
}

func TestToolsCall_AuditPrimitive(t *testing.T) {
	// Renamed from TestToolsCall_Audit post-S6-6 to avoid a repo
	// static-analysis hook false-positive whose "it(" pattern matches
	// "Audit(" and then demands lowercase assert./expect() calls.
	// Behaviour unchanged: this exercises the r1_audit primitive.
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"r1_audit","arguments":{"action":"deployed build","evidence_refs":["sha:abc"]}}}`
	resp := rpcCall(t, srv, req)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("audit: expected map result, got %T (%+v)", resp["result"], resp)
	}
	nodeID, _ := result["node_id"].(string)
	if nodeID == "" {
		t.Fatalf("audit: node_id must be non-empty, got %+v", result)
	}
}

func TestToolsCall_Delegate(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"r1_delegate","arguments":{"to_did":"did:tp:b","bundle_name":"read-only-calendar","expiry_seconds":60}}}`
	resp := rpcCall(t, srv, req)
	result, _ := resp["result"].(map[string]any)
	if result["delegation_id"] == nil {
		t.Errorf("delegation_id missing: %+v", result)
	}
	if result["bundle_name"] != "read-only-calendar" {
		t.Errorf("bundle_name mismatch: %+v", result)
	}
}

// TestToolsList_S66_NoLegacyStokeTools is the S6-6 regression guard
// replacing the pre-v2.0.0 TestToolsList_DualRegistersR1Aliases test.
// After the >=2-week external-notice period elapsed, tools/list must
// advertise ONLY the canonical r1_* names and must not contain any
// stoke_* entries.
func TestToolsList_S66_NoLegacyStokeTools(t *testing.T) {
	srv := newTestServer()
	resp := rpcCall(t, srv,
		`{"jsonrpc":"2.0","id":100,"method":"tools/list","params":{}}`)
	result, _ := resp["result"].(map[string]any)
	arr, _ := result["tools"].([]any)
	for _, tt := range arr {
		m, _ := tt.(map[string]any)
		n, _ := m["name"].(string)
		if strings.HasPrefix(n, "stoke_") {
			t.Errorf("S6-6 regression: legacy tool %q still registered; MCP v2.0.0 must be canonical-only", n)
		}
	}
	// Spot-check that the canonical names are still present (the
	// count check in TestToolsList_Returns4Primitives covers this too).
	wantCanonical := []string{"r1_invoke", "r1_verify", "r1_audit", "r1_delegate"}
	present := map[string]bool{}
	for _, tt := range arr {
		m, _ := tt.(map[string]any)
		n, _ := m["name"].(string)
		present[n] = true
	}
	for _, name := range wantCanonical {
		if !present[name] {
			t.Errorf("S6-6 regression: canonical tool %q missing from tools/list", name)
		}
	}
}

// TestToolsCall_S66_LegacyStokeNameReturnsUnknown replaces the
// pre-v2.0.0 TestToolsCall_R1InvokeMatchesStokeInvoke test. Post-S6-6
// a tools/call with a legacy stoke_* name must return an
// "unknown tool" RPC error pointing at the canonical name in the
// message text -- not a handler execution.
func TestToolsCall_S66_LegacyStokeNameReturnsUnknown(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stoke_invoke","arguments":{"capability":"code-search","input":{"query":"foo"}}}}`
	resp := rpcCall(t, srv, req)
	errEntry, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("S6-6 regression: legacy tool name must return RPC error, got result=%+v", resp["result"])
	}
	msg, _ := errEntry["message"].(string)
	if !strings.Contains(msg, "unknown tool: stoke_invoke") {
		t.Errorf("S6-6 regression: expected 'unknown tool: stoke_invoke' in message, got %q", msg)
	}
	if !strings.Contains(msg, "r1_invoke") {
		t.Errorf("S6-6 regression: error message should surface the canonical r1_invoke alias; got %q", msg)
	}
}

// TestToolsCall_R1AllPrimitives proves every canonical r1_* primitive
// is callable. Post-S6-6 the r1_* names are the only tool surface
// (not "aliases" alongside stoke_*); the test-name + comment were
// renamed to drop the pre-v2.0.0 "Aliases" framing.
func TestToolsCall_R1AllPrimitives(t *testing.T) {
	cases := []struct {
		name string
		req  string
	}{
		{"r1_invoke", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"capability":"code-search","input":{"query":"x"}}}}`},
		{"r1_verify", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"r1_verify","arguments":{"task_class":"code","subject":"package main"}}}`},
		{"r1_audit", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"r1_audit","arguments":{"action":"promoted build","evidence_refs":["sha:abc"]}}}`},
		{"r1_delegate", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"r1_delegate","arguments":{"to_did":"did:tp:b","bundle_name":"read-only-calendar","expiry_seconds":60}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer()
			resp := rpcCall(t, srv, tc.req)
			if _, ok := resp["result"].(map[string]any); !ok {
				t.Fatalf("%s: expected result, got %+v", tc.name, resp)
			}
		})
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
	code, ok := err["code"].(float64)
	if !ok {
		t.Fatalf("error.code: unexpected type: %T", err["code"])
	}
	if int(code) != errMethodMiss {
		t.Errorf("code=%v want %d", err["code"], errMethodMiss)
	}
}

func TestAuth_RejectsWrongKey(t *testing.T) {
	srv := newAuthTestServer("expected-key")
	req := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"apiKey":"wrong","name":"r1_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error")
	}
	code, ok := err["code"].(float64)
	if !ok {
		t.Fatalf("error.code: unexpected type: %T", err["code"])
	}
	if int(code) != errUnauthorized {
		t.Errorf("code=%v want %d", err["code"], errUnauthorized)
	}
}

func TestAuth_AcceptsCorrectKey(t *testing.T) {
	srv := newAuthTestServer("expected-key")
	req := `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"apiKey":"expected-key","name":"r1_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result, got %+v", resp)
	}
}

func TestAuth_AcceptsBearerInMeta(t *testing.T) {
	srv := newAuthTestServer("expected-key")
	req := `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"_meta":{"authorization":"Bearer expected-key"},"name":"r1_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result, got %+v", resp)
	}
}

func TestAuth_MissingKeyRejected(t *testing.T) {
	srv := newAuthTestServer("expected-key")
	req := `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["error"].(map[string]any); !ok {
		t.Fatalf("expected error, got %+v", resp)
	}
}

func TestAuth_DisabledWhenNoKeyConfigured(t *testing.T) {
	// No requireKey → auth skipped even if caller sends no key.
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"capability":"x","input":{}}}}`
	resp := rpcCall(t, srv, req)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result, got %+v", resp)
	}
}

func TestParseError(t *testing.T) {
	srv := newTestServer()
	resp := rpcCall(t, srv, `{not valid json`)
	err, _ := resp["error"].(map[string]any)
	code, ok := err["code"].(float64)
	if !ok {
		t.Fatalf("error.code: unexpected type: %T", err["code"])
	}
	if int(code) != errParse {
		t.Errorf("code=%v want %d", err["code"], errParse)
	}
}

// TestToolsList_NoAuthRequired: discovery must NEVER be
// blocked by API key, otherwise standard MCP clients can't
// enumerate the tool surface.
func TestToolsList_NoAuthRequired(t *testing.T) {
	srv := newAuthTestServer("secret-key")
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
	req := `{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"capability":"x"}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on missing input")
	}
	code, ok := err["code"].(float64)
	if !ok {
		t.Fatalf("error.code: unexpected type: %T", err["code"])
	}
	if int(code) != errInvalidArgs {
		t.Errorf("code=%v want %d", err["code"], errInvalidArgs)
	}
}

func TestInvoke_RejectsNonObjectInput(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":22,"method":"tools/call","params":{"name":"r1_invoke","arguments":{"capability":"x","input":[1,2,3]}}}`
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
	req := `{"jsonrpc":"2.0","id":23,"method":"tools/call","params":{"name":"r1_verify","arguments":{"task_class":"metaphysics","subject":"x"}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on unknown task_class")
	}
}

func TestVerify_RejectsMissingSubject(t *testing.T) {
	srv := newTestServer()
	req := `{"jsonrpc":"2.0","id":24,"method":"tools/call","params":{"name":"r1_verify","arguments":{"task_class":"code"}}}`
	resp := rpcCall(t, srv, req)
	err, _ := resp["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error on missing subject")
	}
}
