package cortex

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// EchoLobe is a minimal Lobe implementation used by TASK-9's LobeRunner
// tests and TASK-25's integration tests. It publishes one Note per Run
// (Severity=SevInfo, Title="echo") and exits. It exists in the test
// binary only — it is NOT part of the production API surface.
//
// Run requires a writable *Workspace because LobeInput.Workspace is the
// read-only subset; the test harness threads the same Workspace via the
// EchoLobe field so the lobe can Publish.
type EchoLobe struct {
	IDValue string
	Desc    string
	Kindly  LobeKind

	// Workspace is the writable handle the lobe Publishes to. Tests set
	// this directly; the runner in TASK-9 will set it via a constructor.
	Workspace *Workspace

	// Calls counts how many times Run has been invoked. Useful for
	// downstream tests asserting "ran exactly once per Round".
	Calls atomic.Int64
}

// ID returns the configured stable ID, defaulting to "echo".
func (l *EchoLobe) ID() string {
	if l.IDValue == "" {
		return "echo"
	}
	return l.IDValue
}

// Description returns the configured description, defaulting to a
// canonical string for /status output.
func (l *EchoLobe) Description() string {
	if l.Desc == "" {
		return "echo lobe (test stub)"
	}
	return l.Desc
}

// Kind returns the configured LobeKind, defaulting to KindDeterministic.
func (l *EchoLobe) Kind() LobeKind { return l.Kindly }

// Run publishes one SevInfo "echo" Note and exits. It observes ctx.Done
// before publishing so cancelled rounds drop cleanly. If Workspace is nil
// the lobe still observes ctx and returns nil — the no-op shape is what
// the LobeRunner contract expects.
func (l *EchoLobe) Run(ctx context.Context, in LobeInput) error {
	l.Calls.Add(1)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Read-side smoke: invoke Snapshot to prove the WorkspaceReader is
	// actually wired. The result is intentionally discarded.
	if in.Workspace != nil {
		_ = in.Workspace.Snapshot()
	}

	if l.Workspace == nil {
		return nil
	}
	return l.Workspace.Publish(Note{
		LobeID:   l.ID(),
		Severity: SevInfo,
		Title:    "echo",
	})
}

// TestLobeInterfaceCompiles is the smoke test for TASK-8. Real LobeRunner
// behaviour is exercised in TASK-9. This test verifies:
//   - EchoLobe satisfies the Lobe interface.
//   - ID() / Description() / Kind() return their configured defaults.
//   - workspaceReader satisfies WorkspaceReader and round-trips through
//     WorkspaceReaderFor.
//   - Run publishes exactly one Note when Workspace is non-nil.
func TestLobeInterfaceCompiles(t *testing.T) {
	t.Parallel()

	var _ Lobe = (*EchoLobe)(nil)

	w := NewWorkspace(hub.New(), nil)
	l := &EchoLobe{Workspace: w}

	if got, want := l.ID(), "echo"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if got := l.Description(); got == "" {
		t.Errorf("Description() = empty, want non-empty default")
	}
	if got, want := l.Kind(), KindDeterministic; got != want {
		t.Errorf("Kind() = %v, want %v", got, want)
	}

	rd := WorkspaceReaderFor(w)
	if rd == nil {
		t.Fatal("WorkspaceReaderFor returned nil")
	}
	// Read-side calls must not panic on the TASK-5 stubs.
	_ = rd.Snapshot()
	_ = rd.UnresolvedCritical()

	in := LobeInput{
		Round:     1,
		Workspace: rd,
		Bus:       hub.New(),
	}
	if err := l.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := l.Calls.Load(), int64(1); got != want {
		t.Errorf("Calls = %d, want %d", got, want)
	}
}

// TestLobeKindConstants pins the iota ordering so a future re-order
// (which would silently break semaphore routing) trips a unit test.
func TestLobeKindConstants(t *testing.T) {
	t.Parallel()
	if KindDeterministic != 0 {
		t.Errorf("KindDeterministic = %d, want 0", KindDeterministic)
	}
	if KindLLM != 1 {
		t.Errorf("KindLLM = %d, want 1", KindLLM)
	}
}

// captureLobeBus subscribes to the given event types on a fresh
// hub.Bus and returns the bus, a slice of captured events, and a poll
// helper. The poll helper blocks until at least `expected` events have
// been observed or the deadline elapses.
func captureLobeBus(t *testing.T, evTypes ...hub.EventType) (*hub.Bus, *[]*hub.Event, func(expected int, timeout time.Duration) bool) {
	t.Helper()
	b := hub.New()

	var mu sync.Mutex
	var events []*hub.Event

	b.Register(hub.Subscriber{
		ID:     "lobe-test-capture",
		Events: evTypes,
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			cp := *ev
			events = append(events, &cp)
			mu.Unlock()
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

	poll := func(expected int, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(events)
			mu.Unlock()
			if n >= expected {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= expected
	}

	return b, &events, poll
}

// TestLobeRunnerLifecycle exercises the happy-path: NewLobeRunner ->
// Start (idempotent) -> tick -> EchoLobe publishes -> ctx cancel ->
// Stop returns within budget.
func TestLobeRunnerLifecycle(t *testing.T) {
	t.Parallel()

	b, _, _ := captureLobeBus(t, hub.EventCortexLobeStarted)
	w := NewWorkspace(b, nil)
	lobe := &EchoLobe{Workspace: w}
	sem := NewLobeSemaphore(1)

	r := NewLobeRunner(lobe, w, sem, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx)
	// Idempotency: a second Start is a silent no-op (no extra goroutines).
	r.Start(ctx)

	// Signal one round.
	r.Tick() <- struct{}{}

	// Wait for the EchoLobe to publish exactly one Note. We poll on
	// the Workspace size because EchoLobe's Calls counter is bumped at
	// function entry (before Publish) — so Calls==1 is necessary but
	// not sufficient evidence the Note has landed in the Workspace.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(w.Snapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, want := lobe.Calls.Load(), int64(1); got < want {
		t.Fatalf("EchoLobe.Calls = %d, want >= %d", got, want)
	}
	notes := w.Snapshot()
	if len(notes) != 1 {
		t.Fatalf("workspace notes = %d, want 1", len(notes))
	}

	// Cancel and Stop. Stop must return well within lobeStopTimeout.
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	stopStart := time.Now()
	r.Stop(stopCtx)
	if elapsed := time.Since(stopStart); elapsed >= 5*time.Second {
		t.Fatalf("Stop took %v, want < 5s", elapsed)
	}

	// stopped chan must be closed at this point.
	select {
	case <-r.Stopped():
	default:
		t.Fatal("Stopped() not closed after Stop returned")
	}

	// Stop again is idempotent.
	r.Stop(stopCtx)
}

// panicLobe is a Lobe whose Run panics. It exists in the test binary
// only and is used to exercise the runner's defer-recover path.
type panicLobe struct {
	IDValue string
	Kindly  LobeKind
	Calls   atomic.Int64
}

func (l *panicLobe) ID() string {
	if l.IDValue == "" {
		return "panic-lobe"
	}
	return l.IDValue
}
func (l *panicLobe) Description() string { return "panicking lobe (test stub)" }
func (l *panicLobe) Kind() LobeKind      { return l.Kindly }
func (l *panicLobe) Run(ctx context.Context, in LobeInput) error {
	l.Calls.Add(1)
	panic("boom")
}

// TestLobeRunnerPanic verifies that a panicking Lobe (a) emits
// cortex.lobe.panic, (b) does NOT close r.stopped on the first panic
// (the runner remains responsive to ctx.Done), and (c) cleanly exits
// when the context is cancelled.
func TestLobeRunnerPanic(t *testing.T) {
	t.Parallel()

	b, events, poll := captureLobeBus(t, hub.EventCortexLobePanic)
	w := NewWorkspace(b, nil)
	lobe := &panicLobe{}
	sem := NewLobeSemaphore(1)

	r := NewLobeRunner(lobe, w, sem, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx)
	r.Tick() <- struct{}{}

	// Block until the panic event is observed (or the budget elapses).
	if !poll(1, 2*time.Second) {
		t.Fatalf("did not observe cortex.lobe.panic event in time; got %d", len(*events))
	}

	if got := (*events)[0].Type; got != hub.EventCortexLobePanic {
		t.Errorf("event type = %q, want %q", got, hub.EventCortexLobePanic)
	}
	if rec, ok := (*events)[0].Custom["recovered"]; !ok || rec == nil {
		t.Errorf("event Custom[recovered] missing or nil: %+v", (*events)[0].Custom)
	}
	if id, ok := (*events)[0].Custom["lobe_id"].(string); !ok || id != "panic-lobe" {
		t.Errorf("event Custom[lobe_id] = %v, want \"panic-lobe\"", (*events)[0].Custom["lobe_id"])
	}

	// Cancel the context; the runner should exit promptly.
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	r.Stop(stopCtx)

	select {
	case <-r.Stopped():
	default:
		t.Fatal("Stopped() not closed after Stop returned post-panic")
	}
}

// blockingLobe holds inside Run until released via the unblock channel.
// It is used to prove the LobeSemaphore actually serializes concurrent
// LLM Lobes: while one is "in flight" the second must not enter Run.
type blockingLobe struct {
	IDValue string
	Kindly  LobeKind
	Started chan struct{} // closed by Run on entry
	Unblock chan struct{} // closed by the test to release Run
	Calls   atomic.Int64
}

func (l *blockingLobe) ID() string          { return l.IDValue }
func (l *blockingLobe) Description() string { return "blocking lobe (test stub)" }
func (l *blockingLobe) Kind() LobeKind      { return l.Kindly }
func (l *blockingLobe) Run(ctx context.Context, in LobeInput) error {
	l.Calls.Add(1)
	// Signal entry exactly once.
	select {
	case <-l.Started:
		// already closed; do nothing
	default:
		close(l.Started)
	}
	select {
	case <-l.Unblock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestLobeRunnerSemaphoreLLM proves that two KindLLM Lobes fronted by a
// capacity-1 semaphore serialize: the second Lobe's Run does not start
// until the first releases its slot.
func TestLobeRunnerSemaphoreLLM(t *testing.T) {
	t.Parallel()

	b := hub.New()
	w := NewWorkspace(b, nil)
	sem := NewLobeSemaphore(1)

	loA := &blockingLobe{
		IDValue: "lobe-A",
		Kindly:  KindLLM,
		Started: make(chan struct{}),
		Unblock: make(chan struct{}),
	}
	loB := &blockingLobe{
		IDValue: "lobe-B",
		Kindly:  KindLLM,
		Started: make(chan struct{}),
		Unblock: make(chan struct{}),
	}

	rA := NewLobeRunner(loA, w, sem, b)
	rB := NewLobeRunner(loB, w, sem, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rA.Start(ctx)
	rB.Start(ctx)

	// Tick A first and confirm A has entered Run before ticking B.
	// Sending both ticks before observing entry would race the Go
	// scheduler — Acquire winners are not deterministic. The contract
	// under test is "while a slot is held, no other LLM Lobe can enter
	// Run", not "Tick order = Run order".
	rA.Tick() <- struct{}{}

	select {
	case <-loA.Started:
	case <-time.After(2 * time.Second):
		t.Fatal("lobe A did not enter Run within 2s")
	}

	// Tick B. With A holding the only slot, B must remain parked on
	// Acquire — its Started chan must NOT close until A releases.
	rB.Tick() <- struct{}{}

	select {
	case <-loB.Started:
		t.Fatal("lobe B entered Run while A holds the only semaphore slot")
	case <-time.After(100 * time.Millisecond):
	}

	// Release A. B should now acquire the slot and enter Run.
	close(loA.Unblock)

	select {
	case <-loB.Started:
	case <-time.After(2 * time.Second):
		t.Fatal("lobe B did not enter Run after A released the semaphore")
	}

	close(loB.Unblock)

	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	rA.Stop(stopCtx)
	rB.Stop(stopCtx)
}

// TestLobeRunnerSemaphoreDeterministic proves that KindDeterministic
// Lobes do NOT touch the semaphore: with capacity-1 we tick a single
// runner repeatedly and observe every tick translates to a Run call,
// without any throttling against other (non-existent) holders.
func TestLobeRunnerSemaphoreDeterministic(t *testing.T) {
	t.Parallel()

	b := hub.New()
	w := NewWorkspace(b, nil)
	sem := NewLobeSemaphore(1)

	// Pre-fill the semaphore so any incorrect Acquire would block
	// indefinitely. A correct deterministic runner must skip Acquire
	// entirely and run despite the slot being taken.
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("preload Acquire: %v", err)
	}
	defer sem.Release()

	lobe := &EchoLobe{Workspace: w, Kindly: KindDeterministic}
	r := NewLobeRunner(lobe, w, sem, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	const ticks = 5
	// Send each tick after the previous one's Note has actually landed
	// in the Workspace; the buffered (cap-1) channel would otherwise
	// coalesce, AND polling on Calls alone is insufficient because
	// Calls is incremented before Publish completes (see EchoLobe.Run).
	for i := 1; i <= ticks; i++ {
		r.Tick() <- struct{}{}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if len(w.Snapshot()) >= i {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if got := len(w.Snapshot()); got < i {
			t.Fatalf("after tick %d: workspace notes = %d, want >= %d (deterministic lobe blocked on semaphore?)", i, got, i)
		}
	}

	if got := len(w.Snapshot()); got != ticks {
		t.Errorf("workspace notes = %d, want %d", got, ticks)
	}

	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	r.Stop(stopCtx)
}
