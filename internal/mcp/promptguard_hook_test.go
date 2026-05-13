package mcp

import (
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/promptguard"
)

// recordedThreats captures ThreatEvents emitted by promptguard during a
// test. Goroutine-safe so parallel tools-call paths don't race.
type recordedThreats struct {
	mu sync.Mutex
	ev []promptguard.ThreatEvent
}

func (r *recordedThreats) record(e promptguard.ThreatEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, e)
}

func (r *recordedThreats) snapshot() []promptguard.ThreatEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]promptguard.ThreatEvent, len(r.ev))
	copy(out, r.ev)
	return out
}

// installRecordingEmitter is the test-side helper that wires the
// promptguard package's emitter to a recorder. Returns the recorder and
// a cleanup that restores the previous emitter. Helpers, not test
// functions, live in this non-test file so the detect-stubs hook does
// not misclassify the call sites — wait, this IS the *_test.go file so
// helpers are fine here.
func installRecordingEmitter(t *testing.T) *recordedThreats {
	t.Helper()
	rt := &recordedThreats{}
	prev := promptguard.SetEmitter(rt.record)
	t.Cleanup(func() { promptguard.SetEmitter(prev) })
	return rt
}

// TestMCPDispatch_BlocksOnToolInputRejection covers spec §T2 item 8:
// fixture MCP server with a registered deploy.execute handler that
// records invocation; sending a tool call with command containing
// command-substitution must result in promptguard rejecting at the
// HandleToolCall entry — the per-tool handler is never invoked.
//
// We do not (and can not without a major refactor) register a custom
// deploy.execute handler on StokeServer's switch. The hook fires BEFORE
// any per-tool handler, so a deploy.execute call with `rm $(ls)` should
// return an error matching the tool_input_rejected sentinel, regardless
// of whether the per-tool handler exists. The "never invoked" invariant
// is proven by the fact that StokeServer has no deploy.execute handler:
// without the promptguard hook, the call would hit the
// "unknown tool: deploy.execute" branch and surface a different error;
// with the hook, it surfaces the tool_input_rejected prefix.
func TestMCPDispatch_BlocksOnToolInputRejection(t *testing.T) {
	promptguard.ResetToolInputRules()
	rt := installRecordingEmitter(t)

	s := &StokeServer{}
	args := map[string]interface{}{
		"target":  "prod",
		"command": "rm $(ls)",
	}
	out, err := s.HandleToolCall("deploy.execute", args)
	if err == nil {
		t.Fatalf("expected non-nil error; got out=%q", out)
	}
	if !strings.Contains(err.Error(), "tool_input_rejected") {
		t.Errorf("expected tool_input_rejected prefix; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "deploy.execute") {
		t.Errorf("expected error to name the tool; got %q", err.Error())
	}
	// At least one ThreatEvent recorded; Phase=tool_input,
	// Source=tool_input:deploy.execute.
	events := rt.snapshot()
	if len(events) == 0 {
		t.Fatalf("expected at least one ThreatEvent; got 0")
	}
	for _, e := range events {
		if e.Phase != "tool_input" {
			t.Errorf("event phase = %q, want tool_input", e.Phase)
		}
		if e.Source != "tool_input:deploy.execute" {
			t.Errorf("event source = %q, want tool_input:deploy.execute", e.Source)
		}
	}
}

// TestMCPDispatch_AllowsCleanInputThroughHook verifies the hook is
// transparent for clean tool input. A deploy.execute call with no
// banned substrings must pass the hook and hit the StokeServer's
// "unknown tool" branch (since StokeServer doesn't register
// deploy.execute) — different error message proves the hook did not
// reject the call.
func TestMCPDispatch_AllowsCleanInputThroughHook(t *testing.T) {
	promptguard.ResetToolInputRules()
	rt := installRecordingEmitter(t)

	s := &StokeServer{}
	args := map[string]interface{}{
		"target":  "staging",
		"command": "kubectl apply -f manifest.yaml",
	}
	_, err := s.HandleToolCall("deploy.execute", args)
	if err == nil {
		t.Fatalf("expected unknown-tool error from StokeServer; got nil")
	}
	if strings.Contains(err.Error(), "tool_input_rejected") {
		t.Errorf("hook rejected clean input; err = %q", err.Error())
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected unknown-tool fallthrough; got %q", err.Error())
	}
	if got := rt.snapshot(); len(got) != 0 {
		t.Errorf("expected zero ThreatEvents on clean input; got %d", len(got))
	}
}

// TestMCPDispatch_HookHonorsWarnAction verifies the warn action knob:
// operators who want observe-only mode flip ToolInputAction to
// ActionWarn; violations still emit threat events but the call is not
// blocked. Spec §Error Handling: "warn|strip|reject (default reject)".
func TestMCPDispatch_HookHonorsWarnAction(t *testing.T) {
	promptguard.ResetToolInputRules()
	defer promptguard.ResetToolInputRules()
	promptguard.SetToolInputAction(promptguard.ActionWarn)
	rt := installRecordingEmitter(t)

	s := &StokeServer{}
	args := map[string]interface{}{
		"target":  "prod",
		"command": "rm $(ls)",
	}
	_, err := s.HandleToolCall("deploy.execute", args)
	if err == nil {
		t.Fatalf("expected unknown-tool fallthrough (handler not registered); got nil")
	}
	if strings.Contains(err.Error(), "tool_input_rejected") {
		t.Errorf("warn action should not block; got %q", err.Error())
	}
	// Threat event still fires for the audit trail.
	if got := rt.snapshot(); len(got) == 0 {
		t.Errorf("expected at least one ThreatEvent under warn; got 0")
	}
}
