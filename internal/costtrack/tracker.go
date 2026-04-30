// Package costtrack implements real-time cost tracking with budget alerts.
// Inspired by Aider's token/cost reporting and claw-code's usage tracking:
//
// Without cost tracking, multi-agent runs can silently burn through hundreds
// of dollars. This package:
// - Tracks per-request token usage (input, output, cache hits)
// - Computes USD cost using per-model pricing
// - Enforces budget limits with configurable thresholds
// - Provides per-task and per-model cost breakdowns
//
// Budget enforcement prevents runaway costs on stuck agents.
package costtrack

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Pricing holds per-million-token costs in USD.
type Pricing struct {
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million"`
}

// ModelPricing maps model names to pricing.
var ModelPricing = map[string]Pricing{
	"claude-opus-4": {
		InputPerMillion:      15.0,
		OutputPerMillion:     75.0,
		CacheReadPerMillion:  1.5,
		CacheWritePerMillion: 18.75,
	},
	"claude-sonnet-4": {
		InputPerMillion:      3.0,
		OutputPerMillion:     15.0,
		CacheReadPerMillion:  0.3,
		CacheWritePerMillion: 3.75,
	},
	"claude-haiku-3.5": {
		InputPerMillion:      0.8,
		OutputPerMillion:     4.0,
		CacheReadPerMillion:  0.08,
		CacheWritePerMillion: 1.0,
	},
	"gpt-4o": {
		InputPerMillion:  2.5,
		OutputPerMillion: 10.0,
	},
	"o3-mini": {
		InputPerMillion:  1.1,
		OutputPerMillion: 4.4,
	},
	"codex-mini": {
		InputPerMillion:  1.5,
		OutputPerMillion: 6.0,
	},
}

// Usage records token usage for a single request.
type Usage struct {
	Model        string    `json:"model"`
	TaskID       string    `json:"task_id,omitempty"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CacheRead    int       `json:"cache_read,omitempty"`
	CacheWrite   int       `json:"cache_write,omitempty"`
	Cost         float64   `json:"cost"`
	Timestamp    time.Time `json:"timestamp"`
}

// AlertLevel for budget thresholds.
type AlertLevel string

// Budget alert levels fired as spend approaches and crosses the cap.
// Values are stable strings persisted in alert logs.
const (
	AlertInfo     AlertLevel = "info"     // approaching limit
	AlertWarning  AlertLevel = "warning"  // near limit
	AlertCritical AlertLevel = "critical" // at or over limit
)

// Alert is a budget notification.
type Alert struct {
	Level      AlertLevel `json:"level"`
	Message    string     `json:"message"`
	Budget     float64    `json:"budget"`
	Spent      float64    `json:"spent"`
	Percentage float64    `json:"percentage"`
}

// AlertFunc is called when a budget threshold is crossed.
type AlertFunc func(alert Alert)

// Tracker tracks costs across a session.
type Tracker struct {
	mu      sync.RWMutex
	records []Usage
	envCost float64 // accumulated execution environment costs
	budget  float64
	alertFn AlertFunc
	alerted map[AlertLevel]bool

	// amp is the optional amplification-budget tracker. When non-nil,
	// every Record call forwards (inputTokens + outputTokens) to
	// amp.Add so the tracker's state machine fires its OnTransition
	// hook at the alert / exceeded boundaries. Nil means "no
	// amplification budget attached" — pre-B2 behavior.
	amp *AmplificationTracker
}

// AttachAmplification wires an AmplificationTracker into the cost
// tracker. After this call, every Record invocation also accounts
// against the amplification budget. Safe to pass nil to detach.
// Closes matrix gap B2 at the wiring layer — the Q16 baseline-
// measurement effort is still the blocker for meaningful
// BaselineTokens values, but the wire is live so the moment
// baselines land the ceiling is enforced.
func (t *Tracker) AttachAmplification(amp *AmplificationTracker) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.amp = amp
}

// Amplification returns the attached AmplificationTracker (may be
// nil). Exposed for diagnostic rendering.
func (t *Tracker) Amplification() *AmplificationTracker {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.amp
}

// NewTracker creates a cost tracker with an optional budget (0 = unlimited).
func NewTracker(budget float64, alertFn AlertFunc) *Tracker {
	return &Tracker{
		budget:  budget,
		alertFn: alertFn,
		alerted: make(map[AlertLevel]bool),
	}
}

// Record logs a usage event and returns the cost.
func (t *Tracker) Record(model, taskID string, inputTokens, outputTokens, cacheRead, cacheWrite int) float64 {
	cost := ComputeCost(model, inputTokens, outputTokens, cacheRead, cacheWrite)

	u := Usage{
		Model:        model,
		TaskID:       taskID,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CacheRead:    cacheRead,
		CacheWrite:   cacheWrite,
		Cost:         cost,
		Timestamp:    time.Now(),
	}

	t.mu.Lock()
	t.records = append(t.records, u)
	total := t.totalLocked()
	amp := t.amp
	t.mu.Unlock()

	t.checkAlerts(total)
	// Amplification accounting runs OUTSIDE the tracker mutex —
	// amp has its own lock and calling it under t.mu could deadlock
	// any OnTransition callback that also wants tracker state.
	if amp != nil {
		amp.Add(inputTokens + outputTokens)
	}
	return cost
}

// RecordEnvCost adds execution environment cost (compute, storage) to the tracker.
// This is separate from token-based costs but contributes to the same budget.
func (t *Tracker) RecordEnvCost(taskID string, costUSD float64) {
	t.mu.Lock()
	t.envCost += costUSD
	total := t.totalLocked()
	t.mu.Unlock()

	t.checkAlerts(total)
}

// EnvCost returns the accumulated execution environment cost.
func (t *Tracker) EnvCost() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.envCost
}

// Total returns total cost so far (tokens + environment).
func (t *Tracker) Total() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalLocked()
}

func (t *Tracker) totalLocked() float64 {
	var total float64
	for _, r := range t.records {
		total += r.Cost
	}
	return total + t.envCost
}

// ByModel returns cost breakdown by model.
func (t *Tracker) ByModel() map[string]float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	m := make(map[string]float64)
	for _, r := range t.records {
		m[r.Model] += r.Cost
	}
	return m
}

// ByTask returns cost breakdown by task.
func (t *Tracker) ByTask() map[string]float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	m := make(map[string]float64)
	for _, r := range t.records {
		if r.TaskID != "" {
			m[r.TaskID] += r.Cost
		}
	}
	return m
}

// Records returns a stable snapshot of recorded token-usage events.
func (t *Tracker) Records() []Usage {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]Usage, len(t.records))
	copy(out, t.records)
	return out
}

// TokenTotals returns aggregate token counts.
func (t *Tracker) TokenTotals() (input, output, cacheRead, cacheWrite int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, r := range t.records {
		input += r.InputTokens
		output += r.OutputTokens
		cacheRead += r.CacheRead
		cacheWrite += r.CacheWrite
	}
	return
}

// RequestCount returns the total number of API requests.
func (t *Tracker) RequestCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.records)
}

// BudgetRemaining returns remaining budget (negative = over budget).
func (t *Tracker) BudgetRemaining() float64 {
	if t.budget <= 0 {
		return -1 // unlimited
	}
	return t.budget - t.Total()
}

// OverBudget returns true if spending exceeds the budget.
func (t *Tracker) OverBudget() bool {
	if t.budget <= 0 {
		return false
	}
	return t.Total() > t.budget
}

// Summary returns a human-readable cost summary.
func (t *Tracker) Summary() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	total := t.totalLocked()
	input, output, cacheR, cacheW := 0, 0, 0, 0
	for _, r := range t.records {
		input += r.InputTokens
		output += r.OutputTokens
		cacheR += r.CacheRead
		cacheW += r.CacheWrite
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Cost: $%.4f (%d requests)\n", total, len(t.records))
	fmt.Fprintf(&b, "Tokens: %dk input, %dk output", input/1000, output/1000)
	if cacheR > 0 {
		fmt.Fprintf(&b, ", %dk cache-read", cacheR/1000)
	}
	if t.budget > 0 {
		pct := (total / t.budget) * 100
		fmt.Fprintf(&b, "\nBudget: $%.2f / $%.2f (%.0f%%)", total, t.budget, pct)
	}
	return b.String()
}

// ComputeCost calculates the USD cost for a single request.
func ComputeCost(model string, inputTokens, outputTokens, cacheRead, cacheWrite int) float64 {
	pricing, ok := ModelPricing[model]
	if !ok {
		// Default to sonnet pricing
		pricing = ModelPricing["claude-sonnet-4"]
	}

	cost := float64(inputTokens) * pricing.InputPerMillion / 1_000_000
	cost += float64(outputTokens) * pricing.OutputPerMillion / 1_000_000
	cost += float64(cacheRead) * pricing.CacheReadPerMillion / 1_000_000
	cost += float64(cacheWrite) * pricing.CacheWritePerMillion / 1_000_000
	return cost
}

func (t *Tracker) checkAlerts(total float64) {
	if t.budget <= 0 || t.alertFn == nil {
		return
	}

	pct := (total / t.budget) * 100

	t.mu.Lock()
	defer t.mu.Unlock()

	if pct >= 100 && !t.alerted[AlertCritical] {
		t.alerted[AlertCritical] = true
		t.alertFn(Alert{
			Level:      AlertCritical,
			Message:    fmt.Sprintf("Budget exceeded: $%.4f / $%.2f", total, t.budget),
			Budget:     t.budget,
			Spent:      total,
			Percentage: pct,
		})
	} else if pct >= 80 && !t.alerted[AlertWarning] {
		t.alerted[AlertWarning] = true
		t.alertFn(Alert{
			Level:      AlertWarning,
			Message:    fmt.Sprintf("Budget 80%% used: $%.4f / $%.2f", total, t.budget),
			Budget:     t.budget,
			Spent:      total,
			Percentage: pct,
		})
	} else if pct >= 50 && !t.alerted[AlertInfo] {
		t.alerted[AlertInfo] = true
		t.alertFn(Alert{
			Level:      AlertInfo,
			Message:    fmt.Sprintf("Budget 50%% used: $%.4f / $%.2f", total, t.budget),
			Budget:     t.budget,
			Spent:      total,
			Percentage: pct,
		})
	}
}
