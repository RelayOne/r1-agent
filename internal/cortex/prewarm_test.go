package cortex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakePreWarmProvider is a minimal provider.Provider implementation
// used only by prewarm tests. It records the most recent ChatRequest
// it received and returns a configured response (or error) regardless
// of input.
type fakePreWarmProvider struct {
	mu       sync.Mutex
	calls    int
	lastReq  provider.ChatRequest
	resp     *provider.ChatResponse
	err      error
}

func (f *fakePreWarmProvider) Name() string { return "fake-prewarm" }

func (f *fakePreWarmProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	f.mu.Lock()
	f.calls++
	f.lastReq = req
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakePreWarmProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

// captureBus subscribes to the given event type on a fresh hub.Bus
// (returned alongside) and returns a slice that the caller can read
// after Emit*-style work has completed. The returned function blocks
// until at least `expected` events have been observed or the supplied
// timeout elapses.
func captureBus(t *testing.T, evType hub.EventType) (*hub.Bus, *[]*hub.Event, func(expected int, timeout time.Duration) bool) {
	t.Helper()
	b := hub.New()

	var mu sync.Mutex
	var events []*hub.Event
	cond := sync.NewCond(&mu)

	b.Register(hub.Subscriber{
		ID:     "prewarm-test-capture",
		Events: []hub.EventType{evType},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			// Defensive copy so callers can read fields safely.
			cp := *ev
			events = append(events, &cp)
			cond.Broadcast()
			mu.Unlock()
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

	wait := func(expected int, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		mu.Lock()
		defer mu.Unlock()
		for len(events) < expected {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false
			}
			done := make(chan struct{})
			go func() {
				time.Sleep(remaining)
				mu.Lock()
				cond.Broadcast()
				mu.Unlock()
				close(done)
			}()
			cond.Wait()
			select {
			case <-done:
			default:
			}
			if time.Now().After(deadline) {
				return len(events) >= expected
			}
		}
		return true
	}

	return b, &events, wait
}

func TestPreWarmFiresEmitsEvent(t *testing.T) {
	t.Parallel()

	fp := &fakePreWarmProvider{
		resp: &provider.ChatResponse{
			ID:         "msg_warm",
			Model:      "claude-haiku-4-5",
			Content:    nil,
			StopReason: "end_turn",
			Usage:      stream.TokenUsage{Input: 5, Output: 1, CacheRead: 42},
		},
	}

	b, events, wait := captureBus(t, hub.EventCortexPreWarmFired)

	tools := []provider.ToolDef{
		{Name: "z_last", Description: "z", InputSchema: json.RawMessage(`{}`)},
		{Name: "a_first", Description: "a", InputSchema: json.RawMessage(`{}`)},
	}

	if err := runPreWarmOnce(context.Background(), fp, "claude-haiku-4-5", "static system", tools, b); err != nil {
		t.Fatalf("runPreWarmOnce: unexpected error: %v", err)
	}

	if !wait(1, 2*time.Second) {
		t.Fatalf("expected 1 prewarm.fired event, got %d", len(*events))
	}

	if got := len(*events); got != 1 {
		t.Fatalf("expected exactly 1 event, got %d", got)
	}
	ev := (*events)[0]
	if ev.Type != hub.EventCortexPreWarmFired {
		t.Errorf("event type = %q, want %q", ev.Type, hub.EventCortexPreWarmFired)
	}
	cs, ok := ev.Custom["cache_status"].(bool)
	if !ok {
		t.Fatalf("Custom[cache_status] missing or not bool: %#v", ev.Custom["cache_status"])
	}
	if !cs {
		t.Errorf("cache_status = false, want true (CacheRead=42 > 0)")
	}

	// Cache breakpoint parity: tools must be sorted alphabetically so
	// the request is byte-identical to what the main thread builds.
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if got := fp.calls; got != 1 {
		t.Errorf("provider call count = %d, want 1", got)
	}
	if len(fp.lastReq.Tools) != 2 || fp.lastReq.Tools[0].Name != "a_first" || fp.lastReq.Tools[1].Name != "z_last" {
		t.Errorf("tools not sorted deterministically: %+v", fp.lastReq.Tools)
	}
	if fp.lastReq.MaxTokens != 1 {
		t.Errorf("MaxTokens = %d, want 1", fp.lastReq.MaxTokens)
	}
	if !fp.lastReq.CacheEnabled {
		t.Errorf("CacheEnabled = false, want true")
	}
	if fp.lastReq.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", fp.lastReq.Model)
	}
	if len(fp.lastReq.SystemRaw) == 0 {
		t.Error("SystemRaw is empty; expected cached system blocks")
	}
	if len(fp.lastReq.Messages) != 1 || fp.lastReq.Messages[0].Role != "user" {
		t.Errorf("expected one user message, got %+v", fp.lastReq.Messages)
	}
}

func TestPreWarmCacheStatusFalseWhenNoCacheRead(t *testing.T) {
	t.Parallel()

	fp := &fakePreWarmProvider{
		resp: &provider.ChatResponse{
			Usage: stream.TokenUsage{Input: 10, Output: 1, CacheRead: 0},
		},
	}
	b, events, wait := captureBus(t, hub.EventCortexPreWarmFired)

	if err := runPreWarmOnce(context.Background(), fp, "m", "sys", nil, b); err != nil {
		t.Fatalf("runPreWarmOnce: %v", err)
	}
	if !wait(1, 2*time.Second) {
		t.Fatalf("event never observed")
	}
	cs, ok := (*events)[0].Custom["cache_status"].(bool)
	if !ok {
		t.Fatalf("cache_status not bool")
	}
	if cs {
		t.Errorf("cache_status = true, want false (CacheRead=0)")
	}
}

func TestPreWarmFailureEmitsEventAndReturnsError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	fp := &fakePreWarmProvider{err: boom}

	b, events, wait := captureBus(t, hub.EventCortexPreWarmFailed)

	err := runPreWarmOnce(context.Background(), fp, "m", "sys", nil, b)
	if err == nil {
		t.Fatalf("expected non-nil error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("returned error does not wrap boom: %v", err)
	}

	if !wait(1, 2*time.Second) {
		t.Fatalf("expected 1 prewarm.failed event, got %d", len(*events))
	}
	if got := len(*events); got != 1 {
		t.Fatalf("expected 1 event, got %d", got)
	}
	ev := (*events)[0]
	if ev.Type != hub.EventCortexPreWarmFailed {
		t.Errorf("event type = %q, want %q", ev.Type, hub.EventCortexPreWarmFailed)
	}
	es, ok := ev.Custom["err"].(string)
	if !ok {
		t.Fatalf("Custom[err] missing or not string: %#v", ev.Custom["err"])
	}
	if !strings.Contains(es, "boom") {
		t.Errorf("err Custom field = %q, want it to contain 'boom'", es)
	}
}

func TestPreWarmNilProviderReturnsError(t *testing.T) {
	t.Parallel()
	if err := runPreWarmOnce(context.Background(), nil, "m", "sys", nil, nil); err == nil {
		t.Fatal("expected error for nil provider")
	}
}

func TestPreWarmNilBusIsTolerated(t *testing.T) {
	t.Parallel()
	fp := &fakePreWarmProvider{
		resp: &provider.ChatResponse{Usage: stream.TokenUsage{CacheRead: 1}},
	}
	if err := runPreWarmOnce(context.Background(), fp, "m", "sys", nil, nil); err != nil {
		t.Fatalf("nil bus should not cause an error, got %v", err)
	}
}

func TestPreWarmCacheBreakpointParity(t *testing.T) {
	t.Parallel()

	// The pre-warm and main-thread requests must produce byte-identical
	// system blocks AND tool ordering (spec gotcha #8). Reproduce both
	// builders here and compare against what runPreWarmOnce hands the
	// provider.
	tools := []provider.ToolDef{
		{Name: "delta"},
		{Name: "alpha"},
		{Name: "charlie"},
		{Name: "bravo"},
	}
	systemPrompt := "static system prompt content"

	fp := &fakePreWarmProvider{
		resp: &provider.ChatResponse{Usage: stream.TokenUsage{CacheRead: 1}},
	}

	if err := runPreWarmOnce(context.Background(), fp, "m", systemPrompt, tools, nil); err != nil {
		t.Fatalf("runPreWarmOnce: %v", err)
	}

	got := fp.lastReq

	// Tool ordering: alphabetical.
	wantToolOrder := []string{"alpha", "bravo", "charlie", "delta"}
	if len(got.Tools) != len(wantToolOrder) {
		t.Fatalf("tools len = %d, want %d", len(got.Tools), len(wantToolOrder))
	}
	for i, n := range wantToolOrder {
		if got.Tools[i].Name != n {
			t.Errorf("tool[%d] = %q, want %q", i, got.Tools[i].Name, n)
		}
	}
}

func TestPreWarmPumpFiresOnInterval(t *testing.T) {
	t.Parallel()

	var fires atomic.Int64
	fire := func(ctx context.Context) error {
		fires.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runPreWarmPump(ctx, 10*time.Millisecond, fire)
		close(done)
	}()

	// Allow at least 3 ticks of headroom.
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPreWarmPump did not exit after ctx cancel")
	}

	if got := fires.Load(); got < 3 {
		t.Errorf("fires = %d, want >= 3", got)
	}
}

func TestPreWarmPumpContinuesOnError(t *testing.T) {
	t.Parallel()

	var fires atomic.Int64
	boom := errors.New("transient")
	fire := func(ctx context.Context) error {
		n := fires.Add(1)
		// Fail every call — pump must keep going.
		_ = n
		return boom
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runPreWarmPump(ctx, 10*time.Millisecond, fire)
		close(done)
	}()

	// Wait long enough for at least 4 ticks.
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPreWarmPump did not exit after ctx cancel")
	}

	if got := fires.Load(); got < 3 {
		t.Errorf("fires = %d after error-only path, want >= 3 (pump must continue on error)", got)
	}
}

func TestPreWarmPumpRespectsCtxCancellation(t *testing.T) {
	t.Parallel()

	var fires atomic.Int64
	fire := func(ctx context.Context) error {
		fires.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	done := make(chan struct{})
	go func() {
		runPreWarmPump(ctx, 10*time.Millisecond, fire)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runPreWarmPump should exit immediately when ctx already cancelled")
	}
	if got := fires.Load(); got != 0 {
		t.Errorf("fires = %d, want 0 (pump should exit before any tick when ctx was pre-cancelled)", got)
	}
}

func TestPreWarmPumpZeroIntervalIsNoop(t *testing.T) {
	t.Parallel()

	var fires atomic.Int64
	fire := func(ctx context.Context) error {
		fires.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runPreWarmPump(ctx, 0, fire)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runPreWarmPump with zero interval should return immediately")
	}
	if got := fires.Load(); got != 0 {
		t.Errorf("fires = %d, want 0 (zero interval should be a no-op)", got)
	}
}

func TestPreWarmPumpNilFireIsNoop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runPreWarmPump(ctx, 10*time.Millisecond, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runPreWarmPump with nil fire should return immediately")
	}
}
