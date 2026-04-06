// Package cost provides per-task budget enforcement and cost aggregation
// for the benchmark framework.
package cost

import (
	"fmt"
	"sync"
)

// BudgetEnforcer tracks spending per (task, harness) pair and enforces
// per-task budget limits.
type BudgetEnforcer struct {
	mu       sync.Mutex
	limit    float64
	spending map[string]float64 // key: "task:harness"
}

// NewBudgetEnforcer creates a budget enforcer with the given per-task
// spending limit in USD.
func NewBudgetEnforcer(limitUSD float64) *BudgetEnforcer {
	return &BudgetEnforcer{
		limit:    limitUSD,
		spending: make(map[string]float64),
	}
}

// key builds the map key for a (task, harness) pair.
func key(taskID, harness string) string {
	return taskID + ":" + harness
}

// Record adds a cost entry for the given task and harness.
func (b *BudgetEnforcer) Record(taskID, harness string, costUSD float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spending[key(taskID, harness)] += costUSD
}

// Spent returns the total amount spent for a (task, harness) pair.
func (b *BudgetEnforcer) Spent(taskID, harness string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spending[key(taskID, harness)]
}

// Exhausted reports whether the budget for this (task, harness) pair
// has been fully consumed.
func (b *BudgetEnforcer) Exhausted(taskID, harness string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spending[key(taskID, harness)] >= b.limit
}

// Remaining returns the budget remaining for a (task, harness) pair.
// Returns zero if the budget is exhausted.
func (b *BudgetEnforcer) Remaining(taskID, harness string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	rem := b.limit - b.spending[key(taskID, harness)]
	if rem < 0 {
		return 0
	}
	return rem
}

// Limit returns the per-task budget limit.
func (b *BudgetEnforcer) Limit() float64 {
	return b.limit
}

// Summary returns a human-readable summary of all tracked spending.
func (b *BudgetEnforcer) Summary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0.0
	for _, v := range b.spending {
		total += v
	}
	return fmt.Sprintf("budget enforcer: %d entries, total $%.4f, limit $%.4f/task",
		len(b.spending), total, b.limit)
}
