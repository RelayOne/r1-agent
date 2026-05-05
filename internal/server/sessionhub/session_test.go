package sessionhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider drives RunSession through three turns: a tool call, a
// follow-up text, and end_turn. Inspired by the existing
// internal/agentloop/loop_test.go mockProvider but inlined here so this
// test stays independent of agentloop's test helpers.
type fakeProvider struct {
	responses []*provider.ChatResponse
	idx       atomic.Int64
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	i := f.idx.Add(1) - 1
	if int(i) >= len(f.responses) {
		return &provider.ChatResponse{StopReason: "end_turn"}, nil
	}
	return f.responses[i], nil
}

func (f *fakeProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

// TestSessionRun drives Session.Run through a three-message script:
//
//   1. Provider returns a tool_use turn.
//   2. Tool handler returns "ok"; agent loop sends back to provider.
//   3. Provider returns a final text turn with end_turn.
//
// The test asserts:
//   - Run returns a non-nil agentloop.Result with StopReason == "end_turn".
//   - The tool handler fired exactly once with the expected name.
//   - The dispatchHook fired BEFORE the handler.
//   - Subsequent Run calls fail with ErrSessionAlreadyRunning while
//     the first is in flight (drive via a slow handler).
func TestSessionRun(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	wd := t.TempDir()
	s, err := hub.Create(CreateOptions{Workdir: wd, Model: "test-model"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mock := &fakeProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "Reading file."},
					{Type: "tool_use", ID: "toolu_1", Name: "read_file",
						Input: map[string]any{"path": "x"}},
				},
				StopReason: "tool_use",
				Usage:      stream.TokenUsage{Input: 50, Output: 10},
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "Done."},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 80, Output: 5},
			},
		},
	}

	var (
		hookCalls    atomic.Int32
		handlerCalls atomic.Int32
		hookFiredAt  atomic.Int64
	)
	dispatchOrder := func(_ *Session, _ string) {
		hookFiredAt.Store(time.Now().UnixNano())
		hookCalls.Add(1)
	}
	handler := func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		// Capture the timestamp AFTER the hook should have fired.
		// The assertion below verifies hookFiredAt < handler-time.
		t := time.Now().UnixNano()
		if hookFiredAt.Load() >= t {
			// Flag via atomic: any value >0 reported below as failure.
			handlerCalls.Add(1000) // sentinel
			return "ok", nil
		}
		handlerCalls.Add(1)
		return "ok", nil
	}
	s.SetDispatchHook(dispatchOrder)

	res, err := s.Run(context.Background(), RunOptions{
		Provider:    mock,
		Handler:     handler,
		LoopConfig:  agentloop.Config{MaxTurns: 5},
		UserMessage: "do work",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil {
		t.Fatalf("Run: nil result")
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("StopReason: got %q, want end_turn", res.StopReason)
	}
	if h := handlerCalls.Load(); h != 1 {
		t.Fatalf("handler calls: got %d, want 1 (sentinel >=1000 means hook ran AFTER handler)", h)
	}
	if h := hookCalls.Load(); h != 1 {
		t.Fatalf("dispatch hook calls: got %d, want 1", h)
	}
	if s.State != SessionStateStopped {
		t.Fatalf("state after Run: got %q, want %q", s.State, SessionStateStopped)
	}
}

// TestSessionRun_NilProvider asserts the input-validation guard.
func TestSessionRun_NilProvider(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, _ := NewHub()
	s, _ := hub.Create(CreateOptions{Workdir: t.TempDir()})
	if _, err := s.Run(context.Background(), RunOptions{UserMessage: "hi"}); err == nil {
		t.Fatalf("expected error on nil Provider; got nil")
	}
}

// TestSessionRun_AlreadyRunning asserts the single-driver invariant.
// We block the provider on a channel so the first Run is in flight
// when the second Run fires; the second must return
// ErrSessionAlreadyRunning, not race against the first.
func TestSessionRun_AlreadyRunning(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, _ := NewHub()
	s, _ := hub.Create(CreateOptions{Workdir: t.TempDir(), Model: "m"})

	gate := make(chan struct{})
	released := make(chan struct{})
	mock := &blockingProvider{gate: gate, released: released}

	var firstErr error
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, firstErr = s.Run(context.Background(), RunOptions{
			Provider:    mock,
			LoopConfig:  agentloop.Config{MaxTurns: 1},
			UserMessage: "go",
		})
	}()

	// Wait for the first Run to actually enter the provider call.
	<-released

	if _, err := s.Run(context.Background(), RunOptions{
		Provider:    mock,
		LoopConfig:  agentloop.Config{MaxTurns: 1},
		UserMessage: "again",
	}); !errors.Is(err, ErrSessionAlreadyRunning) {
		t.Fatalf("second Run: got %v, want ErrSessionAlreadyRunning", err)
	}

	close(gate)
	<-first
	if firstErr != nil {
		t.Fatalf("first Run: %v", firstErr)
	}
}

// blockingProvider blocks ChatStream on a gate channel so the test can
// observe the in-flight state.
type blockingProvider struct {
	gate     chan struct{}
	released chan struct{}
	once     sync.Once
}

func (b *blockingProvider) Name() string { return "blocking" }
func (b *blockingProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	b.once.Do(func() { close(b.released) })
	<-b.gate
	return &provider.ChatResponse{
		Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
	}, nil
}
func (b *blockingProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return b.Chat(req)
}

// TestWorkspaceFunc asserts the WorkspaceFunc bridge fires before the
// agent loop and an error from it aborts Run.
func TestWorkspaceFunc(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, _ := NewHub()
	s, _ := hub.Create(CreateOptions{Workdir: t.TempDir(), Model: "m"})

	mock := &fakeProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	wantErr := errors.New("workspace boom")
	wsFn := func(_ context.Context, _ any) error { return wantErr }

	if _, err := s.Run(context.Background(), RunOptions{
		Provider:      mock,
		WorkspaceFunc: wsFn,
		LoopConfig:    agentloop.Config{MaxTurns: 1},
		UserMessage:   "go",
	}); err == nil {
		t.Fatalf("expected workspace error to surface; got nil")
	} else if !strings.Contains(err.Error(), "workspace setup") {
		t.Fatalf("error msg: %q (want workspace setup prefix)", err.Error())
	}
}
