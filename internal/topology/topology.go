// Package topology implements STOKE-019: coordination topology
// engine for multi-agent task execution. Declares a Topology
// interface and three built-in implementations
// (SupervisorWorker, Sequential, ConcurrentFanOut) that govern how
// sub-tasks are scheduled across workers.
//
// Selection can be explicit (caller names a topology) or
// heuristic (caller supplies a task class and the engine picks
// the topology with the best historical metrics). Per-topology +
// per-task-class metrics are recorded automatically so the
// heuristic improves over time.
//
// Scope of this file:
//   - Topology interface
//   - 3 built-in topologies
//   - TopologyRegistry with metrics tracking
//   - Heuristic selection based on historical success-rate +
//     token-cost + latency
//
// The topologies delegate actual execution to a Runner callback
// the caller supplies — this package doesn't depend on any
// specific execution engine (Claude Code, Codex, Hermes, etc.)
// so it's reusable across all of them.
package topology

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Name identifies a built-in or registered topology.
type Name string

const (
	NameSupervisorWorker   Name = "supervisor-worker"
	NameSequential         Name = "sequential"
	NameConcurrentFanOut   Name = "concurrent-fan-out"
	NameFlatHandoffs       Name = "flat-handoffs"
	NameDynamic            Name = "dynamic"
)

// Task is one unit of work a topology dispatches to a Runner.
// Opaque to the topology engine — only the Runner interprets it.
type Task struct {
	ID      string
	Payload any
}

// TaskResult is what a Runner returns after processing a Task.
type TaskResult struct {
	TaskID   string
	Output   any
	Err      error
	Tokens   int
	Duration time.Duration
}

// Runner is the execution backend. The topology engine calls
// Runner for each task — what Runner does (call an LLM, shell out
// to a tool, etc.) is the caller's concern.
type Runner func(ctx context.Context, t Task) TaskResult

// Topology coordinates how a batch of Tasks get dispatched. All
// topologies share this signature so the registry + selection
// heuristic can swap implementations without the caller knowing
// which one is running.
type Topology interface {
	Name() Name
	Run(ctx context.Context, tasks []Task, run Runner) []TaskResult
}

// Sequential runs tasks one at a time in order. Each task sees
// the previous task's result via context — but that plumbing is
// the Runner's concern (topologies stay lean).
type Sequential struct{}

func (Sequential) Name() Name { return NameSequential }

func (Sequential) Run(ctx context.Context, tasks []Task, run Runner) []TaskResult {
	results := make([]TaskResult, 0, len(tasks))
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		results = append(results, run(ctx, t))
	}
	return results
}

// ConcurrentFanOut runs all tasks in parallel. Useful for
// embarrassingly-parallel work: per-file linting, per-test
// execution, per-candidate exploration.
//
// Parallelism is unbounded in this basic implementation;
// callers that need a worker-pool limit should wrap this
// topology (e.g. with a semaphore in the Runner) rather than
// teaching this topology about pool sizes.
type ConcurrentFanOut struct{}

func (ConcurrentFanOut) Name() Name { return NameConcurrentFanOut }

func (ConcurrentFanOut) Run(ctx context.Context, tasks []Task, run Runner) []TaskResult {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]TaskResult, len(tasks))
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t Task) {
			defer wg.Done()
			if ctx.Err() != nil {
				out[i] = TaskResult{TaskID: t.ID, Err: ctx.Err()}
				return
			}
			out[i] = run(ctx, t)
		}(i, t)
	}
	wg.Wait()
	return out
}

// SupervisorWorker runs a supervisor task first, then dispatches
// the remaining tasks as workers under the supervisor's
// decisions. The supervisor task is tasks[0]; its output is the
// dispatch plan which this topology interprets as "run all
// remaining tasks concurrently" in this minimal implementation.
//
// A richer implementation would let the supervisor's output
// select which remaining tasks to run (and in what order), but
// this level of sophistication belongs in the Runner that
// interprets the supervisor's output, not in the topology —
// keep the topology dumb + composable.
type SupervisorWorker struct{}

func (SupervisorWorker) Name() Name { return NameSupervisorWorker }

func (SupervisorWorker) Run(ctx context.Context, tasks []Task, run Runner) []TaskResult {
	if len(tasks) == 0 {
		return nil
	}
	// Supervisor first.
	supRes := run(ctx, tasks[0])
	if supRes.Err != nil || len(tasks) == 1 {
		return []TaskResult{supRes}
	}
	// Workers concurrent.
	workers := ConcurrentFanOut{}.Run(ctx, tasks[1:], run)
	out := make([]TaskResult, 0, len(workers)+1)
	out = append(out, supRes)
	out = append(out, workers...)
	return out
}

// Metric records per-topology + per-task-class performance. Used
// by the Registry's heuristic selection.
type Metric struct {
	TotalRuns    int
	SuccessRuns  int
	TotalTokens  int
	TotalLatency time.Duration
}

// SuccessRate reports successful runs / total runs, or 0 when
// TotalRuns is 0. Used by the heuristic so fresh topologies
// don't get immediately de-ranked on zero data.
func (m Metric) SuccessRate() float64 {
	if m.TotalRuns == 0 {
		return 0
	}
	return float64(m.SuccessRuns) / float64(m.TotalRuns)
}

// AvgTokens / AvgLatency are the per-run cost + speed metrics.
func (m Metric) AvgTokens() float64 {
	if m.TotalRuns == 0 {
		return 0
	}
	return float64(m.TotalTokens) / float64(m.TotalRuns)
}

func (m Metric) AvgLatency() time.Duration {
	if m.TotalRuns == 0 {
		return 0
	}
	return m.TotalLatency / time.Duration(m.TotalRuns)
}

// Registry holds topologies + per-topology + per-task-class
// metrics. Thread-safe.
type Registry struct {
	mu           sync.RWMutex
	topologies   map[Name]Topology
	metrics      map[metricKey]*Metric
}

// metricKey is the composite key for per-topology + per-task-
// class metrics.
type metricKey struct {
	topology  Name
	taskClass string
}

// NewRegistry returns a registry with the 3 built-in topologies
// pre-registered. Callers can add more via Register.
func NewRegistry() *Registry {
	r := &Registry{
		topologies: map[Name]Topology{},
		metrics:    map[metricKey]*Metric{},
	}
	r.Register(Sequential{})
	r.Register(ConcurrentFanOut{})
	r.Register(SupervisorWorker{})
	return r
}

// Register adds a topology. Replaces any existing entry with the
// same name. Safe to call after startup.
func (r *Registry) Register(t Topology) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topologies[t.Name()] = t
}

// Get returns a topology by name, or ErrUnknownTopology.
func (r *Registry) Get(name Name) (Topology, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.topologies[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopology, name)
	}
	return t, nil
}

// ErrUnknownTopology is returned by Get / Select when the
// requested topology isn't registered.
var ErrUnknownTopology = errors.New("topology: unknown")

// Select picks a topology for a task class based on historical
// metrics. The heuristic:
//
//  1. Prefer topologies with SuccessRate >= 0.9 (very reliable).
//  2. Among those, prefer lowest AvgLatency.
//  3. Fall back to Sequential (safest default) if no topology
//     has recorded runs for this task class.
func (r *Registry) Select(taskClass string) Topology {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type scored struct {
		name Name
		m    *Metric
	}
	var candidates []scored
	for name := range r.topologies {
		k := metricKey{topology: name, taskClass: taskClass}
		if m, ok := r.metrics[k]; ok && m.TotalRuns > 0 {
			candidates = append(candidates, scored{name, m})
		}
	}
	if len(candidates) == 0 {
		// No metrics yet — default to Sequential for safety.
		return r.topologies[NameSequential]
	}

	// Favor reliable topologies first; among those, faster wins.
	sort.Slice(candidates, func(i, j int) bool {
		ai, aj := candidates[i].m.SuccessRate() >= 0.9, candidates[j].m.SuccessRate() >= 0.9
		if ai != aj {
			return ai
		}
		return candidates[i].m.AvgLatency() < candidates[j].m.AvgLatency()
	})
	return r.topologies[candidates[0].name]
}

// Record ingests a run's outcome into the metrics table. Callers
// invoke this after a topology run completes so Select can learn.
func (r *Registry) Record(name Name, taskClass string, results []TaskResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := metricKey{topology: name, taskClass: taskClass}
	m, ok := r.metrics[k]
	if !ok {
		m = &Metric{}
		r.metrics[k] = m
	}
	m.TotalRuns++
	successful := true
	var tokens int
	var latency time.Duration
	for _, res := range results {
		tokens += res.Tokens
		latency += res.Duration
		if res.Err != nil {
			successful = false
		}
	}
	if successful {
		m.SuccessRuns++
	}
	m.TotalTokens += tokens
	m.TotalLatency += latency
}

// MetricFor returns a snapshot of the metric for a (topology,
// task class) pair. Returns zero-value on miss.
func (r *Registry) MetricFor(name Name, taskClass string) Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.metrics[metricKey{topology: name, taskClass: taskClass}]
	if !ok {
		return Metric{}
	}
	return *m
}

// RunAndRecord is a convenience: pick a topology (explicit or
// via Select), execute, record the metric, return results.
// Callers that want finer control should use Registry.Get +
// Topology.Run + Registry.Record directly.
func (r *Registry) RunAndRecord(ctx context.Context, explicit Name, taskClass string, tasks []Task, run Runner) ([]TaskResult, Name, error) {
	var t Topology
	if explicit != "" {
		got, err := r.Get(explicit)
		if err != nil {
			return nil, "", err
		}
		t = got
	} else {
		t = r.Select(taskClass)
	}
	results := t.Run(ctx, tasks, run)
	r.Record(t.Name(), taskClass, results)
	return results, t.Name(), nil
}
