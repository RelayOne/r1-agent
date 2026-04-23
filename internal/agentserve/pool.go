package agentserve

// Bounded worker pool primitive for async task execution — the
// async-capable half of spec 19 (agent-serve-async). This file is
// deliberately scoped to the worker-pool + cancel primitive; SSE,
// webhooks, and eventlog persistence stay as follow-up work.
//
// The Pool runs `WorkerCount` goroutines that drain a buffered job
// channel. Callers submit `*TaskJob` values via Submit; Pool is
// transport-agnostic so the HTTP handler in server.go can wire in
// later without this file importing net/http. Each accepted job gets
// a cancellable context tracked by ID so `POST /api/task/{id}/cancel`
// can surface here via Pool.Cancel.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// ErrQueueFull is returned by Pool.Submit when the job queue is at
// capacity. HTTP handlers translate this to 503 + Retry-After: 5.
var ErrQueueFull = errors.New("agentserve: worker pool queue full")

// ErrShutdownDeadline is returned by Pool.Shutdown when in-flight
// workers did not exit within Config.ShutdownDeadline. The root
// context is cancelled before the error returns so stragglers
// observe ctx.Done() and eventually exit.
var ErrShutdownDeadline = errors.New("agentserve: shutdown deadline exceeded")

// ErrPoolClosed is returned by Pool.Submit after Shutdown has been
// called. Callers should treat this as a permanent failure for the
// lifetime of the process.
var ErrPoolClosed = errors.New("agentserve: pool is closed")

// ErrJobUnknown is returned by Pool.Cancel when the requested job ID
// is not present in the live-job map (never submitted, already
// completed, or already cancelled). Handlers translate this to 404.
var ErrJobUnknown = errors.New("agentserve: job id unknown")

// RunFunc is the work each job does. It returns once the job
// terminates or ctx is cancelled. Pool never interprets the returned
// error — Done is called unconditionally once RunFunc returns so
// callers can thread their own bookkeeping through the closure.
type RunFunc func(ctx context.Context)

// TaskJob is the unit of work submitted to the Pool. ID must be
// unique per live job; once Done fires the ID is released and can be
// reused. RunFunc is invoked inside a worker goroutine with a child
// context that is cancelled if either Cancel(id) is called or the
// pool shuts down.
type TaskJob struct {
	// ID is the external identifier used to address the job (e.g.
	// the TaskState.ID from server.go). Required; empty IDs are
	// rejected by Submit.
	ID string

	// Run is the work payload. Must not be nil.
	Run RunFunc

	// Done fires exactly once after Run returns, regardless of
	// whether Run exited cleanly, panicked, or was cancelled. The
	// `cancelled` argument reports whether the job's context was
	// cancelled before Run returned (either via Pool.Cancel or
	// shutdown). Optional — nil is allowed.
	Done func(cancelled bool)
}

// PoolConfig configures a Pool. Zero values get sensible defaults in
// NewPool.
type PoolConfig struct {
	// WorkerCount is the number of concurrent worker goroutines.
	// Zero or negative falls back to runtime.NumCPU().
	WorkerCount int

	// QueueSize is the buffered-channel capacity for submitted jobs.
	// Zero falls back to 1000 (matches spec §3).
	QueueSize int

	// ShutdownDeadline bounds Pool.Shutdown. Zero falls back to 30s.
	ShutdownDeadline time.Duration
}

// Pool is a bounded worker pool. Safe for concurrent use.
type Pool struct {
	cfg PoolConfig

	queue    chan *poolTask
	rootCtx  context.Context
	rootStop context.CancelFunc

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // job ID -> cancel
	closed  bool                          // Shutdown called

	wg sync.WaitGroup // workers
}

// NewPool constructs a Pool and spawns WorkerCount worker
// goroutines. Call Pool.Shutdown on process exit to drain cleanly.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = runtime.NumCPU()
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1000
	}
	if cfg.ShutdownDeadline <= 0 {
		cfg.ShutdownDeadline = 30 * time.Second
	}
	rootCtx, rootStop := context.WithCancel(context.Background())
	p := &Pool{
		cfg:      cfg,
		queue:    make(chan *poolTask, cfg.QueueSize),
		rootCtx:  rootCtx,
		rootStop: rootStop,
		cancels:  map[string]context.CancelFunc{},
	}
	for i := 0; i < cfg.WorkerCount; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

// Submit enqueues a job. Returns ErrQueueFull if the buffer is
// saturated, ErrPoolClosed after Shutdown, or a validation error for
// missing ID / Run. Non-blocking — safe to call from an HTTP
// handler without risking a socket hang.
func (p *Pool) Submit(job *TaskJob) error {
	if job == nil {
		return errors.New("agentserve: nil job")
	}
	if job.ID == "" {
		return errors.New("agentserve: job id required")
	}
	if job.Run == nil {
		return errors.New("agentserve: job Run required")
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPoolClosed
	}
	if _, dup := p.cancels[job.ID]; dup {
		p.mu.Unlock()
		return fmt.Errorf("agentserve: job %q already live", job.ID)
	}
	// Reserve the slot before enqueue so a racing Cancel observes
	// the ID even before a worker picks it up.
	ctx, cancel := context.WithCancel(p.rootCtx)
	p.cancels[job.ID] = cancel
	p.mu.Unlock()

	task := &poolTask{job: job, ctx: ctx}
	select {
	case p.queue <- task:
		return nil
	default:
		// Queue full — release the reservation.
		p.mu.Lock()
		delete(p.cancels, job.ID)
		p.mu.Unlock()
		cancel()
		return ErrQueueFull
	}
}

// Cancel signals the context for the job with the given ID. Returns
// ErrJobUnknown if no live job matches. Safe to call concurrently
// with Submit / Shutdown. The job's RunFunc is expected to honor
// ctx.Done(); Cancel does not force-kill goroutines.
func (p *Pool) Cancel(id string) error {
	p.mu.Lock()
	cancel, ok := p.cancels[id]
	p.mu.Unlock()
	if !ok {
		return ErrJobUnknown
	}
	cancel()
	return nil
}

// Active reports the number of jobs currently reserved — queued or
// running. Useful for health endpoints and tests.
func (p *Pool) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cancels)
}

// Capacity reports the configured queue capacity. Pair with Active
// to expose a utilization metric on the /health endpoint without
// leaking internals.
func (p *Pool) Capacity() int { return p.cfg.QueueSize }

// Workers reports the configured worker-goroutine count. Useful for
// operators validating their deployment config.
func (p *Pool) Workers() int { return p.cfg.WorkerCount }

// Shutdown stops accepting new jobs, waits up to
// cfg.ShutdownDeadline for in-flight workers to exit, then cancels
// the root context. Returns ErrShutdownDeadline if any worker was
// still running at deadline; nil on clean drain. Idempotent — a
// second call returns nil immediately.
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	close(p.queue)

	deadline := p.cfg.ShutdownDeadline
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining < deadline {
			deadline = remaining
		}
	}
	if deadline < 0 {
		deadline = 0
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	select {
	case <-done:
		p.rootStop()
		return nil
	case <-timer.C:
		p.rootStop()
		// Give stragglers a brief grace period to unwind once
		// their ctx is cancelled, but do not block forever.
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
		return ErrShutdownDeadline
	case <-ctx.Done():
		p.rootStop()
		return ctx.Err()
	}
}

// worker drains the job queue until it closes. Each job runs inside
// a recover block so a panic in user code cannot crash the pool.
func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.queue {
		p.runOne(task)
	}
}

func (p *Pool) runOne(t *poolTask) {
	cancelled := false
	defer func() {
		// Reap the cancel func before invoking Done so observers
		// that call Active() from inside Done see the post-completion
		// count.
		p.mu.Lock()
		if cancel, ok := p.cancels[t.job.ID]; ok {
			delete(p.cancels, t.job.ID)
			cancel() // release any resources held by the cancel func
		}
		p.mu.Unlock()

		if r := recover(); r != nil {
			// Swallow — spec §3 says mark task failed with
			// "panic: <msg>", but that wiring lives on the Server
			// side. Pool just reports cancellation semantics.
			_ = r
		}
		if t.job.Done != nil {
			t.job.Done(cancelled || t.ctx.Err() != nil)
		}
	}()

	// If cancel already fired before we picked up the job, short-
	// circuit — do not invoke Run.
	if t.ctx.Err() != nil {
		cancelled = true
		return
	}
	t.job.Run(t.ctx)
	if t.ctx.Err() != nil {
		cancelled = true
	}
}

// poolTask is the internal envelope shipped across the queue. Keeps
// the per-job context alongside the job so workers do not touch
// pool.mu on the hot path.
type poolTask struct {
	job *TaskJob
	ctx context.Context
}
