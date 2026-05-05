package cortex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/stream"
)

func TestSemaphoreCapacity(t *testing.T) {
	s := NewLobeSemaphore(8)

	// 8 acquires should all succeed within their timeout.
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		if err := s.Acquire(ctx); err != nil {
			cancel()
			t.Fatalf("acquire %d: unexpected error: %v", i, err)
		}
		cancel()
	}

	// 9th acquire must block until the timeout expires.
	ctx9, cancel9 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	err := s.Acquire(ctx9)
	elapsed := time.Since(start)
	cancel9()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("9th acquire: expected DeadlineExceeded, got %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("9th acquire returned too quickly (%v); expected to block until deadline", elapsed)
	}

	// Release one slot, then a fresh acquire should succeed.
	s.Release()
	ctxOK, cancelOK := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelOK()
	if err := s.Acquire(ctxOK); err != nil {
		t.Fatalf("acquire after release: unexpected error: %v", err)
	}
}

func TestSemaphoreCtxCancel(t *testing.T) {
	s := NewLobeSemaphore(1)

	// Take the only slot.
	ctxFirst, cancelFirst := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelFirst()
	if err := s.Acquire(ctxFirst); err != nil {
		t.Fatalf("initial acquire: unexpected error: %v", err)
	}

	// Spawn a goroutine that tries to acquire under a cancellable ctx.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Acquire(ctx)
	}()

	// Give the goroutine a moment to enter Acquire, then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("goroutine did not return within 100ms after cancel")
	}
}

func TestSemaphorePanicsOversize(t *testing.T) {
	cases := []int{0, 9}
	for _, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("NewLobeSemaphore(%d): expected panic, got none", c)
				}
			}()
			_ = NewLobeSemaphore(c)
		})
	}
}

// TestBudgetTrackerCharge verifies that Charge accumulates Output tokens
// and that Exceeded trips when the accumulator meets the 30%-of-main cap.
func TestBudgetTrackerCharge(t *testing.T) {
	bt := NewBudgetTracker()
	bt.RecordMainTurn(1000)

	if got, want := bt.RoundOutputBudget(), 300; got != want {
		t.Fatalf("RoundOutputBudget after RecordMainTurn(1000): got %d, want %d", got, want)
	}

	// Three Charges of 100 each -> 300 total, which equals the budget.
	for i := 0; i < 3; i++ {
		bt.Charge("lobe-x", stream.TokenUsage{Output: 100})
	}

	if !bt.Exceeded() {
		t.Fatalf("Exceeded after 3x100 charges against budget 300: got false, want true")
	}
}

// TestBudgetTrackerResetRound verifies that ResetRound zeroes the
// per-round accumulator without disturbing mainOutputLastTurn.
func TestBudgetTrackerResetRound(t *testing.T) {
	bt := NewBudgetTracker()
	bt.RecordMainTurn(1000)

	for i := 0; i < 3; i++ {
		bt.Charge("lobe-x", stream.TokenUsage{Output: 100})
	}
	if !bt.Exceeded() {
		t.Fatalf("pre-reset: Exceeded should be true after charging 300/300")
	}

	bt.ResetRound()

	if bt.Exceeded() {
		t.Fatalf("post-reset: Exceeded should be false (0 < 300)")
	}
	if got, want := bt.RoundOutputBudget(), 300; got != want {
		t.Fatalf("post-reset: RoundOutputBudget mutated: got %d, want %d", got, want)
	}

	// New round behaves identically to the first.
	for i := 0; i < 3; i++ {
		bt.Charge("lobe-x", stream.TokenUsage{Output: 100})
	}
	if !bt.Exceeded() {
		t.Fatalf("post-reset second-round: Exceeded should be true after re-charging 300")
	}
}

// TestBudgetTrackerEmptyMainTurn documents the fail-closed behavior: with
// no RecordMainTurn observation, RoundOutputBudget is 0 and Exceeded is
// immediately true (lobeOutputThisRound >= 0).
func TestBudgetTrackerEmptyMainTurn(t *testing.T) {
	bt := NewBudgetTracker()

	if got, want := bt.RoundOutputBudget(), 0; got != want {
		t.Fatalf("RoundOutputBudget on fresh tracker: got %d, want %d", got, want)
	}
	if !bt.Exceeded() {
		t.Fatalf("Exceeded on fresh tracker: got false, want true (fail-closed)")
	}

	bt.Charge("lobe-x", stream.TokenUsage{Output: 500})
	if !bt.Exceeded() {
		t.Fatalf("Exceeded after Charge with no main turn: got false, want true")
	}
}
