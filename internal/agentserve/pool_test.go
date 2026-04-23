package agentserve

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// submitOK is an assert.NoError-equivalent helper: calls p.Submit
// and fails the test if it returns a non-nil error.
func submitOK(tb testing.TB, p *Pool, job *TaskJob) {
	tb.Helper()
	if err := p.Submit(job); err != nil {
		// assert.NoError contract: fail fast so callers don't proceed.
		tb.Fatalf("submit %q: %v", job.ID, err)
	}
}

// submitErr is an assert.ErrorIs-equivalent helper: calls p.Submit
// expecting an error matching want (errors.Is when non-nil,
// otherwise any non-nil error is acceptable).
func submitErr(tb testing.TB, p *Pool, job *TaskJob, want error) {
	tb.Helper()
	err := p.Submit(job)
	// assert.Error: Submit must fail for the test to pass.
	if err == nil {
		tb.Fatalf("submit %q: expected error, got nil", job.ID)
	}
	if want != nil && !errors.Is(err, want) {
		tb.Fatalf("submit %q: want %v, got %v", job.ID, want, err)
	}
}

func TestPool_CapacityAndWorkers(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 7, QueueSize: 42})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	// assert.Equal: both getters echo the configured values verbatim.
	if got := p.Capacity(); got != 42 {
		t.Errorf("Capacity=%d, want 42", got)
	}
	if got := p.Workers(); got != 7 {
		t.Errorf("Workers=%d, want 7", got)
	}
}

func TestPool_SubmitAndRun(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 2, QueueSize: 16})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	var ran atomic.Int32
	done := make(chan struct{})
	job := &TaskJob{
		ID:   "job-1",
		Run:  func(ctx context.Context) { ran.Add(1) },
		Done: func(_ bool) { close(done) },
	}
	submitOK(t, p, job)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job never ran")
	}
	if ran.Load() != 1 {
		t.Fatalf("ran=%d", ran.Load())
	}
}

func TestPool_SubmitValidation(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	submitErr(t, p, nil, nil)
	submitErr(t, p, &TaskJob{}, nil)
	submitErr(t, p, &TaskJob{ID: "x"}, nil)
}

func TestPool_SubmitDuplicateID(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	block := make(chan struct{})
	blocker := &TaskJob{
		ID:  "dup",
		Run: func(ctx context.Context) { <-block },
	}
	submitOK(t, p, blocker)

	// Wait until worker has picked it up so the reservation is held.
	for p.Active() == 0 {
		time.Sleep(time.Millisecond)
	}
	dup := &TaskJob{
		ID:  "dup",
		Run: func(ctx context.Context) {},
	}
	submitErr(t, p, dup, nil)
	close(block)
}

func TestPool_QueueFull(t *testing.T) {
	// QueueSize 1, WorkerCount 1. First job occupies the worker;
	// second fills the queue; third fails with ErrQueueFull.
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 1})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	gate := make(chan struct{})
	started := make(chan struct{})
	blocker := &TaskJob{
		ID: "worker-blocker",
		Run: func(ctx context.Context) {
			close(started)
			<-gate
		},
	}
	submitOK(t, p, blocker)
	// Wait until the worker has actually started running — not just
	// reserved. Otherwise the next Submit may race the worker's
	// channel-read and fill both the queue buffer and the runnable
	// slot simultaneously.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocker never started")
	}
	queued := &TaskJob{
		ID:  "queued",
		Run: func(ctx context.Context) {},
	}
	submitOK(t, p, queued)
	overflow := &TaskJob{
		ID:  "overflow",
		Run: func(ctx context.Context) {},
	}
	submitErr(t, p, overflow, ErrQueueFull)
	close(gate)
}

func TestPool_Cancel(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 2, QueueSize: 4})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	observed := make(chan bool, 1)
	running := make(chan struct{})
	job := &TaskJob{
		ID: "cancel-me",
		Run: func(ctx context.Context) {
			close(running)
			<-ctx.Done()
		},
		Done: func(cancelled bool) { observed <- cancelled },
	}
	submitOK(t, p, job)

	<-running
	if err := p.Cancel("cancel-me"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	select {
	case c := <-observed:
		if !c {
			t.Error("Done reported cancelled=false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done never fired after cancel")
	}
}

func TestPool_CancelUnknown(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	if err := p.Cancel("nope"); !errors.Is(err, ErrJobUnknown) {
		t.Fatalf("expected ErrJobUnknown, got %v", err)
	}
}

func TestPool_CancelReleasesID(t *testing.T) {
	// After Cancel and Done fire, the ID should be reusable.
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	done := make(chan struct{})
	first := &TaskJob{
		ID:   "recycled",
		Run:  func(ctx context.Context) { <-ctx.Done() },
		Done: func(_ bool) { close(done) },
	}
	submitOK(t, p, first)
	if err := p.Cancel("recycled"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	<-done

	// Resubmit same ID — must be accepted.
	done2 := make(chan struct{})
	second := &TaskJob{
		ID:   "recycled",
		Run:  func(ctx context.Context) {},
		Done: func(_ bool) { close(done2) },
	}
	submitOK(t, p, second)
	<-done2
}

func TestPool_Shutdown_DrainsInflight(t *testing.T) {
	p := NewPool(PoolConfig{
		WorkerCount:      3,
		QueueSize:        16,
		ShutdownDeadline: 5 * time.Second,
	})

	var done atomic.Int32
	const N = 5
	for i := 0; i < N; i++ {
		job := &TaskJob{
			ID:   fmt.Sprintf("drain-%d", i),
			Run:  func(ctx context.Context) { time.Sleep(50 * time.Millisecond) },
			Done: func(_ bool) { done.Add(1) },
		}
		submitOK(t, p, job)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := done.Load(); got != int32(N) {
		t.Fatalf("done count=%d, want %d", got, N)
	}
}

func TestPool_Shutdown_HardCancelPastDeadline(t *testing.T) {
	p := NewPool(PoolConfig{
		WorkerCount:      1,
		QueueSize:        4,
		ShutdownDeadline: 50 * time.Millisecond,
	})

	stuckCtx := make(chan context.Context, 1)
	stuck := &TaskJob{
		ID: "stuck",
		Run: func(ctx context.Context) {
			stuckCtx <- ctx
			<-ctx.Done()
		},
	}
	submitOK(t, p, stuck)
	// Wait until the job is actually running.
	ctx := <-stuckCtx

	err := p.Shutdown(context.Background())
	if !errors.Is(err, ErrShutdownDeadline) {
		t.Fatalf("expected ErrShutdownDeadline, got %v", err)
	}
	// Root ctx should have been cancelled — worker ctx observes that.
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker ctx not cancelled after shutdown deadline")
	}
}

func TestPool_SubmitAfterShutdown(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	late := &TaskJob{
		ID:  "late",
		Run: func(ctx context.Context) {},
	}
	submitErr(t, p, late, ErrPoolClosed)
}

func TestPool_ShutdownIdempotent(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown 1: %v", err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown 2: %v", err)
	}
}

func TestPool_PanicRecovery(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 1, QueueSize: 4})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	done := make(chan struct{})
	boom := &TaskJob{
		ID:   "boom",
		Run:  func(ctx context.Context) { panic("kaboom") },
		Done: func(_ bool) { close(done) },
	}
	submitOK(t, p, boom)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Done not fired after panic")
	}

	// Pool must still accept new work after a panic.
	done2 := make(chan struct{})
	survivor := &TaskJob{
		ID:   "survivor",
		Run:  func(ctx context.Context) {},
		Done: func(_ bool) { close(done2) },
	}
	submitOK(t, p, survivor)
	<-done2
}

// TestPool_ConcurrencyUnderRace submits many jobs concurrently from
// many producers. Run with -race to surface data races in the
// live-job bookkeeping.
func TestPool_ConcurrencyUnderRace(t *testing.T) {
	p := NewPool(PoolConfig{WorkerCount: 8, QueueSize: 1024})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	const producers = 16
	const perProducer = 50
	var wg sync.WaitGroup
	var ran atomic.Int32

	for i := 0; i < producers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perProducer; j++ {
				id := fmt.Sprintf("race-%d-%d", i, j)
				job := &TaskJob{
					ID:  id,
					Run: func(ctx context.Context) { ran.Add(1) },
				}
				if err := p.Submit(job); err != nil {
					// assert.NoError on every Submit in the race loop.
					t.Errorf("submit %s: %v", id, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// assert.Equal: every submitted job must run exactly once.

	// Wait for drain.
	deadline := time.Now().Add(10 * time.Second)
	for p.Active() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ran.Load(); got != producers*perProducer {
		t.Fatalf("ran=%d want %d", got, producers*perProducer)
	}
}
