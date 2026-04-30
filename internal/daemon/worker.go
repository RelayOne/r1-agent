package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Worker is a goroutine that pulls Tasks from the Queue and runs them via
// the configured Executor. It logs lifecycle events (intent/done/blocked) to
// the daemon's WAL with proof evidence so the supervisor (or a human) can
// reconstruct what happened.
//
// Workers are started by Daemon.Start and stopped by Daemon.Stop.
type Worker struct {
	ID      string
	q       *Queue
	wal     *WAL
	exec    Executor
	pollGap time.Duration
	observe func(TaskLifecycleEvent)

	stop   chan struct{}
	done   chan struct{}
	active atomic.Bool // true while currently executing a task
}

// NewWorker constructs a worker. pollGap is how long to sleep between
// queue polls when there is no work; default 250ms.
func NewWorker(id string, q *Queue, w *WAL, exec Executor, pollGap time.Duration, observe func(TaskLifecycleEvent)) *Worker {
	if pollGap <= 0 {
		pollGap = 250 * time.Millisecond
	}
	return &Worker{
		ID:      id,
		q:       q,
		wal:     w,
		exec:    exec,
		pollGap: pollGap,
		observe: observe,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Active reports whether the worker is currently processing a task.
func (w *Worker) Active() bool { return w.active.Load() }

// Stop signals the worker to exit at the next poll boundary and blocks
// until the worker's goroutine returns. Safe to call multiple times.
func (w *Worker) Stop() {
	select {
	case <-w.stop:
		// already closed
	default:
		close(w.stop)
	}
	<-w.done
}

// Start begins the worker's poll loop. It returns immediately; the loop
// runs in its own goroutine and exits when Stop is called.
func (w *Worker) Start(ctx context.Context) {
	go w.loop(ctx)
}

func (w *Worker) loop(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		default:
		}

		t, err := w.q.Next(w.ID)
		if err != nil {
			w.logBlocked("", fmt.Sprintf("queue.Next: %v", err))
			time.Sleep(w.pollGap)
			continue
		}
		if t == nil {
			time.Sleep(w.pollGap)
			continue
		}

		w.runOne(ctx, t)
	}
}

func (w *Worker) runOne(ctx context.Context, t *Task) {
	w.active.Store(true)
	defer w.active.Store(false)

	w.logIntent(t.ID, fmt.Sprintf("starting task %q (estimate=%d bytes)", t.Title, t.EstimateBytes))
	w.observeLifecycle(t, "task.started", StateRunning, "", 0, "")

	res := w.exec.Execute(ctx, t)
	if res.Err != nil {
		_ = w.q.Fail(t.ID, res.Err.Error())
		w.logBlocked(t.ID, fmt.Sprintf("executor error: %v", res.Err))
		w.observeLifecycle(t, "task.failed", StateFailed, "", 0, res.Err.Error())
		return
	}

	if err := w.q.Complete(t.ID, res.ActualBytes, res.MissionID, res.ProofsPath); err != nil {
		w.logBlocked(t.ID, fmt.Sprintf("queue.Complete: %v", err))
		return
	}

	evidence := map[string]string{}
	if res.MissionID != "" {
		evidence["mission_id"] = res.MissionID
	}
	if res.ProofsPath != "" {
		evidence["proofs_md"] = res.ProofsPath
	}
	updated := w.q.Get(t.ID)
	if updated != nil && updated.Underdelivered {
		evidence["UNDERDELIVERED"] = fmt.Sprintf("actual=%d estimate=%d", res.ActualBytes, t.EstimateBytes)
	}

	w.logDone(t.ID, fmt.Sprintf("completed (actual=%d bytes)", res.ActualBytes), evidence)
	w.observeLifecycle(t, "task.completed", StateDone, res.ProofsPath, res.ActualBytes, "")
}

func (w *Worker) logIntent(taskID, msg string) {
	if w.wal == nil {
		return
	}
	_ = w.wal.Append(NewIntent(taskID, w.ID, msg))
}

func (w *Worker) logDone(taskID, msg string, evidence map[string]string) {
	if w.wal == nil {
		return
	}
	_ = w.wal.Append(NewDone(taskID, w.ID, msg, evidence))
}

func (w *Worker) logBlocked(taskID, reason string) {
	if w.wal == nil {
		return
	}
	_ = w.wal.Append(NewBlocked(taskID, w.ID, reason))
}

func (w *Worker) observeLifecycle(t *Task, eventType string, state TaskState, proofsPath string, actualBytes int64, errMsg string) {
	if w.observe == nil || t == nil {
		return
	}
	sessionID := ""
	if t.Meta != nil {
		sessionID = t.Meta["agent_session_id"]
	}
	w.observe(TaskLifecycleEvent{
		TS:          time.Now().UTC(),
		Type:        eventType,
		TaskID:      t.ID,
		SessionID:   sessionID,
		WorkerID:    w.ID,
		Message:     t.Title,
		State:       state,
		ProofsPath:  proofsPath,
		ActualBytes: actualBytes,
		Error:       errMsg,
	})
}

// WorkerPool manages a set of workers. The pool can be resized at runtime
// via Resize, which gracefully stops shrinking workers.
type WorkerPool struct {
	mu      sync.Mutex
	workers []*Worker
	q       *Queue
	wal     *WAL
	exec    Executor
	ctx     context.Context
	pollGap time.Duration
	nextID  int
	observe func(TaskLifecycleEvent)
}

// NewWorkerPool constructs a pool. Workers are not started until Resize.
func NewWorkerPool(ctx context.Context, q *Queue, w *WAL, exec Executor, pollGap time.Duration, observe func(TaskLifecycleEvent)) *WorkerPool {
	return &WorkerPool{
		q: q, wal: w, exec: exec, ctx: ctx, pollGap: pollGap, observe: observe,
	}
}

// Size returns the current number of workers.
func (p *WorkerPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

// ActiveCount returns the number of workers currently executing a task.
func (p *WorkerPool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, w := range p.workers {
		if w.Active() {
			n++
		}
	}
	return n
}

// Resize grows or shrinks the pool to exactly n workers. Shrinking calls
// Stop on the removed workers and blocks until they exit.
func (p *WorkerPool) Resize(n int) {
	if n < 0 {
		n = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	cur := len(p.workers)
	switch {
	case n > cur:
		for i := 0; i < n-cur; i++ {
			p.nextID++
			id := fmt.Sprintf("w-%d", p.nextID)
			worker := NewWorker(id, p.q, p.wal, p.exec, p.pollGap, p.observe)
			worker.Start(p.ctx)
			p.workers = append(p.workers, worker)
		}
	case n < cur:
		drop := p.workers[n:]
		p.workers = p.workers[:n]
		// Stop in parallel.
		stopped := make(chan struct{}, len(drop))
		for _, w := range drop {
			go func(w *Worker) {
				w.Stop()
				stopped <- struct{}{}
			}(w)
		}
		for i := 0; i < len(drop); i++ {
			<-stopped
		}
	}
}

// StopAll stops every worker.
func (p *WorkerPool) StopAll() { p.Resize(0) }

// SetExecutor swaps the executor used by NEWLY started workers. Existing
// workers keep their original executor until they are stopped.
func (p *WorkerPool) SetExecutor(exec Executor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exec = exec
}
