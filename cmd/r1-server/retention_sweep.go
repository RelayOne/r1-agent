// Package main — retention_sweep.go
//
// work-stoke T10: hourly retention sweep goroutine.
//
// specs/retention-policies.md §6 / §7 calls for r1-server to spawn a
// background goroutine that runs retention.EnforceSweep on a
// time.NewTicker(time.Hour). This file owns that goroutine. The real
// sweep work lives in internal/retention/enforce.go — this file is
// purely lifecycle glue (ticker, ctx.Done, single-line per-pass log).
//
// Design notes:
//
//   - interval is injectable so the companion test can drive a tight
//     tick without sleeping for an hour. Production wiring in main.go
//     pins it to time.Hour per the spec.
//   - bus is *membus.Bus. r1-server today is a read-only observation
//     daemon and does not own a membus handle, so main.go passes nil.
//     retention.EnforceSweep special-cases a nil bus by skipping the
//     TTL-memory sweep and running only the stream + checkpoint file
//     sweeps, which is the correct best-effort behavior for a
//     per-machine r1-server instance.
//   - Errors from EnforceSweep are logged but never halt the loop. A
//     transient SQLite lock or a missing .stoke/streams directory on
//     a freshly-cloned repo must not wedge the sweep goroutine.
//   - Sweep counts are logged as one slog.Info line per pass with a
//     fixed field set, matching the "one line per sweep with counts
//     per surface" requirement in the task brief.

package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/memory/membus"
	"github.com/RelayOne/r1/internal/retention"
)

// startRetentionSweep spawns a goroutine that calls
// retention.EnforceSweep every interval until ctx is cancelled. It
// returns a *sync.WaitGroup so the main shutdown path can drain the
// goroutine in lockstep with the HTTP server (mirrors the Scanner
// pattern in this package).
//
// The first sweep fires immediately on start so operators get a
// retention audit at boot rather than waiting a full hour for the
// first tick. Subsequent sweeps then run on the regular cadence.
func startRetentionSweep(
	ctx context.Context,
	logger *slog.Logger,
	interval time.Duration,
	policy retention.Policy,
	bus *membus.Bus,
) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSweep(ctx, logger, policy, bus)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runSweep(ctx, logger, policy, bus)
			}
		}
	}()
	return &wg
}

// runSweep executes a single retention.EnforceSweep pass and logs the
// per-surface counts on a single line. Split out from the goroutine
// body so the test harness can invoke it directly and the ticker loop
// stays trivial.
func runSweep(
	ctx context.Context,
	logger *slog.Logger,
	policy retention.Policy,
	bus *membus.Bus,
) {
	start := time.Now()
	res, err := retention.EnforceSweep(ctx, policy, bus)
	dur := time.Since(start)
	if err != nil {
		// Errors are aggregated inside EnforceSweep; log but keep
		// running. SweepResult counters reflect partial progress and
		// are worth logging even on failure so operators can see what
		// made it through.
		logger.Warn("retention sweep",
			"err", err,
			"session_expired", res.SessionExpired,
			"stream_files_removed", res.StreamFilesRemoved,
			"checkpoints_removed", res.CheckpointsRemoved,
			"dur_ms", dur.Milliseconds(),
		)
		return
	}
	logger.Info("retention sweep",
		"session_expired", res.SessionExpired,
		"stream_files_removed", res.StreamFilesRemoved,
		"checkpoints_removed", res.CheckpointsRemoved,
		"dur_ms", dur.Milliseconds(),
	)
}
