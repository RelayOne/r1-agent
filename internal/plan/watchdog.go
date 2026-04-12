package plan

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Watchdog cancels a ctx when no progress is observed for a configured
// idle window. Different from context.WithTimeout: a Watchdog resets
// its deadline every time Pulse() is called, so long-running
// operations that stream progress (LLM calls with token deltas, task
// loops with per-tool-use events) stay alive as long as they're
// actually doing something. Only genuine silence triggers a kill.
//
// Use case: stoke sessions should not hang silently. When the
// agentloop, acceptance runner, or any other subsystem stops emitting
// progress for N minutes, the watchdog cancels the session's ctx and
// the scheduler moves on.
type Watchdog struct {
	idleWindow time.Duration
	cancel     context.CancelFunc
	lastPulse  atomic.Int64 // unix nano of last pulse
	stopped    atomic.Bool
	mu         sync.Mutex
	label      string
}

// NewWatchdog wraps ctx with a progress-based watchdog that cancels if
// Pulse() isn't called within idleWindow. Returns the derived ctx
// plus the Watchdog itself. Callers must Stop() the watchdog when
// their session exits to release goroutines.
//
// label is the short string that appears in the kill log message so
// the operator knows what died (e.g. "session S3").
//
// The watchdog goroutine is silent when healthy; it only logs when
// the idle window expires and it fires cancellation.
func NewWatchdog(parentCtx context.Context, idleWindow time.Duration, label string) (context.Context, *Watchdog) {
	ctx, cancel := context.WithCancel(parentCtx)
	w := &Watchdog{
		idleWindow: idleWindow,
		cancel:     cancel,
		label:      label,
	}
	w.lastPulse.Store(time.Now().UnixNano())
	go w.run(ctx)
	return ctx, w
}

// Pulse resets the watchdog's idle clock. Called by subsystems that
// are making progress. Thread-safe. No-op after Stop().
func (w *Watchdog) Pulse() {
	if w == nil || w.stopped.Load() {
		return
	}
	w.lastPulse.Store(time.Now().UnixNano())
}

// Stop disables the watchdog. Safe to call multiple times. After Stop
// the watchdog will never cancel the ctx. Callers should always
// defer Stop() when they create a Watchdog.
func (w *Watchdog) Stop() {
	if w == nil {
		return
	}
	w.stopped.Store(true)
}

// LastPulse returns the time of the most recent Pulse call. Zero
// value means the watchdog has never seen a pulse (or has been
// Stop'd). Used for diagnostics.
func (w *Watchdog) LastPulse() time.Time {
	if w == nil {
		return time.Time{}
	}
	return time.Unix(0, w.lastPulse.Load())
}

// run is the watchdog goroutine. Checks the idle window at 1/10th of
// its duration (so 45min idle = check every 4.5min). Cancels the
// ctx when the idle window expires.
func (w *Watchdog) run(ctx context.Context) {
	checkInterval := w.idleWindow / 10
	if checkInterval < 10*time.Second {
		checkInterval = 10 * time.Second
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.stopped.Load() {
				return
			}
			last := time.Unix(0, w.lastPulse.Load())
			idle := time.Since(last)
			if idle >= w.idleWindow {
				fmt.Fprintf(os.Stderr, "⏱  watchdog: %s has been silent for %v (>%v) — cancelling\n",
					w.label, idle.Truncate(time.Second), w.idleWindow)
				w.cancel()
				return
			}
		}
	}
}
