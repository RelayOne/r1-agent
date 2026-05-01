package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// claudeRateLimitDetector gates claude CLI invocations against the
// distinctive rate-limit / account-cutoff failure pattern that killed
// H1-sonnet and H2-opus-full in the hardened cohort (H-10, 2026-04-17):
// every `claude` call returned `claude error: exit status 1` after
// exactly 1 turn at $0.0000 cost, and the outer Step-8 regression
// guard mistook those empty runs for real compliance regressions.
//
// The detector classifies each call outcome and runs a three-state
// machine:
//
//   - Normal    — no rate-limit observed in the rolling window;
//                 WaitIfPaused is a no-op.
//   - Suspected — exactly one rate-limit signature seen within the
//                 5-min rolling window. Next call still proceeds
//                 normally; if a second signature arrives within the
//                 window it promotes us to Active.
//   - Active    — pause mode. WaitIfPaused sleeps for the current
//                 backoff level (1, 2, 4, 8, 15, 30 min cap) before
//                 returning so the caller can make ONE retry call.
//                 A successful result (turns > 1 OR cost > 0) resets
//                 to Normal. Another rate-limit signature escalates
//                 the backoff level and stays Active.
//
// The detector NEVER terminates the run — the 30-min cap is sustained
// forever. The outer operator can Ctrl-C if they want to give up.
//
// The clock is mockable via `now` so unit tests don't sleep through
// real 5-minute windows, and `sleep` is mockable so WaitIfPaused
// doesn't actually sleep in tests.
type claudeRateLimitDetector struct {
	mu sync.Mutex

	state claudeBackoffState

	// signatureTimes is the rolling window of rate-limit-signature
	// timestamps. Entries older than windowSize are dropped on access.
	signatureTimes []time.Time

	// backoffStep indexes into backoffLadder for the NEXT sleep.
	// Incremented on each consecutive rate-limited retry; reset to 0
	// on success. Capped at len(backoffLadder)-1.
	backoffStep int

	// tunables — exposed as fields (not consts) so tests can use
	// smaller windows / shorter ladders without waiting 30 real min.
	windowSize    time.Duration
	backoffLadder []time.Duration

	// now is the mockable clock; sleep is the mockable pause.
	now   func() time.Time
	sleep func(time.Duration)
}

// claudeBackoffState is the detector's current state.
type claudeBackoffState int

const (
	claudeBackoffNormal claudeBackoffState = iota
	claudeBackoffSuspected
	claudeBackoffActive
)

// newClaudeRateLimitDetector constructs a detector with production
// defaults: 5-min rolling window, ladder 1/2/4/8/15/30 min.
func newClaudeRateLimitDetector() *claudeRateLimitDetector {
	return &claudeRateLimitDetector{
		state:      claudeBackoffNormal,
		windowSize: 5 * time.Minute,
		backoffLadder: []time.Duration{
			1 * time.Minute,
			2 * time.Minute,
			4 * time.Minute,
			8 * time.Minute,
			15 * time.Minute,
			30 * time.Minute,
		},
		now:   time.Now,
		sleep: time.Sleep,
	}
}

// pruneLocked drops signatures older than windowSize. Caller holds mu.
func (d *claudeRateLimitDetector) pruneLocked() {
	cutoff := d.now().Add(-d.windowSize)
	i := 0
	for i < len(d.signatureTimes) && d.signatureTimes[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		d.signatureTimes = d.signatureTimes[i:]
	}
}

// State returns the current state. For tests and logging.
func (d *claudeRateLimitDetector) State() claudeBackoffState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// SignatureCount returns how many rate-limit signatures are still in
// the rolling window. For tests.
func (d *claudeRateLimitDetector) SignatureCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked()
	return len(d.signatureTimes)
}

// CurrentBackoff returns the sleep duration that WaitIfPaused would
// apply on the NEXT call, given current state. Zero when Normal/
// Suspected. For tests and log lines.
func (d *claudeRateLimitDetector) CurrentBackoff() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.currentBackoffLocked()
}

func (d *claudeRateLimitDetector) currentBackoffLocked() time.Duration {
	if d.state != claudeBackoffActive {
		return 0
	}
	step := d.backoffStep
	if step < 0 {
		step = 0
	}
	if step >= len(d.backoffLadder) {
		step = len(d.backoffLadder) - 1
	}
	return d.backoffLadder[step]
}

// WaitIfPaused blocks if the detector is currently in Active pause,
// sleeping for the current backoff level. A no-op in Normal/Suspected
// states, making it safe to call unconditionally before every claude
// invocation. After the sleep returns, the caller should make one
// claude call and report the outcome to RecordResult.
//
// The log banner prints once per sustained pause cycle (each time the
// ladder advances). We do NOT rate-limit the log lines; the caller
// genuinely wants to see that the run is paused but alive.
func (d *claudeRateLimitDetector) WaitIfPaused() {
	d.mu.Lock()
	if d.state != claudeBackoffActive {
		d.mu.Unlock()
		return
	}
	delay := d.currentBackoffLocked()
	sigCount := len(d.signatureTimes)
	step := d.backoffStep
	atCap := step >= len(d.backoffLadder)-1
	sleep := d.sleep
	d.mu.Unlock()

	if atCap {
		fmt.Fprintf(os.Stderr,
			"\n⏸ Claude rate-limit: pause sustained at %s (ladder cap). %d signatures in last 5min.\n"+
				"   Still retrying every %s — run is NOT dead. Ctrl-C to give up.\n\n",
			delay, sigCount, delay)
	} else {
		fmt.Fprintf(os.Stderr,
			"\n⏸ Claude rate-limit detected (%d signatures in last 5min). Pausing %s before retry...\n"+
				"   Signatures: exit-1 after 1 turn at $0.00. Common cause: account rate limit or Claude CLI auth expired.\n"+
				"   Run is NOT dead — will resume automatically. To give up, Ctrl-C.\n\n",
			sigCount, delay)
	}
	sleep(delay)
}

// RecordResult classifies one claude-call outcome and advances the
// state machine. `output` is the raw result text (we inspect it for
// the "claude error" substring); `turns` and `costUSD` are the
// tallies from the stream-json final event (0, 0 when unavailable —
// e.g. a reviewer call that doesn't parse them).
//
// Rate-limit signature: output contains "claude error" AND turns <= 1
// AND costUSD == 0. All three together — the production signature.
// We require all three to avoid false positives on normal short
// successful calls (turns>=1 but costUSD>0) or on transient errors
// that happened mid-turn (costUSD>0 even though output contains
// "claude error").
//
// Success signature: turns > 1 OR costUSD > 0 (genuine work happened).
// A success while Active resets to Normal. A success while Suspected
// also clears state — the earlier signature was likely a transient.
func (d *claudeRateLimitDetector) RecordResult(output string, turns int, costUSD float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked()

	isRateLimit := strings.Contains(output, "claude error") && turns <= 1 && costUSD == 0
	isSuccess := turns > 1 || costUSD > 0

	if isRateLimit {
		d.signatureTimes = append(d.signatureTimes, d.now())
		// Promote state based on signature count in window.
		switch d.state {
		case claudeBackoffNormal:
			if len(d.signatureTimes) >= 2 {
				d.state = claudeBackoffActive
				d.backoffStep = 0
			} else {
				d.state = claudeBackoffSuspected
			}
		case claudeBackoffSuspected:
			if len(d.signatureTimes) >= 2 {
				d.state = claudeBackoffActive
				d.backoffStep = 0
			}
		case claudeBackoffActive:
			// Still rate-limited after a backoff retry — escalate.
			d.backoffStep++
			if d.backoffStep >= len(d.backoffLadder) {
				d.backoffStep = len(d.backoffLadder) - 1
			}
		}
		return
	}

	if isSuccess {
		wasActive := d.state == claudeBackoffActive
		d.state = claudeBackoffNormal
		d.backoffStep = 0
		// Clear the rolling window on success — we're definitely not
		// rate-limited any more. Fresh errors will repopulate it.
		d.signatureTimes = nil
		if wasActive {
			fmt.Fprintf(os.Stderr,
				"\n▶ Claude responsive again (got %d turns / $%.2f). Resuming simple-loop.\n\n",
				turns, costUSD)
		}
		return
	}
	// Neither clear rate-limit nor clear success (e.g. empty output
	// with no "claude error" marker). Leave state alone — a truly
	// ambiguous call shouldn't escalate or reset.
}

// Package-level singleton, mirroring codexBackoff. Used by both
// claudeCall and claudeReviewCall sites.
var claudeBackoff = newClaudeRateLimitDetector()
