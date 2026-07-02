package main

import (
	"bufio"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/workflow"
)

// midTurnStubProvider returns a canned ChatResponse and signals each
// call on calls (buffered), letting tests block an execFn until the
// mid-turn Router has actually consulted the model.
type midTurnStubProvider struct {
	resp  *provider.ChatResponse
	calls chan struct{}
}

func (p *midTurnStubProvider) Name() string { return "midturn-stub" }
func (p *midTurnStubProvider) Chat(provider.ChatRequest) (*provider.ChatResponse, error) {
	if p.calls != nil {
		select {
		case p.calls <- struct{}{}:
		default:
		}
	}
	return p.resp, nil
}
func (p *midTurnStubProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

// newMidTurnCortexForTest builds the same router-only cortex shape
// buildMidTurnCortex produces, but around a stub provider.
func newMidTurnCortexForTest(t *testing.T, p provider.Provider) *cortex.Cortex {
	t.Helper()
	c, err := cortex.New(cortex.Config{EventBus: hub.New(), Provider: p})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}
	return c
}

// syncBuilder is a mutex-guarded strings.Builder: the mid-turn
// listener goroutine and the main loop both write session output.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncBuilder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuilder) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestChatInteractiveMidTurnStopCancelsTurn is the REPL half of the
// spec acceptance criterion: typing "stop" while the execute turn is
// in flight routes through Cortex.OnUserInputMidTurn (hard-stop →
// DecisionInterrupt, zero provider calls) and cancels the turn's
// context, unwinding the run.
func TestChatInteractiveMidTurnStopCancelsTurn(t *testing.T) {
	stub := &midTurnStubProvider{}
	out := &syncBuilder{}

	cancelled := make(chan struct{})
	session := &chatInteractiveSession{
		in:        bufio.NewScanner(strings.NewReader("fix the auth bug\ny\nstop\n")),
		out:       out,
		storePath: t.TempDir() + "/chat.json",
		conv:      conversation.NewRuntime("test", 200000),
		midTurn:   newMidTurnCortexForTest(t, stub),
	}
	session.planFn = func(_ context.Context, task string) (workflow.Result, error) {
		return workflow.Result{PlanOutput: "plan for " + task}, nil
	}
	session.execFn = func(ctx context.Context, task string) (workflow.Result, error) {
		// Block until the mid-turn interrupt cancels the turn ctx.
		select {
		case <-ctx.Done():
			close(cancelled)
			return workflow.Result{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return workflow.Result{}, nil
		}
	}

	if err := session.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("execute turn ctx was never cancelled by the mid-turn interrupt")
	}
	if !strings.Contains(out.String(), "interrupting current turn") {
		t.Errorf("output missing interrupt banner:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "execute failed") {
		t.Errorf("output missing cancelled-run surface:\n%s", out.String())
	}
}

// TestChatInteractiveMidTurnQueueMission: a mid-turn line the Router
// classifies as queue_mission is queued and dispatched as the next
// task (through the normal plan → approve flow) after the current one
// completes.
func TestChatInteractiveMidTurnQueueMission(t *testing.T) {
	routed := make(chan struct{}, 1)
	stub := &midTurnStubProvider{
		calls: routed,
		resp: &provider.ChatResponse{
			Content: []provider.ResponseContent{{
				Type: "tool_use", ID: "tu_1", Name: "queue_mission",
				Input: map[string]any{"brief": "also fix the bug in auth.go", "priority": "normal"},
			}},
			StopReason: "tool_use",
		},
	}
	out := &syncBuilder{}

	var planCalls []string
	session := &chatInteractiveSession{
		in:        bufio.NewScanner(strings.NewReader("task one\ny\nafter this, also fix auth.go\nn\n")),
		out:       out,
		storePath: t.TempDir() + "/chat.json",
		conv:      conversation.NewRuntime("test", 200000),
		midTurn:   newMidTurnCortexForTest(t, stub),
	}
	session.planFn = func(_ context.Context, task string) (workflow.Result, error) {
		planCalls = append(planCalls, task)
		return workflow.Result{PlanOutput: "plan for " + task}, nil
	}
	session.execFn = func(ctx context.Context, task string) (workflow.Result, error) {
		// Hold the turn open until the Router consumed the mid-turn
		// line; executeWithMidTurn then joins the listener, which
		// guarantees the queued brief landed before nextTask runs.
		select {
		case <-routed:
		case <-time.After(10 * time.Second):
			t.Error("mid-turn line never reached the Router")
		}
		return workflow.Result{TaskType: "fix", WorktreePath: "/tmp/wt"}, nil
	}

	if err := session.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(planCalls) != 2 || planCalls[0] != "task one" || planCalls[1] != "also fix the bug in auth.go" {
		t.Fatalf("planCalls = %q, want [task one, also fix the bug in auth.go]", planCalls)
	}
	if !strings.Contains(out.String(), "queued for after this task") {
		t.Errorf("output missing queue confirmation:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "starting queued task") {
		t.Errorf("output missing queued-task dispatch banner:\n%s", out.String())
	}
}

// TestChatInteractiveMidTurnSteerAndChatPrint: steer and just_chat
// decisions surface to the operator without cancelling the turn.
func TestChatInteractiveMidTurnSteerAndChatPrint(t *testing.T) {
	routed := make(chan struct{}, 1)
	stub := &midTurnStubProvider{
		calls: routed,
		resp: &provider.ChatResponse{
			Content: []provider.ResponseContent{{
				Type: "tool_use", ID: "tu_1", Name: "steer",
				Input: map[string]any{"severity": "advice", "title": "add tests too", "body": "cover the retry path"},
			}},
			StopReason: "tool_use",
		},
	}
	out := &syncBuilder{}

	turnCancelled := false
	session := &chatInteractiveSession{
		in:        bufio.NewScanner(strings.NewReader("task one\ny\nalso add a tests folder\n")),
		out:       out,
		storePath: t.TempDir() + "/chat.json",
		conv:      conversation.NewRuntime("test", 200000),
	}
	mt := newMidTurnCortexForTest(t, stub)
	session.midTurn = mt
	session.planFn = func(_ context.Context, task string) (workflow.Result, error) {
		return workflow.Result{PlanOutput: "plan for " + task}, nil
	}
	session.execFn = func(ctx context.Context, task string) (workflow.Result, error) {
		select {
		case <-routed:
		case <-time.After(10 * time.Second):
			t.Error("mid-turn line never reached the Router")
		}
		if ctx.Err() != nil {
			turnCancelled = true
		}
		return workflow.Result{TaskType: "fix", WorktreePath: "/tmp/wt"}, nil
	}

	if err := session.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if turnCancelled {
		t.Error("steer must not cancel the in-flight turn")
	}
	if !strings.Contains(out.String(), "steer noted: add tests too") {
		t.Errorf("output missing steer confirmation:\n%s", out.String())
	}
	// The steer Note landed in the REPL cortex Workspace.
	notes := mt.Workspace().Snapshot()
	if len(notes) != 1 || notes[0].Title != "add tests too" {
		t.Errorf("workspace notes = %+v, want the steer note", notes)
	}
}

// TestBuildMidTurnCortexNoProvider: with no API key, no base URL and a
// scrubbed environment, mid-turn routing is disabled (nil), and with a
// proxy BaseURL it constructs.
func TestBuildMidTurnCortexNoProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if c := buildMidTurnCortex(chatInteractiveConfig{}); c != nil {
		t.Fatalf("buildMidTurnCortex with no provider = %v, want nil", c)
	}
	if c := buildMidTurnCortex(chatInteractiveConfig{NativeBaseURL: "http://127.0.0.1:0", NativeModel: "test-model"}); c == nil {
		t.Fatal("buildMidTurnCortex with BaseURL = nil, want a cortex")
	}
}
