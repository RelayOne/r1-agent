// cost.go: Unit-cost tracking helpers for Browserless (spec C6 T12).
//
// Browserless bills by "Unit" = 30s of browser-time per session per
// their pricing page (2026-05). costUnits in client.go is the
// formula; this file exposes a small adapter so the embedder can
// wire `internal/costtrack/` without importing the costtrack types
// into this package (keeps the dep graph flat).

package browserless

import (
	"sync"

	"github.com/RelayOne/r1/internal/browser"
)

// TrackerCostSink is a small adapter satisfying CostSink. The
// embedder constructs it with a Record callback that bridges into
// internal/costtrack/. Tests use an InMemoryCostSink instead.
//
// All three callbacks may be nil; the unset ones default to no-op
// implementations.
type TrackerCostSink struct {
	// RecordFn is called on every Session.Close. Args mirror the
	// CostSink interface signature.
	RecordFn func(provider, tenantID, missionID string, units int, durationSec float64)
	// OverBudgetFn is called from Provider.Open. Returns true to
	// fail-fast with browser.ErrBudgetExhausted.
	OverBudgetFn func(missionID string) bool
}

// RecordSession forwards to RecordFn or no-ops.
func (t *TrackerCostSink) RecordSession(provider, tenantID, missionID string, units int, durationSec float64) {
	if t.RecordFn != nil {
		t.RecordFn(provider, tenantID, missionID, units, durationSec)
	}
}

// OverBudget forwards to OverBudgetFn or returns false.
func (t *TrackerCostSink) OverBudget(missionID string) bool {
	if t.OverBudgetFn != nil {
		return t.OverBudgetFn(missionID)
	}
	return false
}

// Compile-time assertion.
var _ CostSink = (*TrackerCostSink)(nil)

// InMemoryCostSink is a thread-safe sink for tests. Aggregates units
// per tenant + per mission and provides a SetBudget knob to drive
// the OverBudget path.
type InMemoryCostSink struct {
	mu sync.Mutex
	// rows mirror what `internal/costtrack/` writes; we keep them
	// simple so tests can assert on them directly.
	rows []CostRow
	// missionBudgets[missionID] = max units; -1 = unlimited.
	missionBudgets map[string]int
	missionSpent   map[string]int
}

// CostRow is one Session.Close record. Mirrors the spec's expected
// attribute set: every record carries tenant + mission for
// non-negotiable attribution.
type CostRow struct {
	Provider    string
	TenantID    string
	MissionID   string
	Units       int
	DurationSec float64
}

// NewInMemoryCostSink returns a fresh sink with no budgets configured.
func NewInMemoryCostSink() *InMemoryCostSink {
	return &InMemoryCostSink{
		missionBudgets: make(map[string]int),
		missionSpent:   make(map[string]int),
	}
}

// SetBudget records a per-mission Unit budget. Pass -1 to remove.
func (s *InMemoryCostSink) SetBudget(missionID string, units int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.missionBudgets[missionID] = units
}

// RecordSession accumulates units.
func (s *InMemoryCostSink) RecordSession(provider, tenantID, missionID string, units int, durationSec float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, CostRow{
		Provider:    provider,
		TenantID:    tenantID,
		MissionID:   missionID,
		Units:       units,
		DurationSec: durationSec,
	})
	if missionID != "" {
		s.missionSpent[missionID] += units
	}
}

// OverBudget reports whether the mission has exhausted its budget.
func (s *InMemoryCostSink) OverBudget(missionID string) bool {
	if missionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cap, ok := s.missionBudgets[missionID]
	if !ok || cap < 0 {
		return false
	}
	return s.missionSpent[missionID] >= cap
}

// Rows returns a stable snapshot of recorded cost rows.
func (s *InMemoryCostSink) Rows() []CostRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CostRow, len(s.rows))
	copy(out, s.rows)
	return out
}

// UnitsByTenant returns the total Units billed per tenant.
func (s *InMemoryCostSink) UnitsByTenant() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]int)
	for _, r := range s.rows {
		m[r.TenantID] += r.Units
	}
	return m
}

var _ browser.Provider = (*Client)(nil) // re-assert here for symmetry
var _ CostSink = (*InMemoryCostSink)(nil)
