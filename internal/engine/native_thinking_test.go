package engine

// native_thinking_test.go — PhaseSpec.ThinkingBudget must reach the
// agentloop through the native runner's Config. Uses the same offline
// fake-provider pattern as native_runner_mcp_test.go: no API, no
// network, no credentials.

import (
	"context"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// thinkingCaptureProvider records the ChatRequests the agentloop sends.
type thinkingCaptureProvider struct {
	requests []provider.ChatRequest
}

func (f *thinkingCaptureProvider) Name() string { return "fake-thinking" }

func (f *thinkingCaptureProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	f.requests = append(f.requests, req)
	return &provider.ChatResponse{
		Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
	}, nil
}

func (f *thinkingCaptureProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

func TestNativeRunnerThreadsThinkingBudget(t *testing.T) {
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.Phase.ThinkingBudget = 4096

	fp := &thinkingCaptureProvider{}
	runner.ProviderOverride = fp
	if _, err := runner.Run(context.Background(), spec, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fp.requests) == 0 {
		t.Fatal("no ChatRequest captured")
	}
	got := fp.requests[0].Thinking
	if got == nil {
		t.Fatal("ChatRequest.Thinking=nil, want spec threaded from PhaseSpec")
	}
	if got.BudgetTokens != 4096 {
		t.Errorf("BudgetTokens=%d, want 4096", got.BudgetTokens)
	}
}

func TestNativeRunnerZeroThinkingBudgetByDefault(t *testing.T) {
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)

	fp := &thinkingCaptureProvider{}
	runner.ProviderOverride = fp
	if _, err := runner.Run(context.Background(), spec, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fp.requests) == 0 {
		t.Fatal("no ChatRequest captured")
	}
	if fp.requests[0].Thinking != nil {
		t.Errorf("Thinking=%+v, want nil when PhaseSpec.ThinkingBudget is unset", fp.requests[0].Thinking)
	}
}
