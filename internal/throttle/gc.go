package throttle

import (
	"context"
	"time"
)

// gcInterval is how often the background sweep runs. 5 minutes matches
// the spec T16 cadence — short enough to keep the map bounded for a
// daemon that runs a month, long enough to avoid spurious wakeups.
const gcInterval = 5 * time.Minute

// gcMaxAge is the threshold for a bucket to be considered stale and
// eligible for deletion. 1 hour is the spec T16 default; a shorter
// value would risk evicting a bucket that legitimately rate-limits a
// long-idle session that wakes up.
const gcMaxAge = 1 * time.Hour

// StartGC runs the stale-bucket sweep loop until ctx is cancelled.
// Callers (the daemon's startup glue) launch it as a goroutine. It
// returns when ctx is done so the daemon's shutdown path can wait on
// the goroutine to exit cleanly via the supplied done channel.
//
// Per-invocation overhead is bounded by the bucket count; in practice
// even a heavy daemon caps out at low thousands of live buckets
// (sessions × tools), so a single Range pass costs sub-millisecond.
func StartGC(ctx context.Context, l Limiter) {
	impl, ok := l.(*limiter)
	if !ok || impl == nil || impl.disabled {
		return
	}
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			impl.sweepOnce(now)
		}
	}
}

// sweepOnce performs a single GC pass. Exported via a private name
// rather than as a method on the package facade because the only
// caller is StartGC. Test code drives it directly via the
// SweepNowForTest helper (testing.go is intentionally avoided —
// SweepNowForTest is also useful as a one-shot operator hook).
func (l *limiter) sweepOnce(now time.Time) (deleted int) {
	cutoff := now.Add(-gcMaxAge).UnixNano()
	l.buckets.Range(func(k, v any) bool {
		entry, ok := v.(*bucketEntry)
		if !ok || entry == nil {
			return true
		}
		if entry.lastAccess.Load() > cutoff {
			return true
		}
		// Only evict buckets that owe nothing to the principal. If
		// Tokens() < Burst the bucket is still recovering from prior
		// usage and evicting it would silently grant the principal a
		// burst they have not earned back.
		if entry.limiter.Tokens() < float64(entry.limiter.Burst()) {
			return true
		}
		l.buckets.Delete(k)
		deleted++
		return true
	})
	return deleted
}

// SweepNowForTest exposes a single GC pass for unit tests. The
// returned count is the number of buckets evicted on this pass.
func SweepNowForTest(l Limiter, now time.Time) int {
	impl, ok := l.(*limiter)
	if !ok || impl == nil {
		return 0
	}
	return impl.sweepOnce(now)
}
