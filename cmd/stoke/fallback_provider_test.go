package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// scriptedProvider is a test double that implements provider.Provider
// and returns scripted (response, err) pairs. Distinct from
// mockChatProvider in sow_native_smart2_test.go because we need a
// queue of scripted replies, per-name identity, and separate counters
// for Chat vs ChatStream.
type scriptedProvider struct {
	name string

	mu          sync.Mutex
	replies     []scriptedReply
	nextIdx     int
	chatCalls   int32 // atomic
	streamCalls int32 // atomic

	// defaultReply is served after the scripted queue is exhausted.
	defaultReply scriptedReply
}

type scriptedReply struct {
	text string
	err  error
}

func newScriptedProvider(name, defaultText string) *scriptedProvider {
	return &scriptedProvider{
		name:         name,
		defaultReply: scriptedReply{text: defaultText},
	}
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) queue(text string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, scriptedReply{text: text, err: err})
}

func (p *scriptedProvider) ChatCalls() int32   { return atomic.LoadInt32(&p.chatCalls) }
func (p *scriptedProvider) StreamCalls() int32 { return atomic.LoadInt32(&p.streamCalls) }

func (p *scriptedProvider) next() scriptedReply {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nextIdx < len(p.replies) {
		r := p.replies[p.nextIdx]
		p.nextIdx++
		return r
	}
	return p.defaultReply
}

func (p *scriptedProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	atomic.AddInt32(&p.chatCalls, 1)
	r := p.next()
	if r.err != nil {
		if r.text != "" {
			return &provider.ChatResponse{
				Content: []provider.ResponseContent{{Type: "text", Text: r.text}},
			}, r.err
		}
		return nil, r.err
	}
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{{Type: "text", Text: r.text}},
	}, nil
}

func (p *scriptedProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	atomic.AddInt32(&p.streamCalls, 1)
	r := p.next()
	if r.err != nil {
		if r.text != "" {
			if onEvent != nil {
				onEvent(stream.Event{DeltaText: r.text})
			}
			return &provider.ChatResponse{
				Content: []provider.ResponseContent{{Type: "text", Text: r.text}},
			}, r.err
		}
		return nil, r.err
	}
	if onEvent != nil {
		onEvent(stream.Event{DeltaText: r.text})
	}
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{{Type: "text", Text: r.text}},
	}, nil
}

// newTestFallbackProvider builds a FallbackProvider with a mocked
// clock and a healthPing hook that bypasses real provider.Chat. Same
// pattern as newTestPair in fallback_test.go.
func newTestFallbackProvider(role string, primary, secondary provider.Provider, clk *mockClock) *FallbackProvider {
	fp := NewFallbackProvider(role, primary, secondary)
	fp.now = clk.Now
	fp.lastHealthCheck.Store(clk.Now())
	fp.healthPing = func(p provider.Provider) (string, error) {
		resp, err := p.Chat(provider.ChatRequest{MaxTokens: 32})
		if err != nil {
			return "", err
		}
		if resp == nil || len(resp.Content) == 0 {
			return "", fmt.Errorf("empty")
		}
		return resp.Content[0].Text, nil
	}
	return fp
}

func TestFallbackProvider_HealthyPrimaryPassThrough(t *testing.T) {
	p := newScriptedProvider("codex", "codex verdict")
	s := newScriptedProvider("claude-code", "claude verdict")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	resp, err := fp.Chat(provider.ChatRequest{MaxTokens: 100})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp == nil || len(resp.Content) == 0 || resp.Content[0].Text != "codex verdict" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if p.ChatCalls() != 1 {
		t.Fatalf("primary must be called once, got %d", p.ChatCalls())
	}
	if s.ChatCalls() != 0 {
		t.Fatalf("secondary must not be called when primary healthy, got %d", s.ChatCalls())
	}
	if fp.OnSecondary() {
		t.Fatalf("pair must not be on secondary after a healthy call")
	}
}

func TestFallbackProvider_CodexNoLastAgentMessage_SwapsToCC(t *testing.T) {
	p := newScriptedProvider("codex", "unused")
	// Codex's signature transient failure — after retries the wrapped
	// error contains "no last agent message".
	p.queue("", errors.New("codex: 3 retries exhausted: codex: exit status 1 (stderr: no last agent message)"))
	s := newScriptedProvider("claude-code", "claude review verdict")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	resp, err := fp.Chat(provider.ChatRequest{MaxTokens: 100})
	if err != nil {
		t.Fatalf("expected clean recovery, got err=%v", err)
	}
	if resp == nil || len(resp.Content) == 0 || resp.Content[0].Text != "claude review verdict" {
		t.Fatalf("expected claude fallback output, got %+v", resp)
	}
	if p.ChatCalls() != 1 || s.ChatCalls() != 1 {
		t.Fatalf("calls: primary=%d secondary=%d (want 1/1)", p.ChatCalls(), s.ChatCalls())
	}
	if !fp.OnSecondary() {
		t.Fatalf("pair should be on secondary after codex transient failure")
	}
}

func TestFallbackProvider_WroteEmptyContent_Swaps(t *testing.T) {
	p := newScriptedProvider("codex", "unused")
	p.queue("", errors.New("codex: wrote empty content"))
	s := newScriptedProvider("claude-code", "cc ok")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	resp, err := fp.Chat(provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content[0].Text != "cc ok" {
		t.Fatalf("expected secondary, got %q", resp.Content[0].Text)
	}
	if !fp.OnSecondary() {
		t.Fatalf("expected swap to secondary")
	}
}

func TestFallbackProvider_SilentEmptyCountsAsSwapSignal(t *testing.T) {
	// Primary returns nil error AND empty content — a silent failure
	// which should still trigger a swap.
	p := newScriptedProvider("codex", "")
	p.queue("", nil)
	s := newScriptedProvider("claude-code", "cc rescued")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	resp, err := fp.Chat(provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content[0].Text != "cc rescued" {
		t.Fatalf("expected cc output, got %q", resp.Content[0].Text)
	}
}

func TestFallbackProvider_BothFail_ReturnsSecondaryError(t *testing.T) {
	p := newScriptedProvider("codex", "")
	p.queue("", errors.New("no last agent message"))
	s := newScriptedProvider("claude-code", "")
	s.queue("", errors.New("claude-code: exit status 1 (stderr: something)"))
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	_, err := fp.Chat(provider.ChatRequest{})
	if err == nil {
		t.Fatalf("expected error when both fail, got nil")
	}
	if p.ChatCalls() != 1 || s.ChatCalls() != 1 {
		t.Fatalf("each provider must be tried once, got p=%d s=%d", p.ChatCalls(), s.ChatCalls())
	}
	if !fp.OnSecondary() {
		t.Fatalf("pair should be pinned to secondary when primary still toxic")
	}
}

func TestFallbackProvider_HealthCheckRestoresPrimary(t *testing.T) {
	p := newScriptedProvider("codex", "primary recovered")
	// First call triggers a swap.
	p.queue("", errors.New("no last agent message"))
	s := newScriptedProvider("claude-code", "cc response")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	if _, err := fp.Chat(provider.ChatRequest{}); err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if !fp.OnSecondary() {
		t.Fatalf("expected swap after codex failure")
	}

	// Advance past healthCheckEvery. Next Chat triggers a ping of the
	// inactive primary — which now returns its healthy default — so
	// the pair should restore to primary and then serve from primary.
	clk.Advance(6 * time.Minute)
	resp, err := fp.Chat(provider.ChatRequest{})
	if err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if resp.Content[0].Text != "primary recovered" {
		t.Fatalf("expected restored primary output, got %q", resp.Content[0].Text)
	}
	if fp.OnSecondary() {
		t.Fatalf("expected restoration to primary")
	}
}

func TestFallbackProvider_HealthCheckPrimaryStillBroken_StayOnSecondary(t *testing.T) {
	p := newScriptedProvider("codex", "")
	// Two broken replies: first triggers the swap, second (the
	// health ping) keeps primary on the bench.
	p.queue("", errors.New("no last agent message"))
	p.queue("", errors.New("no last agent message"))
	s := newScriptedProvider("claude-code", "cc serving")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	if _, err := fp.Chat(provider.ChatRequest{}); err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if !fp.OnSecondary() {
		t.Fatalf("expected swap")
	}

	clk.Advance(6 * time.Minute)
	resp, err := fp.Chat(provider.ChatRequest{})
	if err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if resp.Content[0].Text != "cc serving" {
		t.Fatalf("expected to stay on secondary, got %q", resp.Content[0].Text)
	}
	if !fp.OnSecondary() {
		t.Fatalf("primary still broken — must stay on secondary")
	}
}

func TestFallbackProvider_ChatStream_FallsBackOnError(t *testing.T) {
	p := newScriptedProvider("codex", "unused")
	p.queue("", errors.New("no last agent message"))
	s := newScriptedProvider("claude-code", "cc stream output")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	var events []string
	onEvent := func(ev stream.Event) {
		events = append(events, ev.DeltaText)
	}
	resp, err := fp.ChatStream(provider.ChatRequest{}, onEvent)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp == nil || len(resp.Content) == 0 || resp.Content[0].Text != "cc stream output" {
		t.Fatalf("expected secondary stream response, got %+v", resp)
	}
	if p.StreamCalls() != 1 || s.StreamCalls() != 1 {
		t.Fatalf("stream calls: primary=%d secondary=%d (want 1/1)", p.StreamCalls(), s.StreamCalls())
	}
	if !fp.OnSecondary() {
		t.Fatalf("expected swap to secondary on ChatStream error")
	}
	found := false
	for _, e := range events {
		if e == "cc stream output" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'cc stream output' in streamed events, got %v", events)
	}
}

func TestFallbackProvider_IsRateLimit_ClassifierMatrix(t *testing.T) {
	fp := NewFallbackProvider("r", nil, nil)
	codex := newScriptedProvider("codex", "")
	cc := newScriptedProvider("claude-code", "")
	mkResp := func(text string) *provider.ChatResponse {
		return &provider.ChatResponse{
			Content: []provider.ResponseContent{{Type: "text", Text: text}},
		}
	}
	cases := []struct {
		name   string
		prov   provider.Provider
		resp   *provider.ChatResponse
		err    error
		expect bool
	}{
		{"codex clean", codex, mkResp("review complete, score=80"), nil, false},
		{"codex empty nil", codex, mkResp(""), nil, true},
		{"codex no-last-agent", codex, nil, errors.New("no last agent message"), true},
		{"codex wrote-empty", codex, nil, errors.New("wrote empty content"), true},
		{"codex hung", codex, nil, errors.New("codex: process hung (no output for 300s)"), true},
		{"cc clean", cc, mkResp("long review body with specific findings and scoring criteria"), nil, false},
		{"cc exit1 short", cc, mkResp(""), errors.New("claude-code: exit status 1 (stderr: boom)"), true},
		{"generic 429", codex, nil, errors.New("provider returned status 429"), true},
		{"generic overloaded", codex, nil, errors.New("API overloaded"), true},
		{"cc quota", cc, nil, errors.New("usage_limit exceeded"), true},
		{"unrelated error with long content", codex, mkResp(longString(300)), errors.New("some unrelated issue"), false},
	}
	classify := fp.isRateLimit
	for _, tc := range cases {
		got := classify(tc.prov, tc.resp, tc.err)
		if got != tc.expect {
			t.Errorf("%s: classifier=%v want %v (err=%v)", tc.name, got, tc.expect, tc.err)
		}
	}
}

func TestFallbackProvider_ActiveRoleName(t *testing.T) {
	p := newScriptedProvider("codex", "")
	p.queue("", errors.New("no last agent message"))
	s := newScriptedProvider("claude-code", "cc ok")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	if got := fp.ActiveRoleName(); got != "harness-reviewer=codex" {
		t.Fatalf("initial ActiveRoleName = %q, want harness-reviewer=codex", got)
	}
	if _, err := fp.Chat(provider.ChatRequest{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := fp.ActiveRoleName(); got != "harness-reviewer=claude-code" {
		t.Fatalf("post-swap ActiveRoleName = %q, want harness-reviewer=claude-code", got)
	}
	if fp.Name() != "claude-code" {
		t.Fatalf("Name() should track active provider; got %q", fp.Name())
	}
}

func TestFallbackReviewProviderFromFlags_NonCodexPrimaryIsNoOp(t *testing.T) {
	anth := newScriptedProvider("anthropic", "anthropic review")
	got := fallbackReviewProviderFromFlags(anth, "claude", ".")
	if got != provider.Provider(anth) {
		t.Fatalf("non-codex primary must pass through unwrapped")
	}
}

func TestFallbackReviewProviderFromFlags_NilPrimaryStaysNil(t *testing.T) {
	got := fallbackReviewProviderFromFlags(nil, "claude", ".")
	if got != nil {
		t.Fatalf("nil primary must return nil")
	}
}

func TestFallbackReviewProviderFromFlags_CodexPrimaryWrapsWithCC(t *testing.T) {
	codex := newScriptedProvider("codex", "codex review")
	got := fallbackReviewProviderFromFlags(codex, "claude", ".")
	if got == nil {
		t.Fatalf("codex primary must yield a non-nil wrapped provider")
	}
	fp, ok := got.(*FallbackProvider)
	if !ok {
		t.Fatalf("expected *FallbackProvider, got %T", got)
	}
	if fp.primary != provider.Provider(codex) {
		t.Fatalf("primary should be the codex input")
	}
	if fp.secondary == nil || fp.secondary.Name() != "claude-code" {
		t.Fatalf("secondary should be a claude-code provider, got %v", fp.secondary)
	}
}

func TestFallbackProvider_ConcurrentChat(t *testing.T) {
	p := newScriptedProvider("codex", "ok")
	s := newScriptedProvider("claude-code", "unused")
	clk := newMockClock(time.Now())
	fp := newTestFallbackProvider("harness-reviewer", p, s, clk)

	const workers = 4
	const perWorker = 25
	done := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		go func() {
			for j := 0; j < perWorker; j++ {
				if _, err := fp.Chat(provider.ChatRequest{}); err != nil {
					t.Errorf("concurrent Chat: %v", err)
					done <- struct{}{}
					return
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	if p.ChatCalls() != workers*perWorker {
		t.Fatalf("primary calls=%d want %d", p.ChatCalls(), workers*perWorker)
	}
	if s.ChatCalls() != 0 {
		t.Fatalf("secondary must not be called when primary healthy, got %d", s.ChatCalls())
	}
}

// longString returns a string of n characters (used to build
// non-short "clean" responses in the classifier matrix test).
func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
