package mission

import (
	"sync"
	"testing"
	"time"
)

func TestMetricsBasicCounts(t *testing.T) {
	m := NewMetrics()

	m.RecordMissionCreated()
	m.RecordMissionCreated()
	m.RecordMissionCompleted()
	m.RecordMissionFailed()

	snap := m.Snapshot()
	if snap.MissionsCreated != 2 {
		t.Errorf("MissionsCreated = %d, want 2", snap.MissionsCreated)
	}
	if snap.MissionsCompleted != 1 {
		t.Errorf("MissionsCompleted = %d, want 1", snap.MissionsCompleted)
	}
	if snap.MissionsFailed != 1 {
		t.Errorf("MissionsFailed = %d, want 1", snap.MissionsFailed)
	}
}

func TestMetricsPhaseTransitions(t *testing.T) {
	m := NewMetrics()

	m.RecordPhaseTransition("executing", 100*time.Millisecond)
	m.RecordPhaseTransition("executing", 200*time.Millisecond)
	m.RecordPhaseTransition("validating", 50*time.Millisecond)

	snap := m.Snapshot()
	if snap.PhaseTransitions != 3 {
		t.Errorf("PhaseTransitions = %d, want 3", snap.PhaseTransitions)
	}
	if snap.PhaseDurations["executing"] != 300*time.Millisecond {
		t.Errorf("executing duration = %v, want 300ms", snap.PhaseDurations["executing"])
	}
	if snap.PhaseDurations["validating"] != 50*time.Millisecond {
		t.Errorf("validating duration = %v, want 50ms", snap.PhaseDurations["validating"])
	}
}

func TestMetricsConvergence(t *testing.T) {
	m := NewMetrics()

	m.RecordConvergenceLoop(1)
	m.RecordConvergenceLoop(2)
	m.RecordConvergenceLoop(3)

	snap := m.Snapshot()
	if snap.ConvergenceLoops != 3 {
		t.Errorf("ConvergenceLoops = %d, want 3", snap.ConvergenceLoops)
	}
	if snap.ConvergenceMaxSeq != 3 {
		t.Errorf("ConvergenceMaxSeq = %d, want 3", snap.ConvergenceMaxSeq)
	}
}

func TestMetricsGaps(t *testing.T) {
	m := NewMetrics()

	m.RecordGapFound(true)  // blocking
	m.RecordGapFound(true)  // blocking
	m.RecordGapFound(false) // non-blocking
	m.RecordGapResolved()
	m.RecordGapResolved()

	snap := m.Snapshot()
	if snap.GapsFound != 3 {
		t.Errorf("GapsFound = %d, want 3", snap.GapsFound)
	}
	if snap.GapsBlocking != 2 {
		t.Errorf("GapsBlocking = %d, want 2", snap.GapsBlocking)
	}
	if snap.GapsResolved != 2 {
		t.Errorf("GapsResolved = %d, want 2", snap.GapsResolved)
	}
}

func TestMetricsConsensus(t *testing.T) {
	m := NewMetrics()

	m.RecordConsensusVote(true)
	m.RecordConsensusVote(true)
	m.RecordConsensusVote(false)

	snap := m.Snapshot()
	if snap.ConsensusVotes != 3 {
		t.Errorf("ConsensusVotes = %d, want 3", snap.ConsensusVotes)
	}
	if snap.ConsensusAccepted != 2 {
		t.Errorf("ConsensusAccepted = %d, want 2", snap.ConsensusAccepted)
	}
	if snap.ConsensusRejected != 1 {
		t.Errorf("ConsensusRejected = %d, want 1", snap.ConsensusRejected)
	}
}

func TestMetricsSuccessRate(t *testing.T) {
	m := NewMetrics()
	if m.SuccessRate() != 0 {
		t.Error("should be 0 with no missions")
	}

	m.RecordMissionCompleted()
	m.RecordMissionCompleted()
	m.RecordMissionFailed()

	rate := m.SuccessRate()
	if rate < 0.66 || rate > 0.67 {
		t.Errorf("SuccessRate = %f, want ~0.667", rate)
	}
}

func TestMetricsGapResolutionRate(t *testing.T) {
	m := NewMetrics()
	if m.GapResolutionRate() != 1.0 {
		t.Error("should be 1.0 with no gaps")
	}

	m.RecordGapFound(false)
	m.RecordGapFound(false)
	m.RecordGapResolved()

	rate := m.GapResolutionRate()
	if rate != 0.5 {
		t.Errorf("GapResolutionRate = %f, want 0.5", rate)
	}
}

func TestMetricsConcurrency(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RecordMissionCreated()
			m.RecordGapFound(true)
			m.RecordConvergenceLoop(1)
			m.RecordHandoff()
			m.RecordResearchEntry()
			m.RecordCost(0.01)
		}()
	}
	wg.Wait()

	snap := m.Snapshot()
	if snap.MissionsCreated != 100 {
		t.Errorf("MissionsCreated = %d, want 100", snap.MissionsCreated)
	}
	if snap.GapsFound != 100 {
		t.Errorf("GapsFound = %d, want 100", snap.GapsFound)
	}
	if snap.HandoffCount != 100 {
		t.Errorf("HandoffCount = %d, want 100", snap.HandoffCount)
	}
}

func TestMetricsSnapshotIsolation(t *testing.T) {
	m := NewMetrics()
	m.RecordPhaseTransition("plan", time.Second)

	snap := m.Snapshot()
	snap.PhaseDurations["plan"] = 99 * time.Second // mutate snapshot

	original := m.Snapshot()
	if original.PhaseDurations["plan"] != time.Second {
		t.Error("snapshot mutation should not affect original")
	}
}

func TestMetricsCostAndDuration(t *testing.T) {
	m := NewMetrics()
	m.RecordCost(1.50)
	m.RecordCost(0.75)
	m.RecordDuration(time.Second)
	m.RecordDuration(2 * time.Second)

	snap := m.Snapshot()
	if snap.TotalCostUSD != 2.25 {
		t.Errorf("TotalCostUSD = %f, want 2.25", snap.TotalCostUSD)
	}
	if snap.TotalDuration != 3*time.Second {
		t.Errorf("TotalDuration = %v, want 3s", snap.TotalDuration)
	}
}

func TestMetricsResearch(t *testing.T) {
	m := NewMetrics()
	m.RecordResearchEntry()
	m.RecordResearchEntry()
	m.RecordResearchQuery()

	snap := m.Snapshot()
	if snap.ResearchEntries != 2 {
		t.Errorf("ResearchEntries = %d, want 2", snap.ResearchEntries)
	}
	if snap.ResearchQueries != 1 {
		t.Errorf("ResearchQueries = %d, want 1", snap.ResearchQueries)
	}
}
