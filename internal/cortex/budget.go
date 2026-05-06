package cortex

import (
	"context"
	"fmt"
	"sync"

	"github.com/RelayOne/r1/internal/stream"
)

// LobeSemaphore is a bounded buffered-channel semaphore that caps the number
// of concurrent in-flight lobe operations. Capacity is constrained to 1..8
// (NewLobeSemaphore panics outside that range) to prevent runaway parallelism
// in the cortex orchestrator.
//
// Acquire blocks (until ctx is done) when capacity is exhausted; Release is a
// non-blocking receive that frees one slot. Calling Release without a matching
// Acquire is a defensive no-op rather than a panic.
type LobeSemaphore struct {
	slots chan struct{}
}

// NewLobeSemaphore returns a LobeSemaphore with the given capacity.
// It panics if capacity is outside the inclusive range [1, 8].
func NewLobeSemaphore(capacity int) *LobeSemaphore {
	if capacity < 1 || capacity > 8 {
		panic(fmt.Sprintf("cortex: LobeSemaphore capacity must be 1..8, got %d", capacity))
	}
	return &LobeSemaphore{slots: make(chan struct{}, capacity)}
}

// Acquire reserves one slot, blocking until either a slot becomes available
// or ctx is done. On context cancellation it returns ctx.Err() and does not
// hold a slot.
func (s *LobeSemaphore) Acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees one previously-acquired slot. It is non-blocking: if no slot
// is currently held it is a no-op (defensive, so a stray Release cannot stall
// the caller or panic on an empty channel).
func (s *LobeSemaphore) Release() {
	select {
	case <-s.slots:
	default:
	}
}

// BudgetTracker enforces a per-round output-token cap on Lobe activity by
// pegging the budget to a fraction (currently 30%) of the main agent's last
// output turn. Cortex calls RecordMainTurn from a hub.Bus subscription on
// EventModelPostCall so that mainOutputLastTurn always reflects the most
// recent main-loop turn. Lobe runners call Charge after each Lobe LLM
// invocation to accumulate lobeOutputThisRound, and consult Exceeded before
// dispatching the next Lobe round; ResetRound is invoked when a new round
// begins so the accumulator measures a single round in isolation.
//
// All methods are safe for concurrent use; mu guards both counters.
//
// LobeRunner integration contract (spec item 21): once TASK-9's
// LobeRunner is wired in lobe.go, its runOnce path consults this
// tracker after a successful Acquire. When Exceeded() reports true the
// runner Releases the slot and emits "cortex.lobe.budget_skipped"
// rather than invoking the LLM. The wiring lives in lobe.go (TASK-9)
// per the agreed split between the two tasks.
type BudgetTracker struct {
	mu                  sync.Mutex
	mainOutputLastTurn  int
	lobeOutputThisRound int
}

// NewBudgetTracker returns a zero-valued BudgetTracker. Until the first
// RecordMainTurn call, RoundOutputBudget is 0 and any non-empty Charge
// trips Exceeded — this is intentional fail-closed behavior.
func NewBudgetTracker() *BudgetTracker {
	return &BudgetTracker{}
}

// Charge accumulates the Output token count from a Lobe's LLM usage into
// the per-round counter. The lobeID parameter is currently unused but is
// accepted for future per-Lobe telemetry (e.g. to attribute consumption).
func (t *BudgetTracker) Charge(lobeID string, usage stream.TokenUsage) {
	_ = lobeID
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lobeOutputThisRound += usage.Output
}

// RoundOutputBudget returns the current per-round Lobe output budget,
// computed as 30% of mainOutputLastTurn. With no main-turn observed yet
// the budget is 0.
func (t *BudgetTracker) RoundOutputBudget() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mainOutputLastTurn * 30 / 100
}

// Exceeded reports whether the per-round Lobe output accumulator has met
// or exceeded the current RoundOutputBudget. Note that with
// mainOutputLastTurn == 0 the budget is 0, so any Charge (and even no
// Charge, since 0 >= 0) trips this — callers should treat the absence of
// a recorded main turn as a deliberate fail-closed gate.
func (t *BudgetTracker) Exceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lobeOutputThisRound >= t.mainOutputLastTurn*30/100
}

// ResetRound zeroes the per-round Lobe output accumulator. It does not
// touch mainOutputLastTurn; the budget is recomputed from whatever
// main-turn value is current at the time Exceeded is consulted.
func (t *BudgetTracker) ResetRound() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lobeOutputThisRound = 0
}

// RecordMainTurn records the Output-token count of the most recent main
// agent turn so subsequent RoundOutputBudget calls can derive the 30% cap.
// Cortex wires this to a hub.Bus subscription on EventModelPostCall;
// see Cortex.Start for the subscriber registration that pulls
// hub.ModelEvent.OutputTokens off the bus and feeds it here.
//
// Signature note: TASK-24 retyped this from
// RecordMainTurn(usage stream.TokenUsage) to RecordMainTurn(outputTokens int)
// because the hub event already exposes OutputTokens as an int — wrapping it
// back into stream.TokenUsage just to unwrap it on the other side added no
// value and forced the bus subscriber to take a stream dependency. The
// previous signature is callable as bt.RecordMainTurn(usage.Output).
func (t *BudgetTracker) RecordMainTurn(outputTokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mainOutputLastTurn = outputTokens
}
