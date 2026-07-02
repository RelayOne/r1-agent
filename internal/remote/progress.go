package remote

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// HubProgress bridges task lifecycle and model-cost events on the shared
// hub.Bus into SessionReporter.Update progress snapshots, so the Ember
// dashboard shows live per-task phase/cost/duration between session
// registration and completion instead of a silent gap.
//
// Design (mirrors the non-blocking observer pattern of
// internal/hub/builtin/analytics_subscriber.go):
//
//   - The bus handler runs in ModeObserve and stays cheap: it mutates a
//     mutex-guarded task map and does a non-blocking send on a bounded
//     signal channel. It never blocks a bus delivery goroutine.
//   - A single drainer goroutine performs the HTTP pushes (the
//     reporter's client has a 10s timeout), throttled to at most one
//     Update per interval; bursts of events coalesce into one snapshot.
//   - Stop halts the drainer and performs one final synchronous flush
//     so the latest snapshot lands before SessionReporter.Complete.
//
// Push errors are intentionally dropped: dashboard visibility is
// best-effort and must never fail or slow a build. Any event delivered
// after Stop's flush is superseded by the Complete call that follows.
type HubProgress struct {
	reporter *SessionReporter
	planID   string
	interval time.Duration

	mu    sync.Mutex
	tasks map[string]*taskEntry

	kick     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// taskEntry is the mutable per-task state behind a TaskProgress snapshot.
type taskEntry struct {
	prog     TaskProgress
	started  time.Time
	terminal bool // completed/failed seen: state is frozen
}

// NewHubProgress creates the bridge and starts its drainer goroutine.
// Returns nil when reporter is nil (no EMBER_API_KEY configured) so the
// caller can gate all wiring on a single nil check — every method is
// nil-receiver-safe.
func NewHubProgress(reporter *SessionReporter, planID string) *HubProgress {
	return newHubProgress(reporter, planID, time.Second)
}

// newHubProgress is the interval-injectable constructor used by tests.
func newHubProgress(reporter *SessionReporter, planID string, interval time.Duration) *HubProgress {
	if reporter == nil {
		return nil
	}
	if interval <= 0 {
		interval = time.Second
	}
	p := &HubProgress{
		reporter: reporter,
		planID:   planID,
		interval: interval,
		tasks:    make(map[string]*taskEntry),
		kick:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

// Register attaches the bridge to the bus as a pure observer. Nil-safe.
func (p *HubProgress) Register(bus *hub.Bus) {
	if p == nil || bus == nil {
		return
	}
	bus.Register(hub.Subscriber{
		ID: "remote.session_progress",
		Events: []hub.EventType{
			hub.EventTaskStarted,
			hub.EventTaskRetrying,
			hub.EventTaskCompleted,
			hub.EventTaskFailed,
			hub.EventGitWorktreeCreated, // first per-task signal the workflow emits
			hub.EventMissionPlanDone,    // plan-phase cost
			hub.EventModelPostCall,      // execute-phase cost per attempt
		},
		Mode:     hub.ModeObserve,
		Priority: 9200, // after cost tracker (9000) and analytics (9100): off the critical path
		Handler:  p.handle,
	})
}

// handle runs on a bus observe goroutine — keep it cheap: fold the event
// into the task map, wake the drainer, return.
func (p *HubProgress) handle(_ context.Context, ev *hub.Event) *hub.HookResponse {
	if p == nil || ev == nil || ev.TaskID == "" {
		return nil
	}

	p.mu.Lock()
	e := p.tasks[ev.TaskID]
	if e == nil {
		e = &taskEntry{
			prog:    TaskProgress{TaskID: ev.TaskID, Phase: "running"},
			started: time.Now(),
		}
		p.tasks[ev.TaskID] = e
	}

	switch ev.Type {
	case hub.EventTaskCompleted, hub.EventTaskFailed:
		if ev.Type == hub.EventTaskCompleted {
			e.prog.Phase = "completed"
		} else {
			e.prog.Phase = "failed"
		}
		if !e.terminal {
			e.terminal = true
			e.prog.DurationMs = time.Since(e.started).Milliseconds()
		}
		// The workflow's terminal event carries the task's authoritative
		// total cost (Model.CostUSD = result.TotalCostUSD): replace the
		// per-call accumulation instead of double-counting it.
		if m := ev.Model; m != nil && m.CostUSD > 0 {
			e.prog.CostUSD = m.CostUSD
		}
		if w := workerFrom(ev.Model); w != "" && e.prog.Worker == "" {
			e.prog.Worker = w
		}
	default:
		if e.terminal {
			break // frozen: ignore stragglers delivered after the terminal event
		}
		switch {
		case ev.Type == hub.EventTaskRetrying:
			e.prog.Phase = "retrying"
		case ev.Phase != "":
			e.prog.Phase = ev.Phase
		}
		if m := ev.Model; m != nil {
			e.prog.CostUSD += m.CostUSD
			if w := workerFrom(m); w != "" {
				e.prog.Worker = w
			}
		}
	}
	p.mu.Unlock()

	// Wake the drainer without ever blocking the bus goroutine.
	select {
	case p.kick <- struct{}{}:
	default:
	}
	return nil
}

// workerFrom picks the most specific worker label from a model event.
func workerFrom(m *hub.ModelEvent) string {
	if m == nil {
		return ""
	}
	if m.Model != "" {
		return m.Model
	}
	return m.Provider
}

// snapshot returns a stable (task-ID-sorted) copy of the current state:
// per-task progress, total cost across tasks, and the count of tasks
// still running (reported as BurstWorkers).
func (p *HubProgress) snapshot() (tasks []TaskProgress, totalCost float64, active int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ids := make([]string, 0, len(p.tasks))
	for id := range p.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	tasks = make([]TaskProgress, 0, len(ids))
	for _, id := range ids {
		e := p.tasks[id]
		prog := e.prog
		if !e.terminal {
			prog.DurationMs = time.Since(e.started).Milliseconds()
			active++
		}
		totalCost += prog.CostUSD
		tasks = append(tasks, prog)
	}
	return tasks, totalCost, active
}

// push sends one progress snapshot. Best-effort: a dashboard outage
// must never affect the build.
func (p *HubProgress) push() {
	tasks, total, active := p.snapshot()
	if len(tasks) == 0 {
		return
	}
	_ = p.reporter.Update(SessionUpdate{
		PlanID:       p.planID,
		Tasks:        tasks,
		TotalCostUSD: total,
		BurstWorkers: active,
	})
}

// run is the single drainer goroutine: push on demand, then swallow
// further kicks for one interval so event bursts cost at most one
// HTTP request per interval.
func (p *HubProgress) run() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			return
		case <-p.kick:
			p.push()
			select {
			case <-p.stop:
				return
			case <-time.After(p.interval):
			}
		}
	}
}

// Stop halts the drainer and performs a final synchronous flush so the
// last snapshot reaches the dashboard before Complete is sent. Nil-safe
// and idempotent.
func (p *HubProgress) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		close(p.stop)
		<-p.done
		p.push()
	})
}
