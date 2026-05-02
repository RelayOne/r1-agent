package cortex

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrRoundDeadlineExceeded is returned by Round.Wait when the per-round
// deadline elapses before all expected participants have called Done.
// Distinct from context.DeadlineExceeded so callers can distinguish a
// round timeout (proceed with partial results) from an outer ctx timeout
// (abort the whole turn).
var ErrRoundDeadlineExceeded = errors.New("cortex: round deadline exceeded")

// Round is the superstep barrier. Pattern:
//  1. Open(roundID, expected) — declare expected participating Lobes for
//     this round.
//  2. Each Lobe calls Done(roundID, lobeID) exactly once when its work
//     for the round is complete.
//  3. Wait(ctx, roundID, deadline) blocks until all expected participants
//     have Done() OR ctx is cancelled OR the per-round deadline elapses.
//  4. Close(roundID) advances the round counter; subsequent rounds
//     carry a strictly greater ID.
//
// Done is idempotent per (roundID, lobeID): duplicate calls do not
// double-decrement the expected counter and never panic. This matters
// because Lobe runners may retry a round on transient errors, and we
// must never close the done-channel before all *distinct* Lobes have
// reported in.
type Round struct {
	mu           sync.Mutex
	current      uint64                       // last Closed round; Open requires roundID > current
	participants map[uint64]map[string]bool   // roundID -> set of lobeIDs that have Done
	done         map[uint64]chan struct{}     // roundID -> close()d when expected hits 0
	expected     map[uint64]int               // roundID -> remaining participants
}

// NewRound constructs an empty Round. The zero "current" means the
// first valid Open uses any roundID >= 1.
func NewRound() *Round {
	return &Round{
		participants: make(map[uint64]map[string]bool),
		done:         make(map[uint64]chan struct{}),
		expected:     make(map[uint64]int),
	}
}

// Open establishes a new round with the given expected participant count.
// Panics if roundID <= r.current (rounds must strictly advance) or if
// the same roundID has already been Open'd. An expected of 0 produces
// an immediately-closed done channel, which lets Wait return nil
// without blocking — useful for rounds where no Lobes were dispatched.
func (r *Round) Open(roundID uint64, expected int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if roundID <= r.current {
		panic("cortex: Round.Open: roundID must be > current")
	}
	if _, exists := r.done[roundID]; exists {
		panic("cortex: Round.Open: roundID already open")
	}
	ch := make(chan struct{})
	r.done[roundID] = ch
	r.expected[roundID] = expected
	r.participants[roundID] = make(map[string]bool)
	if expected <= 0 {
		// Zero participants — close immediately so Wait does not block.
		close(ch)
	}
}

// Done records that lobeID has completed its work for roundID.
// Idempotent per (roundID, lobeID): a repeat call from the same Lobe
// does not decrement expected again and does not panic. Calls for an
// unknown roundID (never Open'd, or already Close'd) are silently
// ignored — a late Lobe from a prior round must not corrupt counters.
func (r *Round) Done(roundID uint64, lobeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	parts, ok := r.participants[roundID]
	if !ok {
		// Unknown or already-closed round: drop on the floor.
		return
	}
	if parts[lobeID] {
		// Duplicate Done from the same Lobe in the same round.
		return
	}
	parts[lobeID] = true
	r.expected[roundID]--
	if r.expected[roundID] <= 0 {
		// Last participant in: signal Wait. Guarded against double-close
		// because the duplicate check above ensures we only reach 0 once.
		if ch, ok := r.done[roundID]; ok {
			close(ch)
			// Replace with nil so a stray Done after-the-fact (which we
			// already filtered above) cannot accidentally re-close. Not
			// strictly necessary given the participants guard, but cheap.
		}
	}
}

// Wait blocks until all expected participants for roundID have Done(),
// the per-round deadline elapses, or ctx is cancelled. Returns nil on
// success, ErrRoundDeadlineExceeded on deadline, or ctx.Err() on
// cancellation. Wait on an unknown roundID returns immediately with
// ErrRoundDeadlineExceeded after the deadline (the caller is asking
// about a round that never existed; we don't pretend it succeeded).
func (r *Round) Wait(ctx context.Context, roundID uint64, deadline time.Duration) error {
	// Read the done channel under the mutex BEFORE entering select,
	// otherwise Close() could swap state between our read and our wait
	// and we'd block on a stale channel.
	r.mu.Lock()
	doneCh, ok := r.done[roundID]
	r.mu.Unlock()

	if !ok {
		// No such round — fall through to deadline so callers don't
		// hang forever, but also so success isn't silently fabricated.
		select {
		case <-time.After(deadline):
			return ErrRoundDeadlineExceeded
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	select {
	case <-doneCh:
		return nil
	case <-time.After(deadline):
		return ErrRoundDeadlineExceeded
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close cleans up bookkeeping for a completed round and advances
// r.current to roundID so subsequent Open calls must use a strictly
// greater ID. Safe to call once per round; calling on an unknown round
// is a no-op (besides the current bump). Late Done calls for the
// closed round are silently dropped (see Done).
func (r *Round) Close(roundID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.participants, roundID)
	delete(r.done, roundID)
	delete(r.expected, roundID)
	if roundID > r.current {
		r.current = roundID
	}
}

// Current returns the highest roundID that has been Close'd. Useful
// for diagnostics and for the agentloop's between-turn hook to confirm
// the cortex has advanced.
func (r *Round) Current() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}
