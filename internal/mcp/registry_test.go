package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClient is a drop-in Client for registry unit tests. Every
// method is no-op by default; tests seed tools / errors via public
// fields. Safe for concurrent use via the embedded mutex.
type fakeClient struct {
	name string

	mu          sync.Mutex
	tools       []Tool
	callErr     error
	callResult  ToolResult
	callCount   atomic.Int64
	listErr     error
	initErr     error
	closeCalled atomic.Bool
}

func (f *fakeClient) Initialize(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.initErr
}

func (f *fakeClient) ListTools(ctx context.Context) ([]Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Tool, len(f.tools))
	copy(out, f.tools)
	return out, nil
}

func (f *fakeClient) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	f.callCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callErr != nil {
		return ToolResult{}, f.callErr
	}
	return f.callResult, nil
}

func (f *fakeClient) Close() error {
	f.closeCalled.Store(true)
	return nil
}

// installFakeFactory swaps the transport factory to return the
// caller-supplied fake clients keyed by server name. Registers a
// cleanup so the override is restored on test exit.
func installFakeFactory(t *testing.T, byName map[string]*fakeClient) {
	t.Helper()
	SetTransportFactory(func(cfg ServerConfig) (Client, error) {
		if c, ok := byName[cfg.Name]; ok {
			c.name = cfg.Name
			return c, nil
		}
		return nil, errors.New("no fake client registered for " + cfg.Name)
	})
	t.Cleanup(func() { SetTransportFactory(nil) })
}

// --- AllTools / AllToolsForTrust ---------------------------------

func TestRegistry_AllTools_AggregatesAcrossServers(t *testing.T) {
	a := &fakeClient{tools: []Tool{
		{Definition: ToolDefinition{Name: "list"}, ServerName: "alpha", Trust: TrustUntrusted},
	}}
	b := &fakeClient{tools: []Tool{
		{Definition: ToolDefinition{Name: "create"}, ServerName: "beta", Trust: TrustTrusted},
	}}
	installFakeFactory(t, map[string]*fakeClient{"alpha": a, "beta": b})

	cfg := Config{Servers: []ServerConfig{
		{Name: "alpha", Transport: "stdio", Command: "x", Trust: TrustUntrusted},
		{Name: "beta", Transport: "stdio", Command: "x", Trust: TrustTrusted},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	tools, err := r.AllTools(context.Background())
	if err != nil {
		t.Fatalf("AllTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}
}

func TestRegistry_AllToolsForTrust_FiltersMatrix(t *testing.T) {
	untrusted := &fakeClient{tools: []Tool{
		{Definition: ToolDefinition{Name: "list"}, ServerName: "alpha", Trust: TrustUntrusted},
	}}
	trusted := &fakeClient{tools: []Tool{
		{Definition: ToolDefinition{Name: "create"}, ServerName: "beta", Trust: TrustTrusted},
	}}
	installFakeFactory(t, map[string]*fakeClient{
		"alpha": untrusted,
		"beta":  trusted,
	})
	cfg := Config{Servers: []ServerConfig{
		{Name: "alpha", Transport: "stdio", Command: "x", Trust: TrustUntrusted},
		{Name: "beta", Transport: "stdio", Command: "x", Trust: TrustTrusted},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	// Trusted worker sees everything.
	trToolsForTrusted, err := r.AllToolsForTrust(context.Background(), TrustTrusted)
	if err != nil {
		t.Fatalf("AllToolsForTrust(trusted): %v", err)
	}
	if len(trToolsForTrusted) != 2 {
		t.Fatalf("trusted worker expected 2 tools, got %d", len(trToolsForTrusted))
	}

	// Untrusted worker only sees untrusted-server tools.
	trToolsForUntrusted, err := r.AllToolsForTrust(context.Background(), TrustUntrusted)
	if err != nil {
		t.Fatalf("AllToolsForTrust(untrusted): %v", err)
	}
	if len(trToolsForUntrusted) != 1 {
		t.Fatalf("untrusted worker expected 1 tool, got %d: %+v", len(trToolsForUntrusted), trToolsForUntrusted)
	}
	if trToolsForUntrusted[0].ServerName != "alpha" {
		t.Fatalf("untrusted worker should see only alpha, got %q", trToolsForUntrusted[0].ServerName)
	}

	// Empty trust label = treated as untrusted.
	trToolsForEmpty, err := r.AllToolsForTrust(context.Background(), "")
	if err != nil {
		t.Fatalf("AllToolsForTrust(empty): %v", err)
	}
	if len(trToolsForEmpty) != 1 {
		t.Fatalf("empty trust should behave like untrusted (1 tool), got %d", len(trToolsForEmpty))
	}

	// Unknown trust label = treated as untrusted (fail-closed).
	trToolsForBogus, err := r.AllToolsForTrust(context.Background(), "superuser")
	if err != nil {
		t.Fatalf("AllToolsForTrust(bogus): %v", err)
	}
	if len(trToolsForBogus) != 1 {
		t.Fatalf("unknown trust should fail-closed to untrusted, got %d tools", len(trToolsForBogus))
	}
}

// --- Call -------------------------------------------------------

func TestRegistry_Call_HappyPath(t *testing.T) {
	client := &fakeClient{
		callResult: ToolResult{Content: []Content{{Type: "text", Text: "ok"}}},
	}
	installFakeFactory(t, map[string]*fakeClient{"github": client})
	cfg := Config{Servers: []ServerConfig{
		{Name: "github", Transport: "stdio", Command: "x", Trust: TrustUntrusted},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	res, err := r.Call(context.Background(), "mcp_github_list_issues", TrustUntrusted, []byte(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "ok" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if client.callCount.Load() != 1 {
		t.Fatalf("expected 1 CallTool invocation, got %d", client.callCount.Load())
	}
}

func TestRegistry_Call_UnknownServer(t *testing.T) {
	installFakeFactory(t, map[string]*fakeClient{"github": {}})
	cfg := Config{Servers: []ServerConfig{
		{Name: "github", Transport: "stdio", Command: "x"},
	}}
	r, _ := NewRegistry(cfg, nil)
	defer r.Close()

	_, err := r.Call(context.Background(), "mcp_linear_list_issues", TrustUntrusted, nil)
	if err == nil {
		t.Fatal("expected error for unknown server, got nil")
	}
	if !strings.Contains(err.Error(), "unknown server") {
		t.Fatalf("expected descriptive 'unknown server' error, got: %v", err)
	}
}

func TestRegistry_Call_InvalidFullName(t *testing.T) {
	r, _ := NewRegistry(Config{}, nil)
	defer r.Close()
	cases := []string{
		"github_list_issues",      // missing mcp_ prefix
		"mcp_github",              // no tool segment
		"mcp_github_",             // empty tool
		"mcp__tool",               // empty server
		"mcp_",                    // nothing after prefix
	}
	for _, name := range cases {
		_, err := r.Call(context.Background(), name, TrustTrusted, nil)
		if err == nil {
			t.Errorf("Call(%q) should have errored but did not", name)
		}
	}
}

func TestRegistry_Call_PolicyDenied(t *testing.T) {
	client := &fakeClient{}
	installFakeFactory(t, map[string]*fakeClient{"github": client})
	cfg := Config{Servers: []ServerConfig{
		{Name: "github", Transport: "stdio", Command: "x", Trust: TrustTrusted},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	_, err = r.Call(context.Background(), "mcp_github_create_pr", TrustUntrusted, []byte(`{}`))
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("expected ErrPolicyDenied, got: %v", err)
	}
	if client.callCount.Load() != 0 {
		t.Fatalf("policy-denied call must NOT reach the client; CallTool hit %d times", client.callCount.Load())
	}
}

func TestRegistry_Call_CircuitOpen(t *testing.T) {
	client := &fakeClient{}
	installFakeFactory(t, map[string]*fakeClient{"github": client})
	cfg := Config{Servers: []ServerConfig{
		{Name: "github", Transport: "stdio", Command: "x", Trust: TrustUntrusted},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	// Force the circuit open by hammering OnFailure past threshold.
	c := r.circuits["github"]
	for i := 0; i < 10; i++ {
		c.OnFailure()
	}
	if c.State() != StateOpen {
		t.Fatalf("circuit should be open, got %s", c.State())
	}

	_, err = r.Call(context.Background(), "mcp_github_list_issues", TrustUntrusted, nil)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got: %v", err)
	}
	if client.callCount.Load() != 0 {
		t.Fatalf("circuit-open call must NOT reach client; CallTool hit %d times", client.callCount.Load())
	}
}

func TestRegistry_Call_TransportFailureIncrementsCircuit(t *testing.T) {
	// assert.Equal-style assertions below via t.Fatalf.
	client := &fakeClient{callErr: errors.New("network boom")}
	installFakeFactory(t, map[string]*fakeClient{"s": client})
	cfg := Config{Servers: []ServerConfig{
		{Name: "s", Transport: "stdio", Command: "x"},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err) // assert.NoError
	}
	defer r.Close()

	// 5 failures to trip the default threshold — assert each Call errors.
	var seenErrors int
	for i := 0; i < 5; i++ {
		_, callErr := r.Call(context.Background(), "mcp_s_do", TrustUntrusted, nil)
		if callErr == nil {
			t.Fatalf("call %d: assert.Error got nil", i)
		}
		seenErrors++
	}
	if seenErrors != 5 {
		t.Fatalf("assert.Equal errors: want 5 got %d", seenErrors)
	}
	if got := r.circuits["s"].State(); got != StateOpen {
		t.Fatalf("assert.Equal state: want open, got %s", got)
	}
	if got := client.callCount.Load(); got != 5 {
		t.Fatalf("assert.Equal calls: want 5, got %d", got)
	}
}

// --- Lifecycle --------------------------------------------------

func TestRegistry_CloseIdempotent(t *testing.T) {
	a := &fakeClient{}
	b := &fakeClient{}
	installFakeFactory(t, map[string]*fakeClient{"a": a, "b": b})
	cfg := Config{Servers: []ServerConfig{
		{Name: "a", Transport: "stdio", Command: "x"},
		{Name: "b", Transport: "stdio", Command: "x"},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if !a.closeCalled.Load() || !b.closeCalled.Load() {
		t.Fatalf("Close must fan out to every client")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close should be nil, got %v", err)
	}
	// Post-close: AllTools / Call should refuse.
	if _, err := r.AllTools(context.Background()); err == nil {
		t.Fatalf("AllTools after Close should error")
	}
	if _, err := r.Call(context.Background(), "mcp_a_x", TrustTrusted, nil); err == nil {
		t.Fatalf("Call after Close should error")
	}
}

func TestRegistry_PartialStartupOnInitFailure(t *testing.T) {
	good := &fakeClient{}
	bad := &fakeClient{initErr: errors.New("initialize failed")}
	installFakeFactory(t, map[string]*fakeClient{"good": good, "bad": bad})
	cfg := Config{Servers: []ServerConfig{
		{Name: "good", Transport: "stdio", Command: "x"},
		{Name: "bad", Transport: "stdio", Command: "x"},
	}}
	r, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry should NOT fail on per-server init failure: %v", err)
	}
	defer r.Close()

	// Bad server: client not registered, circuit open.
	if _, ok := r.clients["bad"]; ok {
		t.Errorf("failed-init server should have no live client")
	}
	if got := r.circuits["bad"].State(); got != StateOpen {
		t.Errorf("failed-init server circuit should be open, got %s", got)
	}
	// Good server: client live, circuit closed.
	if _, ok := r.clients["good"]; !ok {
		t.Errorf("healthy server should have a live client")
	}
	if got := r.circuits["good"].State(); got != StateClosed {
		t.Errorf("healthy server circuit should be closed, got %s", got)
	}
}

func TestRegistry_DuplicateNameRejected(t *testing.T) {
	cfg := Config{Servers: []ServerConfig{
		{Name: "dup", Transport: "stdio", Command: "x"},
		{Name: "dup", Transport: "stdio", Command: "y"},
	}}
	_, err := NewRegistry(cfg, nil)
	if err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-name message, got: %v", err)
	}
}

func TestRegistry_Health(t *testing.T) {
	installFakeFactory(t, map[string]*fakeClient{"s": {}})
	cfg := Config{Servers: []ServerConfig{{Name: "s", Transport: "stdio", Command: "x"}}}
	r, _ := NewRegistry(cfg, nil)
	defer r.Close()

	h := r.Health()
	if h["s"] != "closed" {
		t.Fatalf("expected Health[s]=closed, got %q", h["s"])
	}
}

// --- splitFullName ---------------------------------------------

func TestSplitFullName(t *testing.T) {
	cases := []struct {
		in       string
		server   string
		tool     string
		ok       bool
	}{
		{"mcp_github_list_issues", "github", "list_issues", true},
		{"mcp_linear_get", "linear", "get", true},
		// tools may have multiple underscores — everything after
		// the FIRST post-prefix underscore belongs to the tool.
		{"mcp_a_b_c_d", "a", "b_c_d", true},
		{"mcp__tool", "", "", false},
		{"mcp_server_", "", "", false},
		{"mcp_", "", "", false},
		{"mcp_server", "", "", false},
		{"github_list_issues", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		server, tool, ok := splitFullName(tc.in)
		if ok != tc.ok || server != tc.server || tool != tc.tool {
			t.Errorf("splitFullName(%q) = (%q, %q, %t); want (%q, %q, %t)",
				tc.in, server, tool, ok, tc.server, tc.tool, tc.ok)
		}
	}
}

// --- Events -----------------------------------------------------

func TestRegistry_Call_EmitsStartComplete(t *testing.T) {
	client := &fakeClient{callResult: ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}}
	installFakeFactory(t, map[string]*fakeClient{"s": client})

	b, getEvents, teardown := setupBus(t)
	defer teardown()
	emitter := NewEmitter(b, nil)

	cfg := Config{Servers: []ServerConfig{{Name: "s", Transport: "stdio", Command: "x"}}}
	r, err := NewRegistry(cfg, emitter)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	if _, err := r.Call(context.Background(), "mcp_s_op", TrustUntrusted, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	// Give the bus goroutine a moment to dispatch.
	time.Sleep(50 * time.Millisecond)

	evs := getEvents()
	var sawStart, sawComplete bool
	for _, e := range evs {
		switch e.Type {
		case EvtMCPCallStart:
			sawStart = true
		case EvtMCPCallComplete:
			sawComplete = true
		}
	}
	if !sawStart || !sawComplete {
		t.Fatalf("expected start + complete events; saw start=%v complete=%v (all: %+v)", sawStart, sawComplete, evs)
	}
}

func TestRegistry_Call_PolicyDenied_EmitsError(t *testing.T) {
	installFakeFactory(t, map[string]*fakeClient{"s": {}})

	b, getEvents, teardown := setupBus(t)
	defer teardown()
	emitter := NewEmitter(b, nil)

	cfg := Config{Servers: []ServerConfig{{Name: "s", Transport: "stdio", Command: "x", Trust: TrustTrusted}}}
	r, _ := NewRegistry(cfg, emitter)
	defer r.Close()

	_, err := r.Call(context.Background(), "mcp_s_op", TrustUntrusted, nil)
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("expected ErrPolicyDenied, got %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	evs := getEvents()
	var payload map[string]any
	for _, e := range evs {
		if e.Type == EvtMCPCallError {
			_ = json.Unmarshal(e.Payload, &payload)
			break
		}
	}
	if payload == nil {
		t.Fatalf("expected mcp.call.error event, got: %+v", evs)
	}
	if got, _ := payload["err_kind"].(string); got != ErrKindPolicyDenied {
		t.Fatalf("expected err_kind=%s, got %q", ErrKindPolicyDenied, got)
	}
}
