// Package telemetry implements structured metrics collection for performance analysis.
// Inspired by claw-code's telemetry and OpenHands' execution tracking:
//
// Understanding agent performance requires structured metrics:
// - Latency per tool call, per phase, per task
// - Token efficiency (useful output / total tokens)
// - Success/failure rates by error class
// - Cost per task type
// - Time-to-resolution distribution
//
// These metrics feed back into model routing, budget allocation, and
// retry policy tuning.
package telemetry

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is a single telemetry event.
type Event struct {
	Name      string         `json:"name"`
	Category  string         `json:"category"`
	Duration  time.Duration  `json:"duration"`
	Tokens    int            `json:"tokens,omitempty"`
	Cost      float64        `json:"cost,omitempty"`
	Success   bool           `json:"success"`
	Tags      map[string]string `json:"tags,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// Collector gathers telemetry events.
type Collector struct {
	mu     sync.RWMutex
	events []Event
}

// New creates a telemetry collector.
func New() *Collector {
	return &Collector{}
}

// Record adds a telemetry event.
func (c *Collector) Record(e Event) {
	e.Timestamp = time.Now()
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// RecordDuration is a convenience for timing operations.
func (c *Collector) RecordDuration(name, category string, d time.Duration, success bool) {
	c.Record(Event{
		Name:     name,
		Category: category,
		Duration: d,
		Success:  success,
	})
}

// Timer starts a timer that records when stopped.
func (c *Collector) Timer(name, category string) *Timer {
	return &Timer{
		collector: c,
		name:      name,
		category:  category,
		start:     time.Now(),
	}
}

// Timer tracks elapsed time for an operation.
type Timer struct {
	collector *Collector
	name      string
	category  string
	start     time.Time
}

// Stop records the elapsed duration.
func (t *Timer) Stop(success bool) time.Duration {
	d := time.Since(t.start)
	t.collector.RecordDuration(t.name, t.category, d, success)
	return d
}

// Summary returns aggregate statistics.
func (c *Collector) Summary() MetricsSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s := MetricsSummary{
		TotalEvents:  len(c.events),
		ByCategory:   make(map[string]CategoryStats),
	}

	for _, e := range c.events {
		s.TotalDuration += e.Duration
		s.TotalTokens += e.Tokens
		s.TotalCost += e.Cost
		if e.Success {
			s.Successes++
		} else {
			s.Failures++
		}

		cat := s.ByCategory[e.Category]
		cat.Count++
		cat.TotalDuration += e.Duration
		cat.TotalTokens += e.Tokens
		cat.TotalCost += e.Cost
		if e.Success {
			cat.Successes++
		}
		s.ByCategory[e.Category] = cat
	}

	if s.TotalEvents > 0 {
		s.SuccessRate = float64(s.Successes) / float64(s.TotalEvents)
		s.AvgDuration = s.TotalDuration / time.Duration(s.TotalEvents)
	}

	return s
}

// MetricsSummary holds aggregate metrics.
type MetricsSummary struct {
	TotalEvents   int                      `json:"total_events"`
	Successes     int                      `json:"successes"`
	Failures      int                      `json:"failures"`
	SuccessRate   float64                  `json:"success_rate"`
	TotalDuration time.Duration            `json:"total_duration"`
	AvgDuration   time.Duration            `json:"avg_duration"`
	TotalTokens   int                      `json:"total_tokens"`
	TotalCost     float64                  `json:"total_cost"`
	ByCategory    map[string]CategoryStats `json:"by_category"`
}

// CategoryStats holds per-category metrics.
type CategoryStats struct {
	Count         int           `json:"count"`
	Successes     int           `json:"successes"`
	TotalDuration time.Duration `json:"total_duration"`
	TotalTokens   int           `json:"total_tokens"`
	TotalCost     float64       `json:"total_cost"`
}

// Percentiles returns latency percentiles for a category.
func (c *Collector) Percentiles(category string) map[string]time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var durations []time.Duration
	for _, e := range c.events {
		if e.Category == category {
			durations = append(durations, e.Duration)
		}
	}

	if len(durations) == 0 {
		return nil
	}

	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})

	return map[string]time.Duration{
		"p50": percentile(durations, 50),
		"p90": percentile(durations, 90),
		"p95": percentile(durations, 95),
		"p99": percentile(durations, 99),
	}
}

func percentile(sorted []time.Duration, pct float64) time.Duration {
	idx := int(math.Ceil(pct/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// EventCount returns the total number of events.
func (c *Collector) EventCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.events)
}

// Events returns events filtered by category.
func (c *Collector) Events(category string) []Event {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if category == "" {
		result := make([]Event, len(c.events))
		copy(result, c.events)
		return result
	}

	var filtered []Event
	for _, e := range c.events {
		if e.Category == category {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// Clear removes all events.
func (c *Collector) Clear() {
	c.mu.Lock()
	c.events = nil
	c.mu.Unlock()
}

// Format produces a human-readable report.
func (c *Collector) Format() string {
	s := c.Summary()

	var b strings.Builder
	fmt.Fprintf(&b, "Telemetry: %d events (%.0f%% success)\n", s.TotalEvents, s.SuccessRate*100)
	fmt.Fprintf(&b, "Duration: %s total, %s avg\n", s.TotalDuration.Round(time.Millisecond), s.AvgDuration.Round(time.Millisecond))
	if s.TotalTokens > 0 {
		fmt.Fprintf(&b, "Tokens: %d, Cost: $%.4f\n", s.TotalTokens, s.TotalCost)
	}

	if len(s.ByCategory) > 0 {
		b.WriteString("\nBy category:\n")
		for cat, cs := range s.ByCategory {
			rate := float64(cs.Successes) / float64(cs.Count) * 100
			fmt.Fprintf(&b, "  %s: %d events (%.0f%% success), %s total\n",
				cat, cs.Count, rate, cs.TotalDuration.Round(time.Millisecond))
		}
	}
	return b.String()
}
