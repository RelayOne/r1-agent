package llm

import (
	"context"
)

// Per-call vs. runner-level semaphore acquisition
// -----------------------------------------------
//
// internal/cortex.LobeRunner calls semaphore Acquire/Release once per
// Run for KindLLM Lobes (see internal/cortex/lobe.go runOnce). That
// outer gating is sufficient for Lobes whose ENTIRE LLM workload is a
// single ChatStream call invoked from inside Run — and only from Run.
// For those single-call Lobes (e.g. planupdate) the per-call helper
// below is unnecessary; the runner's Acquire is the slot-cap source
// of truth.
//
// However, several KindLLM Lobes (clarifyq, memorycurator) drive their
// LLM call from a hub.Bus subscriber callback (cortex.user.message,
// task.completed) instead of — or in addition to — the runner's per-
// Round Tick. Hub-subscriber callbacks run in the bus's own goroutine
// and bypass LobeRunner.runOnce entirely, so the runner-level Acquire
// never fires for that path. To keep the slot cap honest for those
// Lobes, the bus-subscriber entry point is wrapped in a per-call
// MustAcquire/release pair seeded from the SlotAcquirer the Lobe was
// constructed with. A nil SlotAcquirer is the legacy/test mode and
// behaves as a no-op (see MustAcquire below).
//
// IMPORTANT: a Lobe that ALSO has a runner-driven path must place the
// per-call gate at the BUS-SUBSCRIBER entry point (not deep in the
// shared ChatStream callsite), otherwise the cadence path would
// double-acquire on top of the runner's outer Acquire and could
// deadlock when cap=1. See memorycurator.handleTaskCompleted for the
// reference layout.
//
// Pattern (bus-subscriber entry point):
//
//	rel, err := llm.MustAcquire(ctx, l.semaphore)
//	if err != nil {
//	    return // ctx cancelled or deadline; do NOT call rel
//	}
//	defer rel()
//	l.fireTrigger(ctx, in) // or the path's shared LLM helper
//
// Spec: specs/cortex-concerns.md item 5; audit follow-up
// "scan-governance-gaps.md cortex-concerns 5".

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
