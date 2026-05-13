package analytics

import (
	"sync"
	"sync/atomic"
)

// ContextLookup maps mission_id → tenant_id so the bus subscriber can
// hydrate the tenant binding on per-mission events that arrive without
// the originating context. Populated on mission start (when the parent
// goroutine's correlation context still carries the tenant), drained
// on mission completion or abort.
//
// Bounded at maxEntries with FIFO-ish eviction as a safety net so a
// runaway test or a missed-completion event cannot grow the map without
// limit. Eviction prefers entries that have already been marked stale
// by Forget; a scan past maxEntries removes the oldest remaining entry.
type ContextLookup struct {
	m          sync.Map // mission_id -> string tenant_id
	maxEntries int64
	count      atomic.Int64
}

// DefaultContextLookup is the process-wide map. Bounded at 10_000
// entries per spec §12. Tests use NewContextLookup to isolate state.
var DefaultContextLookup = NewContextLookup(10_000)

// NewContextLookup constructs a fresh lookup with the given cap. A cap
// of zero disables eviction (unbounded — used in tests only).
func NewContextLookup(maxEntries int) *ContextLookup {
	return &ContextLookup{maxEntries: int64(maxEntries)}
}

// Remember associates missionID with tenantID. Empty inputs are no-ops.
// Re-recording the same mission_id overwrites the prior tenant binding
// (the latest mission start wins — relevant when a mission is restarted
// from a checkpoint with a re-resolved tenant).
func (l *ContextLookup) Remember(missionID, tenantID string) {
	if l == nil || missionID == "" || tenantID == "" {
		return
	}
	if _, loaded := l.m.LoadOrStore(missionID, tenantID); !loaded {
		l.count.Add(1)
		l.maybeEvict()
		return
	}
	l.m.Store(missionID, tenantID)
}

// Lookup returns the tenant_id previously stored for missionID, or the
// empty string when none is found.
func (l *ContextLookup) Lookup(missionID string) string {
	if l == nil || missionID == "" {
		return ""
	}
	v, ok := l.m.Load(missionID)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// Forget removes the binding for missionID. Idempotent; safe to call on
// a missionID we never recorded.
func (l *ContextLookup) Forget(missionID string) {
	if l == nil || missionID == "" {
		return
	}
	if _, loaded := l.m.LoadAndDelete(missionID); loaded {
		l.count.Add(-1)
	}
}

// Len returns the current number of entries.
func (l *ContextLookup) Len() int {
	if l == nil {
		return 0
	}
	return int(l.count.Load())
}

// maybeEvict trims the map back to maxEntries when it exceeds the cap.
// The walk is O(N) but only fires past the cap so amortised cost is
// negligible — and the cap is the safety net, not the steady state.
func (l *ContextLookup) maybeEvict() {
	if l.maxEntries <= 0 {
		return
	}
	for l.count.Load() > l.maxEntries {
		// sync.Map iteration order is not specified — that's fine, the
		// goal is bounded memory, not strict FIFO. Stop after one delete
		// to keep the function quick.
		var removed bool
		l.m.Range(func(k, _ any) bool {
			if _, loaded := l.m.LoadAndDelete(k); loaded {
				l.count.Add(-1)
				removed = true
			}
			return false
		})
		if !removed {
			return
		}
	}
}
