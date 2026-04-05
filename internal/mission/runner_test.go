package mission

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func newTestRunner(t *testing.T) (*Runner, *Store) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	runner := NewRunner(store, DefaultRunnerConfig())
	return runner, store
}

func createTestMission(t *testing.T, store *Store, id string) *Mission {
	t.Helper()
	m := &Mission{
		ID:     id,
		Title:  "Test Mission",
		Intent: "Test the convergence loop",
		Criteria: []Criterion{
			{ID: "c-1", Description: "Feature implemented"},
			{ID: "c-2", Description: "Tests passing"},
		},
	}
	if err := store.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return m
}

// noopHandler returns a handler that always succeeds.
func noopHandler(summary string) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		return &PhaseResult{Summary: summary}, nil
	}
}

// failHandler returns a handler that always fails.
func failHandler(msg string) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		return nil, fmt.Errorf("%s", msg)
	}
}

// --- Happy Path: Full Lifecycle ---

func TestRunnerHappyPath(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-happy")

	// Register handlers for all phases
	runner.RegisterHandler(PhaseCreated, noopHandler("research done"))
	runner.RegisterHandler(PhaseResearching, noopHandler("plan created"))
	runner.RegisterHandler(PhasePlanning, noopHandler("code written"))

	// Executing handler satisfies criteria
	runner.RegisterHandler(PhaseExecuting, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.SetCriteriaSatisfied(m.ID, "c-1", "feature.go implements it", "agent-1")
		store.SetCriteriaSatisfied(m.ID, "c-2", "tests pass", "agent-1")
		return &PhaseResult{Summary: "all criteria satisfied"}, nil
	})

	// Validating handler — just let the runner check convergence status
	runner.RegisterHandler(PhaseValidating, noopHandler("validation passed"))

	// Consensus handler records two "complete" votes
	runner.RegisterHandler(PhaseConverged, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.RecordConsensus(&ConsensusRecord{
			MissionID: m.ID, Model: "claude", Verdict: "complete", Reasoning: "all good",
		})
		store.RecordConsensus(&ConsensusRecord{
			MissionID: m.ID, Model: "codex", Verdict: "complete", Reasoning: "confirmed",
		})
		return &PhaseResult{Summary: "consensus reached"}, nil
	})

	result, err := runner.Run(context.Background(), "m-happy")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success, got %s", result.FinalPhase)
	}
	if len(result.Phases) < 5 {
		t.Errorf("expected at least 5 phases, got %d", len(result.Phases))
	}

	// Verify mission is in Completed state
	got, _ := store.Get("m-happy")
	if got.Phase != PhaseCompleted {
		t.Errorf("mission phase = %s, want completed", got.Phase)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
}

// --- Convergence Loop ---

func TestRunnerConvergenceLoop(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-loop")

	var executeCount int32

	// Auto-advance through early phases
	runner.RegisterHandler(PhaseCreated, noopHandler("researched"))
	runner.RegisterHandler(PhaseResearching, noopHandler("planned"))
	runner.RegisterHandler(PhasePlanning, noopHandler("ready"))

	// First execution leaves criteria unsatisfied, second satisfies them
	runner.RegisterHandler(PhaseExecuting, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		count := atomic.AddInt32(&executeCount, 1)
		if count >= 2 {
			// Second time: satisfy all criteria
			store.SetCriteriaSatisfied(m.ID, "c-1", "done", "agent")
			store.SetCriteriaSatisfied(m.ID, "c-2", "done", "agent")
			// Resolve any gaps
			gaps, _ := store.OpenGaps(m.ID)
			for _, g := range gaps {
				store.ResolveGap(m.ID, g.ID)
			}
		}
		return &PhaseResult{Summary: fmt.Sprintf("execute #%d", count)}, nil
	})

	// Validation handler adds a gap on first pass
	runner.RegisterHandler(PhaseValidating, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		status, _ := store.GetConvergenceStatus(m.ID, 2)
		if !status.IsConverged {
			store.AddGap(&Gap{
				ID: "g-1", MissionID: m.ID, Category: "test",
				Severity: "blocking", Description: "tests not passing",
			})
		}
		return &PhaseResult{Summary: "validated"}, nil
	})

	// Consensus auto-approves
	runner.RegisterHandler(PhaseConverged, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "claude", Verdict: "complete"})
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "codex", Verdict: "complete"})
		return &PhaseResult{Summary: "consensus"}, nil
	})

	var loopCount int32
	runner.config.OnConvergenceLoop = func(missionID string, iteration, gapCount int) {
		atomic.AddInt32(&loopCount, 1)
	}

	result, err := runner.Run(context.Background(), "m-loop")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success, got %s", result.FinalPhase)
	}
	if result.ConvergenceLoops < 1 {
		t.Errorf("expected at least 1 convergence loop, got %d", result.ConvergenceLoops)
	}
	if atomic.LoadInt32(&executeCount) < 2 {
		t.Errorf("expected at least 2 executions, got %d", atomic.LoadInt32(&executeCount))
	}
	if atomic.LoadInt32(&loopCount) == 0 {
		t.Error("OnConvergenceLoop callback should have fired")
	}
}

// --- Convergence Loop Exhaustion ---

func TestRunnerConvergenceExhaustion(t *testing.T) {
	runner, store := newTestRunner(t)
	runner.config.MaxConvergenceLoops = 2
	createTestMission(t, store, "m-exhaust")

	runner.RegisterHandler(PhaseCreated, noopHandler("r"))
	runner.RegisterHandler(PhaseResearching, noopHandler("p"))
	runner.RegisterHandler(PhasePlanning, noopHandler("e"))

	// Execute never satisfies criteria
	runner.RegisterHandler(PhaseExecuting, noopHandler("still failing"))

	// Validation always finds gaps
	runner.RegisterHandler(PhaseValidating, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.AddGap(&Gap{
			ID: fmt.Sprintf("g-%d", time.Now().UnixNano()), MissionID: m.ID,
			Category: "test", Severity: "blocking", Description: "still broken",
		})
		return &PhaseResult{Summary: "gaps remain"}, nil
	})

	result, err := runner.Run(context.Background(), "m-exhaust")
	if err == nil {
		t.Fatal("expected error from convergence exhaustion")
	}
	if result.FinalPhase != PhaseFailed {
		t.Errorf("expected Failed, got %s", result.FinalPhase)
	}

	// Verify mission is Failed in store
	got, _ := store.Get("m-exhaust")
	if got.Phase != PhaseFailed {
		t.Errorf("stored phase = %s, want failed", got.Phase)
	}
}

// --- Phase Failure ---

func TestRunnerPhaseFailure(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-fail")

	runner.RegisterHandler(PhaseCreated, noopHandler("ok"))
	runner.RegisterHandler(PhaseResearching, failHandler("research API down"))

	result, err := runner.Run(context.Background(), "m-fail")
	if err == nil {
		t.Fatal("expected error from failed phase")
	}
	if result.FinalPhase != PhaseFailed {
		t.Errorf("expected Failed, got %s", result.FinalPhase)
	}

	// Verify transition audit trail
	transitions, _ := store.Transitions("m-fail")
	foundFailed := false
	for _, tr := range transitions {
		if tr.ToPhase == PhaseFailed {
			foundFailed = true
			if tr.Reason == "" {
				t.Error("failure transition should have a reason")
			}
		}
	}
	if !foundFailed {
		t.Error("should have a transition to Failed in audit trail")
	}
}

// --- Auto-advance (No Handler) ---

func TestRunnerAutoAdvanceNoHandlers(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-auto")

	// No handlers registered. Runner auto-advances through phases.
	// With 2 unsatisfied criteria and no handlers to satisfy them,
	// convergence check returns IsConverged=false, so it loops back
	// to Executing until convergence exhaustion → Failed.
	// This is correct: you can't converge without satisfying criteria.
	result, err := runner.Run(context.Background(), "m-auto")
	if err == nil {
		t.Fatal("expected error from convergence exhaustion")
	}
	if result.FinalPhase != PhaseFailed {
		t.Errorf("expected Failed (unsatisfied criteria), got %s", result.FinalPhase)
	}
}

// --- Context Cancellation ---

func TestRunnerContextCancellation(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-cancel")

	ctx, cancel := context.WithCancel(context.Background())

	// Handler that blocks until context is cancelled
	runner.RegisterHandler(PhaseCreated, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		cancel() // cancel immediately
		return &PhaseResult{Summary: "started"}, nil
	})

	result, err := runner.Run(ctx, "m-cancel")
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	_ = result
}

// --- Phase Timeout ---

func TestRunnerPhaseTimeout(t *testing.T) {
	runner, store := newTestRunner(t)
	runner.config.PhaseTimeout = 50 * time.Millisecond
	createTestMission(t, store, "m-timeout")

	runner.RegisterHandler(PhaseCreated, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		// Block longer than timeout
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &PhaseResult{Summary: "should not reach"}, nil
		}
	})

	result, err := runner.Run(context.Background(), "m-timeout")
	if err == nil {
		t.Fatal("expected error from timeout")
	}
	if result.FinalPhase != PhaseFailed {
		t.Errorf("expected Failed, got %s", result.FinalPhase)
	}
}

// --- Nonexistent Mission ---

func TestRunnerNonexistentMission(t *testing.T) {
	runner, _ := newTestRunner(t)
	_, err := runner.Run(context.Background(), "ghost")
	if err == nil {
		t.Error("should error for nonexistent mission")
	}
}

// --- Already Terminal ---

func TestRunnerAlreadyCompleted(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-done")

	// Manually advance to Completed
	store.Advance("m-done", PhaseResearching, "r", "test")
	store.Advance("m-done", PhasePlanning, "p", "test")
	store.Advance("m-done", PhaseExecuting, "e", "test")
	store.Advance("m-done", PhaseValidating, "v", "test")
	store.Advance("m-done", PhaseConverged, "c", "test")
	store.Advance("m-done", PhaseCompleted, "done", "test")

	result, err := runner.Run(context.Background(), "m-done")
	if err != nil {
		t.Fatalf("Run on completed mission: %v", err)
	}
	if result.FinalPhase != PhaseCompleted {
		t.Errorf("expected Completed, got %s", result.FinalPhase)
	}
	if len(result.Phases) != 0 {
		t.Errorf("should not have run any phases, got %d", len(result.Phases))
	}
}

// --- Consensus Rejection ---

func TestRunnerConsensusRejection(t *testing.T) {
	runner, store := newTestRunner(t)
	runner.config.MaxConvergenceLoops = 3
	m := createTestMission(t, store, "m-reject")

	runner.RegisterHandler(PhaseCreated, noopHandler("r"))
	runner.RegisterHandler(PhaseResearching, noopHandler("p"))
	runner.RegisterHandler(PhasePlanning, noopHandler("e"))

	var execCount int32
	runner.RegisterHandler(PhaseExecuting, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		count := atomic.AddInt32(&execCount, 1)
		store.SetCriteriaSatisfied(m.ID, "c-1", "done", "agent")
		store.SetCriteriaSatisfied(m.ID, "c-2", "done", "agent")
		// Resolve gaps on second+ execution
		if count >= 2 {
			gaps, _ := store.OpenGaps(m.ID)
			for _, g := range gaps {
				store.ResolveGap(m.ID, g.ID)
			}
		}
		return &PhaseResult{Summary: fmt.Sprintf("exec #%d", count)}, nil
	})

	runner.RegisterHandler(PhaseValidating, noopHandler("valid"))

	// First consensus rejects, second accepts
	var consensusCount int32
	runner.RegisterHandler(PhaseConverged, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		count := atomic.AddInt32(&consensusCount, 1)
		if count == 1 {
			store.RecordConsensus(&ConsensusRecord{
				MissionID: m.ID, Model: "claude", Verdict: "reject",
				Reasoning: "missing edge case test",
				GapsFound: []string{"g-edge"},
			})
			return &PhaseResult{Summary: "rejected"}, nil
		}
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "claude", Verdict: "complete"})
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "codex", Verdict: "complete"})
		return &PhaseResult{Summary: "accepted"}, nil
	})

	result, err := runner.Run(context.Background(), "m-reject")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success after retry, got %s", result.FinalPhase)
	}
	if atomic.LoadInt32(&execCount) < 2 {
		t.Errorf("expected at least 2 executions, got %d", atomic.LoadInt32(&execCount))
	}
	_ = m
}

// --- Callbacks ---

func TestRunnerCallbacks(t *testing.T) {
	runner, store := newTestRunner(t)
	m := createTestMission(t, store, "m-cb")

	// Remove criteria so we can converge easily
	store.Delete("m-cb")
	store.Create(&Mission{
		ID: "m-cb", Title: "Callback Test", Intent: "test callbacks",
		Criteria: []Criterion{{ID: "c-1", Description: "done"}},
	})

	runner.RegisterHandler(PhaseCreated, noopHandler("r"))
	runner.RegisterHandler(PhaseResearching, noopHandler("p"))
	runner.RegisterHandler(PhasePlanning, noopHandler("e"))
	runner.RegisterHandler(PhaseExecuting, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.SetCriteriaSatisfied(m.ID, "c-1", "yes", "a")
		return &PhaseResult{Summary: "done"}, nil
	})
	runner.RegisterHandler(PhaseValidating, noopHandler("v"))
	runner.RegisterHandler(PhaseConverged, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "a", Verdict: "complete"})
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "b", Verdict: "complete"})
		return &PhaseResult{Summary: "c"}, nil
	})

	var phaseCompleteCount int32
	var missionCompleteCount int32

	runner.config.OnPhaseComplete = func(missionID string, result *PhaseResult) {
		atomic.AddInt32(&phaseCompleteCount, 1)
	}
	runner.config.OnMissionComplete = func(missionID string, phase Phase, summary string) {
		atomic.AddInt32(&missionCompleteCount, 1)
		if phase != PhaseCompleted {
			t.Errorf("OnMissionComplete phase = %s, want completed", phase)
		}
	}

	result, err := runner.Run(context.Background(), "m-cb")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success, got %s", result.FinalPhase)
	}
	if atomic.LoadInt32(&phaseCompleteCount) == 0 {
		t.Error("OnPhaseComplete should have been called")
	}
	if atomic.LoadInt32(&missionCompleteCount) != 1 {
		t.Errorf("OnMissionComplete called %d times, want 1", atomic.LoadInt32(&missionCompleteCount))
	}
	_ = m
}

// --- RunSummary ---

func TestRunSummary(t *testing.T) {
	s := &RunSummary{
		MissionID:        "m-1",
		FinalPhase:       PhaseCompleted,
		Phases:           []PhaseResult{{Phase: PhaseCreated}, {Phase: PhaseResearching}},
		ConvergenceLoops: 1,
		TotalDuration:    2 * time.Second,
	}

	if !s.IsSuccess() {
		t.Error("should be success")
	}
	if s.IsFailed() {
		t.Error("should not be failed")
	}
	summary := s.Summary()
	if summary == "" {
		t.Error("summary should not be empty")
	}

	s2 := &RunSummary{FinalPhase: PhaseFailed}
	if s2.IsSuccess() {
		t.Error("should not be success")
	}
	if !s2.IsFailed() {
		t.Error("should be failed")
	}
}

// --- Nil Store Panics ---

func TestNewRunnerPanicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewRunner(nil) should panic")
		}
	}()
	NewRunner(nil, DefaultRunnerConfig())
}

// --- Nil Handler Panics ---

func TestRegisterNilHandlerPanics(t *testing.T) {
	runner, _ := newTestRunner(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterHandler(nil) should panic")
		}
	}()
	runner.RegisterHandler(PhaseCreated, nil)
}

// --- Resume ---

func TestRunnerResume(t *testing.T) {
	runner, store := newTestRunner(t)
	createTestMission(t, store, "m-resume")

	// Manually advance to Executing (simulating a crash mid-run)
	store.Advance("m-resume", PhaseResearching, "r", "test")
	store.Advance("m-resume", PhasePlanning, "p", "test")
	store.Advance("m-resume", PhaseExecuting, "e", "test")

	// Now register only the handlers needed from Executing onwards
	runner.RegisterHandler(PhaseExecuting, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.SetCriteriaSatisfied(m.ID, "c-1", "done", "a")
		store.SetCriteriaSatisfied(m.ID, "c-2", "done", "a")
		return &PhaseResult{Summary: "resumed and finished"}, nil
	})
	runner.RegisterHandler(PhaseValidating, noopHandler("valid"))
	runner.RegisterHandler(PhaseConverged, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "a", Verdict: "complete"})
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "b", Verdict: "complete"})
		return &PhaseResult{Summary: "consensus"}, nil
	})

	result, err := runner.Resume(context.Background(), "m-resume")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success after resume, got %s", result.FinalPhase)
	}
}

// --- DefaultRunnerConfig ---

func TestDefaultRunnerConfig(t *testing.T) {
	cfg := DefaultRunnerConfig()
	if cfg.MaxConvergenceLoops != 10 {
		t.Errorf("MaxConvergenceLoops = %d", cfg.MaxConvergenceLoops)
	}
	if cfg.RequiredConsensus != 2 {
		t.Errorf("RequiredConsensus = %d", cfg.RequiredConsensus)
	}
	if cfg.MaxPhaseRetries != 3 {
		t.Errorf("MaxPhaseRetries = %d", cfg.MaxPhaseRetries)
	}
}

func TestRunnerPersistsFilesChanged(t *testing.T) {
	runner, store := newTestRunner(t)
	m := createTestMission(t, store, "m-files")

	// Research and plan just pass through
	runner.RegisterHandler(PhaseCreated, noopHandler("research done"))
	runner.RegisterHandler(PhaseResearching, noopHandler("plan ready"))

	// Planning returns successfully
	runner.RegisterHandler(PhasePlanning, noopHandler("plan done"))

	// Execute handler reports files changed
	runner.RegisterHandler(PhaseExecuting, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.SetCriteriaSatisfied(m.ID, "c-1", "done", "test")
		store.SetCriteriaSatisfied(m.ID, "c-2", "done", "test")
		return &PhaseResult{
			Summary:      "executed",
			FilesChanged: []string{"internal/auth/jwt.go", "internal/auth/jwt_test.go"},
		}, nil
	})

	runner.RegisterHandler(PhaseValidating, noopHandler("validated"))
	runner.RegisterHandler(PhaseConverged, func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "a", Verdict: "complete", Reasoning: "ok"})
		store.RecordConsensus(&ConsensusRecord{MissionID: m.ID, Model: "b", Verdict: "complete", Reasoning: "ok"})
		return &PhaseResult{Summary: "consensus"}, nil
	})

	_, err := runner.Run(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify files_changed was persisted in metadata
	updated, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	files, ok := updated.Metadata["files_changed"]
	if !ok {
		t.Fatal("files_changed not in metadata")
	}
	if files != "internal/auth/jwt.go\ninternal/auth/jwt_test.go" {
		t.Errorf("unexpected files_changed: %q", files)
	}
}
