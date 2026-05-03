package memorycurator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider is a minimal provider.Provider stub for the curator
// tests. ChatStream returns a fixed Content slice on every call;
// counts the number of calls in callCount for assertions.
type fakeProvider struct {
	mu        sync.Mutex
	content   []provider.ResponseContent
	callCount atomic.Uint64
	failWith  error
}

func (f *fakeProvider) Name() string { return "fake-haiku" }

func (f *fakeProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return f.ChatStream(req, nil)
}

func (f *fakeProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	f.callCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := make([]provider.ResponseContent, len(f.content))
	copy(out, f.content)
	return &provider.ChatResponse{
		Model:      req.Model,
		StopReason: "end_turn",
		Content:    out,
	}, nil
}

// newCuratorForTest constructs a curator with an in-memory store, a
// fresh hub.Bus, and a fresh Workspace. The privacy config is the
// production default (auto-curate fact, skip private, audit log path
// rooted in t.TempDir()).
func newCuratorForTest(t *testing.T, fp *fakeProvider) (*MemoryCuratorLobe, *cortex.Workspace, *hub.Bus) {
	t.Helper()
	mem, err := memory.NewStore(memory.Config{Path: ""})
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	bus := hub.New()
	ws := cortex.NewWorkspace(hub.New(), nil)
	privacy := PrivacyConfig{
		AutoCurateCategories: []memory.Category{memory.CatFact},
		SkipPrivateMessages:  true,
		AuditLogPath:         t.TempDir() + "/curator-audit.jsonl",
	}
	l := NewMemoryCuratorLobe(fp, llm.NewEscalator(false), mem, privacy, ws, bus)
	return l, ws, bus
}

// TestMemoryCuratorLobe_TriggerCadence covers TASK-29.
//
// Asserts the every-5-turns predicate: 4 ticks → 0 trigger fires;
// the 5th tick fires; 4 more → no fires; the 10th fires. The
// task.completed hub event additionally fires the trigger
// out-of-cadence.
//
// We override onTrigger with a counting hook so the cadence is
// observable without making real provider calls.
func TestMemoryCuratorLobe_TriggerCadence(t *testing.T) {
	t.Parallel()

	l, _, bus := newCuratorForTest(t, nil)

	var fired atomic.Uint64
	l.SetOnTrigger(func(ctx context.Context, in cortex.LobeInput) {
		fired.Add(1)
	})

	// Run() must install the subscriber the first time it ticks. The
	// test uses an empty LobeInput because the trigger predicate only
	// reads turnCount, not History.
	for i := 1; i <= 4; i++ {
		if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
			t.Fatalf("Run(%d): %v", i, err)
		}
	}
	if got := fired.Load(); got != 0 {
		t.Errorf("after 4 ticks: fired=%d, want 0", got)
	}

	// 5th tick should fire.
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run(5): %v", err)
	}
	if got := fired.Load(); got != 1 {
		t.Errorf("after 5 ticks: fired=%d, want 1", got)
	}

	// Ticks 6..9 should NOT fire.
	for i := 6; i <= 9; i++ {
		if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
			t.Fatalf("Run(%d): %v", i, err)
		}
	}
	if got := fired.Load(); got != 1 {
		t.Errorf("after 9 ticks: fired=%d, want 1", got)
	}

	// 10th tick should fire.
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run(10): %v", err)
	}
	if got := fired.Load(); got != 2 {
		t.Errorf("after 10 ticks: fired=%d, want 2", got)
	}

	// task.completed event should fire out-of-cadence (no Run tick
	// in between). hub.ModeObserve is async; emitAndPollFireCount polls
	// up to 100ms for fired to reach the target.
	if got := emitAndPollFireCount(bus, &fired, 3, 100*time.Millisecond); got != 3 {
		t.Errorf("after task.completed: fired=%d, want 3", got)
	}

	// turnCount must have advanced exactly 10 (the task.completed
	// path does NOT bump turnCount — it bypasses the per-Run cadence).
	if got, want := l.TurnCount(), uint64(10); got != want {
		t.Errorf("TurnCount = %d, want %d", got, want)
	}
	if got, want := l.TriggerCount(), uint64(3); got != want {
		t.Errorf("TriggerCount = %d, want %d", got, want)
	}
}

