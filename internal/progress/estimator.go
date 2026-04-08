// Package progress implements plan-aware progress estimation.
// Inspired by OmX's task decomposition and claw-code's status tracking:
//
// When executing a multi-task plan, users need to know:
// - How far along are we? (percentage, tasks done/total)
// - How long until completion? (ETA based on observed velocity)
// - Which tasks are blocking? (critical path)
// - Is the pace on track? (velocity trending up/down)
//
// This estimator uses observed task durations to predict remaining time,
// adjusting for task complexity and accounting for dependencies.
package progress

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// TaskStatus represents task completion state.
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusRunning    TaskStatus = "running"
	StatusCompleted  TaskStatus = "completed"
	StatusFailed     TaskStatus = "failed"
	StatusSkipped    TaskStatus = "skipped"
)

// Task is a unit of work in a plan.
type Task struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Status       TaskStatus    `json:"status"`
	Weight       float64       `json:"weight"`       // relative complexity (1.0 = normal)
	Dependencies []string      `json:"dependencies"` // task IDs this depends on
	StartTime    time.Time     `json:"start_time,omitempty"`
	EndTime      time.Time     `json:"end_time,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	Retries      int           `json:"retries"`
}

// Estimator tracks plan progress and predicts completion.
type Estimator struct {
	mu         sync.RWMutex
	tasks      map[string]*Task
	order      []string // insertion order
	startTime  time.Time
	velocities []float64 // weight-per-second for completed tasks
}

// Snapshot is a point-in-time progress report.
type Snapshot struct {
	Total       int           `json:"total"`
	Completed   int           `json:"completed"`
	Failed      int           `json:"failed"`
	Running     int           `json:"running"`
	Pending     int           `json:"pending"`
	Skipped     int           `json:"skipped"`
	Percentage  float64       `json:"percentage"`   // 0-100
	Elapsed     time.Duration `json:"elapsed"`
	ETA         time.Duration `json:"eta"`
	Velocity    float64       `json:"velocity"`     // weight-per-second (smoothed)
	WeightDone  float64       `json:"weight_done"`
	WeightTotal float64       `json:"weight_total"`
	OnTrack     bool          `json:"on_track"`
}

// New creates an estimator with the given tasks.
func New(tasks []Task) *Estimator {
	e := &Estimator{
		tasks:     make(map[string]*Task),
		startTime: time.Now(),
	}
	for i := range tasks {
		t := tasks[i]
		if t.Weight == 0 {
			t.Weight = 1.0
		}
		if t.Status == "" {
			t.Status = StatusPending
		}
		e.tasks[t.ID] = &t
		e.order = append(e.order, t.ID)
	}
	return e
}

// Start marks a task as running.
func (e *Estimator) Start(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[id]; ok {
		t.Status = StatusRunning
		t.StartTime = time.Now()
	}
}

// Complete marks a task as completed.
func (e *Estimator) Complete(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[id]; ok {
		t.Status = StatusCompleted
		t.EndTime = time.Now()
		t.Duration = t.EndTime.Sub(t.StartTime)
		if t.Duration > 0 {
			v := t.Weight / t.Duration.Seconds()
			e.velocities = append(e.velocities, v)
		}
	}
}

// Fail marks a task as failed.
func (e *Estimator) Fail(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[id]; ok {
		t.Status = StatusFailed
		t.EndTime = time.Now()
		t.Duration = t.EndTime.Sub(t.StartTime)
		t.Retries++
	}
}

// Skip marks a task as skipped.
func (e *Estimator) Skip(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[id]; ok {
		t.Status = StatusSkipped
	}
}

// Retry resets a failed task to pending with incremented retry count.
func (e *Estimator) Retry(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[id]; ok {
		t.Status = StatusPending
		t.StartTime = time.Time{}
		t.EndTime = time.Time{}
		t.Duration = 0
	}
}

// Progress returns a snapshot of current progress.
func (e *Estimator) Progress() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	snap := Snapshot{
		Total:   len(e.tasks),
		Elapsed: time.Since(e.startTime),
	}

	var weightDone, weightTotal float64
	for _, id := range e.order {
		t := e.tasks[id]
		weightTotal += t.Weight
		switch t.Status {
		case StatusCompleted:
			snap.Completed++
			weightDone += t.Weight
		case StatusFailed:
			snap.Failed++
		case StatusRunning:
			snap.Running++
			// Count partial progress for running tasks
			if !t.StartTime.IsZero() {
				elapsed := time.Since(t.StartTime)
				avgDuration := e.avgDuration()
				if avgDuration > 0 {
					frac := elapsed.Seconds() / avgDuration.Seconds()
					if frac > 0.9 {
						frac = 0.9 // cap at 90%
					}
					weightDone += t.Weight * frac
				}
			}
		case StatusPending:
			snap.Pending++
		case StatusSkipped:
			snap.Skipped++
			weightDone += t.Weight // skipped counts as done for progress
		}
	}

	snap.WeightDone = weightDone
	snap.WeightTotal = weightTotal

	if weightTotal > 0 {
		snap.Percentage = (weightDone / weightTotal) * 100
	}

	snap.Velocity = e.smoothedVelocity()

	// ETA based on remaining weight and velocity
	remaining := weightTotal - weightDone
	if snap.Velocity > 0 && remaining > 0 {
		snap.ETA = time.Duration(remaining/snap.Velocity) * time.Second
	}

	// On track: actual velocity >= expected velocity
	if snap.Elapsed > 0 && weightTotal > 0 {
		expectedVelocity := weightTotal / (snap.Elapsed.Seconds() + snap.ETA.Seconds() + 0.001)
		actualVelocity := weightDone / (snap.Elapsed.Seconds() + 0.001)
		snap.OnTrack = actualVelocity >= expectedVelocity*0.8 // 80% threshold
	}

	return snap
}

// Ready returns task IDs that are ready to run (pending + all deps complete).
func (e *Estimator) Ready() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var ready []string
	for _, id := range e.order {
		t := e.tasks[id]
		if t.Status != StatusPending {
			continue
		}
		if e.depsComplete(t) {
			ready = append(ready, id)
		}
	}
	return ready
}

// CriticalPath returns the longest dependency chain (by weight).
func (e *Estimator) CriticalPath() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Find the path with maximum total weight using DFS + memoization
	memo := make(map[string][]string)
	var best []string
	var bestWeight float64

	for _, id := range e.order {
		path := e.longestPath(id, memo)
		weight := 0.0
		for _, pid := range path {
			if t, ok := e.tasks[pid]; ok {
				weight += t.Weight
			}
		}
		if weight > bestWeight {
			bestWeight = weight
			best = path
		}
	}

	return best
}

// Blocked returns task IDs that are blocked by incomplete dependencies.
func (e *Estimator) Blocked() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var blocked []string
	for _, id := range e.order {
		t := e.tasks[id]
		if t.Status != StatusPending {
			continue
		}
		if !e.depsComplete(t) {
			blocked = append(blocked, id)
		}
	}
	return blocked
}

// Summary returns a human-readable progress summary.
func (e *Estimator) Summary() string {
	snap := e.Progress()
	var b strings.Builder
	fmt.Fprintf(&b, "Progress: %.1f%% (%d/%d tasks)\n", snap.Percentage, snap.Completed, snap.Total)
	fmt.Fprintf(&b, "Elapsed: %s", formatDuration(snap.Elapsed))
	if snap.ETA > 0 {
		fmt.Fprintf(&b, " | ETA: %s", formatDuration(snap.ETA))
	}
	b.WriteString("\n")
	if snap.Failed > 0 {
		fmt.Fprintf(&b, "Failed: %d tasks\n", snap.Failed)
	}
	if snap.Running > 0 {
		fmt.Fprintf(&b, "Running: %d tasks\n", snap.Running)
	}
	if snap.OnTrack {
		b.WriteString("Status: on track\n")
	} else if snap.Completed > 0 {
		b.WriteString("Status: behind schedule\n")
	}
	return b.String()
}

// ProgressBar returns an ASCII progress bar.
func (e *Estimator) ProgressBar(width int) string {
	snap := e.Progress()
	filled := int(snap.Percentage / 100 * float64(width))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %.1f%%", bar, snap.Percentage)
}

// --- internals ---

func (e *Estimator) depsComplete(t *Task) bool {
	for _, dep := range t.Dependencies {
		if dt, ok := e.tasks[dep]; ok {
			if dt.Status != StatusCompleted && dt.Status != StatusSkipped {
				return false
			}
		}
	}
	return true
}

func (e *Estimator) longestPath(id string, memo map[string][]string) []string {
	if cached, ok := memo[id]; ok {
		return cached
	}

	t, ok := e.tasks[id]
	if !ok {
		return nil
	}

	best := []string{id}
	for _, dep := range t.Dependencies {
		sub := e.longestPath(dep, memo)
		candidate := append([]string{id}, sub...)
		subWeight := 0.0
		for _, pid := range sub {
			if pt, ok := e.tasks[pid]; ok {
				subWeight += pt.Weight
			}
		}
		bestWeight := 0.0
		for _, pid := range best[1:] {
			if pt, ok := e.tasks[pid]; ok {
				bestWeight += pt.Weight
			}
		}
		if subWeight > bestWeight {
			best = candidate
		}
	}

	memo[id] = best
	return best
}

func (e *Estimator) smoothedVelocity() float64 {
	if len(e.velocities) == 0 {
		return 0
	}
	// Exponential moving average (recent tasks weighted more)
	alpha := 0.3
	ema := e.velocities[0]
	for _, v := range e.velocities[1:] {
		ema = alpha*v + (1-alpha)*ema
	}
	return ema
}

func (e *Estimator) avgDuration() time.Duration {
	var total time.Duration
	count := 0
	for _, t := range e.tasks {
		if t.Status == StatusCompleted && t.Duration > 0 {
			total += t.Duration
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / time.Duration(count)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0fm%.0fs", d.Minutes(), d.Seconds()-d.Minutes()*60+0.5)
	}
	return fmt.Sprintf("%.0fh%.0fm", d.Hours(), d.Minutes()-d.Hours()*60+0.5)
}
