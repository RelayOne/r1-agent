// Package cortex — race-free Lane snapshot accessor.
//
// Lane.Clone (lane.go) is a value-receiver copy: copying the receiver
// reads every field WITHOUT the workspace mutex, racing with
// Transition / Kill / emitLaneEvent which mutate Status / EndedAt /
// LastSeq under w.mu. That race was latent while no production code
// read lanes concurrently; the tui-lanes panel activation (audit A073)
// is the first concurrent reader, so this file adds the locked
// accessor the lane.go doc comment always promised ("readers should
// grab a snapshot ... before reading from outside the workspace").
//
// Kept in its own file to stay additive/conflict-free with parallel
// lanes-protocol work; Clone remains for same-goroutine copies.
package cortex

import "sync/atomic"

// Snapshot returns a copy of the Lane taken under the workspace
// read-lock, safe to call concurrently with Transition / Kill /
// SetPinned / event emission. The deltaSeq counter is loaded
// atomically because EmitDelta increments it without the workspace
// mutex.
//
// Lanes constructed without a workspace back-pointer (tests, direct
// struct literals inside the package) fall back to a plain copy — no
// Workspace method can mutate them concurrently.
func (l *Lane) Snapshot() Lane {
	if l == nil {
		return Lane{}
	}
	w := l.ws
	if w == nil {
		c := *l
		return c
	}
	w.mu.RLock()
	c := Lane{
		ID:        l.ID,
		Kind:      l.Kind,
		ParentID:  l.ParentID,
		Label:     l.Label,
		Status:    l.Status,
		Pinned:    l.Pinned,
		StartedAt: l.StartedAt,
		EndedAt:   l.EndedAt,
		LastSeq:   l.LastSeq,
		ws:        l.ws,
	}
	w.mu.RUnlock()
	c.deltaSeq = atomic.LoadUint64(&l.deltaSeq)
	return c
}
