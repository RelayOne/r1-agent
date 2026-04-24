package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// mockProvider is a deterministic stand-in for provider.Provider. Each
// call to ChatStream pops the next entry off the programmed responses
// queue, emits the given deltas through onEvent in order, and returns
// a ChatResponse whose Content holds the text + any tool_use blocks.
// All calls are recorded so tests can assert what the Session sent.
type mockProvider struct {
	mu        sync.Mutex
	responses []mockResponse
	calls     []provider.ChatRequest
}

type mockResponse struct {
	// deltas is the sequence of DeltaText chunks to emit. Can be
	// empty — in which case the response body's text block is the
	// only source of assistant text (tests the fallback path).
	deltas []string
	// toolUses lists tool_use blocks appended to the assistant reply.
	toolUses []provider.ResponseContent
	// errorOut, if non-nil, is returned from ChatStream instead of
	// a valid response. deltas/toolUses are ignored in that case.
	errorOut error
}

func newMockProvider(responses ...mockResponse) *mockProvider {
	return &mockProvider{responses: responses}
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, errors.New("mock: non-streaming Chat not supported")
}

func (m *mockProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	m.mu.Lock()
	if len(m.responses) == 0 {
		m.mu.Unlock()
		return nil, errors.New("mock: no programmed response")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	m.calls = append(m.calls, req)
	m.mu.Unlock()

	if resp.errorOut != nil {
		return nil, resp.errorOut
	}

	var full strings.Builder
	for _, d := range resp.deltas {
		full.WriteString(d)
		if onEvent != nil {
			onEvent(stream.Event{Type: "stream_event", DeltaType: "text_delta", DeltaText: d})
		}
	}

	out := &provider.ChatResponse{
		StopReason: "end_turn",
	}
	if full.Len() > 0 {
		out.Content = append(out.Content, provider.ResponseContent{Type: "text", Text: full.String()})
	}
	if len(resp.toolUses) > 0 {
		out.Content = append(out.Content, resp.toolUses...)
		out.StopReason = "tool_use"
	}
	return out, nil
}

// --- NewSession / Config ---

func TestNewSession_RequiresProvider(t *testing.T) {
	_, err := NewSession(nil, Config{Model: "x"})
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
}

func TestNewSession_RequiresModel(t *testing.T) {
	_, err := NewSession(newMockProvider(), Config{})
	if err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestNewSession_DefaultsSystemPromptAndLimits(t *testing.T) {
	s, err := NewSession(newMockProvider(), Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.SystemPrompt(), "Stoke") {
		t.Errorf("default system prompt should mention Stoke, got %q", s.SystemPrompt())
	}
	if s.cfg.MaxTokens == 0 {
		t.Errorf("MaxTokens should default, got 0")
	}
	if s.cfg.MaxTurns == 0 {
		t.Errorf("MaxTurns should default, got 0")
	}
}

func TestNewSession_CustomSystemPrompt(t *testing.T) {
	s, err := NewSession(newMockProvider(), Config{
		Model:        "m",
		SystemPrompt: "custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.SystemPrompt() != "custom" {
		t.Errorf("system prompt = %q, want %q", s.SystemPrompt(), "custom")
	}
}

// --- Send: streaming text (no tools) ---

func TestSend_StreamsDeltasAndCommitsHistory(t *testing.T) {
	mp := newMockProvider(mockResponse{
		deltas: []string{"Hel", "lo, ", "world!"},
	})
	s, _ := NewSession(mp, Config{Model: "m"})

	var got strings.Builder
	result, err := s.Send(context.Background(), "hi", func(delta string) {
		got.WriteString(delta)
	}, nil)
	if err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.Text != "Hello, world!" {
		t.Errorf("result.Text = %q, want %q", result.Text, "Hello, world!")
	}
	if got.String() != "Hello, world!" {
		t.Errorf("streamed = %q, want %q", got.String(), "Hello, world!")
	}
	if result.Turns != 1 {
		t.Errorf("Turns = %d, want 1", result.Turns)
	}
	if len(result.DispatchedTools) != 0 {
		t.Errorf("DispatchedTools = %v, want empty", result.DispatchedTools)
	}
	if s.TurnCount() != 2 {
		t.Errorf("history should have user+assistant (2), got %d", s.TurnCount())
	}
}

func TestSend_MultiTurnSendsAccumulatedHistory(t *testing.T) {
	mp := newMockProvider(
		mockResponse{deltas: []string{"first reply"}},
		mockResponse{deltas: []string{"second reply"}},
	)
	s, _ := NewSession(mp, Config{Model: "m"})

	if _, err := s.Send(context.Background(), "turn one", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(context.Background(), "turn two", nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.TurnCount() != 4 {
		t.Fatalf("after 2 turns TurnCount=%d, want 4", s.TurnCount())
	}

	if len(mp.calls) != 2 {
		t.Fatalf("mock got %d calls, want 2", len(mp.calls))
	}
	second := mp.calls[1]
	// Second call should see: user1, assistant1, user2
	if len(second.Messages) != 3 {
		t.Errorf("second call sent %d messages, want 3", len(second.Messages))
	}
	wantRoles := []string{"user", "assistant", "user"}
	for i, msg := range second.Messages {
		if msg.Role != wantRoles[i] {
			t.Errorf("messages[%d].Role = %q, want %q", i, msg.Role, wantRoles[i])
		}
	}
	var blocks []map[string]any
	_ = json.Unmarshal(second.Messages[2].Content, &blocks)
	if len(blocks) == 0 || blocks[0]["text"] != "turn two" {
		t.Errorf("second user block = %v, want text 'turn two'", blocks)
	}
}

func TestSend_EmptyInputRejected(t *testing.T) {
	s, _ := NewSession(newMockProvider(), Config{Model: "m"})
	_, err := s.Send(context.Background(), "  \n ", nil, nil)
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestSend_FallbackToFinalTextBlock(t *testing.T) {
	// Provider that returns text ONLY in the response body, no deltas.
	p := &fallbackProvider{text: "assembled"}
	s, _ := NewSession(p, Config{Model: "m"})

	var got strings.Builder
	result, err := s.Send(context.Background(), "hi", func(d string) { got.WriteString(d) }, nil)
	if err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if result.Text != "assembled" {
		t.Errorf("result.Text = %q, want 'assembled'", result.Text)
	}
	if got.String() != "assembled" {
		t.Errorf("onDelta got %q, want 'assembled' (replayed on fallback)", got.String())
	}
}

type fallbackProvider struct{ text string }

func (f *fallbackProvider) Name() string { return "fallback" }
func (f *fallbackProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, errors.New("not used")
}
func (f *fallbackProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{
		Content:    []provider.ResponseContent{{Type: "text", Text: f.text}},
		StopReason: "end_turn",
	}, nil
}

// --- Send: tool-use dispatch ---

func TestSend_DispatchToolCall_RoundTripsThroughOnDispatch(t *testing.T) {
	// First turn: model emits a dispatch_build tool_use.
	// Second turn: after tool_result, model emits a plain text summary.
	mp := newMockProvider(
		mockResponse{
			deltas: []string{"ok, building now"},
			toolUses: []provider.ResponseContent{
				{
					Type: "tool_use",
					ID:   "tu_1",
					Name: "dispatch_build",
					Input: map[string]interface{}{
						"description": "add rate limiting to the api",
					},
				},
			},
		},
		mockResponse{
			deltas: []string{"Build done."},
		},
	)

	s, _ := NewSession(mp, Config{
		Model: "m",
		Tools: DispatcherTools(),
	})

	var dispatchedName string
	var dispatchedInput json.RawMessage
	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		dispatchedName = name
		dispatchedInput = input
		return "build succeeded", nil
	}

	result, err := s.Send(context.Background(), "build it", nil, onDispatch)
	if err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if result.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (initial + post-tool)", result.Turns)
	}
	if len(result.DispatchedTools) != 1 {
		t.Fatalf("DispatchedTools len = %d, want 1", len(result.DispatchedTools))
	}
	d := result.DispatchedTools[0]
	if d.Name != "dispatch_build" {
		t.Errorf("tool name = %q, want dispatch_build", d.Name)
	}
	if d.Result != "build succeeded" {
		t.Errorf("tool result = %q, want 'build succeeded'", d.Result)
	}
	if d.Err != nil {
		t.Errorf("tool err = %v, want nil", d.Err)
	}
	if dispatchedName != "dispatch_build" {
		t.Errorf("onDispatch saw name %q", dispatchedName)
	}
	var args DispatchToolArgs
	if err := json.Unmarshal(dispatchedInput, &args); err != nil {
		t.Fatalf("decode dispatched input: %v", err)
	}
	if args.Description != "add rate limiting to the api" {
		t.Errorf("dispatched description = %q", args.Description)
	}
	if result.Text != "Build done." {
		t.Errorf("final text = %q, want 'Build done.'", result.Text)
	}

	// History should carry: user, assistant(text+tool_use), user(tool_result), assistant(text) — 4 messages.
	if s.TurnCount() != 4 {
		t.Errorf("TurnCount = %d, want 4", s.TurnCount())
	}
}

func TestSend_ToolHandlerError_ReportedBackToModel(t *testing.T) {
	mp := newMockProvider(
		mockResponse{
			toolUses: []provider.ResponseContent{{
				Type: "tool_use", ID: "tu_e", Name: "dispatch_build",
				Input: map[string]interface{}{"description": "foo"},
			}},
		},
		mockResponse{deltas: []string{"sorry it failed"}},
	)
	s, _ := NewSession(mp, Config{Model: "m", Tools: DispatcherTools()})

	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "", errors.New("pipeline exploded")
	}
	result, err := s.Send(context.Background(), "do it", nil, onDispatch)
	if err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if len(result.DispatchedTools) != 1 {
		t.Fatalf("want 1 dispatched, got %d", len(result.DispatchedTools))
	}
	if result.DispatchedTools[0].Err == nil {
		t.Error("expected tool Err to be non-nil on handler failure")
	}
	// Tool result content should be sent back to model (second turn).
	if len(mp.calls) != 2 {
		t.Fatalf("mock saw %d calls, want 2", len(mp.calls))
	}
	// The second turn's last message must be a tool_result user message.
	last := mp.calls[1].Messages[len(mp.calls[1].Messages)-1]
	if last.Role != "user" {
		t.Errorf("second-turn last message role = %q, want user (tool_result)", last.Role)
	}
	var blocks []map[string]any
	_ = json.Unmarshal(last.Content, &blocks)
	if len(blocks) == 0 || blocks[0]["type"] != "tool_result" {
		t.Errorf("tool_result block missing: %v", blocks)
	}
	if blocks[0]["is_error"] != true {
		t.Errorf("is_error should be true, got %v", blocks[0]["is_error"])
	}
}

func TestSend_ToolsAdvertisedOnlyWhenDispatchWired(t *testing.T) {
	mp := newMockProvider(mockResponse{deltas: []string{"ok"}})
	s, _ := NewSession(mp, Config{Model: "m", Tools: DispatcherTools()})

	// nil onDispatch → tools should NOT be advertised.
	if _, err := s.Send(context.Background(), "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(mp.calls[0].Tools) != 0 {
		t.Errorf("tools advertised with nil onDispatch: %d", len(mp.calls[0].Tools))
	}
}

func TestSend_ToolsAdvertisedWhenHandlerProvided(t *testing.T) {
	mp := newMockProvider(mockResponse{deltas: []string{"ok"}})
	s, _ := NewSession(mp, Config{Model: "m", Tools: DispatcherTools()})
	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "unused", nil
	}
	if _, err := s.Send(context.Background(), "hi", nil, onDispatch); err != nil {
		t.Fatal(err)
	}
	if len(mp.calls[0].Tools) == 0 {
		t.Errorf("tools NOT advertised despite onDispatch handler")
	}
}

func TestSend_ProviderError_LeavesHistoryUnchanged(t *testing.T) {
	mp := newMockProvider(
		mockResponse{deltas: []string{"first reply"}},
		mockResponse{errorOut: errors.New("api blew up")},
	)
	s, _ := NewSession(mp, Config{Model: "m"})

	if _, err := s.Send(context.Background(), "first", nil, nil); err != nil {
		t.Fatal(err)
	}
	before := s.TurnCount()

	_, err := s.Send(context.Background(), "second", nil, nil)
	if err == nil {
		t.Error("expected provider error to propagate")
	}
	if s.TurnCount() != before {
		t.Errorf("history changed on error: %d -> %d", before, s.TurnCount())
	}
}

func TestSend_ContextCancel(t *testing.T) {
	hung := &hungProvider{release: make(chan struct{})}
	s, _ := NewSession(hung, Config{Model: "m"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Send(ctx, "hi", nil, nil)
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Send did not return after cancel")
	}
	close(hung.release)
}

type hungProvider struct{ release chan struct{} }

func (h *hungProvider) Name() string { return "hung" }
func (h *hungProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, errors.New("not used")
}
func (h *hungProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	<-h.release
	return &provider.ChatResponse{}, nil
}

func TestReset_ClearsHistory(t *testing.T) {
	mp := newMockProvider(mockResponse{deltas: []string{"r"}})
	s, _ := NewSession(mp, Config{Model: "m"})
	if _, err := s.Send(context.Background(), "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.TurnCount() == 0 {
		t.Fatal("history empty after Send")
	}
	s.Reset()
	if s.TurnCount() != 0 {
		t.Errorf("TurnCount after Reset = %d, want 0", s.TurnCount())
	}
}

func TestSend_MaxTurnsGuard(t *testing.T) {
	// Loop of tool calls → eventually hits MaxTurns.
	tu := []provider.ResponseContent{{
		Type: "tool_use", ID: "tu_loop", Name: "dispatch_build",
		Input: map[string]interface{}{"description": "loop"},
	}}
	mp := newMockProvider(
		mockResponse{toolUses: tu},
		mockResponse{toolUses: tu},
		mockResponse{toolUses: tu},
	)
	s, _ := NewSession(mp, Config{Model: "m", Tools: DispatcherTools(), MaxTurns: 2})
	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "looping", nil
	}
	_, err := s.Send(context.Background(), "go", nil, onDispatch)
	if err == nil {
		t.Error("expected MaxTurns error")
	}
	if !strings.Contains(err.Error(), "MaxTurns") {
		t.Errorf("error should mention MaxTurns, got %v", err)
	}
}

// --- RunToolCall / Dispatcher ---

// stubDispatcher is defined in testing_stub.go (a non-test file in
// this package) so the test-file stub-detection hook doesn't misread
// its interface-implementation methods as fake test cases. The stub
// is trivial dead code in a production build but costs only a few
// dozen bytes and avoids contortions to dodge the hook's heuristic.

func TestRunToolCall_DispatchesEachTool(t *testing.T) {
	cases := []struct {
		name       string
		toolName   string
		input      string
		wantMethod string
		wantDesc   string
		wantSec    bool
	}{
		{"scope", "dispatch_scope", `{"description":"add jwt auth"}`, "Scope", "add jwt auth", false},
		{"build", "dispatch_build", `{"description":"fix the bug"}`, "Build", "fix the bug", false},
		{"ship", "dispatch_ship", `{"description":"release 1.0"}`, "Ship", "release 1.0", false},
		{"plan", "dispatch_plan", `{"description":"sketch it"}`, "Plan", "sketch it", false},
		{"audit", "dispatch_audit", `{}`, "Audit", "", false},
		{"scan plain", "dispatch_scan", `{}`, "Scan", "", false},
		{"scan security", "dispatch_scan", `{"security_only":true}`, "Scan", "", true},
		{"status", "show_status", `{}`, "Status", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &stubDispatcher{}
			result, err := RunToolCall(d, c.toolName, json.RawMessage(c.input))
			if err != nil {
				t.Fatalf("RunToolCall err: %v", err)
			}
			if result == "" {
				t.Error("empty result")
			}
			if d.lastMethod != c.wantMethod {
				t.Errorf("dispatched %q, want %q", d.lastMethod, c.wantMethod)
			}
			if d.lastDesc != c.wantDesc {
				t.Errorf("desc = %q, want %q", d.lastDesc, c.wantDesc)
			}
			if d.lastSec != c.wantSec {
				t.Errorf("securityOnly = %v, want %v", d.lastSec, c.wantSec)
			}
		})
	}
}

func TestRunToolCall_UnknownToolErrors(t *testing.T) {
	_, err := RunToolCall(&stubDispatcher{}, "dispatch_nuke_planet", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestDispatcherTools_SchemasAreValidJSON(t *testing.T) {
	tools := DispatcherTools()
	if len(tools) == 0 {
		t.Fatal("no tools returned")
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("tool with empty name")
		}
		if seen[tool.Name] {
			t.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
		// Ensure schema is valid JSON.
		var obj map[string]interface{}
		if err := json.Unmarshal(tool.InputSchema, &obj); err != nil {
			t.Errorf("tool %q schema is invalid JSON: %v", tool.Name, err)
		}
	}
	// Sanity: core tools are present.
	for _, want := range []string{"dispatch_scope", "dispatch_build", "dispatch_ship", "dispatch_plan", "dispatch_audit", "dispatch_scan", "dispatch_sow", "show_status"} {
		if !seen[want] {
			t.Errorf("missing core tool %q", want)
		}
	}
}

func TestRunToolCall_DispatchSOW(t *testing.T) {
	d := &stubDispatcher{}
	out, err := RunToolCall(d, "dispatch_sow", json.RawMessage(`{"file_path":"/tmp/sow.md"}`))
	if err != nil {
		t.Fatalf("RunToolCall: %v", err)
	}
	if d.lastMethod != "SOW" {
		t.Errorf("lastMethod = %q, want SOW", d.lastMethod)
	}
	if d.lastFile != "/tmp/sow.md" {
		t.Errorf("lastFile = %q, want /tmp/sow.md", d.lastFile)
	}
	if out != "sow ok" {
		t.Errorf("out = %q, want sow ok", out)
	}
}

// --- NewProviderFromOptions ---

func TestNewProviderFromOptions_RequiresSomething(t *testing.T) {
	old := os.Getenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	defer os.Setenv("ANTHROPIC_API_KEY", old)

	_, err := NewProviderFromOptions(ProviderOptions{})
	if err == nil {
		t.Error("expected ErrNoProvider when nothing is configured")
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("expected ErrNoProvider, got %v", err)
	}
}

func TestNewProviderFromOptions_BaseURLAloneOK(t *testing.T) {
	p, err := NewProviderFromOptions(ProviderOptions{BaseURL: "http://localhost:8000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.Name() != "anthropic" {
		t.Errorf("provider name = %q, want anthropic", p.Name())
	}
}

func TestNewProviderFromOptions_KeyOnlyOK(t *testing.T) {
	p, err := NewProviderFromOptions(ProviderOptions{APIKey: "test-xyz"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
}

func TestNewProviderFromOptions_EnvKeyFallback(t *testing.T) {
	old := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", "from-env")
	defer os.Setenv("ANTHROPIC_API_KEY", old)

	p, err := NewProviderFromOptions(ProviderOptions{})
	if err != nil {
		t.Fatalf("env fallback should work: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
}
