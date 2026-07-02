// lspdiag_test.go — integration tests for the opt-in edit-time LSP
// diagnostics verification step (audit A067). A fake LSP 3.17 server
// speaking Content-Length-framed JSON-RPC over io.Pipe stands in for
// gopls et al., so no external binary is required.
package verify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/lsp/client"
)

// fakeLSPDiag is a minimal LSP server: answers initialize/shutdown and
// pushes the configured diagnostics for every opened document.
type fakeLSPDiag struct {
	in    io.Reader
	out   io.Writer
	diags []client.Diagnostic
}

func (f *fakeLSPDiag) writeFrame(v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintf(f.out, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (f *fakeLSPDiag) run() {
	br := bufio.NewReader(f.in)
	for {
		// Parse Content-Length header block.
		var length int
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				fmt.Sscanf(strings.TrimSpace(line[len("content-length:"):]), "%d", &length)
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return
		}
		switch req.Method {
		case "initialize":
			f.writeFrame(map[string]any{"jsonrpc": "2.0", "id": *req.ID,
				"result": map[string]any{"capabilities": map[string]any{}}})
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.writeFrame(map[string]any{"jsonrpc": "2.0",
				"method": "textDocument/publishDiagnostics",
				"params": map[string]any{"uri": p.TextDocument.URI, "diagnostics": f.diags}})
		case "shutdown":
			if req.ID != nil {
				f.writeFrame(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": nil})
			}
		}
	}
}

// fakeLauncher returns an lspLauncher backed by an in-process fakeLSPDiag.
func fakeLauncher(diags []client.Diagnostic) lspLauncher {
	return func(lang, root string) (*client.Client, error) {
		srvIn, cliOut := io.Pipe() // client writes -> server reads
		cliIn, srvOut := io.Pipe() // server writes -> client reads
		go (&fakeLSPDiag{in: srvIn, out: srvOut, diags: diags}).run()
		c := client.WithTransport(cliOut, cliIn)
		c.SetRequestTimeout(2 * time.Second)
		return c, nil
	}
}

// gitWorktree creates a temp git repo with one modified Go file.
func gitWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLSPDiagnosticsDefaultOff(t *testing.T) {
	dir := gitWorktree(t)
	p := NewPipeline("", "", "")
	p.lspLaunch = fakeLauncher([]client.Diagnostic{{Severity: 1, Message: "boom"}})
	outcomes, err := p.Run(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outcomes {
		if o.Name == "lsp" {
			t.Fatalf("lsp outcome present without %s=1: %+v", lspDiagEnv, o)
		}
	}
}

func TestLSPDiagnosticsErrorFailsVerification(t *testing.T) {
	t.Setenv(lspDiagEnv, "1")
	dir := gitWorktree(t)
	p := NewPipeline("", "", "")
	p.lspLaunch = fakeLauncher([]client.Diagnostic{{
		Severity: 1,
		Message:  "undefined: frobnicate",
		Range:    client.Range{Start: client.Position{Line: 1, Character: 5}},
	}})
	p.lspWait = time.Second

	outcomes, err := p.Run(context.Background(), dir)
	if err == nil {
		t.Fatal("expected verification failure from Error-severity LSP diagnostic")
	}
	var lsp *Outcome
	for i := range outcomes {
		if outcomes[i].Name == "lsp" {
			lsp = &outcomes[i]
		}
	}
	if lsp == nil {
		t.Fatalf("no lsp outcome in %+v", outcomes)
	}
	if lsp.Success {
		t.Fatal("lsp outcome should fail on Error-severity diagnostic")
	}
	if !strings.Contains(lsp.Output, "undefined: frobnicate") || !strings.Contains(lsp.Output, "main.go:2:6") {
		t.Errorf("lsp output missing diagnostic detail: %q", lsp.Output)
	}
}

func TestLSPDiagnosticsWarningsPass(t *testing.T) {
	t.Setenv(lspDiagEnv, "1")
	dir := gitWorktree(t)
	p := NewPipeline("", "", "")
	p.lspLaunch = fakeLauncher([]client.Diagnostic{{Severity: 2, Message: "unused variable"}})
	p.lspWait = 300 * time.Millisecond

	outcomes, err := p.Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("warnings must not fail verification: %v (%+v)", err, outcomes)
	}
	found := false
	for _, o := range outcomes {
		if o.Name == "lsp" {
			found = true
			if !o.Success || o.Skipped {
				t.Errorf("lsp outcome should be non-skipped success, got %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("expected lsp outcome when enabled")
	}
}

func TestLSPDiagnosticsMissingServerDegrades(t *testing.T) {
	t.Setenv(lspDiagEnv, "1")
	dir := gitWorktree(t)
	p := NewPipeline("", "", "")
	p.lspLaunch = func(lang, root string) (*client.Client, error) {
		return nil, errors.New("gopls not found on PATH")
	}
	outcomes, err := p.Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("missing language server must degrade gracefully: %v", err)
	}
	for _, o := range outcomes {
		if o.Name == "lsp" {
			if !o.Skipped || !o.Success {
				t.Errorf("expected skipped success outcome, got %+v", o)
			}
			if !strings.Contains(o.Output, "unavailable") {
				t.Errorf("expected unavailability note, got %q", o.Output)
			}
			return
		}
	}
	t.Fatal("expected lsp outcome when enabled")
}

func TestLSPDiagnosticsNonGitDirSkips(t *testing.T) {
	t.Setenv(lspDiagEnv, "1")
	dir := t.TempDir() // no git repo — and no parent repo interference expected? use GIT_DIR? see below
	// Guard: if the temp dir is inside a git repo (unlikely for TempDir),
	// git status would succeed; force failure via env override.
	t.Setenv("GIT_DIR", filepath.Join(dir, "definitely-missing.git"))
	p := NewPipeline("", "", "")
	p.lspLaunch = fakeLauncher(nil)
	outcomes, err := p.Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("non-git dir must degrade gracefully: %v", err)
	}
	for _, o := range outcomes {
		if o.Name == "lsp" {
			if !o.Skipped || !o.Success {
				t.Errorf("expected skipped success outcome, got %+v", o)
			}
			return
		}
	}
	t.Fatal("expected lsp outcome when enabled")
}
