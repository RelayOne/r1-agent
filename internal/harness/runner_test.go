package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/concern"
	"github.com/RelayOne/r1/internal/harness"
	"github.com/RelayOne/r1/internal/ledger"
)

// recordingExecutor records every tool call it receives and returns a canned
// result. The runner must never send it an unauthorized call.
type recordingExecutor struct {
	mu    sync.Mutex
	calls []harness.ToolCall
}

func (e *recordingExecutor) Execute(_ context.Context, _ string, call harness.ToolCall) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call)
	return "executed " + call.Name, nil
}

func (e *recordingExecutor) callNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, len(e.calls))
	for i, c := range e.calls {
		names[i] = c.Name
	}
	return names
}

// setupWithDeps is setup(t) but also returns the ledger and bus so runner
// tests can assert on cost_record nodes and worker.action.* events.
func setupWithDeps(t *testing.T) (*harness.Harness, *ledger.Ledger, *bus.Bus) {
	t.Helper()

	tmp := t.TempDir()
	l, err := ledger.New(tmp + "/ledger")
	if err != nil {
		t.Fatal(err)
	}
	b, err := bus.New(tmp + "/bus")
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b.Close()
		l.Close()
	})

	cb := concern.NewBuilder(l, b)
	cb.RegisterTemplate("dev_proposing", concern.Template{
		Role: concern.RoleDev,
		Face: concern.FaceProposing,
	})

	h := harness.New(harness.Config{
		MissionID:    "test-mission",
		DefaultModel: "claude-opus-4-6",
	}, l, b, cb)
	return h, l, b
}

// replayActions collects worker.action.* events for the mission.
func replayActions(t *testing.T, b *bus.Bus) []bus.Event {
	t.Helper()
	var events []bus.Event
	if err := b.Replay(bus.Pattern{TypePrefix: "worker.action."}, 0, func(evt bus.Event) {
		events = append(events, evt)
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return events
}

// TestStanceRunner_FullLoop is the A041 headline integration test:
// SpawnStance -> runner turns with a mock provider -> tool authorization
// enforced -> cost accounting -> bus events observed.
func TestStanceRunner_FullLoop(t *testing.T) {
	h, l, b := setupWithDeps(t)
	ctx := context.Background()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	mock := &harness.MockProvider{
		Responses: []*harness.ChatResponse{
			{
				Content:   "reading the file first",
				TokensIn:  100,
				TokensOut: 20,
				CostUSD:   0.01,
				ToolCalls: []harness.ToolCall{
					{Name: "file_read", Args: json.RawMessage(`{"path":"main.go"}`)},
				},
			},
			{
				Content:   "done: implemented the change",
				TokensIn:  150,
				TokensOut: 30,
				CostUSD:   0.02,
			},
		},
	}
	exec := &recordingExecutor{}

	runner := h.NewStanceRunner(mock, exec, harness.RunnerConfig{MaxTurns: 5})
	outcome, err := runner.Run(ctx, handle.ID, "implement task-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Loop shape.
	if outcome.Turns != 2 {
		t.Errorf("Turns = %d, want 2", outcome.Turns)
	}
	if outcome.FinalContent != "done: implemented the change" {
		t.Errorf("FinalContent = %q", outcome.FinalContent)
	}
	if outcome.HitMaxTurns || outcome.Terminated {
		t.Errorf("unexpected stop flags: %+v", outcome)
	}
	if outcome.ToolCallsTotal != 1 || outcome.ToolCallsDenied != 0 {
		t.Errorf("tool calls = %d/%d denied, want 1/0", outcome.ToolCallsTotal, outcome.ToolCallsDenied)
	}

	// The provider saw the spawn-built system prompt and the tool result.
	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	if !strings.Contains(calls[0].SystemPrompt, "concern_field") {
		t.Error("SystemPrompt does not contain the rendered concern field")
	}
	if !strings.Contains(calls[0].SystemPrompt, "file_read") {
		t.Error("SystemPrompt does not list authorized tools")
	}
	lastMsg := calls[1].Messages[len(calls[1].Messages)-1]
	if !strings.Contains(lastMsg.Content, "executed file_read") {
		t.Errorf("tool result not fed back to model: %q", lastMsg.Content)
	}

	// Executor received exactly the authorized call.
	if names := exec.callNames(); len(names) != 1 || names[0] != "file_read" {
		t.Errorf("executor calls = %v, want [file_read]", names)
	}

	// Cost accounting on the session (under h.mu, via InspectStance).
	state, err := h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.TokensUsed != 300 {
		t.Errorf("TokensUsed = %d, want 300", state.TokensUsed)
	}
	if state.CostUSD < 0.029 || state.CostUSD > 0.031 {
		t.Errorf("CostUSD = %f, want ~0.03", state.CostUSD)
	}
	if outcome.TokensUsed != 300 {
		t.Errorf("outcome.TokensUsed = %d, want 300", outcome.TokensUsed)
	}

	// cost_record ledger nodes (the shape bench.ComputeMetrics sums).
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "cost_record", MissionID: "test-mission"})
	if err != nil {
		t.Fatalf("ledger.Query: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("cost_record nodes = %d, want 2", len(nodes))
	}
	var totalTokens int64
	for _, n := range nodes {
		var rec struct {
			TokensUsed int64 `json:"tokens_used"`
		}
		if err := json.Unmarshal(n.Content, &rec); err != nil {
			t.Fatalf("unmarshal cost_record: %v", err)
		}
		totalTokens += rec.TokensUsed
	}
	if totalTokens != 300 {
		t.Errorf("ledger tokens_used total = %d, want 300", totalTokens)
	}

	// worker.action.* bus events: 2 model turns + 1 tool call, each with a
	// started/completed pair.
	events := replayActions(t, b)
	var started, completed int
	for _, evt := range events {
		switch evt.Type {
		case bus.EvtWorkerActionStarted:
			started++
		case bus.EvtWorkerActionCompleted:
			completed++
		}
		if evt.EmitterID != handle.ID {
			t.Errorf("event emitter = %q, want %q", evt.EmitterID, handle.ID)
		}
	}
	if started != 3 || completed != 3 {
		t.Errorf("action events = %d started / %d completed, want 3/3", started, completed)
	}
}

// TestStanceRunner_ToolAuthorizationEnforced proves an unauthorized call is
// denied before the executor and surfaced to the model.
func TestStanceRunner_ToolAuthorizationEnforced(t *testing.T) {
	h, _, b := setupWithDeps(t)
	ctx := context.Background()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	mock := &harness.MockProvider{
		Responses: []*harness.ChatResponse{
			{
				Content: "searching the web",
				ToolCalls: []harness.ToolCall{
					// web_search is NOT in the dev role's tool set.
					{Name: "web_search", Args: json.RawMessage(`{"q":"x"}`)},
				},
			},
			{Content: "understood, staying in scope"},
		},
	}
	exec := &recordingExecutor{}

	runner := h.NewStanceRunner(mock, exec, harness.RunnerConfig{MaxTurns: 5})
	outcome, err := runner.Run(ctx, handle.ID, "do the task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if outcome.ToolCallsDenied != 1 {
		t.Errorf("ToolCallsDenied = %d, want 1", outcome.ToolCallsDenied)
	}
	if names := exec.callNames(); len(names) != 0 {
		t.Errorf("executor received unauthorized calls: %v", names)
	}

	// The denial is fed back to the model, not silently dropped.
	calls := mock.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	lastMsg := calls[1].Messages[len(calls[1].Messages)-1]
	if !strings.Contains(lastMsg.Content, "tool denied") {
		t.Errorf("denial not surfaced to model: %q", lastMsg.Content)
	}

	// And visible on the bus.
	var deniedEvents int
	for _, evt := range replayActions(t, b) {
		if evt.Type != bus.EvtWorkerActionCompleted {
			continue
		}
		var payload struct {
			Action     string `json:"action"`
			Authorized *bool  `json:"authorized"`
		}
		if json.Unmarshal(evt.Payload, &payload) == nil &&
			payload.Action == "tool_call" && payload.Authorized != nil && !*payload.Authorized {
			deniedEvents++
		}
	}
	if deniedEvents != 1 {
		t.Errorf("denied tool_call completed events = %d, want 1", deniedEvents)
	}
}

// TestStanceRunner_CheckpointPauseHonored proves PauseStance no longer times
// out: the runner acknowledges at a between-turn checkpoint, freezes while
// paused, and continues after ResumeStance.
func TestStanceRunner_CheckpointPauseHonored(t *testing.T) {
	h, _, _ := setupWithDeps(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	// Provider loops with tool calls until released, then finishes.
	var released atomic.Bool
	mock := &harness.MockProvider{}
	mock.ChatFn = func(_ context.Context, _ harness.ChatRequest) (*harness.ChatResponse, error) {
		if released.Load() {
			return &harness.ChatResponse{Content: "final"}, nil
		}
		return &harness.ChatResponse{
			Content:   "working",
			ToolCalls: []harness.ToolCall{{Name: "file_read"}},
		}, nil
	}

	runner := h.NewStanceRunner(mock, &recordingExecutor{}, harness.RunnerConfig{MaxTurns: 100000})
	runDone := make(chan struct{})
	var outcome *harness.RunOutcome
	var runErr error
	go func() {
		outcome, runErr = runner.Run(ctx, handle.ID, "loop until released")
		close(runDone)
	}()

	// Wait until the runner is actually turning.
	deadline := time.Now().Add(5 * time.Second)
	for mock.CallCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("runner never called the provider")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Pause: must return via checkpoint ack, NOT the 30s timeout.
	pauseStart := time.Now()
	if err := h.PauseStance(ctx, handle.ID, "operator hold"); err != nil {
		t.Fatalf("PauseStance: %v", err)
	}
	if elapsed := time.Since(pauseStart); elapsed > 10*time.Second {
		t.Fatalf("PauseStance took %v — looks like the ack timeout path", elapsed)
	}

	state, err := h.InspectStance(ctx, handle.ID)
	if err != nil {
		t.Fatalf("InspectStance: %v", err)
	}
	if state.State != harness.StatusPaused {
		t.Errorf("state = %q, want %q", state.State, harness.StatusPaused)
	}

	// While paused, the runner is parked in CheckpointCheck: the provider
	// call count must freeze.
	frozen := mock.CallCount()
	time.Sleep(150 * time.Millisecond)
	if got := mock.CallCount(); got != frozen {
		t.Errorf("provider called while paused: %d -> %d", frozen, got)
	}

	// Resume and let the provider finish.
	released.Store(true)
	if err := h.ResumeStance(ctx, handle.ID, "carry on"); err != nil {
		t.Fatalf("ResumeStance: %v", err)
	}

	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish after resume")
	}
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if outcome.FinalContent != "final" {
		t.Errorf("FinalContent = %q, want %q", outcome.FinalContent, "final")
	}
	if mock.CallCount() <= frozen {
		t.Errorf("provider did not run again after resume (calls = %d)", mock.CallCount())
	}
}

// TestStanceRunner_TerminatedStanceStops proves terminating a stance stops
// the loop at the next checkpoint.
func TestStanceRunner_TerminatedStanceStops(t *testing.T) {
	h, _, _ := setupWithDeps(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	terminateOnce := sync.OnceFunc(func() {
		if err := h.TerminateStance(ctx, handle.ID); err != nil {
			t.Errorf("TerminateStance: %v", err)
		}
	})
	mock := &harness.MockProvider{}
	mock.ChatFn = func(_ context.Context, _ harness.ChatRequest) (*harness.ChatResponse, error) {
		// Terminate from inside a turn; the runner must notice at the
		// next between-turn checkpoint.
		terminateOnce()
		return &harness.ChatResponse{
			Content:   "still going",
			ToolCalls: []harness.ToolCall{{Name: "file_read"}},
		}, nil
	}

	runner := h.NewStanceRunner(mock, &recordingExecutor{}, harness.RunnerConfig{MaxTurns: 100000})
	outcome, err := runner.Run(ctx, handle.ID, "run until terminated")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !outcome.Terminated {
		t.Errorf("outcome.Terminated = false, want true (outcome: %+v)", outcome)
	}
}

// TestStanceRunner_MaxTurns proves the loop is bounded.
func TestStanceRunner_MaxTurns(t *testing.T) {
	h, _, _ := setupWithDeps(t)
	ctx := context.Background()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: "task-1",
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	// Always returns a tool call: without the cap this would never stop.
	mock := &harness.MockProvider{
		Responses: []*harness.ChatResponse{
			{Content: "looping", ToolCalls: []harness.ToolCall{{Name: "file_read"}}},
		},
	}

	runner := h.NewStanceRunner(mock, &recordingExecutor{}, harness.RunnerConfig{MaxTurns: 3})
	outcome, err := runner.Run(ctx, handle.ID, "loop forever")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !outcome.HitMaxTurns {
		t.Error("HitMaxTurns = false, want true")
	}
	if outcome.Turns != 3 {
		t.Errorf("Turns = %d, want 3", outcome.Turns)
	}
}

// TestStanceRunner_UnknownStance rejects IDs the harness does not track.
func TestStanceRunner_UnknownStance(t *testing.T) {
	h, _, _ := setupWithDeps(t)

	runner := h.NewStanceRunner(&harness.MockProvider{}, nil, harness.RunnerConfig{})
	if _, err := runner.Run(context.Background(), "no-such-stance", "task"); err == nil {
		t.Fatal("expected error for unknown stance, got nil")
	}
}
