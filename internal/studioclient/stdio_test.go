package studioclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/mcp"
)

// fakeStdioClient implements mcp.Client for deterministic testing.
// Every method is driven by knobs set by the test before Invoke.
type fakeStdioClient struct {
	mu               sync.Mutex
	initErr          error
	callErr          error
	callResult       mcp.ToolResult
	closed           int32
	initCount        int32
	callCount        int32
	lastCalledTool   string
	lastCalledArgs   json.RawMessage
	crashAfterInit   bool
}

func (f *fakeStdioClient) Initialize(_ context.Context) error {
	atomic.AddInt32(&f.initCount, 1)
	return f.initErr
}

func (f *fakeStdioClient) ListTools(_ context.Context) ([]mcp.Tool, error) {
	return nil, nil
}

func (f *fakeStdioClient) CallTool(_ context.Context, name string, args json.RawMessage) (mcp.ToolResult, error) {
	f.mu.Lock()
	f.lastCalledTool = name
	f.lastCalledArgs = args
	f.mu.Unlock()
	atomic.AddInt32(&f.callCount, 1)
	if f.crashAfterInit {
		return mcp.ToolResult{}, errors.New("subprocess died")
	}
	if f.callErr != nil {
		return mcp.ToolResult{}, f.callErr
	}
	return f.callResult, nil
}

func (f *fakeStdioClient) Close() error {
	atomic.StoreInt32(&f.closed, 1)
	return nil
}

func stdioCfg() config.StudioConfig {
	return config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportStdioMCP,
		StdioMCP:  config.StudioStdioMCPConfig{Command: []string{"echo", "fake"}},
	}
}

func TestStdioTransport_Name(t *testing.T) {
	tr := &StdioMCPTransport{}
	if tr.Name() != "stdio-mcp" {
		t.Errorf("Name = %q", tr.Name())
	}
}

func TestStdioTransport_InvokeSuccess_ResultText(t *testing.T) {
	fake := &fakeStdioClient{
		callResult: mcp.ToolResult{Content: []mcp.Content{
			{Type: "text", Text: `{"site_id":"s-1","status":"ready","steps":[]}`},
		}},
	}
	tr, err := NewStdioMCPTransport(stdioCfg(), nil)
	if err != nil {
		t.Fatalf("NewStdioMCPTransport: %v", err)
	}
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return fake, nil }

	body, err := tr.Invoke(context.Background(), "studio.scaffold_site", map[string]any{"brief": "xxxxxxxxxx", "brand": map[string]any{"name": "Y"}})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var parsed struct{ SiteID string `json:"site_id"` }
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.SiteID != "s-1" {
		t.Errorf("site_id = %q", parsed.SiteID)
	}
	if fake.lastCalledTool != "studio_scaffold_site" {
		t.Errorf("tool sent = %q, want studio_scaffold_site (prefix-mapping)", fake.lastCalledTool)
	}
}

func TestStdioTransport_InvokeSuccess_ThinSkillNameMapping(t *testing.T) {
	fake := &fakeStdioClient{callResult: mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "[]"}}}}
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return fake, nil }
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if fake.lastCalledTool != "list_sites" {
		t.Errorf("tool = %q, want list_sites (studio. prefix stripped)", fake.lastCalledTool)
	}
}

func TestStdioTransport_CompositeHeroes_RejectedValidation(t *testing.T) {
	// Composite heroes (update_content etc.) must fail with Validation
	// under stdio-MCP per skillToStudioToolName switch.
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) {
		return &fakeStdioClient{}, nil
	}
	for _, composite := range []string{"studio.update_content", "studio.publish", "studio.diff_versions", "studio.list_templates", "studio.site_status"} {
		_, err := tr.Invoke(context.Background(), composite, nil)
		if !errors.Is(err, ErrStudioValidation) {
			t.Errorf("composite %q: err = %v, want ErrStudioValidation", composite, err)
		}
	}
}

func TestStdioTransport_UnknownSkill_Validation(t *testing.T) {
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return &fakeStdioClient{}, nil }
	_, err := tr.Invoke(context.Background(), "not.a.studio.tool", nil)
	if !errors.Is(err, ErrStudioValidation) {
		t.Fatalf("err = %v, want ErrStudioValidation", err)
	}
}

func TestStdioTransport_IsErrorResult_Validation(t *testing.T) {
	fake := &fakeStdioClient{
		callResult: mcp.ToolResult{
			IsError: true,
			Content: []mcp.Content{{Type: "text", Text: "brief too short"}},
		},
	}
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return fake, nil }
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioValidation) {
		t.Fatalf("err = %v, want ErrStudioValidation", err)
	}
	var se *StudioError
	if !errors.As(err, &se) || !strings.Contains(se.BodyExcerpt, "brief too short") {
		t.Errorf("body excerpt missing: %v", err)
	}
}

func TestStdioTransport_CallErr_UnavailableAndRespawn(t *testing.T) {
	// First call: crash. Second call: factory returns new fake, success.
	crashFake := &fakeStdioClient{crashAfterInit: true}
	okFake := &fakeStdioClient{callResult: mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "{}"}}}}
	calls := int32(0)
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return crashFake, nil
		}
		return okFake, nil
	}

	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioUnavailable) {
		t.Fatalf("first call err = %v, want ErrStudioUnavailable", err)
	}
	// Second call should respawn.
	_, err = tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("factory calls = %d, want 2 (spawn + respawn)", calls)
	}
	if atomic.LoadInt32(&crashFake.closed) == 0 {
		t.Errorf("dead client not closed after crash")
	}
}

func TestStdioTransport_DisablesAfter3Crashes(t *testing.T) {
	calls := int32(0)
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) {
		atomic.AddInt32(&calls, 1)
		return &fakeStdioClient{crashAfterInit: true}, nil
	}
	for i := 0; i < 4; i++ {
		_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
		if err == nil {
			t.Fatalf("iteration %d: no error, want unavailable", i)
		}
	}
	// After the 3rd in-flight crash, ensureClient should stop respawning.
	if atomic.LoadInt32(&calls) > 3 {
		t.Errorf("factory called %d times, want ≤ 3", calls)
	}
}

func TestStdioTransport_InitErr_Unavailable(t *testing.T) {
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) {
		return &fakeStdioClient{initErr: errors.New("handshake failed")}, nil
	}
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioUnavailable) {
		t.Fatalf("err = %v, want ErrStudioUnavailable", err)
	}
}

func TestStdioTransport_FactoryErr_Unavailable(t *testing.T) {
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) {
		return nil, errors.New("spawn failed")
	}
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioUnavailable) {
		t.Fatalf("err = %v, want ErrStudioUnavailable", err)
	}
}

func TestStdioTransport_AuthMissingMapping(t *testing.T) {
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	fake := &fakeStdioClient{callErr: mcp.ErrAuthMissing}
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return fake, nil }
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioAuth) {
		t.Fatalf("err = %v, want ErrStudioAuth", err)
	}
}

func TestStdioTransport_PolicyDeniedMapping(t *testing.T) {
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	fake := &fakeStdioClient{callErr: mcp.ErrPolicyDenied}
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return fake, nil }
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioScope) {
		t.Fatalf("err = %v, want ErrStudioScope", err)
	}
}

func TestStdioTransport_Close_Idempotent(t *testing.T) {
	fake := &fakeStdioClient{callResult: mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "{}"}}}}
	tr, _ := NewStdioMCPTransport(stdioCfg(), nil)
	tr.clientFactory = func(_ config.StudioStdioMCPConfig) (mcp.Client, error) { return fake, nil }
	// Force a spawn.
	_, _ = tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err := tr.Close(); err != nil {
		t.Errorf("Close 1: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("Close 2: %v", err)
	}
	if atomic.LoadInt32(&fake.closed) == 0 {
		t.Errorf("fake never closed")
	}
	// Post-close Invoke must refuse.
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioDisabled) {
		t.Errorf("post-close Invoke err = %v, want ErrStudioDisabled", err)
	}
}

func TestStdioTransport_NewStdioMCPTransport_WrongTransport(t *testing.T) {
	cfg := config.StudioConfig{Enabled: true, Transport: config.StudioTransportHTTP, HTTP: config.StudioHTTPConfig{BaseURL: "http://x"}}
	_, err := NewStdioMCPTransport(cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("err = %v, want http-transport diagnostic", err)
	}
}

func TestStdioTransport_NewStdioMCPTransport_Disabled(t *testing.T) {
	_, err := NewStdioMCPTransport(config.StudioConfig{Enabled: false}, nil)
	if !errors.Is(err, ErrStudioDisabled) {
		t.Fatalf("err = %v, want ErrStudioDisabled", err)
	}
}

func TestSkillToStudioToolName(t *testing.T) {
	cases := []struct {
		skill string
		want  string
		ok    bool
	}{
		{"studio.scaffold_site", "studio_scaffold_site", true},
		{"studio.get_scaffold_status", "studio_get_scaffold_status", true},
		{"studio.list_sites", "list_sites", true},
		{"studio.get_page", "get_page", true},
		{"studio.update_content", "", false}, // composite
		{"studio.publish", "", false},        // composite
		{"foo.bar", "", false},
		{"studio.", "", false},
	}
	for _, tc := range cases {
		got, ok := skillToStudioToolName(tc.skill)
		if got != tc.want || ok != tc.ok {
			t.Errorf("skillToStudioToolName(%q) = (%q, %v), want (%q, %v)", tc.skill, got, ok, tc.want, tc.ok)
		}
	}
}

func TestResolve_PicksHTTP(t *testing.T) {
	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP:      config.StudioHTTPConfig{BaseURL: "https://x"},
	}
	tr, err := Resolve(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tr.Name() != "http" {
		t.Errorf("Name = %q, want http", tr.Name())
	}
}

func TestResolve_PicksStdio(t *testing.T) {
	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportStdioMCP,
		StdioMCP:  config.StudioStdioMCPConfig{Command: []string{"echo"}},
	}
	tr, err := Resolve(cfg, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tr.Name() != "stdio-mcp" {
		t.Errorf("Name = %q, want stdio-mcp", tr.Name())
	}
}

func TestResolve_DisabledReturnsErrDisabled(t *testing.T) {
	_, err := Resolve(config.StudioConfig{Enabled: false}, nil, nil)
	if !errors.Is(err, ErrStudioDisabled) {
		t.Fatalf("err = %v, want ErrStudioDisabled", err)
	}
}

func TestResolve_UnknownTransport(t *testing.T) {
	cfg := config.StudioConfig{Enabled: true, Transport: "grpc"}
	_, err := Resolve(cfg, nil, nil)
	// Validate() rejects unknown transport, so Resolve returns that.
	if err == nil || !strings.Contains(err.Error(), "grpc") {
		t.Fatalf("err = %v, want grpc diagnostic", err)
	}
}
