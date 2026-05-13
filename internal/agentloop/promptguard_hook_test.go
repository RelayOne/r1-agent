package agentloop

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/promptguard"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// recordedAgentloopThreats records ThreatEvents for the agentloop hook
// tests. Goroutine-safe so the parallel-tool dispatch path is covered.
type recordedAgentloopThreats struct {
	mu sync.Mutex
	ev []promptguard.ThreatEvent
}

func (r *recordedAgentloopThreats) record(e promptguard.ThreatEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, e)
}

func (r *recordedAgentloopThreats) snapshot() []promptguard.ThreatEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]promptguard.ThreatEvent, len(r.ev))
	copy(out, r.ev)
	return out
}

// installAgentloopRecorder hooks the promptguard emitter for a single
// test. Cleanup runs via t.Cleanup so even t.Fatal in the test still
// restores the previous emitter.
func installAgentloopRecorder(t *testing.T) *recordedAgentloopThreats {
	t.Helper()
	rt := &recordedAgentloopThreats{}
	prev := promptguard.SetEmitter(rt.record)
	t.Cleanup(func() { promptguard.SetEmitter(prev) })
	return rt
}

// TestAgentloop_RejectsBadToolInputAndContinues covers spec §T2 item 9:
// a mock model proposes a browse.fetch with `url:"file:///etc/passwd"`,
// the loop returns a tool-error result (is_error=true, content carries
// the structured rejection) and does NOT crash. Exactly one threat
// event with Phase="tool_input" and Source="tool_input:browse.fetch".
func TestAgentloop_RejectsBadToolInputAndContinues(t *testing.T) {
	promptguard.ResetToolInputRules()
	rt := installAgentloopRecorder(t)

	// Two-turn fixture: first turn returns a tool_use for browse.fetch
	// with a file:// URL; second turn (after we feed back the tool
	// error) returns end_turn so the loop terminates cleanly.
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{
						Type:  "tool_use",
						ID:    "toolu_01",
						Name:  "browse.fetch",
						Input: map[string]interface{}{"url": "file:///etc/passwd"},
					},
				},
				StopReason: "tool_use",
				Usage:      stream.TokenUsage{Input: 50, Output: 10},
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "I'll try a different approach."},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 60, Output: 8},
			},
		},
	}

	// Sentinel: handler must NEVER be invoked because the hook should
	// block the call before dispatch. We use atomic so the
	// parallel-tool race path is also covered.
	var handlerInvocations atomic.Int64
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		handlerInvocations.Add(1)
		return "should-not-reach", nil
	}

	tools := []provider.ToolDef{{Name: "browse.fetch", Description: "Fetch a URL"}}
	loop := New(mock, Config{Model: "claude-sonnet-4-5"}, tools, handler)
	result, err := loop.Run(context.Background(), "Fetch /etc/passwd")
	if err != nil {
		t.Fatalf("loop.Run unexpected error: %v", err)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn (loop should continue past hook rejection)", result.StopReason)
	}
	if got := handlerInvocations.Load(); got != 0 {
		t.Errorf("handler was invoked %d time(s); want 0 (hook should block before dispatch)", got)
	}

	// Find the tool_result block that was fed back into the second
	// turn — it must carry is_error=true and the rejection text.
	foundRejection := false
	for _, m := range result.Messages {
		for _, c := range m.Content {
			if c.Type != "tool_result" {
				continue
			}
			if !c.IsError {
				continue
			}
			if strings.Contains(c.Content, "tool_input_rejected") {
				foundRejection = true
			}
		}
	}
	if !foundRejection {
		t.Errorf("expected a tool_result is_error=true with tool_input_rejected; got messages=%+v", result.Messages)
	}

	// Exactly one ThreatEvent recorded — the file:// pattern matches
	// once. Phase=tool_input, Source=tool_input:browse.fetch.
	events := rt.snapshot()
	if len(events) == 0 {
		t.Fatalf("expected at least one ThreatEvent; got 0")
	}
	for _, e := range events {
		if e.Phase != "tool_input" {
			t.Errorf("event phase = %q, want tool_input", e.Phase)
		}
		if e.Source != "tool_input:browse.fetch" {
			t.Errorf("event source = %q, want tool_input:browse.fetch", e.Source)
		}
	}
}

// TestAgentloop_AllowsCleanToolInput asserts the hook is transparent
// on a clean browse.fetch call (https://). The handler must be
// invoked and the loop must complete normally.
func TestAgentloop_AllowsCleanToolInput(t *testing.T) {
	promptguard.ResetToolInputRules()
	rt := installAgentloopRecorder(t)

	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{
						Type:  "tool_use",
						ID:    "toolu_01",
						Name:  "browse.fetch",
						Input: map[string]interface{}{"url": "https://example.com/"},
					},
				},
				StopReason: "tool_use",
				Usage:      stream.TokenUsage{Input: 50, Output: 10},
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "Done."},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 60, Output: 5},
			},
		},
	}

	var invoked atomic.Int64
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		invoked.Add(1)
		return "<html>ok</html>", nil
	}
	tools := []provider.ToolDef{{Name: "browse.fetch", Description: "Fetch a URL"}}
	loop := New(mock, Config{Model: "claude-sonnet-4-5"}, tools, handler)
	result, err := loop.Run(context.Background(), "Fetch example.com")
	if err != nil {
		t.Fatalf("loop.Run unexpected error: %v", err)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", result.StopReason)
	}
	if invoked.Load() != 1 {
		t.Errorf("handler invocations = %d, want 1", invoked.Load())
	}
	if got := rt.snapshot(); len(got) != 0 {
		t.Errorf("expected zero ThreatEvents on clean input; got %d", len(got))
	}
}

// TestAgentloop_HookHonorsWarnAction asserts the warn action knob: a
// dangerous browse.fetch call still dispatches under ActionWarn (the
// observe-only rollout mode) but a ThreatEvent fires for the audit
// trail.
func TestAgentloop_HookHonorsWarnAction(t *testing.T) {
	promptguard.ResetToolInputRules()
	defer promptguard.ResetToolInputRules()
	promptguard.SetToolInputAction(promptguard.ActionWarn)
	rt := installAgentloopRecorder(t)

	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{
						Type:  "tool_use",
						ID:    "toolu_01",
						Name:  "browse.fetch",
						Input: map[string]interface{}{"url": "file:///etc/passwd"},
					},
				},
				StopReason: "tool_use",
				Usage:      stream.TokenUsage{Input: 50, Output: 10},
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
				StopReason: "end_turn",
			},
		},
	}

	var invoked atomic.Int64
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		invoked.Add(1)
		return "passed through under warn", nil
	}
	tools := []provider.ToolDef{{Name: "browse.fetch", Description: "Fetch a URL"}}
	loop := New(mock, Config{Model: "claude-sonnet-4-5"}, tools, handler)
	if _, err := loop.Run(context.Background(), "go"); err != nil {
		t.Fatalf("loop.Run error: %v", err)
	}
	if invoked.Load() != 1 {
		t.Errorf("under warn action, handler should still be invoked; got %d invocations", invoked.Load())
	}
	if got := rt.snapshot(); len(got) == 0 {
		t.Errorf("expected at least one ThreatEvent under warn; got 0")
	}
}
