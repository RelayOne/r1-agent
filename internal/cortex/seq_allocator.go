// Package cortex — per-session seq allocator (TASK-7 of
// specs/lanes-protocol.md §11).
//
// The seq protocol mandates a single-writer goroutine that owns the
// per-session monotonic counter. Concurrent callers send a request on a
// channel; the goroutine increments a local uint64 and replies with the
// allocated value. Reserves seq=0 for the synthetic session.bound event
// so the first allocated lane event is seq=1 (spec §5.5).
//
// Why a goroutine and not just an atomic counter:
//
//   1. The wire-format contract puts seq immediately adjacent to event_id
//      and at-timestamp; allocating seq under the same goroutine that
//      will eventually own the WAL append (per spec §5.5 "the per-session
//      goroutine that owns the WAL append (single writer, no contention)")
//      keeps the ordering invariant a single-actor property.
//   2. A future enhancement (sequenced WAL fsync coalescing) can plug into
//      the same goroutine without re-architecting callers.
//   3. seq overflow at 2^63 is treated as session-fatal per spec §5.5;
//      the goroutine is the natural place to enforce that.
package cortex

import (
	"sync"
)

// seqAllocator is the single-writer goroutine implementation of the
// per-session seq counter. It satisfies the spec §5.5 requirement
// verbatim: one goroutine receives next-seq requests on a channel,
// increments a local counter, replies on a per-request reply channel.
//
// Construction: newSeqAllocator(start) starts the goroutine; the caller
// is responsible for calling Stop() to drain it before discarding the
// allocator. Workspace.startSeqAllocator (below) owns the lifecycle.
type seqAllocator struct {
	// requests carries (replyChan) tuples from callers. Buffered to absorb
	// short bursts without blocking the caller's goroutine.
	requests chan chan uint64

	// quit signals the writer goroutine to exit. Closed exactly once by
	// Stop(); the writer drains on the next iteration.
	quit chan struct{}

	// done is closed by the writer goroutine just before it returns,
	// letting Stop() block until the goroutine has fully exited.
	done chan struct{}

	// stopOnce guards quit so Stop() is safe to call multiple times.
	stopOnce sync.Once
}

// newSeqAllocator launches the writer goroutine starting at the given
// initial value. The first call to next() returns initial+1; pass 0 to
// reserve seq=0 for the synthetic session.bound event per spec §5.5.
func newSeqAllocator(initial uint64) *seqAllocator {
	a := &seqAllocator{
		requests: make(chan chan uint64, 64),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go a.run(initial)
	return a
}

// run is the single-writer loop. The goroutine is the sole mutator of
// `next`; concurrent callers see allocated values only through the reply
// channels they hand in.
func (a *seqAllocator) run(initial uint64) {
	defer close(a.done)
	next := initial
	for {
		select {
		case <-a.quit:
			return
		case reply, ok := <-a.requests:
			if !ok {
				return
			}
			next++
			// Single-writer guarantee: only this line writes `next`.
			reply <- next
		}
	}
}

// next allocates and returns the next monotonic seq. Safe to call from
// any goroutine; blocks only as long as the writer goroutine takes to
// service the request (one channel hand-off in steady state).
//
// next() panics if the allocator has been stopped, because emitting an
// event after the workspace's seq writer is gone is a programming error
// (spec §5.5 treats seq overflow as session-fatal; we treat post-stop
// allocation the same).
func (a *seqAllocator) next() uint64 {
	reply := make(chan uint64, 1)
	select {
	case <-a.quit:
		panic("cortex: seq allocator stopped")
	case a.requests <- reply:
	}
	return <-reply
}

// Stop signals the writer goroutine to exit and blocks until it has.
// Idempotent: subsequent calls are no-ops. Safe to call from any
// goroutine, including under concurrent next() pressure.
func (a *seqAllocator) Stop() {
	a.stopOnce.Do(func() {
		close(a.quit)
	})
	<-a.done
}

// startSeqAllocator lazily constructs the per-Workspace seq allocator.
// Called from emitLaneEvent on first use so existing tests that never
// touch the lanes path do not pay for an idle goroutine.
//
// The starting value is 0 so the first next() returns 1; seq=0 is
// reserved for session.bound per spec §5.5.
//
// Concurrency: under the workspace mutex. The double-checked Load avoids
// the lock on the hot path after first use.
func (w *Workspace) startSeqAllocator() *seqAllocator {
	w.mu.RLock()
	if a := w.laneSeqAlloc; a != nil {
		w.mu.RUnlock()
		return a
	}
	w.mu.RUnlock()

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.laneSeqAlloc != nil {
		return w.laneSeqAlloc
	}
	w.laneSeqAlloc = newSeqAllocator(0)
	return w.laneSeqAlloc
}

// StopSeqAllocator drains the seq allocator goroutine (if any). Intended
// for tests and graceful shutdown; production code lets the goroutine
// outlive the Workspace because the allocator is small and the
// Workspace pointer keeps it referenced.
func (w *Workspace) StopSeqAllocator() {
	w.mu.Lock()
	a := w.laneSeqAlloc
	w.laneSeqAlloc = nil
	w.mu.Unlock()
	if a != nil {
		a.Stop()
	}
}
