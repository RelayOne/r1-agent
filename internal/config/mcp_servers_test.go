package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture loads a YAML fixture from testdata/ as raw bytes. Keeps
// each test stanza focused on its validation expectation instead of
// repeating boilerplate path joins.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// TestMCPServersValid parses a fixture with three servers covering
// every transport path (stdio / http / sse) and asserts validation
// passes and default-application fires for the one server that omits
// the timeout / trust / max_concurrent knobs.
func TestMCPServersValid(t *testing.T) {
	raw := readFixture(t, "mcp_servers_valid.yaml")
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d: %+v", len(servers), servers)
	}
	if err := ValidateMCPServers(servers); err != nil {
		t.Fatalf("ValidateMCPServers: unexpected error: %v", err)
	}

	byName := map[string]int{}
	for i, s := range servers {
		byName[s.Name] = i
	}
	for _, want := range []string{"linear", "github", "slack-legacy"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("expected server %q in parsed set, got %+v", want, byName)
		}
	}

	// Defaults should have been applied to slack-legacy (no timeout,
	// no max_concurrent, no trust in the fixture).
	slack := servers[byName["slack-legacy"]]
	if slack.Timeout != "30s" {
		t.Errorf("slack-legacy Timeout default not applied: got %q, want %q", slack.Timeout, "30s")
	}
	if slack.MaxConcurrent != 8 {
		t.Errorf("slack-legacy MaxConcurrent default not applied: got %d, want %d", slack.MaxConcurrent, 8)
	}
	if slack.Trust != "untrusted" {
		t.Errorf("slack-legacy Trust default not applied: got %q, want %q", slack.Trust, "untrusted")
	}

	// Non-default values should be preserved on linear (trust: trusted).
	linear := servers[byName["linear"]]
	if linear.Trust != "trusted" {
		t.Errorf("linear Trust should be preserved as 'trusted', got %q", linear.Trust)
	}
	if linear.MaxConcurrent != 4 {
		t.Errorf("linear MaxConcurrent should be 4, got %d", linear.MaxConcurrent)
	}
	if linear.Timeout != "20s" {
		t.Errorf("linear Timeout should be '20s', got %q", linear.Timeout)
	}
}

// TestMCPServersBadName rejects a fixture whose Name contains capital
// letters (fails the ^[a-z][a-z0-9_-]{0,31}$ regex). The error text
// must mention the field so an operator can fix it.
func TestMCPServersBadName(t *testing.T) {
	raw := readFixture(t, "mcp_servers_bad_name.yaml")
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	err = ValidateMCPServers(servers)
	if err == nil {
		t.Fatal("expected validation error for bad name, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid name") || !strings.Contains(msg, "Bad_Name_With_Caps") {
		t.Errorf("error should mention invalid name and the offender, got: %q", msg)
	}
}

// TestMCPServersHTTPNotHTTPS rejects a remote http:// URL (anything
// other than localhost / 127.0.0.1). Prevents plaintext leakage of
// bearer tokens to third-party MCP servers.
func TestMCPServersHTTPNotHTTPS(t *testing.T) {
	raw := readFixture(t, "mcp_servers_http_not_https.yaml")
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	err = ValidateMCPServers(servers)
	if err == nil {
		t.Fatal("expected validation error for http://example.com, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "insecure") && !strings.Contains(msg, "http://") {
		t.Errorf("error should explain the http-vs-https rule, got: %q", msg)
	}
}

// TestMCPServersLocalhostHTTP accepts http://localhost:8080/... and
// http://127.0.0.1:9000/... — the narrow dev-loop exception.
func TestMCPServersLocalhostHTTP(t *testing.T) {
	raw := readFixture(t, "mcp_servers_localhost_http.yaml")
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	if err := ValidateMCPServers(servers); err != nil {
		t.Fatalf("ValidateMCPServers: localhost http should pass: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 localhost servers, got %d", len(servers))
	}
}

// TestMCPServersStdioNoCommand rejects a stdio-transport entry that
// omits Command (stdio requires a subprocess to spawn).
func TestMCPServersStdioNoCommand(t *testing.T) {
	raw := readFixture(t, "mcp_servers_stdio_no_command.yaml")
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	err = ValidateMCPServers(servers)
	if err == nil {
		t.Fatal("expected validation error for stdio without command, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "stdio") || !strings.Contains(msg, "command") {
		t.Errorf("error should name the missing field, got: %q", msg)
	}
}

// TestMCPServersBadTimeout catches Timeout values that do not parse as
// a time.Duration (e.g. "forever"). The defaulting path should NOT
// overwrite an explicit-but-invalid value — the operator sees the
// parse error with their bad value surfaced.
func TestMCPServersBadTimeout(t *testing.T) {
	raw := readFixture(t, "mcp_servers_bad_timeout.yaml")
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	err = ValidateMCPServers(servers)
	if err == nil {
		t.Fatal("expected validation error for timeout=forever, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "timeout") || !strings.Contains(msg, "forever") {
		t.Errorf("error should surface the bad timeout value, got: %q", msg)
	}
}

// TestMCPServersTransportEnum asserts the Transport enum rejects any
// value outside {stdio, http, streamable-http, sse}. Caught via a
// hand-constructed config (no fixture needed — the enum rule is
// structural, not tied to any YAML quirk).
func TestMCPServersTransportEnum(t *testing.T) {
	raw := []byte(`mcp_servers:
  - name: weird
    transport: websocket
    url: https://example.com
`)
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		t.Fatalf("parseMCPServersBlock: %v", err)
	}
	if err := ValidateMCPServers(servers); err == nil {
		t.Fatal("expected validation error for unknown transport, got nil")
	}
}

// TestMCPServersLoadPolicyIntegration verifies the end-to-end
// LoadPolicy path — the existing custom YAML scanner ignores the
// mcp_servers block, but LoadPolicy still parses it via the yaml.v3
// helper and attaches the validated result to Policy.MCPServers.
func TestMCPServersLoadPolicyIntegration(t *testing.T) {
	dir := t.TempDir()
	content := `phases:
  plan:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
  execute:
    builtin_tools: [Read, Edit]
    denied_rules: []
    allowed_rules: [Read, Edit]
  verify:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
files:
  protected: []
mcp_servers:
  - name: linear
    transport: stdio
    command: ./bin/linear-mcp
    timeout: 20s
`
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if len(p.MCPServers) != 1 {
		t.Fatalf("expected 1 mcp server on Policy.MCPServers, got %d", len(p.MCPServers))
	}
	if p.MCPServers[0].Name != "linear" {
		t.Errorf("expected server name 'linear', got %q", p.MCPServers[0].Name)
	}
	// Defaults applied by ValidateMCPServers should be visible on the
	// returned Policy.
	if p.MCPServers[0].Trust != "untrusted" {
		t.Errorf("expected default Trust='untrusted', got %q", p.MCPServers[0].Trust)
	}
	if p.MCPServers[0].MaxConcurrent != 8 {
		t.Errorf("expected default MaxConcurrent=8, got %d", p.MCPServers[0].MaxConcurrent)
	}
}

// TestMCPServersLoadPolicyRejectsBad asserts LoadPolicy surfaces the
// ValidateMCPServers error instead of silently dropping a bad server
// config. A bad mcp_servers block must fail policy load hard.
func TestMCPServersLoadPolicyRejectsBad(t *testing.T) {
	dir := t.TempDir()
	content := `phases:
  plan:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
  execute:
    builtin_tools: [Read, Edit]
    denied_rules: []
    allowed_rules: [Read, Edit]
  verify:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
files:
  protected: []
mcp_servers:
  - name: Bad-Caps
    transport: stdio
    command: ./x
`
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(path); err == nil {
		t.Fatal("expected LoadPolicy to fail on invalid mcp_servers, got nil")
	}
}
