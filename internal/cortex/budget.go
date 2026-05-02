package cortex

import (
	"context"
	"fmt"
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
