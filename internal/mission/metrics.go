// Mission metrics tracks operational metrics for mission lifecycle events.
//
// Metrics are thread-safe and designed for aggregation across concurrent
// mission executions. They integrate with the existing metrics.Registry
// pattern for unified reporting.
package mission

import (
	"sync"
	"time"
)

// Metrics tracks operational statistics for mission execution.
// All methods are safe for concurrent use.
type Metrics struct {
	mu sync.Mutex

	// Mission counts
	MissionsCreated   int64 `json:"missions_created"`
	MissionsCompleted int64 `json:"missions_completed"`
	MissionsFailed    int64 `json:"missions_failed"`

	// Phase counts
	PhaseTransitions int64 `json:"phase_transitions"`

	// Convergence stats
	ConvergenceLoops  int64 `json:"convergence_loops"`
	ConvergenceMaxSeq int   `json:"convergence_max_seq"` // longest loop sequence

	// Validation stats
	GapsFound    int64 `json:"gaps_found"`
	GapsResolved int64 `json:"gaps_resolved"`
	GapsBlocking int64 `json:"gaps_blocking"`

	// Consensus stats
	ConsensusVotes    int64 `json:"consensus_votes"`
	ConsensusAccepted int64 `json:"consensus_accepted"`
	ConsensusRejected int64 `json:"consensus_rejected"`

	// Handoff stats
	HandoffCount int64 `json:"handoff_count"`

	// Research stats
	ResearchEntries int64 `json:"research_entries"`
	ResearchQueries int64 `json:"research_queries"`

	// Timing
	TotalDuration     time.Duration `json:"total_duration_ns"`
	PhaseDurations    map[string]time.Duration `json:"phase_durations_ns"`

	// Cost
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// NewMetrics creates a zeroed metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
		PhaseDurations: make(map[string]time.Duration),
	}
}

// RecordMissionCreated increments the created counter.
func (m *Metrics) RecordMissionCreated() {
	m.mu.Lock()
	m.MissionsCreated++
	m.mu.Unlock()
}

// RecordMissionCompleted increments the completed counter.
func (m *Metrics) RecordMissionCompleted() {
	m.mu.Lock()
	m.MissionsCompleted++
	m.mu.Unlock()
}

// RecordMissionFailed increments the failed counter.
func (m *Metrics) RecordMissionFailed() {
	m.mu.Lock()
	m.MissionsFailed++
	m.mu.Unlock()
}

// RecordPhaseTransition increments transition counter and accumulates phase duration.
func (m *Metrics) RecordPhaseTransition(phase string, duration time.Duration) {
	m.mu.Lock()
	m.PhaseTransitions++
	m.PhaseDurations[phase] += duration
	m.mu.Unlock()
}

// RecordConvergenceLoop records a convergence loop iteration.
func (m *Metrics) RecordConvergenceLoop(loopNumber int) {
	m.mu.Lock()
	m.ConvergenceLoops++
	if loopNumber > m.ConvergenceMaxSeq {
		m.ConvergenceMaxSeq = loopNumber
	}
	m.mu.Unlock()
}

// RecordGapFound increments the gap counter with severity tracking.
func (m *Metrics) RecordGapFound(blocking bool) {
	m.mu.Lock()
	m.GapsFound++
	if blocking {
		m.GapsBlocking++
	}
	m.mu.Unlock()
}

// RecordGapResolved increments the resolved gap counter.
func (m *Metrics) RecordGapResolved() {
	m.mu.Lock()
	m.GapsResolved++
	m.mu.Unlock()
}

// RecordConsensusVote tracks a consensus vote.
func (m *Metrics) RecordConsensusVote(accepted bool) {
	m.mu.Lock()
	m.ConsensusVotes++
	if accepted {
		m.ConsensusAccepted++
	} else {
		m.ConsensusRejected++
	}
	m.mu.Unlock()
}

// RecordHandoff increments the handoff counter.
func (m *Metrics) RecordHandoff() {
	m.mu.Lock()
	m.HandoffCount++
	m.mu.Unlock()
}

// RecordResearchEntry increments the research entry counter.
func (m *Metrics) RecordResearchEntry() {
	m.mu.Lock()
	m.ResearchEntries++
	m.mu.Unlock()
}

// RecordResearchQuery increments the research query counter.
func (m *Metrics) RecordResearchQuery() {
	m.mu.Lock()
	m.ResearchQueries++
	m.mu.Unlock()
}

// RecordDuration adds to the total duration.
func (m *Metrics) RecordDuration(d time.Duration) {
	m.mu.Lock()
	m.TotalDuration += d
	m.mu.Unlock()
}

// RecordCost adds to the total cost.
func (m *Metrics) RecordCost(usd float64) {
	m.mu.Lock()
	m.TotalCostUSD += usd
	m.mu.Unlock()
}

// Snapshot returns a copy of the current metrics state.
// The returned value does not share any mutable state with the original.
func (m *Metrics) Snapshot() *Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := &Metrics{
		MissionsCreated:   m.MissionsCreated,
		MissionsCompleted: m.MissionsCompleted,
		MissionsFailed:    m.MissionsFailed,
		PhaseTransitions:  m.PhaseTransitions,
		ConvergenceLoops:  m.ConvergenceLoops,
		ConvergenceMaxSeq: m.ConvergenceMaxSeq,
		GapsFound:         m.GapsFound,
		GapsResolved:      m.GapsResolved,
		GapsBlocking:      m.GapsBlocking,
		ConsensusVotes:    m.ConsensusVotes,
		ConsensusAccepted: m.ConsensusAccepted,
		ConsensusRejected: m.ConsensusRejected,
		HandoffCount:      m.HandoffCount,
		ResearchEntries:   m.ResearchEntries,
		ResearchQueries:   m.ResearchQueries,
		TotalDuration:     m.TotalDuration,
		TotalCostUSD:      m.TotalCostUSD,
		PhaseDurations:    make(map[string]time.Duration, len(m.PhaseDurations)),
	}
	for k, v := range m.PhaseDurations {
		snap.PhaseDurations[k] = v
	}
	return snap
}

// SuccessRate returns the fraction of missions that completed successfully.
// Returns 0 if no missions have finished.
func (m *Metrics) SuccessRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.MissionsCompleted + m.MissionsFailed
	if total == 0 {
		return 0
	}
	return float64(m.MissionsCompleted) / float64(total)
}

// GapResolutionRate returns the fraction of gaps that have been resolved.
func (m *Metrics) GapResolutionRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GapsFound == 0 {
		return 1.0
	}
	return float64(m.GapsResolved) / float64(m.GapsFound)
}
