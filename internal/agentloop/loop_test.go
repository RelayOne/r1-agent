package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// mockProvider simulates the Anthropic API for testing.
type mockProvider struct {
	responses []*provider.ChatResponse
	callIdx   int
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	if m.callIdx >= len(m.responses) {
		return &provider.ChatResponse{StopReason: "end_turn"}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

func (m *mockProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	resp, err := m.Chat(req)
	if err != nil {
		return nil, err
	}
	// Simulate streaming text events
	for _, c := range resp.Content {
		if c.Type == "text" && onEvent != nil {
			onEvent(stream.Event{DeltaText: c.Text})
		}
	}
	return resp, err
}

func TestLoopSingleTurnNoTools(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{{
			Content: []provider.ResponseContent{
				{Type: "text", Text: "Hello, I can help with that."},
			},
			StopReason: "end_turn",
			Usage:      stream.TokenUsage{Input: 100, Output: 20},
		}},
	}

	loop := New(mock, Config{Model: "claude-sonnet-4-5"}, nil, nil)
	result, err := loop.Run(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("stop_reason=%q, want end_turn", result.StopReason)
	}
	if result.Turns != 1 {
		t.Errorf("turns=%d, want 1", result.Turns)
	}
	if result.FinalText != "Hello, I can help with that." {
		t.Errorf("final_text=%q", result.FinalText)
	}
	if result.TotalCost.InputTokens != 100 {
		t.Errorf("input_tokens=%d, want 100", result.TotalCost.InputTokens)
	}
}

func TestLoopToolUseAndResult(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "Let me read that file."},
					{Type: "tool_use", ID: "toolu_01", Name: "read_file",
						Input: map[string]interface{}{"path": "/src/main.go"}},
				},
				StopReason: "tool_use",
				Usage:      stream.TokenUsage{Input: 150, Output: 30},
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "The file contains a hello world program."},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 200, Output: 25},
			},
		},
	}

	var toolCalls []string
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		toolCalls = append(toolCalls, name)
		return "package main\n\nfunc main() {}", nil
	}

	tools := []provider.ToolDef{{Name: "read_file", Description: "Read a file"}}
	loop := New(mock, Config{Model: "claude-sonnet-4-5"}, tools, handler)
	result, err := loop.Run(context.Background(), "Read main.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Turns != 2 {
		t.Errorf("turns=%d, want 2", result.Turns)
	}
	if len(toolCalls) != 1 || toolCalls[0] != "read_file" {
		t.Errorf("tool_calls=%v, want [read_file]", toolCalls)
	}
	if result.TotalCost.InputTokens != 350 {
		t.Errorf("input_tokens=%d, want 350", result.TotalCost.InputTokens)
	}
	if result.TotalCost.OutputTokens != 55 {
		t.Errorf("output_tokens=%d, want 55", result.TotalCost.OutputTokens)
	}
}

func TestLoopToolError(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "tool_use", ID: "toolu_01", Name: "read_file",
						Input: map[string]interface{}{"path": "/missing"}},
				},
				StopReason: "tool_use",
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "The file was not found."},
				},
				StopReason: "end_turn",
			},
		},
	}

	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "", fmt.Errorf("ENOENT: no such file or directory")
	}

	loop := New(mock, Config{Model: "claude-sonnet-4-5"}, nil, handler)
	result, err := loop.Run(context.Background(), "Read missing file")
	if err != nil {
		t.Fatal(err)
	}

	// Check that tool_result with is_error was sent
	if len(result.Messages) < 3 {
		t.Fatalf("expected 3+ messages, got %d", len(result.Messages))
	}
	toolResultMsg := result.Messages[2] // user message with tool_result
	if toolResultMsg.Content[0].Type != "tool_result" {
		t.Errorf("expected tool_result, got %s", toolResultMsg.Content[0].Type)
	}
	if !toolResultMsg.Content[0].IsError {
		t.Error("expected is_error=true")
	}
}

func TestLoopMaxConsecutiveErrors(t *testing.T) {
	// Every turn produces a tool call that errors
	var responses []*provider.ChatResponse
	for i := 0; i < 5; i++ {
		responses = append(responses, &provider.ChatResponse{
			Content: []provider.ResponseContent{
				{Type: "tool_use", ID: fmt.Sprintf("toolu_%d", i), Name: "bad_tool",
					Input: map[string]interface{}{}},
			},
			StopReason: "tool_use",
		})
	}
	mock := &mockProvider{responses: responses}

	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "", fmt.Errorf("always fails")
	}

	loop := New(mock, Config{Model: "test", MaxConsecutiveErrs: 3}, nil, handler)
	result, err := loop.Run(context.Background(), "Do something")
	if err == nil {
		t.Fatal("expected error from max consecutive errors")
	}
	if result.StopReason != "max_errors" {
		t.Errorf("stop_reason=%q, want max_errors", result.StopReason)
	}
	if !strings.Contains(err.Error(), "3 consecutive") {
		t.Errorf("error=%q, should mention 3 consecutive", err.Error())
	}
}

func TestLoopMaxTurns(t *testing.T) {
	// Every response requests another tool call
	var responses []*provider.ChatResponse
	for i := 0; i < 10; i++ {
		responses = append(responses, &provider.ChatResponse{
			Content: []provider.ResponseContent{
				{Type: "tool_use", ID: fmt.Sprintf("toolu_%d", i), Name: "read_file",
					Input: map[string]interface{}{"path": "/file"}},
			},
			StopReason: "tool_use",
		})
	}
	mock := &mockProvider{responses: responses}

	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "ok", nil
	}

	loop := New(mock, Config{Model: "test", MaxTurns: 3}, nil, handler)
	result, err := loop.Run(context.Background(), "Keep going")
	if err == nil {
		t.Fatal("expected error from max turns")
	}
	if result.StopReason != "max_turns" {
		t.Errorf("stop_reason=%q, want max_turns", result.StopReason)
	}
}

func TestLoopContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := &mockProvider{
		responses: []*provider.ChatResponse{{StopReason: "end_turn"}},
	}

	loop := New(mock, Config{Model: "test"}, nil, nil)
	result, err := loop.Run(ctx, "Hello")
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
	if result.StopReason != "cancelled" {
		t.Errorf("stop_reason=%q, want cancelled", result.StopReason)
	}
}

func TestLoopStreamingCallback(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{{
			Content: []provider.ResponseContent{
				{Type: "text", Text: "Hello world"},
			},
			StopReason: "end_turn",
		}},
	}

	var streamed string
	loop := New(mock, Config{Model: "test"}, nil, nil)
	loop.SetOnText(func(text string) {
		streamed += text
	})
	_, err := loop.Run(context.Background(), "Hi")
	if err != nil {
		t.Fatal(err)
	}
	if streamed != "Hello world" {
		t.Errorf("streamed=%q, want 'Hello world'", streamed)
	}
}

func TestLoopParallelToolExecution(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "tool_use", ID: "toolu_01", Name: "read_file",
						Input: map[string]interface{}{"path": "/a.go"}},
					{Type: "tool_use", ID: "toolu_02", Name: "read_file",
						Input: map[string]interface{}{"path": "/b.go"}},
				},
				StopReason: "tool_use",
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "Both files read."},
				},
				StopReason: "end_turn",
			},
		},
	}

	// callCount is incremented concurrently from parallel tool
	// goroutines, so it must use sync/atomic to avoid a -race report.
	var callCount int64
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		atomic.AddInt64(&callCount, 1)
		return "content", nil
	}

	loop := New(mock, Config{Model: "test"}, nil, handler)
	result, err := loop.Run(context.Background(), "Read both files")
	if err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt64(&callCount); got != 2 {
		t.Errorf("call_count=%d, want 2", got)
	}

	// Should have tool_result messages for both
	toolResultMsg := result.Messages[2] // user message with tool results
	if len(toolResultMsg.Content) != 2 {
		t.Errorf("expected 2 tool results, got %d", len(toolResultMsg.Content))
	}
}

func TestCostTrackerPricing(t *testing.T) {
	ct := CostTracker{
		InputTokens:      1_000_000,
		OutputTokens:     100_000,
		CacheWriteTokens: 500_000,
		CacheReadTokens:  2_000_000,
	}

	// Sonnet pricing: $3 input, $15 output, $3.75 cache write, $0.30 cache read
	cost := ct.TotalCostUSD("claude-sonnet-4-5")
	expected := 3.0 + 1.5 + 1.875 + 0.6 // = 6.975
	if cost < 6.97 || cost > 6.98 {
		t.Errorf("sonnet cost=%.4f, want ~%.4f", cost, expected)
	}

	// Opus pricing: $5 input, $25 output, $6.25 cache write, $0.50 cache read
	cost = ct.TotalCostUSD("claude-opus-4-5")
	expected = 5.0 + 2.5 + 3.125 + 1.0 // = 11.625
	if cost < 11.62 || cost > 11.63 {
		t.Errorf("opus cost=%.4f, want ~%.4f", cost, expected)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}
	cfg.defaults()
	if cfg.MaxTurns != 25 {
		t.Errorf("MaxTurns=%d, want 25", cfg.MaxTurns)
	}
	if cfg.MaxConsecutiveErrs != 3 {
		t.Errorf("MaxConsecutiveErrs=%d, want 3", cfg.MaxConsecutiveErrs)
	}
	if cfg.MaxTokens != 16000 {
		t.Errorf("MaxTokens=%d, want 16000", cfg.MaxTokens)
	}
}

func TestExtractText(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "thinking", Text: "internal reasoning"},
		{Type: "text", Text: "Hello "},
		{Type: "tool_use", Name: "read_file"},
		{Type: "text", Text: "world"},
	}
	got := extractText(blocks)
	if got != "Hello world" {
		t.Errorf("extractText=%q, want 'Hello world'", got)
	}
}
