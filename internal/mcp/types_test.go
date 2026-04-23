package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestClientInterfaceShape is a compile-time + behavioral check that the
// Client interface from types.go has exactly the four methods the spec
// (specs/mcp-client.md §Data Models) enumerates, and that a concrete
// implementation satisfies it. A change to the interface shape (adding
// a method, renaming one, changing a signature) will break this test.
func TestClientInterfaceShape(t *testing.T) {
	var c Client = (*stubClient)(nil)
	// Exercise every method. The stub is a nil-safe sentinel — the
	// receivers on stubClient use value-typed returns so calling on a
	// nil pointer doesn't panic.
	ctx := context.Background()
	stub := &stubClient{tools: []Tool{{Definition: ToolDefinition{Name: "t"}, ServerName: "s"}}}
	c = stub
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Definition.Name != "t" {
		t.Fatalf("ListTools returned wrong payload: %+v", tools)
	}
	res, err := c.CallTool(ctx, "noop", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected non-error result, got %+v", res)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

type stubClient struct {
	tools []Tool
}

func (s *stubClient) Initialize(context.Context) error { return nil }
func (s *stubClient) ListTools(context.Context) ([]Tool, error) {
	return s.tools, nil
}
func (s *stubClient) CallTool(context.Context, string, json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
}
func (s *stubClient) Close() error { return nil }

// TestServerConfigYAMLRoundTrip asserts the new canonical ServerConfig
// parses every field documented in specs/mcp-client.md §Data Models from
// YAML and that field tags match the operator-facing schema (not JSON).
func TestServerConfigYAMLRoundTrip(t *testing.T) {
	src := `name: github
transport: http
command: ""
args: []
url: https://api.githubcopilot.com/mcp/
auth_env: GITHUB_MCP_TOKEN
env:
  EXTRA: one
trust: untrusted
max_concurrent: 4
timeout: 30s
enabled: true
`
	var got ServerConfig
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	want := ServerConfig{
		Name:          "github",
		Transport:     "http",
		URL:           "https://api.githubcopilot.com/mcp/",
		AuthEnv:       "GITHUB_MCP_TOKEN",
		Env:           map[string]string{"EXTRA": "one"},
		Trust:         "untrusted",
		MaxConcurrent: 4,
		Timeout:       "30s",
		Enabled:       true,
		Args:          []string{},
	}
	if got.Name != want.Name ||
		got.Transport != want.Transport ||
		got.URL != want.URL ||
		got.AuthEnv != want.AuthEnv ||
		got.Trust != want.Trust ||
		got.MaxConcurrent != want.MaxConcurrent ||
		got.Timeout != want.Timeout ||
		got.Enabled != want.Enabled {
		t.Fatalf("ServerConfig YAML mismatch:\n got: %+v\nwant: %+v", got, want)
	}
	if got.Env["EXTRA"] != "one" {
		t.Fatalf("Env map not parsed: %+v", got.Env)
	}
}

// TestToolDefinitionJSONShape confirms the ToolDefinition JSON tags
// match what MCP servers send on the wire (input_schema, not inputSchema;
// description omitempty). Server-side code (CodebaseServer, StokeServer)
// depends on this shape — regressing it would break every MCP client.
func TestToolDefinitionJSONShape(t *testing.T) {
	td := ToolDefinition{
		Name:        "search_symbols",
		Description: "find symbols",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	raw, err := json.Marshal(td)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Round-trip to assert the tags resolve to exactly these keys.
	var back map[string]json.RawMessage
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := back["name"]; !ok {
		t.Errorf("missing name tag: %s", raw)
	}
	if _, ok := back["description"]; !ok {
		t.Errorf("missing description tag: %s", raw)
	}
	if _, ok := back["input_schema"]; !ok {
		t.Errorf("expected input_schema tag (snake_case per MCP spec), got: %s", raw)
	}
	// omitempty on description
	td2 := ToolDefinition{Name: "n", InputSchema: json.RawMessage(`{}`)}
	raw2, _ := json.Marshal(td2)
	var back2 map[string]json.RawMessage
	_ = json.Unmarshal(raw2, &back2)
	if _, ok := back2["description"]; ok {
		t.Errorf("description should be omitempty when zero, got: %s", raw2)
	}
}

// TestErrorSentinels asserts every error sentinel in types.go is a
// distinct, non-nil value and that errors.Is works. Downstream
// consumers (transport, registry, CLI) are expected to errors.Is
// against these; a nil or collapsed sentinel would silently degrade
// error classification.
func TestErrorSentinels(t *testing.T) {
	sentinels := map[string]error{
		"ErrCircuitOpen":   ErrCircuitOpen,
		"ErrAuthMissing":   ErrAuthMissing,
		"ErrPolicyDenied":  ErrPolicyDenied,
		"ErrSchemaInvalid": ErrSchemaInvalid,
		"ErrSizeCap":       ErrSizeCap,
	}
	seen := map[error]string{}
	for name, err := range sentinels {
		if err == nil {
			t.Errorf("%s is nil", name)
			continue
		}
		if prev, ok := seen[err]; ok {
			t.Errorf("%s collides with %s (same pointer)", name, prev)
		}
		seen[err] = name
		if !errors.Is(err, err) {
			t.Errorf("%s fails errors.Is(self, self)", name)
		}
	}
	// Wrapped error round-trip
	wrapped := errorsWrap("context: ", ErrCircuitOpen)
	if !errors.Is(wrapped, ErrCircuitOpen) {
		t.Errorf("errors.Is should unwrap to ErrCircuitOpen")
	}
	if errors.Is(wrapped, ErrAuthMissing) {
		t.Errorf("wrapped ErrCircuitOpen should not match ErrAuthMissing")
	}
}

// errorsWrap is a minimal fmt.Errorf("%w") equivalent that avoids pulling
// fmt just for a one-liner.
func errorsWrap(prefix string, err error) error {
	return &wrappedErr{prefix: prefix, err: err}
}

type wrappedErr struct {
	prefix string
	err    error
}

func (w *wrappedErr) Error() string { return w.prefix + w.err.Error() }
func (w *wrappedErr) Unwrap() error { return w.err }

// TestLegacyServerConfigRenamed guards against accidental reintroduction
// of the old ServerConfig JSON-stdio shape under the canonical name. The
// legacy launch-shape should only be reachable via LegacyServerConfig;
// ServerConfig is now the YAML operator config.
func TestLegacyServerConfigRenamed(t *testing.T) {
	// LegacyServerConfig must still have the JSON fields the
	// .claude/mcp.json loader depends on.
	lc := LegacyServerConfig{
		Name:    "fs",
		Command: "npx",
		Args:    []string{"-y", "server"},
		Env:     map[string]string{"K": "v"},
		Enabled: true,
	}
	raw, err := json.Marshal(lc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]json.RawMessage
	_ = json.Unmarshal(raw, &back)
	for _, k := range []string{"name", "command", "args", "env", "enabled"} {
		if _, ok := back[k]; !ok {
			t.Errorf("LegacyServerConfig missing JSON tag %q: %s", k, raw)
		}
	}
	// ServerConfig (the new canonical) must NOT round-trip through the
	// legacy JSON shape: its tags are yaml-only, so json.Marshal emits
	// the Go field names, not the lowercase keys the legacy loader
	// expects. This catches anyone who future-day re-adds json tags to
	// ServerConfig and collapses the two types.
	sc := ServerConfig{Name: "x", Transport: "stdio"}
	scRaw, _ := json.Marshal(sc)
	var scBack map[string]json.RawMessage
	_ = json.Unmarshal(scRaw, &scBack)
	if _, ok := scBack["name"]; ok {
		t.Errorf("ServerConfig should not have lowercase JSON tags (caller is YAML-only); got: %s", scRaw)
	}
	if _, ok := scBack["Name"]; !ok {
		t.Errorf("ServerConfig JSON should fall back to Go field names; got: %s", scRaw)
	}
}
