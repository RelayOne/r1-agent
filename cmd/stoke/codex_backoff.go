package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// codexErrorBackoff tracks codex reviewer errors over a rolling
// 5-minute window and applies exponential backoff to the NEXT
// reviewer call when the error rate exceeds the threshold.
//
// H-7 (2026-04-17) — D1-default simple-loop saw codex errors
// escalate from 7→9 in 5 min. Provider-level retry absorbs
// transients but nothing reacted to the PATTERN of recurring
// errors, so the final-review loop could stall the same way
// MS-full did (17-min wedged CC child). This tracker watches
// the pattern and throttles reviewer calls before they all fail
// in a tight loop.
//
// Escalation:
//   - errors within the window < threshold → multiplier 1 (no delay)
//   - threshold crossed → next call gets 2x base delay
//   - if the PREVIOUS call also tripped the threshold → 4x, then 8x
//   - cap at 8x
//   - one successful call resets multiplier to 1
//
// The "base delay" is whatever the caller passes in (typically the
// existing inter-call delay in the reviewer loop; may be zero). The
// multiplier is applied to that base delay as the sleep BEFORE the
// next call.
//
// The clock is mockable via `now` so unit tests don't have to
// actually sleep through the 5-minute window.
type codexErrorBackoff struct {
	mu sync.Mutex

	// errorTimes is the rolling window of error timestamps.
	// Entries older than windowSize are dropped on every access.
	errorTimes []time.Time

	// multiplier is the CURRENT backoff multiplier (1, 2, 4, 8).
	// Applied to the base delay for the NEXT call. Escalates each
	// consecutive trip; resets to 1 on success.
	multiplier int

	// lastCallTrippedBackoff tracks whether the immediately prior
	// reviewer call was one that triggered backoff (i.e. the window
	// exceeded threshold at its dispatch). Used to decide whether
	// the NEXT trip is "consecutive" (escalate) or "first"
	// (start at 2x).
	lastCallTrippedBackoff bool

	// tunables — exposed as fields (not consts) so tests can use
	// smaller windows without waiting 5 actual minutes.
	windowSize time.Duration // rolling window (5 min)
	threshold  int           // errors-in-window before backoff (5)
	maxMult    int           // multiplier cap (8)

	// now is the mockable clock. In production it's time.Now; in
	// tests it's a controllable function so we can advance without
	// real sleeps.
	now func() time.Time
}

// newCodexErrorBackoff constructs a tracker with production
// defaults: 5-min window, 5-error threshold, 8x cap, wall clock.
func newCodexErrorBackoff() *codexErrorBackoff {
	return &codexErrorBackoff{
		multiplier: 1,
		windowSize: 5 * time.Minute,
		threshold:  5,
		maxMult:    8,
		now:        time.Now,
	}
}

// pruneLocked drops error timestamps older than windowSize.
// Caller MUST hold the mutex.
func (b *codexErrorBackoff) pruneLocked() {
	cutoff := b.now().Add(-b.windowSize)
	i := 0
	for i < len(b.errorTimes) && b.errorTimes[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		b.errorTimes = b.errorTimes[i:]
	}
}

// errorCountLocked returns the number of errors currently in the
// window. Caller MUST hold the mutex.
func (b *codexErrorBackoff) errorCountLocked() int {
	b.pruneLocked()
	return len(b.errorTimes)
}

// NextDelay returns the sleep duration to apply BEFORE the next
// reviewer call, based on the current multiplier. Does NOT mutate
// state. The caller passes the base delay (often 0 in the reviewer
// loop) and gets back `base * multiplier`.
//
// When backoff is active (multiplier > 1) and base is zero, we
// still apply a sensible floor so the throttle is observable —
// 30s * multiplier. This matches the 30-s watchdog tick in the
// reviewer loop, so a backoff of 2x yields 60s, 4x yields 120s,
// 8x yields 240s. Without the floor, multiplier*0 = 0 and the
// throttle would be invisible.
func (b *codexErrorBackoff) NextDelay(base time.Duration) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.multiplier <= 1 {
		return base
	}
	if base == 0 {
		return time.Duration(b.multiplier) * 30 * time.Second
	}
	return time.Duration(b.multiplier) * base
}

// Multiplier returns the current backoff multiplier. Primarily for
// testing and logging.
func (b *codexErrorBackoff) Multiplier() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.multiplier
}

// ErrorCount returns the number of errors currently in the window.
// Primarily for testing and logging.
func (b *codexErrorBackoff) ErrorCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.errorCountLocked()
}

// RecordError adds one error at the current time. If the window
// now exceeds the threshold, escalate the multiplier. "Escalation"
// depends on whether the prior call already tripped backoff:
//   - first trip → multiplier = 2
//   - consecutive trip → multiplier *= 2 (capped at maxMult)
//
// Returns true if this error caused the tracker to enter or
// re-escalate backoff. Caller can use the return to log a message.
func (b *codexErrorBackoff) RecordError() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.errorTimes = append(b.errorTimes, b.now())
	b.pruneLocked()
	if len(b.errorTimes) < b.threshold {
		// Not yet over threshold. Remember this call didn't trip.
		b.lastCallTrippedBackoff = false
		return false
	}
	// Over threshold. Escalate.
	if b.lastCallTrippedBackoff {
		// Consecutive trip: double the multiplier.
		b.multiplier *= 2
		if b.multiplier < 2 {
			b.multiplier = 2
		}
	} else {
		// First trip after a clean stretch: jump to 2x.
		b.multiplier = 2
	}
	if b.multiplier > b.maxMult {
		b.multiplier = b.maxMult
	}
	b.lastCallTrippedBackoff = true
	return true
}

// RecordSuccess resets the multiplier to 1 and marks the prior
// call as "did not trip". The error-time window is NOT cleared —
// a future error still counts against the rolling window, so the
// tracker can re-enter backoff quickly if the pattern resumes.
func (b *codexErrorBackoff) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.multiplier = 1
	b.lastCallTrippedBackoff = false
}

// Package-level singleton used by the reviewer call site in
// simple_loop.go. A single shared tracker is fine because the
// reviewer loop is single-threaded (commits are reviewed serially
// within a round).
var codexBackoff = newCodexErrorBackoff()

// applyCodexBackoff sleeps for the current backoff delay (if any)
// and logs when backoff is active. Called BEFORE each codex
// reviewer call. Safe to call unconditionally — a no-op when
// multiplier == 1.
func applyCodexBackoff() {
	delay := codexBackoff.NextDelay(0)
	if delay > 0 {
		mult := codexBackoff.Multiplier()
		count := codexBackoff.ErrorCount()
		fmt.Fprintf(os.Stderr,
			"  ⏸ codex backoff: %dx delay for %d min (codex errors: %d in last 5min)\n",
			mult, int(delay/time.Minute), count)
		time.Sleep(delay)
	}
}
