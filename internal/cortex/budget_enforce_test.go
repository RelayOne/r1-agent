package cortex

// Regression tests for audit A061: the BudgetTracker's enforcement half.
// The collection side (RecordMainTurn via the EventModelPostCall hub
// subscriber) was already live; these tests pin the runner-side gate —
// runOnce must skip a KindLLM lobe's Run and emit
// cortex.lobe.budget_skipped when the per-round 30% output cap is
// exhausted, MidturnNote must reset the accumulator each round, and
// lobes must receive the tracker via LobeInput.Budget so they can
// Charge.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/stream"
)

// chargingLobe is a KindLLM test lobe that records Run invocations,
// asserts LobeInput.Budget arrives, and charges a fixed output-token
// amount per Run — simulating an LLM lobe's post-call Charge contract.
type chargingLobe struct {
	Calls     atomic.Int64
	SawBudget atomic.Bool
	ChargePer int
}

func (l *chargingLobe) ID() string          { return "charging-lobe" }
func (l *chargingLobe) Description() string { return "budget-charging lobe (test stub)" }
func (l *chargingLobe) Kind() LobeKind      { return KindLLM }
func (l *chargingLobe) Run(ctx context.Context, in LobeInput) error {
	l.Calls.Add(1)
	if in.Budget != nil {
		l.SawBudget.Store(true)
		if l.ChargePer > 0 {
			in.Budget.Charge(l.ID(), stream.TokenUsage{Output: l.ChargePer})
		}
	}
	return nil
}

// waitForCalls polls the lobe's Run counter until it reaches want or
// the timeout elapses. Returns the final observed count.
func waitForCalls(counter *atomic.Int64, want int64, timeout time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if counter.Load() >= want {
			return counter.Load()
		}
		time.Sleep(5 * time.Millisecond)
	}
	return counter.Load()
}

// TestLobeRunner_BudgetExceededSkipsRun asserts the fail-closed gate:
// with no main turn recorded the budget is 0, Exceeded() is true, and a
// KindLLM lobe's Run must be skipped with cortex.lobe.budget_skipped
// emitted (spec item 21's Acquire-then-check ordering).
func TestLobeRunner_BudgetExceededSkipsRun(t *testing.T) {
	t.Parallel()

	b, events, poll := captureLobeBus(t, EventCortexLobeBudgetSkipped)
	w := NewWorkspace(b, nil)
	lobe := &chargingLobe{}
	sem := NewLobeSemaphore(1)
	tracker := NewBudgetTracker() // no RecordMainTurn: budget 0, fail-closed

	r := NewLobeRunner(lobe, w, sem, b, tracker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Tick() <- struct{}{}

	if !poll(1, 2*time.Second) {
		t.Fatalf("did not observe cortex.lobe.budget_skipped; got %d events", len(*events))
	}
	ev := (*events)[0]
	if got := ev.Type; got != EventCortexLobeBudgetSkipped {
		t.Errorf("event type = %q, want %q", got, EventCortexLobeBudgetSkipped)
	}
	if id, ok := ev.Custom["lobe_id"].(string); !ok || id != "charging-lobe" {
		t.Errorf("event Custom[lobe_id] = %v, want \"charging-lobe\"", ev.Custom["lobe_id"])
	}
	if got := lobe.Calls.Load(); got != 0 {
		t.Errorf("lobe.Run invoked %d times despite exhausted budget, want 0", got)
	}

	// The early return must have released the semaphore slot (deferred
	// Release): a fresh Acquire must succeed immediately.
	acqCtx, acqCancel := context.WithTimeout(context.Background(), time.Second)
	defer acqCancel()
	if err := sem.Acquire(acqCtx); err != nil {
		t.Errorf("semaphore slot leaked by budget skip: %v", err)
	}
}

// TestLobeRunner_BudgetAvailableRunsAndCharges asserts the positive
// path: with headroom the KindLLM lobe runs, receives the tracker via
// LobeInput.Budget, and its Charge is visible to the tracker so a
// subsequent round is gated.
func TestLobeRunner_BudgetAvailableRunsAndCharges(t *testing.T) {
	t.Parallel()

	b, events, poll := captureLobeBus(t, EventCortexLobeBudgetSkipped)
	w := NewWorkspace(b, nil)
	// Budget: 30% of 1000 = 300. First Run charges 400 → next round
	// exceeded.
	lobe := &chargingLobe{ChargePer: 400}
	sem := NewLobeSemaphore(1)
	tracker := NewBudgetTracker()
	tracker.RecordMainTurn(1000)

	r := NewLobeRunner(lobe, w, sem, b, tracker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	r.Tick() <- struct{}{}
	if got := waitForCalls(&lobe.Calls, 1, 2*time.Second); got != 1 {
		t.Fatalf("lobe.Run calls = %d after first tick, want 1 (budget had headroom)", got)
	}
	if !lobe.SawBudget.Load() {
		t.Error("LobeInput.Budget was nil; runner must forward its tracker to lobes")
	}
	if !tracker.Exceeded() {
		t.Fatalf("tracker.Exceeded() = false after 400-token charge against a 300 budget")
	}

	// Second round without ResetRound: the charge from round 1 trips
	// the gate, so Run is skipped and budget_skipped fires.
	r.Tick() <- struct{}{}
	if !poll(1, 2*time.Second) {
		t.Fatalf("did not observe cortex.lobe.budget_skipped on exhausted second round; got %d events", len(*events))
	}
	if got := lobe.Calls.Load(); got != 1 {
		t.Errorf("lobe.Run calls = %d after gated second tick, want 1", got)
	}
}

// TestLobeRunner_DeterministicLobeExemptFromBudget asserts deterministic
// lobes run regardless of budget state — they spend no model tokens.
func TestLobeRunner_DeterministicLobeExemptFromBudget(t *testing.T) {
	t.Parallel()

	b := hub.New()
	w := NewWorkspace(b, nil)
	lobe := &EchoLobe{Kindly: KindDeterministic, Workspace: w}
	tracker := NewBudgetTracker() // budget 0 — would gate an LLM lobe

	r := NewLobeRunner(lobe, w, NewLobeSemaphore(1), b, tracker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Tick() <- struct{}{}

	if got := waitForCalls(&lobe.Calls, 1, 2*time.Second); got != 1 {
		t.Errorf("deterministic lobe.Run calls = %d, want 1 (budget must not gate deterministic lobes)", got)
	}
}

// TestMidturnNote_ResetsRoundAccumulator asserts Cortex.MidturnNote
// zeroes the per-round accumulator before ticking the round so each
// round measures its own consumption (spec item 21).
func TestMidturnNote_ResetsRoundAccumulator(t *testing.T) {
	t.Parallel()

	ws := NewWorkspace(hub.New(), nil)
	c, err := New(Config{
		SessionID:     "budget-reset-test",
		EventBus:      hub.New(),
		Provider:      stubProvider{},
		Workspace:     ws,
		Lobes:         []Lobe{&EchoLobe{Workspace: ws}},
		RoundDeadline: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}

	tracker := c.Tracker()
	tracker.RecordMainTurn(1000) // budget 300
	tracker.Charge("stale", stream.TokenUsage{Output: 500})
	if !tracker.Exceeded() {
		t.Fatal("precondition: tracker should be exceeded before the round")
	}

	// Cortex was never Started, so no runner consumes the tick; the
	// Round waits out the short deadline and MidturnNote returns. The
	// reset happens up front regardless.
	_ = c.MidturnNote(nil, 1)

	if tracker.Exceeded() {
		t.Error("MidturnNote must ResetRound: accumulator still exceeded after a new round began")
	}
	if got := tracker.RoundOutputBudget(); got != 300 {
		t.Errorf("RoundOutputBudget = %d after reset, want 300 (mainOutputLastTurn untouched)", got)
	}
}
