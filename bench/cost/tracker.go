package cost

import (
	"fmt"
	"strings"
	"sync"
)

// RunTracker aggregates costs across a full benchmark run. It is thread-safe.
type RunTracker struct {
	mu          sync.Mutex
	total       float64
	perHarness  map[string]float64
	perCategory map[string]float64
	entries     []CostEntry
}

// CostEntry records a single cost event.
type CostEntry struct {
	TaskID   string  `json:"task_id"`
	Harness  string  `json:"harness"`
	Category string  `json:"category"`
	CostUSD  float64 `json:"cost_usd"`
}

// NewRunTracker creates a new cost tracker for a benchmark run.
func NewRunTracker() *RunTracker {
	return &RunTracker{
		perHarness:  make(map[string]float64),
		perCategory: make(map[string]float64),
	}
}

// Record adds a cost entry to the tracker.
func (t *RunTracker) Record(entry CostEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total += entry.CostUSD
	t.perHarness[entry.Harness] += entry.CostUSD
	t.perCategory[entry.Category] += entry.CostUSD
	t.entries = append(t.entries, entry)
}

// Total returns the total cost across all entries.
func (t *RunTracker) Total() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

// PerHarness returns a copy of the per-harness cost map.
func (t *RunTracker) PerHarness() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := make(map[string]float64, len(t.perHarness))
	for k, v := range t.perHarness {
		m[k] = v
	}
	return m
}

// PerCategory returns a copy of the per-category cost map.
func (t *RunTracker) PerCategory() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := make(map[string]float64, len(t.perCategory))
	for k, v := range t.perCategory {
		m[k] = v
	}
	return m
}

// Entries returns a copy of all recorded cost entries.
func (t *RunTracker) Entries() []CostEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]CostEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

// Count returns the number of recorded entries.
func (t *RunTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// Summary returns a human-readable summary of costs.
func (t *RunTracker) Summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sb strings.Builder
	fmt.Fprintf(&sb, "Total: $%.4f (%d entries)\n", t.total, len(t.entries))
	fmt.Fprintln(&sb, "Per harness:")
	for k, v := range t.perHarness {
		fmt.Fprintf(&sb, "  %s: $%.4f\n", k, v)
	}
	fmt.Fprintln(&sb, "Per category:")
	for k, v := range t.perCategory {
		fmt.Fprintf(&sb, "  %s: $%.4f\n", k, v)
	}
	return sb.String()
}
