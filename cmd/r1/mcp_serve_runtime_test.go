package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCPServeRuntime_ThreeMessageExchange spawns `r1 mcp serve` and
// pipes initialize → tools/list → tools/call through stdin.
// Asserts the response shapes for each frame.
func TestMCPServeRuntime_ThreeMessageExchange(t *testing.T) {
	r1Bin := buildR1ForTest(t)
	var stdin bytes.Buffer
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	stdin.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n")
	stdin.WriteString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"r1.cortex.lobes_list","arguments":{"session_id":"test-session"}}}` + "\n")
	cmd := exec.Command(r1Bin, "mcp", "serve", "--session-id", "test-session")
	cmd.Stdin = &stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	if len(stderr.Bytes()) > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	if runErr != nil {
		t.Fatalf("subprocess failed: %v", runErr)
	}
	out := strings.TrimSpace(string(stdout))
	lines := chompLines(out)
	if got, want := len(lines), 3; got != want {
		t.Fatalf("expected %d response lines, got %d:\n%s", want, got, out)
	}
	// Frame 1: initialize → protocolVersion + serverInfo
	var init struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatalf("decode initialize response: %v\n%s", err, lines[0])
	}
	if init.Result["protocolVersion"] != "2024-11-05" {
		t.Errorf("initialize protocolVersion = %v, want 2024-11-05", init.Result["protocolVersion"])
	}
	// Frame 2: tools/list → tools[] non-empty + r1.cortex.lobes_list present
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("decode tools/list: %v\n%s", err, lines[1])
	}
	if len(listResp.Result.Tools) < 5 {
		t.Errorf("tools/list returned only %d tools; expected at least 5", len(listResp.Result.Tools))
	}
	foundLobesList := false
	for _, td := range listResp.Result.Tools {
		if td.Name == "r1.cortex.lobes_list" {
			foundLobesList = true
			break
		}
	}
	if !foundLobesList {
		t.Errorf("tools/list missing r1.cortex.lobes_list")
	}
	// Frame 3: tools/call r1.cortex.lobes_list → content[0].text contains lobes JSON
	var callResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
				Type string `json:"type"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &callResp); err != nil {
		t.Fatalf("decode tools/call: %v\n%s", err, lines[2])
	}
	if len(callResp.Result.Content) == 0 {
		t.Fatalf("tools/call returned no content")
	}
	if !strings.Contains(callResp.Result.Content[0].Text, `"lobes"`) {
		t.Errorf("tools/call result missing 'lobes' key: %s", callResp.Result.Content[0].Text)
	}
}

// TestMCPServeRuntime_AuthGate sets R1_MCP_KEY and confirms the
// server rejects tools/call without a matching meta.r1_mcp_key,
// while accepting calls that supply the correct key.
func TestMCPServeRuntime_AuthGate(t *testing.T) {
	r1Bin := buildR1ForTest(t)
	var stdin bytes.Buffer
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	// missing meta.r1_mcp_key → unauthorized
	stdin.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"r1.cortex.lobes_list","arguments":{"session_id":"auth-test"}}}` + "\n")
	// correct meta.r1_mcp_key → success
	stdin.WriteString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"r1.cortex.lobes_list","arguments":{"session_id":"auth-test"},"meta":{"r1_mcp_key":"secret-key"}}}` + "\n")
	cmd := exec.Command(r1Bin, "mcp", "serve", "--session-id", "auth-test")
	cmd.Stdin = &stdin
	cmd.Env = append(os.Environ(), "R1_MCP_KEY=secret-key")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	if len(stderr.Bytes()) > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	if runErr != nil {
		t.Fatalf("subprocess failed: %v", runErr)
	}
	lines := chompLines(strings.TrimSpace(string(stdout)))
	if got, want := len(lines), 3; got != want {
		t.Fatalf("expected %d response lines, got %d:\n%s", want, got, string(stdout))
	}
	// Frame 2: should be JSON-RPC error -32000 unauthorized
	var unauth struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &unauth); err != nil {
		t.Fatalf("decode unauthenticated response: %v", err)
	}
	if unauth.Error == nil {
		t.Fatalf("expected error on unauthenticated call, got result: %v", unauth.Result)
	}
	if unauth.Error.Code != -32000 {
		t.Errorf("error code = %d, want -32000", unauth.Error.Code)
	}
	if unauth.Error.Message != "unauthorized" {
		t.Errorf("error message = %q, want 'unauthorized'", unauth.Error.Message)
	}
	// Frame 3: should be a result, not an error
	var ok struct {
		Error  *struct{}      `json:"error"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &ok); err != nil {
		t.Fatalf("decode authenticated response: %v", err)
	}
	if ok.Error != nil {
		t.Errorf("expected success on authenticated call, got error")
	}
	if ok.Result == nil {
		t.Errorf("expected non-nil result on authenticated call")
	}
}

// TestMCPServeRuntime_NoCortex confirms --no-cortex disables the
// cortex backend so r1.cortex.* calls return the documented
// "cortex backend not wired" error.
func TestMCPServeRuntime_NoCortex(t *testing.T) {
	r1Bin := buildR1ForTest(t)
	var stdin bytes.Buffer
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"r1.cortex.lobes_list","arguments":{"session_id":"no-cortex-test"}}}` + "\n")
	cmd := exec.Command(r1Bin, "mcp", "serve", "--no-cortex", "--session-id", "no-cortex-test")
	cmd.Stdin = &stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	if len(stderr.Bytes()) > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	if runErr != nil {
		t.Fatalf("subprocess failed: %v", runErr)
	}
	out := strings.TrimSpace(string(stdout))
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if !resp.Result.IsError {
		t.Errorf("expected isError=true with --no-cortex; got %s", out)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("response missing content")
	}
	if !strings.Contains(strings.ToLower(resp.Result.Content[0].Text), "cortex") {
		t.Errorf("expected error text to mention cortex; got %s", resp.Result.Content[0].Text)
	}
}

// chompLines breaks s on '\n' boundaries by hand. Returns the lines
// in order, with the empty trailing element trimmed.
func chompLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// buildR1ForTest builds the r1 binary into a temp dir for the test
// to spawn as a subprocess. Skipped in -short mode.
func buildR1ForTest(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "r1")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	build := exec.Command("go", "build", "-o", bin, "./")
	build.Dir = wd
	if buildOut, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, buildOut)
	}
	return bin
}
