package llm

import (
	"context"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
)

// TestLLMSlot_BlocksAtCapFiveByDefault verifies the shared semaphore at
// DefaultLLMSlotCap=5 admits exactly 5 concurrent acquirers, blocks the
// 6th until ctx deadline, and frees a slot when one of the holders
// releases.
//
// Spec: specs/cortex-concerns.md item 5.
func TestLLMSlot_BlocksAtCapFiveByDefault(t *testing.T) {
	sem := cortex.NewLobeSemaphore(DefaultLLMSlotCap)
	ctx := context.Background()

	// Acquire 5 slots successfully.
	releases := make([]func(), 0, DefaultLLMSlotCap)
	for i := 0; i < DefaultLLMSlotCap; i++ {
		rel, err := MustAcquire(ctx, sem)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		releases = append(releases, rel)
	}

	// Sixth blocks; verify by using a short-deadline ctx.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	rel6, err := MustAcquire(short, sem)
	if err == nil {
		// Release immediately if we somehow got a slot, then fail.
		if rel6 != nil {
			rel6()
		}
		t.Fatal("expected DeadlineExceeded on 6th acquire, got nil")
	}
	if rel6 != nil {
		t.Fatal("expected nil release on failed acquire, got non-nil")
	}

	// Release one; next acquire should succeed.
	releases[0]()
	rel, err := MustAcquire(ctx, sem)
	if err != nil {
		t.Fatalf("after-release acquire: %v", err)
	}
	rel()

	// Drain the rest so the test leaves no goroutines blocked.
	for _, r := range releases[1:] {
		r()
	}
}

// TestLLMSlot_NilAcquirerNoOps confirms that passing a nil SlotAcquirer
// is treated as "no semaphore configured" — MustAcquire succeeds
// immediately and the returned release is safe to call.
func TestLLMSlot_NilAcquirerNoOps(t *testing.T) {
	rel, err := MustAcquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil acquirer: %v", err)
	}
	if rel == nil {
		t.Fatal("expected non-nil release closure for nil acquirer")
	}
	rel() // must not panic
}
