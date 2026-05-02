package cortex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeRouterProvider is a minimal provider.Provider implementation that
// returns a canned ChatResponse on each ChatStream/Chat call. Tests
// assemble a response with one or more tool_use ResponseContent blocks
// to drive Router.Route through every decoding branch without ever
// touching the network. The captured `lastReq` lets tests assert the
// Router built the expected request (system prompt, tools, history).
type fakeRouterProvider struct {
	resp    *provider.ChatResponse
	err     error
	lastReq *provider.ChatRequest
	calls   int
}

func (f *fakeRouterProvider) Name() string { return "fake-router" }

func (f *fakeRouterProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	f.calls++
	r := req
	f.lastReq = &r
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeRouterProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

// toolUseResp builds a ChatResponse carrying exactly one tool_use block
// with the given name and Input map. Reused across the four happy-path
// subtests so the assertions stay tight.
func toolUseResp(name string, input map[string]any) *provider.ChatResponse {
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{
			{Type: "tool_use", ID: "tu_1", Name: name, Input: input},
		},
		StopReason: "tool_use",
	}
}

func newRouterForTest(t *testing.T, p provider.Provider) *Router {
	t.Helper()
	r, err := NewRouter(RouterConfig{Provider: p, Bus: hub.New()})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func TestNewRouterValidatesProvider(t *testing.T) {
	if _, err := NewRouter(RouterConfig{}); err == nil {
		t.Fatalf("NewRouter with nil Provider: want error, got nil")
	}
}

func TestNewRouterDefaults(t *testing.T) {
	r, err := NewRouter(RouterConfig{Provider: &fakeRouterProvider{}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if r.cfg.Model != "claude-haiku-4-5" {
		t.Errorf("Model default = %q, want claude-haiku-4-5", r.cfg.Model)
	}
	if r.cfg.MaxTokens != 1024 {
		t.Errorf("MaxTokens default = %d, want 1024", r.cfg.MaxTokens)
	}
	if r.cfg.SystemPrompt != DefaultRouterSystemPrompt {
		t.Errorf("SystemPrompt default mismatch")
	}
}

// TestRouteAllFour drives Router.Route through every tool branch and
// asserts the matching RouterDecision payload is populated, the others
// stay nil, and Kind/RawToolName are correct.
func TestRouteAllFour(t *testing.T) {
	cases := []struct {
		name     string
		input    map[string]any
		wantKind DecisionKind
		check    func(t *testing.T, dec *RouterDecision)
	}{
		{
			name:     "interrupt",
			input:    map[string]any{"reason": "user said stop", "new_direction": "switch to bash"},
			wantKind: DecisionInterrupt,
			check: func(t *testing.T, dec *RouterDecision) {
				if dec.Interrupt == nil {
					t.Fatal("Interrupt payload nil")
				}
				if dec.Interrupt.Reason != "user said stop" {
					t.Errorf("Reason=%q", dec.Interrupt.Reason)
				}
				if dec.Interrupt.NewDirection != "switch to bash" {
					t.Errorf("NewDirection=%q", dec.Interrupt.NewDirection)
				}
				if dec.Steer != nil || dec.Queue != nil || dec.JustChat != nil {
					t.Error("non-Interrupt payloads should be nil")
				}
			},
		},
		{
			name:     "steer",
			input:    map[string]any{"severity": "advice", "title": "use Postgres", "body": "the user clarified the DB"},
			wantKind: DecisionSteer,
			check: func(t *testing.T, dec *RouterDecision) {
				if dec.Steer == nil {
					t.Fatal("Steer payload nil")
				}
				if dec.Steer.Severity != "advice" || dec.Steer.Title != "use Postgres" {
					t.Errorf("Steer payload = %+v", dec.Steer)
				}
				if dec.Interrupt != nil || dec.Queue != nil || dec.JustChat != nil {
					t.Error("non-Steer payloads should be nil")
				}
			},
		},
		{
			name:     "queue_mission",
			input:    map[string]any{"brief": "fix auth.go bug after current task", "priority": "high"},
			wantKind: DecisionQueueMission,
			check: func(t *testing.T, dec *RouterDecision) {
				if dec.Queue == nil {
					t.Fatal("Queue payload nil")
				}
				if dec.Queue.Brief != "fix auth.go bug after current task" {
					t.Errorf("Brief=%q", dec.Queue.Brief)
				}
				if dec.Queue.Priority != "high" {
					t.Errorf("Priority=%q", dec.Queue.Priority)
				}
				if dec.Interrupt != nil || dec.Steer != nil || dec.JustChat != nil {
					t.Error("non-Queue payloads should be nil")
				}
			},
		},
		{
			name:     "just_chat",
			input:    map[string]any{"reply": "yes, still working on it"},
			wantKind: DecisionJustChat,
			check: func(t *testing.T, dec *RouterDecision) {
				if dec.JustChat == nil {
					t.Fatal("JustChat payload nil")
				}
				if dec.JustChat.Reply != "yes, still working on it" {
					t.Errorf("Reply=%q", dec.JustChat.Reply)
				}
				if dec.Interrupt != nil || dec.Steer != nil || dec.Queue != nil {
					t.Error("non-JustChat payloads should be nil")
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakeRouterProvider{resp: toolUseResp(tc.name, tc.input)}
			r := newRouterForTest(t, fp)
			dec, err := r.Route(context.Background(), RouterInput{
				UserInput: "test input for " + tc.name,
			})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if dec.Kind != tc.wantKind {
				t.Errorf("Kind=%q want %q", dec.Kind, tc.wantKind)
			}
			if dec.RawToolName != tc.name {
				t.Errorf("RawToolName=%q want %q", dec.RawToolName, tc.name)
			}
			tc.check(t, dec)
		})
	}
}

// TestRouteStopForcesInterrupt is a two-part assertion. Per the spec
// note, "stop"-input → interrupt is enforced by the SYSTEM PROMPT, not
// by deterministic Go code (the model decides). So we (a) assert the
// system prompt contains the literal rule, and (b) confirm that when a
// fake provider returns interrupt for a "stop" input, Route surfaces
// DecisionInterrupt as expected.
func TestRouteStopForcesInterrupt(t *testing.T) {
	// Part (a): the prompt rule must literally name "stop" and "interrupt"
	// — that's the whole enforcement mechanism in cortex-core.
	if !strings.Contains(DefaultRouterSystemPrompt, "stop") {
		t.Error(`DefaultRouterSystemPrompt missing literal "stop"`)
	}
	if !strings.Contains(DefaultRouterSystemPrompt, "interrupt") {
		t.Error(`DefaultRouterSystemPrompt missing literal "interrupt"`)
	}
	if !strings.Contains(DefaultRouterSystemPrompt, "MUST use") {
		t.Error(`DefaultRouterSystemPrompt missing the hard rule wording`)
	}

	// Part (b): a model that follows the rule (mocked here) returns
	// DecisionInterrupt for a "stop" input.
	fp := &fakeRouterProvider{resp: toolUseResp("interrupt", map[string]any{
		"reason":        "user typed stop",
		"new_direction": "halt and await new instructions",
	})}
	r := newRouterForTest(t, fp)
	dec, err := r.Route(context.Background(), RouterInput{UserInput: "stop"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Kind != DecisionInterrupt {
		t.Errorf("Kind=%q want %q", dec.Kind, DecisionInterrupt)
	}
	if dec.Interrupt == nil || dec.Interrupt.Reason == "" {
		t.Error("Interrupt payload missing")
	}
}

// TestRouteNoToolCallErrors confirms a text-only response (zero tool_use
// blocks) returns the spec-mandated error string.
func TestRouteNoToolCallErrors(t *testing.T) {
	fp := &fakeRouterProvider{resp: &provider.ChatResponse{
		Content: []provider.ResponseContent{
			{Type: "text", Text: "I am thinking about what to do"},
		},
		StopReason: "end_turn",
	}}
	r := newRouterForTest(t, fp)
	_, err := r.Route(context.Background(), RouterInput{UserInput: "hi"})
	if err == nil {
		t.Fatal("Route: want error for zero tool calls, got nil")
	}
	const wantSubstr = "no tool call"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error=%q does not contain %q", err.Error(), wantSubstr)
	}
}

// TestRouteMultipleToolCallsUsesFirst confirms that when the model
// emits >1 tool_use block we pick the first and continue (only logging
// a warning). The first tool_use should still drive the decision.
func TestRouteMultipleToolCallsUsesFirst(t *testing.T) {
	fp := &fakeRouterProvider{resp: &provider.ChatResponse{
		Content: []provider.ResponseContent{
			{Type: "tool_use", Name: "steer", Input: map[string]any{
				"severity": "info", "title": "first", "body": "x",
			}},
			{Type: "tool_use", Name: "just_chat", Input: map[string]any{
				"reply": "second",
			}},
		},
		StopReason: "tool_use",
	}}
	r := newRouterForTest(t, fp)
	dec, err := r.Route(context.Background(), RouterInput{UserInput: "x"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Kind != DecisionSteer {
		t.Errorf("Kind=%q want %q (first tool wins)", dec.Kind, DecisionSteer)
	}
	if dec.Steer == nil || dec.Steer.Title != "first" {
		t.Error("first tool's payload should populate Steer")
	}
}

// TestRouteUnknownToolName errors gracefully when the model invents a
// tool not in the schema (defense in depth; the prompt forbids this).
func TestRouteUnknownToolName(t *testing.T) {
	fp := &fakeRouterProvider{resp: toolUseResp("rm_rf", map[string]any{"path": "/"})}
	r := newRouterForTest(t, fp)
	_, err := r.Route(context.Background(), RouterInput{UserInput: "x"})
	if err == nil {
		t.Fatal("Route: want error for unknown tool, got nil")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error=%q does not name the issue", err.Error())
	}
}

// TestRouteProviderError surfaces transport failures verbatim wrapped
// so the REPL can decide whether to retry vs. give up.
func TestRouteProviderError(t *testing.T) {
	fp := &fakeRouterProvider{err: errors.New("boom")}
	r := newRouterForTest(t, fp)
	_, err := r.Route(context.Background(), RouterInput{UserInput: "x"})
	if err == nil {
		t.Fatal("Route: want error from provider, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error=%q lost provider cause", err.Error())
	}
}

// TestRouteRequestShape spot-checks the constructed ChatRequest:
// system prompt is the default, all 4 tools are attached, history is
// trimmed to last-10 messages, and the final user message ends with
// the new input verbatim.
func TestRouteRequestShape(t *testing.T) {
	fp := &fakeRouterProvider{resp: toolUseResp("just_chat", map[string]any{"reply": "ok"})}
	r := newRouterForTest(t, fp)

	// 12 fake history messages — Router should drop the oldest 2.
	hist := make([]agentloop.Message, 12)
	for i := range hist {
		hist[i] = agentloop.Message{
			Role: "user",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "msg-" + string(rune('a'+i))},
			},
		}
	}

	_, err := r.Route(context.Background(), RouterInput{
		UserInput: "the actual new input",
		History:   hist,
		Workspace: []Note{
			{Severity: SevAdvice, Title: "t", Body: "b"},
		},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if fp.lastReq == nil {
		t.Fatal("provider not called")
	}
	if fp.lastReq.Model != "claude-haiku-4-5" {
		t.Errorf("Model=%q", fp.lastReq.Model)
	}
	if fp.lastReq.System != DefaultRouterSystemPrompt {
		t.Error("System prompt mismatch")
	}
	if fp.lastReq.MaxTokens != 1024 {
		t.Errorf("MaxTokens=%d", fp.lastReq.MaxTokens)
	}
	if len(fp.lastReq.Tools) != 4 {
		t.Errorf("Tools len=%d, want 4", len(fp.lastReq.Tools))
	}
	// 10 history + 1 new user = 11 messages.
	if got := len(fp.lastReq.Messages); got != 11 {
		t.Errorf("Messages len=%d, want 11", got)
	}
	last := fp.lastReq.Messages[len(fp.lastReq.Messages)-1]
	if last.Role != "user" {
		t.Errorf("last msg role=%q", last.Role)
	}
	// Confirm the new input appears in the final user message verbatim.
	var blocks []map[string]any
	if err := json.Unmarshal(last.Content, &blocks); err != nil {
		t.Fatalf("unmarshal last content: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("last content blocks=%d", len(blocks))
	}
	text, _ := blocks[0]["text"].(string)
	if !strings.Contains(text, "the actual new input") {
		t.Errorf("final user message missing new input; got %q", text)
	}
	if !strings.Contains(text, "1 advice") {
		t.Errorf("final user message missing workspace summary; got %q", text)
	}
}

func TestRouteEmitsHubEvent(t *testing.T) {
	fp := &fakeRouterProvider{resp: toolUseResp("just_chat", map[string]any{"reply": "hi"})}
	bus := hub.New()

	// Register an Observe-mode subscriber that captures the cortex
	// router event. Buffered channel avoids deadlocking EmitAsync's
	// goroutine if the test reads slowly.
	got := make(chan *hub.Event, 1)
	bus.Register(hub.Subscriber{
		ID:     "router-test-observer",
		Events: []hub.EventType{hub.EventCortexRouterDecided},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			select {
			case got <- ev:
			default:
			}
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

	r, err := NewRouter(RouterConfig{Provider: fp, Bus: bus})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dec, err := r.Route(context.Background(), RouterInput{UserInput: "hi"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Kind != DecisionJustChat {
		t.Fatalf("Kind=%q", dec.Kind)
	}

	// EmitAsync runs the dispatcher in a goroutine; the buffered
	// channel + bounded wait keeps the test deterministic.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case ev := <-got:
		if ev.Type != hub.EventCortexRouterDecided {
			t.Errorf("event Type=%q", ev.Type)
		}
		if kind, _ := ev.Custom["kind"].(string); kind != "just_chat" {
			t.Errorf("event kind=%q", kind)
		}
		if _, ok := ev.Custom["latency_ms"].(int64); !ok {
			t.Errorf("event missing latency_ms int64 (got %T)", ev.Custom["latency_ms"])
		}
	case <-timer.C:
		t.Fatal("timeout waiting for router.decided event")
	}
}

// TestHistoryWindowTrimsLastN verifies the trim-to-last-N invariant
// independent of Route. Pure function so we don't need a provider.
func TestHistoryWindowTrimsLastN(t *testing.T) {
	msgs := make([]agentloop.Message, 15)
	for i := range msgs {
		msgs[i] = agentloop.Message{Role: "user"}
	}
	got := historyWindow(msgs, 10)
	if len(got) != 10 {
		t.Errorf("len=%d want 10", len(got))
	}
	short := historyWindow(msgs[:3], 10)
	if len(short) != 3 {
		t.Errorf("short len=%d want 3", len(short))
	}
	if historyWindow(nil, 10) != nil {
		t.Error("nil history should return nil")
	}
}
