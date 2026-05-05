package llm

import (
	"context"
)

// SlotAcquirer is the minimal interface for grabbing an LLM-output slot.
// internal/cortex.LobeSemaphore satisfies this directly via its Acquire
// and Release methods, so callers in cortex.New can pass the shared
// semaphore through to each LLM Lobe without an adapter type.
//
// Spec: specs/cortex-concerns.md item 5.
type SlotAcquirer interface {
	Acquire(ctx context.Context) error
	Release()
}

// MustAcquire reserves one LLM slot and returns a release closure that
// the caller must invoke when its LLM call returns. A nil SlotAcquirer
// is treated as "no semaphore configured": MustAcquire succeeds
// immediately and the returned release is a no-op. This keeps Lobe
// implementations simple — they can call MustAcquire unconditionally
// without nil-checking the semaphore field.
//
// On Acquire failure (typically ctx cancellation or deadline exceeded)
// MustAcquire returns the ctx error and a nil release closure; callers
// must NOT invoke the release in that case.
func MustAcquire(ctx context.Context, s SlotAcquirer) (release func(), err error) {
	if s == nil {
		return func() {}, nil
	}
	if err := s.Acquire(ctx); err != nil {
		return nil, err
	}
	return s.Release, nil
}

// DefaultLLMSlotCap is the recommended capacity for the shared LLM-Lobe
// semaphore. It mirrors cortex-core's MaxLLMLobes default (item 12 of
// specs/cortex-core.md) so an LLM-Lobe-only deployment behaves
// identically whether or not it shares the LobeSemaphore with
// deterministic Lobes.
const DefaultLLMSlotCap = 5
