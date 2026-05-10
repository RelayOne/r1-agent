package main

// mcp_serve_print_tools_test.go — exercises `r1 mcp serve --print-tools`
// per spec 8 §12 item 9. The lint at tools/lint-view-without-api/ uses
// this output to learn the wire catalog without spawning the daemon.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/mcp"
)

func TestMCPServe_PrintToolsJSON_Returns38Tools(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--print-tools"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	var got []mcp.ToolDefinition
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if len(got) != 38 {
		t.Errorf("got %d tools, want 38", len(got))
	}
}

func TestMCPServe_PrintToolsJSON_AllSchemasValid(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--print-tools"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got []mcp.ToolDefinition
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, td := range got {
		if !strings.HasPrefix(td.Name, "r1.") {
			t.Errorf("tool %q missing r1. prefix", td.Name)
		}
		if !json.Valid(td.InputSchema) {
			t.Errorf("tool %q has invalid InputSchema", td.Name)
		}
	}
}

// TestMCPServe_NoFlagsRunsServer asserts the post-PR-#248 behavior:
// `r1 mcp serve` with no flags now starts the stdio MCP JSON-RPC
// server (instead of returning the prior "back-end not yet wired"
// stub). With empty stdin (test default) the server reads EOF
// immediately and exits 0. End-to-end behavior with real frames is
// covered by mcp_serve_runtime_test.go.
//
// Note: in-process invocation of runMCPServe attaches stdin = the
// test process's os.Stdin, so we run it in a goroutine and time out
// after a short window — under `go test` stdin is /dev/null which
// scans return immediately, but a TTY-attached run would block
// forever.
func TestMCPServe_NoFlagsRunsServer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runMCPServe([]string{"--no-cortex"}, &stdout, &stderr)
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("expected exit 0 on EOF stdin, got %d; stderr=%q", code, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("runMCPServe did not exit within 3s on EOF stdin; stderr=%q", stderr.String())
	}
}
