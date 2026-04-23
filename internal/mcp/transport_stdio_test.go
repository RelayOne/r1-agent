// Package mcp — transport_stdio_test.go — MCP-3 coverage for
// transport_stdio.go.
//
// The tests build a tiny in-repo Go helper at test time, compile it
// into a temp binary, and point the transport at it. This avoids
// depending on any external MCP server while still exercising the
// real exec.Cmd / stdio / JSON-RPC path.

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildFakeServer compiles the `fake server` source below into a
// temp binary and returns its path. The t.TempDir() cleanup sweeps
// the whole directory at end-of-test, so no manual removal needed.
func buildFakeServer(t *testing.T, src string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stdio process-group semantics are POSIX-only")
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write fake server source: %v", err)
	}
	binPath := filepath.Join(dir, "fake_mcp_server")
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake server: %v\n%s", err, out)
	}
	return binPath
}

// fakeServerEchoSource is a minimal stdio MCP server that understands
// `initialize`, `tools/list`, `tools/call`, and sends the
// "notifications/initialized" it receives into the void. Enough to
// exercise the happy path end-to-end.
const fakeServerEchoSource = `
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type req struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

type resp struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Result  any             ` + "`json:\"result,omitempty\"`" + `
	Error   *errObj         ` + "`json:\"error,omitempty\"`" + `
}

type errObj struct {
	Code    int    ` + "`json:\"code\"`" + `
	Message string ` + "`json:\"message\"`" + `
}

func main() {
	// Echo a known env var on stderr so the test can (optionally)
	// verify env forwarding. Stderr is drained by the transport but
	// we don't assert on it here.
	fmt.Fprintln(os.Stderr, "FAKE_AUTH_PRESENT=", os.Getenv("FAKE_AUTH"))

	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		var in req
		if json.Unmarshal([]byte(line), &in) != nil {
			continue
		}
		// Notifications (no id) are absorbed silently.
		if len(in.ID) == 0 || string(in.ID) == "null" {
			continue
		}
		out := resp{JSONRPC: "2.0", ID: in.ID}
		switch in.Method {
		case "initialize":
			out.Result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "fake", "version": "0.0.1"},
			}
		case "tools/list":
			out.Result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "echoes its input",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"msg": map[string]any{"type": "string"}},
						},
					},
				},
			}
		case "tools/call":
			var p struct {
				Name      string         ` + "`json:\"name\"`" + `
				Arguments map[string]any ` + "`json:\"arguments\"`" + `
			}
			_ = json.Unmarshal(in.Params, &p)
			msg, _ := p.Arguments["msg"].(string)
			out.Result = map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "echo:" + msg},
				},
			}
		default:
			out.Error = &errObj{Code: -32601, Message: "method not found: " + in.Method}
		}
		b, _ := json.Marshal(out)
		fmt.Fprintln(os.Stdout, string(b))
	}
}
`

// fakeServerSleepSource is a stdio MCP server that answers
// `initialize` normally and then sits on stdin forever — used to
// verify that Close() reaps the subprocess via the process group.
const fakeServerSleepSource = `
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type req struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
}

type resp struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Result  any             ` + "`json:\"result,omitempty\"`" + `
}

func main() {
	trap := os.Getenv("FAKE_TRAP_SIGTERM") == "1"
	if trap {
		// Ignore SIGTERM so the test can verify SIGKILL escalation.
		sigch := make(chan os.Signal, 1)
		signal.Notify(sigch, syscall.SIGTERM)
		go func() {
			for range sigch {
				// swallow
			}
		}()
	}

	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			// If stdin closes we still refuse to exit promptly when
			// we're supposed to trap SIGTERM — mimicking a wedged
			// server. Otherwise a clean exit.
			if trap {
				time.Sleep(60 * time.Second)
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		var in req
		if json.Unmarshal([]byte(line), &in) != nil {
			continue
		}
		if len(in.ID) == 0 || string(in.ID) == "null" {
			continue
		}
		if in.Method == "initialize" {
			out := resp{
				JSONRPC: "2.0",
				ID:      in.ID,
				Result: map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "sleep", "version": "0"},
				},
			}
			b, _ := json.Marshal(out)
			fmt.Fprintln(os.Stdout, string(b))
			continue
		}
	}
}
`

func TestStdioTransport_InitializeListClose(t *testing.T) {
	// Cannot use t.Parallel with t.Setenv below.
	bin := buildFakeServer(t, fakeServerEchoSource)

	cfg := ServerConfig{
		Name:      "fake",
		Transport: "stdio",
		Command:   bin,
		AuthEnv:   "FAKE_AUTH",
		Env:       map[string]string{"FAKE_EXTRA": "1"},
		Trust:     "untrusted",
	}
	t.Setenv("FAKE_AUTH", "sekret-value")

	tr, err := NewStdioTransport(cfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Definition.Name != "echo" {
		t.Errorf("tool name = %q, want %q", tools[0].Definition.Name, "echo")
	}
	if tools[0].ServerName != "fake" {
		t.Errorf("ServerName = %q, want %q", tools[0].ServerName, "fake")
	}
	if tools[0].Trust != "untrusted" {
		t.Errorf("Trust = %q, want %q", tools[0].Trust, "untrusted")
	}
	if len(tools[0].Definition.InputSchema) == 0 {
		t.Errorf("expected non-empty input schema")
	}

	res, err := tr.CallTool(ctx, "echo", json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError = true, want false")
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(res.Content))
	}
	if res.Content[0].Type != "text" || res.Content[0].Text != "echo:hello" {
		t.Errorf("content = %+v", res.Content[0])
	}

	firstErr := tr.Close()
	if firstErr != nil {
		// Close can return a "signal: terminated" Wait error from the
		// subprocess; accept that as a normal termination signal.
		if !strings.Contains(firstErr.Error(), "signal:") && !strings.Contains(firstErr.Error(), "file already closed") {
			t.Fatalf("Close: %v", firstErr)
		}
	}

	// Second Close must be idempotent and return the same cached
	// error (or nil) without re-signaling the already-dead process.
	secondErr := tr.Close()
	if (firstErr == nil) != (secondErr == nil) {
		t.Errorf("second Close: got %v, want same shape as first (%v)", secondErr, firstErr)
	}
	if firstErr != nil && secondErr != nil && firstErr.Error() != secondErr.Error() {
		t.Errorf("second Close: got %q, want cached %q", secondErr, firstErr)
	}
}

func TestStdioTransport_CloseKillsProcess(t *testing.T) {
	t.Parallel()
	bin := buildFakeServer(t, fakeServerSleepSource)

	cfg := ServerConfig{
		Name:      "sleep",
		Transport: "stdio",
		Command:   bin,
		// FAKE_TRAP_SIGTERM=1 makes the child ignore SIGTERM so we
		// verify the SIGKILL escalation path.
		Env: map[string]string{"FAKE_TRAP_SIGTERM": "1"},
	}
	tr, err := NewStdioTransport(cfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Snapshot the pid so we can poll it from outside after Close.
	tr.cmdMu.Lock()
	pid := tr.cmd.Process.Pid
	tr.cmdMu.Unlock()
	if pid <= 0 {
		t.Fatalf("no captured subprocess pid")
	}

	start := time.Now()
	_ = tr.Close()
	elapsed := time.Since(start)

	// SIGTERM + 3s grace + SIGKILL is the contract; if Close() takes
	// more than ~6s something is very wrong.
	if elapsed > 6*time.Second {
		t.Errorf("Close took too long: %v", elapsed)
	}

	// The process must no longer be reachable. signal 0 returns
	// ESRCH once the kernel has reaped; give it up to 2s post-Close
	// since the subprocess reaper runs after signaling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
			return // gone — success
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d still alive after Close + 2s", pid)
}

// TestStdioTransport_RejectsBadConfig ensures the constructor fails
// closed on obvious misconfiguration rather than deferring the
// error until after a fork. Important because a late failure in
// Initialize can leak a child; a failed constructor cannot.
func TestStdioTransport_RejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		want string
	}{
		{
			name: "wrong transport",
			cfg:  ServerConfig{Transport: "http", Command: "x"},
			want: "transport must be",
		},
		{
			name: "empty command",
			cfg:  ServerConfig{Transport: "stdio", Command: ""},
			want: "command must be non-empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStdioTransport(tc.cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want contains %q", err, tc.want)
			}
		})
	}
}

