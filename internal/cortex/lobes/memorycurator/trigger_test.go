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
	l := NewMemoryCuratorLobe(fp, llm.NewEscalator(false), mem, privacy, ws, bus, nil)
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
	// for fired to reach the target. 100ms was too tight under -race
	// in slow CI containers (PR #211 r1-agent-pr failed on
	// trigger_test.go:132 with fired=2,want=3); bump to 2s — generous
	// enough to never flake on healthy CI, fast path still completes
	// in well under 10ms.
	if got := emitAndPollFireCount(bus, &fired, 3, 2*time.Second); got != 3 {
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

// countingSemaphore is a counting fake llm.SlotAcquirer used to assert
// the per-call Acquire/Release contract for MemoryCuratorLobe (audit
// follow-up: scan-governance-gaps cortex-concerns 5).
//
// Each Acquire increments acquires; each Release increments releases.
// All operations are safe for concurrent use so the test can drive the
// hub-subscriber path without serializing access to the counters.
type countingSemaphore struct {
	acquires atomic.Uint64
	releases atomic.Uint64
}

func (s *countingSemaphore) Acquire(ctx context.Context) error {
	s.acquires.Add(1)
	return nil
}

func (s *countingSemaphore) Release() {
	s.releases.Add(1)
}

// TestMemoryCuratorLobe_PerCallAcquire asserts that every ChatStream
// call driven from the EventTaskCompleted hub subscriber is guarded
// by exactly one llm.MustAcquire/release pair on the supplied
// semaphore. The hub-subscriber path bypasses the cortex
// LobeRunner.runOnce Acquire/Release wrapper, so per-call gating is
// the only thing that keeps the slot cap honest for this Lobe.
//
// Spec: specs/cortex-concerns.md item 5; audit follow-up
// "scan-governance-gaps.md cortex-concerns 5".
func TestMemoryCuratorLobe_PerCallAcquire(t *testing.T) {
	t.Parallel()

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
	fp := &fakeProvider{
		// Empty content keeps defaultOnTrigger in its fast path: the
		// Lobe still makes the ChatStream call, which is what the
		// per-call Acquire test cares about.
		content: nil,
	}
	sem := &countingSemaphore{}

	l := NewMemoryCuratorLobe(fp, llm.NewEscalator(false), mem, privacy, ws, bus, sem)

	// Run() once to install the EventTaskCompleted subscriber. The
	// cadence predicate (turn % 5 == 0) is NOT satisfied at turn 1, so
	// the cadence path does NOT fire from this Run; only the
	// task.completed events below drive haikuCall.
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Drive three independent task.completed events. Each one fires
	// haikuCall in the bus's goroutine via the EventTaskCompleted
	// subscriber — bypassing the runner-level Acquire entirely.
	const triggers = 3
	for i := 0; i < triggers; i++ {
		emitTaskCompletedForTest(bus)
	}

	// Wait for the provider to record N calls AND the deferred Release
	// to fire N times. Each haikuCall defers release() after Acquire,
	// so Release fires AFTER the provider returns. Polling on
	// callCount alone races with the deferred Release; poll on both
	// counters to make the test deterministic under the bus's async
	// goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fp.callCount.Load() >= triggers && sem.releases.Load() >= triggers {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := fp.callCount.Load(); got != uint64(triggers) {
		t.Fatalf("provider call count = %d, want %d", got, triggers)
	}

	// One Acquire per call, one Release per call. Equality with the
	// provider call count proves both halves of the per-call pair fired.
	if got := sem.acquires.Load(); got != uint64(triggers) {
		t.Errorf("semaphore Acquire count = %d, want %d", got, triggers)
	}
	if got := sem.releases.Load(); got != uint64(triggers) {
		t.Errorf("semaphore Release count = %d, want %d", got, triggers)
	}
}

