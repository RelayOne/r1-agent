package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
	"github.com/RelayOne/r1/internal/bench/agents"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// mockProvider is a scripted provider.Provider for the self-benchmark
// integration test: on the first turn it emits a write_file tool call
// that creates a file in the working tree, then on the next turn it emits
// a final text answer and stops. No network, no credentials.
type mockProvider struct {
	turn int
	file string // relative path to create
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return m.next()
}

func (m *mockProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	resp, err := m.next()
	if err != nil {
		return nil, err
	}
	// Surface the response as a stream event so the invoker's trajectory
	// log captures the tool call (mirrors a real streamed turn).
	for _, c := range resp.Content {
		if c.Type == "tool_use" {
			onEvent(stream.Event{Type: "tool_use", ToolUses: []stream.ToolUse{{ID: c.ID, Name: c.Name, Input: c.Input}}})
		}
		if c.Type == "text" && c.Text != "" {
			onEvent(stream.Event{Type: "text", DeltaText: c.Text})
		}
	}
	return resp, nil
}

func (m *mockProvider) next() (*provider.ChatResponse, error) {
	m.turn++
	if m.turn == 1 {
		return &provider.ChatResponse{
			Model:      "mock",
			StopReason: "tool_use",
			Content: []provider.ResponseContent{{
				Type: "tool_use", ID: "t1", Name: "write_file",
				Input: map[string]any{"path": m.file, "content": "package main\n\nfunc Added() {}\n"},
			}},
		}, nil
	}
	return &provider.ChatResponse{
		Model:      "mock",
		StopReason: "end_turn",
		Content:    []provider.ResponseContent{{Type: "text", Text: "Done: created " + m.file + " as requested."}},
	}, nil
}

// TestNativeInvokerDrivesRealLoop is the SOTA gap #1 proof: the production
// NativeInvoker drives r1's real native agentloop (via NativeRunner) end
// to end, so the truthful-completion benchmark now actually runs r1. It
// uses a scripted mock provider so it needs no model credentials — the
// exact reason NativeRunner.ProviderOverride exists.
func TestNativeInvokerDrivesRealLoop(t *testing.T) {
	workDir := t.TempDir()

	inv := &NativeInvoker{
		Model:            "mock",
		ProviderOverride: &mockProvider{file: "added.go"},
		MaxTurns:         4,
	}
	mission := &bench.MissionConfig{
		ID:         "m-selftest",
		Intent:     "Create added.go with an Added function",
		Acceptance: []string{"added.go exists"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := inv.Invoke(ctx, mission, workDir, false)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	// The agent's file edit must have actually landed in the working tree.
	if _, statErr := os.Stat(filepath.Join(workDir, "added.go")); statErr != nil {
		t.Fatalf("invoker did not drive the real tool loop — added.go not created: %v", statErr)
	}
	if !res.CompletionAttempted {
		t.Error("CompletionAttempted should be true after a completed run")
	}
	if res.ExitReason != agents.ExitReasonCompletionClaimed {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, agents.ExitReasonCompletionClaimed)
	}
	if !strings.Contains(res.LastAssistantText, "Done") {
		t.Errorf("LastAssistantText missing the final answer: %q", res.LastAssistantText)
	}
}

// TestSetR1ModelInvokerWiresDispatcher proves the registry setter installs
// the invoker on the registered r1 dispatchers (so `--agent r1` uses it).
func TestSetR1ModelInvokerWiresDispatcher(t *testing.T) {
	inv := &NativeInvoker{Model: "mock", ProviderOverride: &mockProvider{file: "x.go"}}
	agents.SetR1ModelInvoker(inv)
	defer agents.SetR1ModelInvoker(nil) // restore stub behavior for other tests

	d, ok := agents.Lookup("r1").(*agents.R1Dispatcher)
	if !ok || d == nil {
		t.Fatal("r1 dispatcher not registered")
	}
	if d.ModelInvoker == nil {
		t.Error("SetR1ModelInvoker did not install the invoker on the r1 dispatcher")
	}
}
