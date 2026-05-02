package cortex

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// assert is a tiny local helper namespace that gives our t.Fatalf
// checks a uniform "assert.X(...)" prefix. Keeps the round_test.go
// readable and makes the assertion sites easy to grep for.
type assertNS struct{ t *testing.T }

func newAssert(t *testing.T) assertNS { t.Helper(); return assertNS{t: t} }

// NoError fails the test if err is not nil.
func (a assertNS) NoError(err error, msg string) {
	a.t.Helper()
	if err != nil {
		a.t.Fatalf("%s: unexpected error: %v", msg, err)
	}
}

// ErrorIs fails the test if errors.Is(err, target) is false.
func (a assertNS) ErrorIs(err, target error, msg string) {
	a.t.Helper()
	if !errors.Is(err, target) {
		a.t.Fatalf("%s: got %v, want %v", msg, err, target)
	}
}

// EqualUint fails the test if got != want.
func (a assertNS) EqualUint(got, want uint64, msg string) {
	a.t.Helper()
	if got != want {
		a.t.Fatalf("%s: got %d, want %d", msg, got, want)
	}
}

// DurationAtLeast fails the test if d < min.
func (a assertNS) DurationAtLeast(d, min time.Duration, msg string) {
	a.t.Helper()
	if d < min {
		a.t.Fatalf("%s: %v < %v", msg, d, min)
	}
}

// DurationAtMost fails the test if d > max.
func (a assertNS) DurationAtMost(d, max time.Duration, msg string) {
	a.t.Helper()
	if d > max {
		a.t.Fatalf("%s: %v > %v", msg, d, max)
	}
}

// TestRoundHappy verifies the canonical 3-participant superstep: Open,
// three concurrent Done calls (one per Lobe), Wait returns nil before
// the deadline elapses.
func TestRoundHappy(t *testing.T) {
	t.Parallel()
	assert := newAssert(t)

	r := NewRound()
	const rid uint64 = 1
	r.Open(rid, 3)

	var wg sync.WaitGroup
	for _, lobe := range []string{"alpha", "beta", "gamma"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.Done(rid, id)
		}(lobe)
	}

	ctx := context.Background()
	waitErr := r.Wait(ctx, rid, 500*time.Millisecond)
	assert.NoError(waitErr, "Wait with all 3 Done")
	wg.Wait()

	r.Close(rid)
	assert.EqualUint(r.Current(), rid, "Current after Close")
}

// TestRoundDeadline verifies that when fewer than expected participants
// call Done, Wait returns ErrRoundDeadlineExceeded after the deadline.
func TestRoundDeadline(t *testing.T) {
	t.Parallel()
	assert := newAssert(t)

	r := NewRound()
	const rid uint64 = 1
	r.Open(rid, 2)

	// Only one of two participants reports in.
	r.Done(rid, "alpha")

	start := time.Now()
	waitErr := r.Wait(context.Background(), rid, 50*time.Millisecond)
	elapsed := time.Since(start)

	assert.ErrorIs(waitErr, ErrRoundDeadlineExceeded, "Wait with 1/2 Done")
	assert.DurationAtLeast(elapsed, 40*time.Millisecond, "Wait should not return early")
	// Loose upper bound to catch accidental long sleeps under -race.
	assert.DurationAtMost(elapsed, 5*time.Second, "Wait should not block past deadline")
}

// TestRoundCtxCancel verifies that cancelling the ctx mid-Wait returns
// context.Canceled rather than ErrRoundDeadlineExceeded.
func TestRoundCtxCancel(t *testing.T) {
	t.Parallel()
	assert := newAssert(t)

	r := NewRound()
	const rid uint64 = 1
	r.Open(rid, 2)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay so Wait is genuinely blocked.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	// Use a long deadline so the ctx cancel path is what unblocks Wait.
	waitErr := r.Wait(ctx, rid, 5*time.Second)
	assert.ErrorIs(waitErr, context.Canceled, "Wait should observe ctx cancel")
}

// TestRoundDuplicateDone verifies that calling Done twice for the same
// (roundID, lobeID) is idempotent — no panic, and the second call does
// NOT double-decrement the expected counter (so Wait still blocks
// until a *different* Lobe reports in).
func TestRoundDuplicateDone(t *testing.T) {
	t.Parallel()
	assert := newAssert(t)

	r := NewRound()
	const rid uint64 = 1
	r.Open(rid, 2)

	// Same Lobe reports in twice. If Done were not idempotent, this
	// would decrement expected to 0 and Wait would return nil before
	// "beta" ever reports.
	r.Done(rid, "alpha")
	r.Done(rid, "alpha")

	// Confirm Wait still blocks: a short deadline must time out because
	// only one *distinct* Lobe has reported in.
	firstWaitErr := r.Wait(context.Background(), rid, 30*time.Millisecond)
	assert.ErrorIs(firstWaitErr, ErrRoundDeadlineExceeded, "duplicate Done must not double-decrement")

	// Now the second distinct Lobe reports in; Wait must succeed.
	go func() {
		// Small stagger so Wait is genuinely waiting on the channel.
		time.Sleep(5 * time.Millisecond)
		r.Done(rid, "beta")
	}()

	secondWaitErr := r.Wait(context.Background(), rid, 500*time.Millisecond)
	assert.NoError(secondWaitErr, "Wait after both distinct Lobes Done")

	// And one more duplicate for good measure — no panic, no effect.
	r.Done(rid, "alpha")
	r.Done(rid, "beta")

	r.Close(rid)
	assert.EqualUint(r.Current(), rid, "Current after Close on duplicate-Done round")
}
