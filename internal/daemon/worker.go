package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/failure"
)

// Worker is a goroutine that pulls Tasks from the Queue and runs them via
// the configured Executor. It logs lifecycle events (intent/done/blocked) to
// the daemon's WAL with proof evidence so the supervisor (or a human) can
// reconstruct what happened.
//
// Workers are started by Daemon.Start and stopped by Daemon.Stop.
type Worker struct {
	ID       string
	q        *Queue
	wal      *WAL
	exec     Executor
	pollGap  time.Duration
	observe  func(TaskLifecycleEvent)
	throttle func() time.Duration

	stop   chan struct{}
	done   chan struct{}
	active atomic.Bool // true while currently executing a task
}

// NewWorker constructs a worker. pollGap is how long to sleep between
// queue polls when there is no work; default 250ms.
func NewWorker(id string, q *Queue, w *WAL, exec Executor, pollGap time.Duration, observe func(TaskLifecycleEvent), throttle func() time.Duration) *Worker {
	if pollGap <= 0 {
		pollGap = 250 * time.Millisecond
	}
	return &Worker{
		ID:       id,
		q:        q,
		wal:      w,
		exec:     exec,
		pollGap:  pollGap,
		observe:  observe,
		throttle: throttle,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
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

		if w.throttle != nil {
			if delay := w.throttle(); delay > 0 {
				time.Sleep(delay)
			}
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

	if t.Meta == nil {
		t.Meta = map[string]string{}
	}
	if t.ResumeCheckpoint != "" {
		t.Meta["resume_checkpoint"] = t.ResumeCheckpoint
	}
	w.logEvent(WALEvent{
		Type:     "start",
		TaskID:   t.ID,
		WorkerID: w.ID,
		Message:  fmt.Sprintf("starting task %q attempt %d/%d", t.Title, t.Attempts, t.MaxAttempts),
		Evidence: lifecycleEvidence(t, map[string]string{"executor": w.exec.Type(), "runner": NormalizeRunner(t.Runner)}),
	})
	w.observeLifecycle(t, "task.started", StateRunning, "", 0, "")

	if !SupportsRunner(w.exec, t.Runner) {
		errMsg := fmt.Sprintf("executor %q does not support runner %q", w.exec.Type(), NormalizeRunner(t.Runner))
		_ = w.q.Fail(t.ID, errMsg)
		w.logBlocked(t.ID, errMsg)
		w.observeLifecycle(t, "task.failed", StateFailed, "", 0, errMsg)
		return
	}

	res := w.exec.Execute(ctx, t)
	if res.Err != nil {
		if delay, retry, class, reason := classifyRetry(t.Attempts, res.Err); retry && t.Attempts < t.MaxAttempts {
			resumeCheckpoint := checkpointForRetry(t, class)
			nextRetry := time.Now().UTC().Add(delay)
			_ = w.q.Retry(t.ID, res.Err.Error(), class, resumeCheckpoint, nextRetry)
			w.logEvent(WALEvent{
				Type:     "retry",
				TaskID:   t.ID,
				WorkerID: w.ID,
				Message:  reason,
				Evidence: map[string]string{"attempt": fmt.Sprintf("%d", t.Attempts), "retry_after": delay.String(), "resume_checkpoint": resumeCheckpoint, "error_class": class},
			})
			w.observeLifecycle(t, "task.retrying", StateQueued, "", 0, res.Err.Error())
			return
		}
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

func (w *Worker) logEvent(ev WALEvent) {
	if w.wal == nil {
		return
	}
	_ = w.wal.Append(ev)
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
	cur := len(p.workers)
	var drop []*Worker
	switch {
	case n > cur:
		for i := 0; i < n-cur; i++ {
			p.nextID++
			id := fmt.Sprintf("w-%d", p.nextID)
			worker := NewWorker(id, p.q, p.wal, p.exec, p.pollGap, p.observe, p.dispatchDelay)
			worker.Start(p.ctx)
			p.workers = append(p.workers, worker)
		}
	case n < cur:
		drop = append(drop, p.workers[n:]...)
		p.workers = p.workers[:n]
	}
	p.mu.Unlock()
	if len(drop) == 0 {
		return
	}
	// Stop in parallel after releasing the pool lock so worker-side
	// telemetry reads cannot deadlock against resize/shutdown.
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

// StopAll stops every worker.
func (p *WorkerPool) StopAll() { p.Resize(0) }

// SetExecutor swaps the executor used by NEWLY started workers. Existing
// workers keep their original executor until they are stopped.
func (p *WorkerPool) SetExecutor(exec Executor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exec = exec
}

func (p *WorkerPool) dispatchDelay() time.Duration {
	snapshot := failure.BackpressureSnapshot{
		Active:   p.ActiveCount(),
		Capacity: p.Size(),
		Queued:   p.q.ReadyQueuedCount(),
	}
	return failure.ComputeBackpressure(snapshot).Delay
}

func classifyRetry(attempt int, err error) (time.Duration, bool, string, string) {
	mirror := failure.MirrorLagClassifier{}
	delay, ok := mirror.ShouldRetry(attempt)
	if mirror.Detect(err) && ok {
		return delay, true, "mirror_lag", "transient source mirror lag"
	}
	decision := failure.ClassifyAPIFailure(err, attempt)
	if decision.Retryable {
		return decision.Backoff, true, string(decision.Class), decision.Reason
	}
	return 0, false, "", ""
}

func checkpointForRetry(t *Task, class string) string {
	if t.ResumeCheckpoint != "" {
		return t.ResumeCheckpoint
	}
	if class == "" {
		return "retry task"
	}
	return "retry after " + class
}

func lifecycleEvidence(t *Task, extra map[string]string) map[string]string {
	evidence := map[string]string{
		"attempt": fmt.Sprintf("%d", t.Attempts),
	}
	if t.IdempotencyKey != "" {
		evidence["idempotency_key"] = t.IdempotencyKey
	}
	if t.ResumeCheckpoint != "" {
		evidence["resume_checkpoint"] = t.ResumeCheckpoint
	}
	for k, v := range extra {
		evidence[k] = v
	}
	return evidence
}
