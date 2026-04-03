// Package metrics provides thread-safe counters, gauges, timers, and histograms
// for tracking operational metrics across all Stoke components. Metrics are
// collected in-process and can be exported via Snapshot() for reporting.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing thread-safe counter.
type Counter struct {
	value atomic.Int64
}

// Inc increments the counter by 1.
func (c *Counter) Inc() { c.value.Add(1) }

// Add increments the counter by n.
func (c *Counter) Add(n int64) { c.value.Add(n) }

// Value returns the current counter value.
func (c *Counter) Value() int64 { return c.value.Load() }

// Gauge is a thread-safe value that can go up or down.
type Gauge struct {
	value atomic.Int64
}

// Set sets the gauge to a specific value.
func (g *Gauge) Set(v int64) { g.value.Store(v) }

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.value.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.value.Add(-1) }

// Value returns the current gauge value.
func (g *Gauge) Value() int64 { return g.value.Load() }

// Timer records durations for operations.
type Timer struct {
	mu    sync.Mutex
	count int64
	total time.Duration
	min   time.Duration
	max   time.Duration
}

// Record adds a duration observation to the timer.
func (t *Timer) Record(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	t.total += d
	if t.count == 1 || d < t.min {
		t.min = d
	}
	if d > t.max {
		t.max = d
	}
}

// TimerSnapshot is a point-in-time snapshot of a Timer's state.
type TimerSnapshot struct {
	Count int64         `json:"count"`
	Total time.Duration `json:"total_ns"`
	Min   time.Duration `json:"min_ns"`
	Max   time.Duration `json:"max_ns"`
	Avg   time.Duration `json:"avg_ns"`
}

// Snapshot returns a point-in-time snapshot of the timer.
func (t *Timer) Snapshot() TimerSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snap := TimerSnapshot{Count: t.count, Total: t.total, Min: t.min, Max: t.max}
	if t.count > 0 {
		snap.Avg = t.total / time.Duration(t.count)
	}
	return snap
}

// Since is a convenience that records the duration since start.
func (t *Timer) Since(start time.Time) {
	t.Record(time.Since(start))
}

// CostAccumulator tracks monetary costs in a thread-safe manner.
type CostAccumulator struct {
	mu    sync.Mutex
	total float64
	count int64
}

// Add records a cost.
func (c *CostAccumulator) Add(cost float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += cost
	c.count++
}

// Total returns the accumulated cost and count.
func (c *CostAccumulator) Total() (cost float64, count int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total, c.count
}

// Registry is a named collection of metrics. Thread-safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	gauges   map[string]*Gauge
	timers   map[string]*Timer
	costs    map[string]*CostAccumulator
}

// NewRegistry creates an empty metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counters: make(map[string]*Counter),
		gauges:   make(map[string]*Gauge),
		timers:   make(map[string]*Timer),
		costs:    make(map[string]*CostAccumulator),
	}
}

// Counter returns the named counter, creating it if it doesn't exist.
func (r *Registry) Counter(name string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &Counter{}
	r.counters[name] = c
	return c
}

// Gauge returns the named gauge, creating it if it doesn't exist.
func (r *Registry) Gauge(name string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{}
	r.gauges[name] = g
	return g
}

// Timer returns the named timer, creating it if it doesn't exist.
func (r *Registry) Timer(name string) *Timer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.timers[name]; ok {
		return t
	}
	t := &Timer{}
	r.timers[name] = t
	return t
}

// Cost returns the named cost accumulator, creating it if it doesn't exist.
func (r *Registry) Cost(name string) *CostAccumulator {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.costs[name]; ok {
		return c
	}
	c := &CostAccumulator{}
	r.costs[name] = c
	return c
}

// Snapshot is a serializable point-in-time view of all metrics.
type Snapshot struct {
	Counters map[string]int64         `json:"counters"`
	Gauges   map[string]int64         `json:"gauges"`
	Timers   map[string]TimerSnapshot `json:"timers"`
	Costs    map[string]CostSnapshot  `json:"costs"`
}

// CostSnapshot is a point-in-time view of a cost accumulator.
type CostSnapshot struct {
	Total float64 `json:"total_usd"`
	Count int64   `json:"count"`
}

// Snapshot returns a serializable snapshot of all registered metrics.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Snapshot{
		Counters: make(map[string]int64, len(r.counters)),
		Gauges:   make(map[string]int64, len(r.gauges)),
		Timers:   make(map[string]TimerSnapshot, len(r.timers)),
		Costs:    make(map[string]CostSnapshot, len(r.costs)),
	}
	for name, c := range r.counters {
		s.Counters[name] = c.Value()
	}
	for name, g := range r.gauges {
		s.Gauges[name] = g.Value()
	}
	for name, t := range r.timers {
		s.Timers[name] = t.Snapshot()
	}
	for name, c := range r.costs {
		total, count := c.Total()
		s.Costs[name] = CostSnapshot{Total: total, Count: count}
	}
	return s
}

// DefaultRegistry is the global metrics registry.
var DefaultRegistry = NewRegistry()
