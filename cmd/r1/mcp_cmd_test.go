package main

// mcp_cmd_test.go — unit-level dispatch tests for `r1 mcp` (MCP-13).
//
// Uses mcp.SetTransportFactory to install a fake Client so each test
// exercises dispatch + flag parsing + output shaping without touching
// the filesystem, spinning subprocesses, or dialing the network. A
// tiny on-disk stoke.policy.yaml fixture under t.TempDir() supplies
// mcp_servers: entries that the production loader then picks up.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/mcp"
)

// writePolicyFixture drops a minimal stoke.policy.yaml into dir and
// returns its absolute path. Two untrusted stdio servers (alpha,
// beta) are enough to exercise every command's fan-out / filter
// behavior.
func writePolicyFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "stoke.policy.yaml")
	body := `phases:
  execute:
    builtin_tools: [Read]
    allowed_rules: [Read]
    denied_rules: []
    mcp_enabled: true

mcp_servers:
  - name: alpha
    transport: stdio
    command: "/usr/bin/true"
    trust: untrusted
  - name: beta
    transport: stdio
    command: "/usr/bin/true"
    trust: untrusted
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// --- fake Client ---------------------------------------------------

type stubClient struct {
	name string

	mu          sync.Mutex
	tools       []mcp.Tool
	callResult  mcp.ToolResult
	callErr     error
	listErr     error
	initErr     error
	callCount   atomic.Int64
	closeCalled atomic.Bool
}

func (c *stubClient) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initErr
}

func (c *stubClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := make([]mcp.Tool, len(c.tools))
	copy(out, c.tools)
	return out, nil
}

func (c *stubClient) CallTool(ctx context.Context, name string, args json.RawMessage) (mcp.ToolResult, error) {
	c.callCount.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callErr != nil {
		return mcp.ToolResult{}, c.callErr
	}
	return c.callResult, nil
}

func (c *stubClient) Close() error {
	c.closeCalled.Store(true)
	return nil
}

// installStubFactory routes every server name through the supplied
// map of fakes. Cleans up at t.Cleanup time.
func installStubFactory(t *testing.T, byName map[string]*stubClient) {
	t.Helper()
	mcp.SetTransportFactory(func(cfg mcp.ServerConfig) (mcp.Client, error) {
		if c, ok := byName[cfg.Name]; ok {
			c.name = cfg.Name
			return c, nil
		}
		return nil, errors.New("no stub for " + cfg.Name)
	})
	t.Cleanup(func() { mcp.SetTransportFactory(nil) })
}

// --- dispatch / usage ---------------------------------------------

func TestMCP_NoSubcommand_PrintsUsage(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMCPCmd(nil, &out, &errBuf, failingLoader)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "r1 mcp") {
		t.Errorf("stderr should print usage, got %q", errBuf.String())
	}
}

func TestMCP_UnknownSubcommand_ExitsNonZero(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"banana"}, &out, &errBuf, failingLoader)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "unknown mcp subcommand") {
		t.Errorf("stderr should identify the unknown subcommand, got %q", errBuf.String())
	}
}

func TestMCP_HelpFlag_ExitsZero(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"--help"}, &out, &errBuf, failingLoader)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "r1 mcp") {
		t.Errorf("stdout should print usage, got %q", out.String())
	}
}

// failingLoader is the loader used by tests that never get past
// dispatch — if it ever fires, the test setup is wrong.
func failingLoader(string) (*mcp.Registry, []mcp.ServerConfig, func(), error) {
	return nil, nil, func() {}, errors.New("loader should not have been called")
}

// --- real-policy integration via AutoLoadPolicy chdir ---

// withFixture installs a stoke.policy.yaml under t.TempDir() and
// chdirs there so config.AutoLoadPolicy discovers it. Stub clients
// for "alpha" and "beta" are wired into the transport factory.
func withFixture(t *testing.T, stubs map[string]*stubClient) string {
	t.Helper()
	dir := t.TempDir()
	writePolicyFixture(t, dir)
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	installStubFactory(t, stubs)
	return dir
}

func TestMCPListServers_OneLinePerConfiguredServer(t *testing.T) {
	stubs := map[string]*stubClient{
		"alpha": {},
		"beta":  {},
	}
	withFixture(t, stubs)

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"list-servers"}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	lines := splitNonEmpty(out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out.String())
	}
	for i, want := range []string{"alpha", "beta"} {
		if !strings.HasPrefix(lines[i], want+" |") {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], want+" |")
		}
		// name | transport | endpoint | trust | circuit
		if got := strings.Count(lines[i], " | "); got != 4 {
			t.Errorf("line %d has %d separators, want 4: %q", i, got, lines[i])
		}
	}
}

func TestMCPListTools_EmitsPrefixedNames(t *testing.T) {
	alpha := &stubClient{tools: []mcp.Tool{
		{Definition: mcp.ToolDefinition{Name: "list", Description: "list stuff"}, ServerName: "alpha", Trust: mcp.TrustUntrusted},
	}}
	beta := &stubClient{tools: []mcp.Tool{
		{Definition: mcp.ToolDefinition{Name: "create", Description: "create stuff"}, ServerName: "beta", Trust: mcp.TrustUntrusted},
	}}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": beta})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"list-tools"}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "mcp_alpha_list") {
		t.Errorf("output missing mcp_alpha_list: %q", s)
	}
	if !strings.Contains(s, "mcp_beta_create") {
		t.Errorf("output missing mcp_beta_create: %q", s)
	}
}

func TestMCPListTools_ServerFilter(t *testing.T) {
	alpha := &stubClient{tools: []mcp.Tool{
		{Definition: mcp.ToolDefinition{Name: "list"}, ServerName: "alpha", Trust: mcp.TrustUntrusted},
	}}
	beta := &stubClient{tools: []mcp.Tool{
		{Definition: mcp.ToolDefinition{Name: "create"}, ServerName: "beta", Trust: mcp.TrustUntrusted},
	}}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": beta})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"list-tools", "--server", "beta"}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if strings.Contains(out.String(), "mcp_alpha_list") {
		t.Errorf("alpha leaked through --server beta filter: %q", out.String())
	}
	if !strings.Contains(out.String(), "mcp_beta_create") {
		t.Errorf("beta tool not present under --server beta: %q", out.String())
	}
}

func TestMCPListTools_UnknownServer_ExitsOne(t *testing.T) {
	withFixture(t, map[string]*stubClient{"alpha": {}, "beta": {}})
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"list-tools", "--server", "ghost"}, &out, &errBuf, loadMCPRegistry)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "ghost") {
		t.Errorf("stderr should name the bad server: %q", errBuf.String())
	}
}

func TestMCPListTools_JSONOutput(t *testing.T) {
	alpha := &stubClient{tools: []mcp.Tool{
		{Definition: mcp.ToolDefinition{Name: "list", Description: "d"}, ServerName: "alpha", Trust: mcp.TrustUntrusted},
	}}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": {}})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"list-tools", "--json"}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	var tools []mcp.Tool
	if err := json.Unmarshal(out.Bytes(), &tools); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Definition.Name != "list" {
		t.Errorf("got %+v, want name=list", tools[0])
	}
}

func TestMCPTest_HappyPath(t *testing.T) {
	alpha := &stubClient{
		tools: []mcp.Tool{
			{Definition: mcp.ToolDefinition{Name: "ping"}, ServerName: "alpha", Trust: mcp.TrustUntrusted},
		},
		callResult: mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "pong"}}},
	}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": {}})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"test", "alpha"}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q, stdout=%q", code, errBuf.String(), out.String())
	}
	for _, want := range []string{"initialize: PASS", "list-tools: PASS", "call-tool:  PASS"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in stdout, got %q", want, out.String())
		}
	}
	if alpha.callCount.Load() != 1 {
		t.Errorf("expected one CallTool, got %d", alpha.callCount.Load())
	}
}

func TestMCPTest_CallFailure_Exits3(t *testing.T) {
	alpha := &stubClient{
		tools: []mcp.Tool{
			{Definition: mcp.ToolDefinition{Name: "ping"}, ServerName: "alpha", Trust: mcp.TrustUntrusted},
		},
		callErr: errors.New("boom"),
	}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": {}})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"test", "alpha"}, &out, &errBuf, loadMCPRegistry)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (stdout=%q)", code, out.String())
	}
	if !strings.Contains(out.String(), "call-tool:  FAIL") {
		t.Errorf("expected call-tool FAIL marker, got %q", out.String())
	}
}

func TestMCPTest_UnknownServer_ExitsOne(t *testing.T) {
	withFixture(t, map[string]*stubClient{"alpha": {}, "beta": {}})
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"test", "ghost"}, &out, &errBuf, loadMCPRegistry)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "ghost") {
		t.Errorf("stderr should name ghost: %q", errBuf.String())
	}
}

func TestMCPTest_NoTools_Skips(t *testing.T) {
	alpha := &stubClient{} // no tools, no error
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": {}})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"test", "alpha"}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q, stdout=%q)", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "call-tool:  SKIP") {
		t.Errorf("expected SKIP marker when no tools advertised, got %q", out.String())
	}
}

func TestMCPCall_HappyPath(t *testing.T) {
	alpha := &stubClient{
		callResult: mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}},
	}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": {}})

	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"call", "alpha", "do_thing", "--args-json", `{"k":"v"}`}, &out, &errBuf, loadMCPRegistry)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q, stdout=%q", code, errBuf.String(), out.String())
	}
	var result mcp.ToolResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("output is not JSON ToolResult: %v\n%s", err, out.String())
	}
	if len(result.Content) != 1 || result.Content[0].Text != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
	if alpha.callCount.Load() != 1 {
		t.Errorf("expected 1 CallTool, got %d", alpha.callCount.Load())
	}
}

func TestMCPCall_BadJSON_ExitsOne(t *testing.T) {
	withFixture(t, map[string]*stubClient{"alpha": {}, "beta": {}})
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"call", "alpha", "do_thing", "--args-json", `not-json`}, &out, &errBuf, loadMCPRegistry)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "not valid JSON") {
		t.Errorf("expected JSON-validity error, got %q", errBuf.String())
	}
}

func TestMCPCall_UnknownServer_ExitsOne(t *testing.T) {
	withFixture(t, map[string]*stubClient{"alpha": {}, "beta": {}})
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"call", "ghost", "tool"}, &out, &errBuf, loadMCPRegistry)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errBuf.String())
	}
}

func TestMCPCall_CallErr_Exits3(t *testing.T) {
	alpha := &stubClient{callErr: errors.New("nope")}
	withFixture(t, map[string]*stubClient{"alpha": alpha, "beta": {}})
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"call", "alpha", "tool"}, &out, &errBuf, loadMCPRegistry)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (stderr=%q)", code, errBuf.String())
	}
}

func TestMCPCall_MissingArgs_ExitsOne(t *testing.T) {
	withFixture(t, map[string]*stubClient{"alpha": {}, "beta": {}})
	var out, errBuf bytes.Buffer
	code := runMCPCmd([]string{"call", "alpha"}, &out, &errBuf, loadMCPRegistry)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q)", code, errBuf.String())
	}
}

// --- helpers ------------------------------------------------------

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// compile-time assertion that stubClient satisfies mcp.Client even
// though the package-private stub in registry_test.go already does —
// having a local assertion catches interface drift in this package.
var _ mcp.Client = (*stubClient)(nil)

// silence unused import warning on `io` when future test changes drop
// a writer-based helper.
var _ = io.Discard
