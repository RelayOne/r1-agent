package sessionhub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// toolUseProvider drives one tool_use turn followed by an end_turn.
// We use it to force a single tool dispatch per Run so the sentinel's
// pre-handler hook fires exactly once.
type toolUseProvider struct {
	idx atomic.Int64
}

func (t *toolUseProvider) Name() string { return "tool-use" }
func (t *toolUseProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	i := t.idx.Add(1) - 1
	switch i {
	case 0:
		return &provider.ChatResponse{
			Content: []provider.ResponseContent{
				{Type: "tool_use", ID: "u1", Name: "noop",
					Input: map[string]any{}},
			},
			StopReason: "tool_use",
		}, nil
	default:
		return &provider.ChatResponse{
			Content:    []provider.ResponseContent{{Type: "text", Text: "done"}},
			StopReason: "end_turn",
		}, nil
	}
}
func (t *toolUseProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return t.Chat(req)
}

// TestDispatchTool_DefaultHookIsAssertCwd asserts that when no
// DispatchHook is installed, Run installs defaultDispatchHook
// automatically. The test runs the agent loop with cwd matching
// SessionRoot and observes no panic — the happy path.
func TestDispatchTool_DefaultHookIsAssertCwd(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub2, _ := NewHub()
	wd := t.TempDir()
	// Chdir into the session's workdir so the default sentinel passes.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(prev) }()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// SessionRoot must be the absolute, cleaned path of `wd` since the
	// hub normalises via filepath.Abs+Clean. On macOS, /tmp resolves
	// to /private/tmp via symlink; we'd need to chdir into that
	// resolved form too. Use os.Getwd to capture what the kernel
	// reports, then create the session against THAT path.
	livePath, _ := os.Getwd()
	s, err := hub2.Create(CreateOptions{Workdir: livePath, Model: "m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mock := &toolUseProvider{}
	handler := func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		return "ok", nil
	}
	if _, err := s.Run(context.Background(), RunOptions{
		Provider:    mock,
		Handler:     handler,
		LoopConfig:  agentloop.Config{MaxTurns: 3},
		UserMessage: "go",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestDispatchTool_PanicsOnCwdDrift simulates the catastrophic case:
// the session was created against workdir A, but the process cwd is
// pointing at workdir B at the moment a tool dispatch fires. The
// default DispatchHook (assertCwd) MUST panic, NOT silently allow the
// dispatch to proceed.
//
// We can't reliably invoke the agent loop in a state where cwd != A
// without racing the test against itself, so we construct a Session
// with SessionRoot = A and call the wrapped handler directly with the
// ambient cwd (the test's working dir, !=A by construction since A is
// a fresh t.TempDir()).
func TestDispatchTool_PanicsOnCwdDrift(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub2, _ := NewHub()
	wd := t.TempDir()
	s, err := hub2.Create(CreateOptions{Workdir: wd, Model: "m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Force SessionRoot to a known-not-cwd path so the sentinel
	// triggers on call. We override by writing the field directly —
	// this is white-box test access; the hub's public API doesn't
	// expose a setter (and shouldn't).
	bogus := filepath.Join(t.TempDir(), "different-dir")
	if err := os.MkdirAll(bogus, 0o700); err != nil {
		t.Fatalf("mkdir bogus: %v", err)
	}
	s.SessionRoot = bogus

	// Build the wrapped handler the way Run would.
	handler := func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		t.Fatalf("inner handler must NOT be reached when sentinel panics")
		return "", nil
	}
	wrapped := s.wrapHandler(handler, defaultDispatchHook)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on cwd drift; got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value: got %T %v, want string", r, r)
		}
		if !strings.Contains(msg, "leaked workdir") {
			t.Fatalf("panic msg missing 'leaked workdir' sentinel: %q", msg)
		}
		if !strings.Contains(msg, "cwd drifted") {
			t.Fatalf("panic msg missing 'cwd drifted' label: %q", msg)
		}
	}()
	// This call MUST panic before reaching the inner handler.
	_, _ = wrapped(context.Background(), "noop", json.RawMessage(`{}`))
}

// TestDispatchTool_HookFiresBeforeHandler asserts the wrapping
// invariant: the hook runs first, then the inner handler. This is the
// shape that makes the sentinel's panic load-bearing — if the hook
// fired AFTER the handler, the wrong-workdir damage would already be
// done before the sentinel could observe the drift.
func TestDispatchTool_HookFiresBeforeHandler(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub2, _ := NewHub()
	wd := t.TempDir()
	s, _ := hub2.Create(CreateOptions{Workdir: wd, Model: "m"})

	var hookCalledAt, handlerCalledAt atomic.Int64
	hook := func(_ *Session, _ string) {
		hookCalledAt.Store(int64(seqNext()))
	}
	handler := func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		handlerCalledAt.Store(int64(seqNext()))
		return "ok", nil
	}
	wrapped := s.wrapHandler(handler, hook)
	if _, err := wrapped(context.Background(), "noop", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	if hookCalledAt.Load() == 0 {
		t.Fatalf("hook never fired")
	}
	if handlerCalledAt.Load() == 0 {
		t.Fatalf("handler never fired")
	}
	if hookCalledAt.Load() >= handlerCalledAt.Load() {
		t.Fatalf("hook fired AFTER handler: hook=%d handler=%d",
			hookCalledAt.Load(), handlerCalledAt.Load())
	}
}

// seqNext is a process-wide monotonic ticker used by ordering tests.
// We use it instead of time.Now() because two calls inside the same
// nanosecond round to the same value on Linux.
var seqCounter atomic.Int64

func seqNext() int64 { return seqCounter.Add(1) }
