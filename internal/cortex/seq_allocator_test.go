package cortex

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// TestSeqAllocatorReservesZero asserts the first call to next() returns 1,
// reserving seq=0 for the synthetic session.bound event per
// specs/lanes-protocol.md §5.5.
func TestSeqAllocatorReservesZero(t *testing.T) {
	t.Parallel()
	a := newSeqAllocator(0)
	defer a.Stop()
	if got := a.next(); got != 1 {
		t.Errorf("first next() = %d, want 1 (seq=0 reserved)", got)
	}
}

// TestSeqAllocatorMonotonic spins up 1000 concurrent requesters against
// one allocator, then asserts:
//
//   1. every returned id is unique (no duplicates);
//   2. the union of returned ids equals the contiguous range 1..1000;
//   3. ordering is deterministic in the sense that the goroutine is
//      single-writer — so for any two completed requests the seq values
//      are totally ordered (no two callers got the same id).
//
// This is the spec §5.5 contract for the per-session seq allocator
// (TASK-7 of specs/lanes-protocol.md §11).
func TestSeqAllocatorMonotonic(t *testing.T) {
	t.Parallel()
	const N = 1000
	a := newSeqAllocator(0)
	defer a.Stop()

	// Each goroutine writes its allocated seq into a channel; we read N
	// values back and stop. Channel join is equivalent to a WaitGroup
	// barrier here and just as race-safe.
	resultsCh := make(chan uint64, N)
	for i := 0; i < N; i++ {
		go func() {
			resultsCh <- a.next()
		}()
	}
	results := make([]uint64, 0, N)
	for i := 0; i < N; i++ {
		select {
		case v := <-resultsCh:
			results = append(results, v)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out collecting result %d/%d", i+1, N)
		}
	}
	if len(results) != N {
		t.Fatalf("results buffer corrupted: len=%d want %d", len(results), N)
	}

	// Uniqueness.
	seen := make(map[uint64]struct{}, N)
	for i, v := range results {
		if v == 0 {
			t.Errorf("request %d got seq=0 (reserved for session.bound)", i)
		}
		if _, dup := seen[v]; dup {
			t.Errorf("duplicate seq %d at request %d", v, i)
		}
		seen[v] = struct{}{}
	}
	if len(seen) != N {
		t.Errorf("uniqueness check found %d distinct values, want %d", len(seen), N)
	}

	// Contiguity: the union of returned values is exactly 1..N.
	sorted := make([]uint64, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i, v := range sorted {
		want := uint64(i + 1)
		if v != want {
			t.Errorf("sorted[%d] = %d, want %d (gap or out-of-range)", i, v, want)
			break
		}
	}
}

// TestSeqAllocatorStop asserts Stop drains the goroutine and is
// idempotent.
func TestSeqAllocatorStop(t *testing.T) {
	t.Parallel()
	a := newSeqAllocator(0)
	if got := a.next(); got != 1 {
		t.Fatalf("first next() = %d, want 1", got)
	}
	a.Stop()

	// Idempotent.
	a.Stop()

	// done channel must be closed.
	select {
	case <-a.done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Stop did not close done channel within 1s")
	}
}

// TestWorkspaceSeqAllocatorPathThroughLanes is an end-to-end test: emit
// a thousand lane events through a Workspace and assert that every one
// carries a unique, contiguous seq starting at 1. This exercises the
// integration between lane_lifecycle.go and seq_allocator.go that
// TASK-7 wires up.
func TestWorkspaceSeqAllocatorPathThroughLanes(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)
	defer w.StopSeqAllocator()

	const N = 200
	main := w.NewMainLane(context.Background())
	if err := main.Transition(hub.LaneStatusRunning, "started", "started"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	for i := 0; i < N; i++ {
		main.EmitCost(1, 1, 0.01)
	}

	// Wait for lane.created + lane.status + N× lane.cost = N+2 events.
	want := N + 2
	got := rec.waitForEvents(t, want, 5*time.Second)
	got = got[:want]

	// Collect seqs and assert they are unique and contiguous starting at
	// 1 (seq=0 reserved per §5.5).
	seqs := make([]uint64, len(got))
	for i, e := range got {
		if e.Lane == nil {
			t.Fatalf("event[%d] has nil Lane payload", i)
		}
		seqs[i] = e.Lane.Seq
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, s := range seqs {
		want := uint64(i + 1)
		if s != want {
			t.Fatalf("seqs[%d] = %d, want %d (expected contiguous 1..%d)", i, s, want, len(seqs))
		}
	}
}
