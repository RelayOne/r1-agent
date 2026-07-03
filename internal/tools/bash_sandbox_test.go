package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/sandbox"
)

// stubWrapper is a controllable sandbox.Wrapper: it either fails at
// Command time or rewrites the command so tests can prove handleBash
// actually routes through the wrapper instead of direct exec.
type stubWrapper struct {
	cmdErr  error
	rewrite string // when non-empty, run this instead of the requested command
}

func (s *stubWrapper) Name() string                    { return "stub" }
func (s *stubWrapper) Available(sandbox.Policy) error  { return nil }
func (s *stubWrapper) Command(ctx context.Context, shellCmd, workDir string, _ sandbox.Policy) (*exec.Cmd, error) {
	if s.cmdErr != nil {
		return nil, s.cmdErr
	}
	run := shellCmd
	if s.rewrite != "" {
		run = s.rewrite
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", run)
	cmd.Dir = workDir
	return cmd, nil
}

func bashInput(t *testing.T, command string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The nil-sandbox path must behave exactly like the historical direct
// exec: same output, cwd = workDir, exit codes reported in the result.
func TestHandleBashNoSandboxUnchanged(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	out, err := r.handleBash(context.Background(), bashInput(t, "echo hi && pwd"))
	if err != nil {
		t.Fatalf("handleBash: %v", err)
	}
	if !strings.Contains(out, "hi\n") {
		t.Errorf("output missing echo: %q", out)
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(out, dir) && !strings.Contains(out, resolved) {
		t.Errorf("cwd not the registry workDir: %q", out)
	}
}

// A wrapper failure must abort BEFORE the command runs: the error names
// the opt-out, and the command's side effect must not be observable.
func TestHandleBashSandboxFailClosed(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	r.sbx = &stubWrapper{cmdErr: errors.New("userns blocked")}

	marker := filepath.Join(dir, "escaped.marker")
	out, err := r.handleBash(context.Background(), bashInput(t, "touch "+marker))
	if err == nil {
		t.Fatalf("want fail-closed error, got output %q", out)
	}
	for _, sub := range []string{"sandbox", "unavailable", "R1_NATIVE_SANDBOX=off"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q missing %q", err, sub)
		}
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("command executed despite sandbox failure")
	}
}

// When a wrapper is wired, handleBash must run the wrapper's command,
// not the raw host exec.
func TestHandleBashRoutesThroughWrapper(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.sbx = &stubWrapper{rewrite: "echo WRAPPED"}

	out, err := r.handleBash(context.Background(), bashInput(t, "echo direct"))
	if err != nil {
		t.Fatalf("handleBash: %v", err)
	}
	if !strings.Contains(out, "WRAPPED") || strings.Contains(out, "direct") {
		t.Errorf("command did not route through the wrapper: %q", out)
	}
}

// The bashBreakerCheck floor stays layer 1: a blocked command must be
// refused before the sandbox wrapper is even consulted.
func TestHandleBashBreakerRunsBeforeSandbox(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.sbx = &stubWrapper{cmdErr: fmt.Errorf("wrapper consulted")}

	_, err := r.handleBash(context.Background(), bashInput(t, "rm -rf /"))
	if err == nil {
		t.Fatal("breaker must block rm -rf /")
	}
	if strings.Contains(err.Error(), "wrapper consulted") {
		t.Errorf("sandbox consulted before breaker floor: %v", err)
	}
}

// SetSandbox selects eagerly: an explicitly requested but unavailable
// backend fails at wiring time, before any command dispatch.
func TestSetSandboxEagerFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := NewRegistry(t.TempDir())
	err := r.SetSandbox(sandbox.Policy{Mode: sandbox.ModeBwrap})
	if err == nil {
		t.Fatal("SetSandbox must fail when bwrap is unavailable")
	}
	if r.sbx != nil {
		t.Error("failed SetSandbox must not leave a wrapper configured")
	}
}

// Mode off clears a previously wired sandbox (kill-switch semantics).
func TestSetSandboxOffClears(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.sbx = &stubWrapper{}
	if err := r.SetSandbox(sandbox.Policy{Mode: sandbox.ModeOff}); err != nil {
		t.Fatalf("SetSandbox(off): %v", err)
	}
	if r.sbx != nil {
		t.Error("mode off must clear the wrapper")
	}
}
