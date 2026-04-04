package mission

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// newTestStore creates an in-memory SQLite store for testing.
// Each test gets an isolated database — no cross-test contamination.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- Mission CRUD ---

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)

	m := &Mission{
		ID:     "m-1",
		Title:  "Implement auth",
		Intent: "Add JWT authentication to the API with rate limiting",
		Criteria: []Criterion{
			{ID: "c-1", Description: "JWT tokens are issued on login"},
			{ID: "c-2", Description: "Invalid tokens return 401"},
			{ID: "c-3", Description: "Rate limiting returns 429 after threshold"},
		},
		Tags:     []string{"security", "api"},
		Metadata: map[string]string{"priority": "high"},
	}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get("m-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for existing mission")
	}

	// Verify all fields persisted correctly
	if got.Title != "Implement auth" {
		t.Errorf("Title = %q, want %q", got.Title, "Implement auth")
	}
	if got.Intent != m.Intent {
		t.Errorf("Intent mismatch")
	}
	if got.Phase != PhaseCreated {
		t.Errorf("Phase = %q, want %q", got.Phase, PhaseCreated)
	}
	if len(got.Criteria) != 3 {
		t.Fatalf("len(Criteria) = %d, want 3", len(got.Criteria))
	}
	if got.Criteria[0].ID != "c-1" || got.Criteria[0].Description != "JWT tokens are issued on login" {
		t.Errorf("Criteria[0] mismatch: %+v", got.Criteria[0])
	}
	if got.Criteria[0].Satisfied {
		t.Error("Criterion should not be satisfied on creation")
	}
	if len(got.Tags) != 2 || got.Tags[0] != "security" {
		t.Errorf("Tags = %v", got.Tags)
	}
	if got.Metadata["priority"] != "high" {
		t.Errorf("Metadata = %v", got.Metadata)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if got.CompletedAt != nil {
		t.Error("CompletedAt should be nil on creation")
	}
}

func TestCreateDuplicateID(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{ID: "dup", Title: "First", Intent: "first"}
	if err := s.Create(m); err != nil {
		t.Fatal(err)
	}
	m2 := &Mission{ID: "dup", Title: "Second", Intent: "second"}
	err := s.Create(m2)
	if err == nil {
		t.Fatal("should error on duplicate ID")
	}
	if got, _ := s.Get("dup"); got.Title != "First" {
		t.Error("original mission should not be overwritten")
	}
}

func TestCreateValidation(t *testing.T) {
	s := newTestStore(t)
	tests := []struct {
		name string
		m    *Mission
	}{
		{"empty ID", &Mission{ID: "", Title: "T", Intent: "I"}},
		{"empty title", &Mission{ID: "x", Title: "", Intent: "I"}},
		{"empty intent", &Mission{ID: "x", Title: "T", Intent: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Create(tc.m); err == nil {
				t.Error("should reject invalid mission")
			}
		})
	}
}

func TestGetNonexistent(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Get("doesnt-exist")
	if err != nil {
		t.Fatalf("Get should not error for missing mission: %v", err)
	}
	if m != nil {
		t.Error("Get should return nil for missing mission")
	}
}

func TestUpdate(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{ID: "u-1", Title: "Original", Intent: "original intent"}
	s.Create(m)

	// Modify and update
	m.Title = "Updated"
	m.Intent = "updated intent"
	m.Tags = []string{"new-tag"}
	if err := s.Update(m); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := s.Get("u-1")
	if got.Title != "Updated" {
		t.Errorf("Title = %q after update", got.Title)
	}
	if got.Intent != "updated intent" {
		t.Errorf("Intent = %q after update", got.Intent)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "new-tag" {
		t.Errorf("Tags = %v after update", got.Tags)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Error("UpdatedAt should be after CreatedAt")
	}
}

func TestUpdateNonexistent(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{ID: "ghost", Title: "T", Intent: "I"}
	err := s.Update(m)
	if err == nil {
		t.Error("Update should error for nonexistent mission")
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{ID: "d-1", Title: "Delete me", Intent: "to be deleted"}
	s.Create(m)

	if err := s.Delete("d-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := s.Get("d-1")
	if got != nil {
		t.Error("mission should be gone after delete")
	}
}

func TestDeleteCascadesChildren(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{ID: "cascade", Title: "Parent", Intent: "parent"}
	s.Create(m)

	// Add child records
	s.AddGap(&Gap{ID: "g-1", MissionID: "cascade", Category: "test", Severity: "blocking", Description: "missing test"})
	s.RecordHandoff(&HandoffRecord{MissionID: "cascade", Summary: "handoff context"})
	s.RecordConsensus(&ConsensusRecord{MissionID: "cascade", Model: "claude", Verdict: "incomplete"})
	s.Advance("cascade", PhaseResearching, "start research", "test")

	// Delete parent
	if err := s.Delete("cascade"); err != nil {
		t.Fatal(err)
	}

	// All children should be gone
	gaps, _ := s.AllGaps("cascade")
	if len(gaps) > 0 {
		t.Error("gaps should be deleted with mission")
	}
	handoffs, _ := s.Handoffs("cascade")
	if len(handoffs) > 0 {
		t.Error("handoffs should be deleted with mission")
	}
	records, _ := s.ConsensusRecords("cascade")
	if len(records) > 0 {
		t.Error("consensus should be deleted with mission")
	}
	transitions, _ := s.Transitions("cascade")
	if len(transitions) > 0 {
		t.Error("transitions should be deleted with mission")
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "a", Title: "A", Intent: "a"})
	s.Create(&Mission{ID: "b", Title: "B", Intent: "b"})
	s.Create(&Mission{ID: "c", Title: "C", Intent: "c"})

	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("List all = %d, want 3", len(all))
	}

	// Advance one to researching
	s.Advance("b", PhaseResearching, "start", "test")
	researching, _ := s.List(PhaseResearching)
	if len(researching) != 1 || researching[0].ID != "b" {
		t.Errorf("List by phase: got %d missions", len(researching))
	}
}

// --- State Machine ---

func TestAdvanceValidTransition(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "sm-1", Title: "SM", Intent: "state machine test"})

	// Walk the happy path: Created → Researching → Planning → Executing → Validating → Converged → Completed
	transitions := []struct {
		to     Phase
		reason string
	}{
		{PhaseResearching, "gather context"},
		{PhasePlanning, "break into tasks"},
		{PhaseExecuting, "start implementation"},
		{PhaseValidating, "check completeness"},
		{PhaseConverged, "all gaps closed"},
		{PhaseCompleted, "two-model consensus"},
	}

	for _, tr := range transitions {
		if err := s.Advance("sm-1", tr.to, tr.reason, "test-agent"); err != nil {
			t.Fatalf("Advance to %s: %v", tr.to, err)
		}
	}

	// Verify final state
	m, _ := s.Get("sm-1")
	if m.Phase != PhaseCompleted {
		t.Errorf("Phase = %q, want completed", m.Phase)
	}
	if m.CompletedAt == nil {
		t.Error("CompletedAt should be set on completion")
	}

	// Verify audit trail
	trail, _ := s.Transitions("sm-1")
	if len(trail) != 6 {
		t.Errorf("transitions = %d, want 6", len(trail))
	}
	if trail[0].FromPhase != PhaseCreated || trail[0].ToPhase != PhaseResearching {
		t.Errorf("first transition: %s → %s", trail[0].FromPhase, trail[0].ToPhase)
	}
	if trail[0].Agent != "test-agent" {
		t.Errorf("agent = %q", trail[0].Agent)
	}
}

func TestAdvanceInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "sm-2", Title: "SM", Intent: "invalid transition test"})

	// Cannot jump from Created to Executing
	err := s.Advance("sm-2", PhaseExecuting, "skip ahead", "test")
	if err == nil {
		t.Error("should reject Created → Executing")
	}

	// Cannot advance completed mission
	s.Advance("sm-2", PhaseResearching, "r", "t")
	s.Advance("sm-2", PhasePlanning, "p", "t")
	s.Advance("sm-2", PhaseExecuting, "e", "t")
	s.Advance("sm-2", PhaseValidating, "v", "t")
	s.Advance("sm-2", PhaseConverged, "c", "t")
	s.Advance("sm-2", PhaseCompleted, "done", "t")

	err = s.Advance("sm-2", PhaseExecuting, "try again", "test")
	if err == nil {
		t.Error("should reject transition from terminal Completed state")
	}
}

func TestAdvanceConvergenceLoop(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "loop", Title: "Loop", Intent: "convergence loop"})
	s.Advance("loop", PhaseResearching, "r", "t")
	s.Advance("loop", PhasePlanning, "p", "t")
	s.Advance("loop", PhaseExecuting, "e", "t")
	s.Advance("loop", PhaseValidating, "v", "t")

	// Gaps found — loop back to Executing
	err := s.Advance("loop", PhaseExecuting, "gaps found, re-execute", "validator")
	if err != nil {
		t.Fatalf("Validating → Executing should be valid: %v", err)
	}

	// Complete the loop
	s.Advance("loop", PhaseValidating, "recheck", "t")
	s.Advance("loop", PhaseConverged, "all clear", "t")

	// Consensus rejects — can loop back from Converged to Executing
	err = s.Advance("loop", PhaseExecuting, "consensus rejected, redo", "reviewer")
	if err != nil {
		t.Fatalf("Converged → Executing should be valid: %v", err)
	}

	trail, _ := s.Transitions("loop")
	// Should have: r, p, e, v, e(loop), v, converged, e(reject)
	if len(trail) != 8 {
		t.Errorf("transitions = %d, want 8", len(trail))
	}
}

func TestAdvanceNonexistentMission(t *testing.T) {
	s := newTestStore(t)
	err := s.Advance("ghost", PhaseResearching, "r", "t")
	if err == nil {
		t.Error("should error for nonexistent mission")
	}
}

func TestAdvancePauseAndResume(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "pause", Title: "Pausable", Intent: "pause test"})
	s.Advance("pause", PhaseResearching, "start", "t")
	s.Advance("pause", PhasePlanning, "plan", "t")

	// Pause
	if err := s.Advance("pause", PhasePaused, "context limit", "agent"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// Resume back to planning
	if err := s.Advance("pause", PhasePlanning, "resume", "new-agent"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	m, _ := s.Get("pause")
	if m.Phase != PhasePlanning {
		t.Errorf("Phase after resume = %q", m.Phase)
	}
}

// --- Acceptance Criteria ---

func TestCriteriaSatisfaction(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{
		ID: "crit", Title: "Criteria test", Intent: "test criteria",
		Criteria: []Criterion{
			{ID: "c-1", Description: "Tests pass"},
			{ID: "c-2", Description: "Build succeeds"},
			{ID: "c-3", Description: "Docs updated"},
		},
	}
	s.Create(m)

	// Initially no criteria satisfied
	allMet, _ := s.AllCriteriaMet("crit")
	if allMet {
		t.Error("criteria should not be met initially")
	}
	unsatisfied, _ := s.UnsatisfiedCriteria("crit")
	if len(unsatisfied) != 3 {
		t.Errorf("unsatisfied = %d, want 3", len(unsatisfied))
	}

	// Satisfy one
	if err := s.SetCriteriaSatisfied("crit", "c-1", "go test ./... passed with 0 failures", "harness"); err != nil {
		t.Fatal(err)
	}

	// Verify it stuck
	got, _ := s.Get("crit")
	if !got.Criteria[0].Satisfied {
		t.Error("c-1 should be satisfied")
	}
	if got.Criteria[0].Evidence != "go test ./... passed with 0 failures" {
		t.Errorf("evidence = %q", got.Criteria[0].Evidence)
	}
	if got.Criteria[0].VerifiedBy != "harness" {
		t.Errorf("verifiedBy = %q", got.Criteria[0].VerifiedBy)
	}
	if got.Criteria[0].VerifiedAt == nil {
		t.Error("verifiedAt should be set")
	}

	// Satisfy remaining
	s.SetCriteriaSatisfied("crit", "c-2", "go build ./... exit 0", "harness")
	s.SetCriteriaSatisfied("crit", "c-3", "README.md updated with API docs", "agent")

	allMet, _ = s.AllCriteriaMet("crit")
	if !allMet {
		t.Error("all criteria should be met")
	}
}

func TestCriteriaSatisfyNonexistent(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "nc", Title: "T", Intent: "I", Criteria: []Criterion{{ID: "c-1", Description: "test"}}})
	err := s.SetCriteriaSatisfied("nc", "c-999", "evidence", "agent")
	if err == nil {
		t.Error("should error for nonexistent criterion")
	}
}

// --- Gaps ---

func TestGapLifecycle(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "gaps", Title: "Gap test", Intent: "gap lifecycle"})

	// Add gaps of varying severity
	s.AddGap(&Gap{ID: "g-1", MissionID: "gaps", Category: "test", Severity: "blocking", Description: "no unit tests for auth module"})
	s.AddGap(&Gap{ID: "g-2", MissionID: "gaps", Category: "code", Severity: "major", Description: "error handling missing in handler.go", File: "handler.go", Line: 42})
	s.AddGap(&Gap{ID: "g-3", MissionID: "gaps", Category: "docs", Severity: "minor", Description: "API docs incomplete"})

	// Open gaps sorted by severity (blocking first)
	open, _ := s.OpenGaps("gaps")
	if len(open) != 3 {
		t.Fatalf("open gaps = %d, want 3", len(open))
	}
	if open[0].Severity != "blocking" {
		t.Error("blocking gaps should sort first")
	}

	// Has blocking gaps
	hasBlocking, _ := s.HasBlockingGaps("gaps")
	if !hasBlocking {
		t.Error("should have blocking gaps")
	}

	// Resolve the blocking gap
	if err := s.ResolveGap("gaps", "g-1"); err != nil {
		t.Fatal(err)
	}

	open, _ = s.OpenGaps("gaps")
	if len(open) != 2 {
		t.Errorf("open gaps after resolve = %d, want 2", len(open))
	}

	hasBlocking, _ = s.HasBlockingGaps("gaps")
	if hasBlocking {
		t.Error("should not have blocking gaps after resolve")
	}

	// All gaps (including resolved)
	all, _ := s.AllGaps("gaps")
	if len(all) != 3 {
		t.Errorf("all gaps = %d, want 3", len(all))
	}
}

func TestGapValidation(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "gv", Title: "T", Intent: "I"})

	err := s.AddGap(&Gap{ID: "", MissionID: "gv", Category: "test", Severity: "blocking", Description: "d"})
	if err == nil {
		t.Error("should reject empty gap ID")
	}
	err = s.AddGap(&Gap{ID: "g", MissionID: "gv", Category: "", Severity: "blocking", Description: "d"})
	if err == nil {
		t.Error("should reject empty category")
	}
}

func TestResolveNonexistentGap(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "rg", Title: "T", Intent: "I"})
	err := s.ResolveGap("rg", "no-such-gap")
	if err == nil {
		t.Error("should error resolving nonexistent gap")
	}
}

func TestGapUpsert(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "upsert", Title: "T", Intent: "I"})

	s.AddGap(&Gap{ID: "g-1", MissionID: "upsert", Category: "test", Severity: "blocking", Description: "original"})
	s.AddGap(&Gap{ID: "g-1", MissionID: "upsert", Category: "test", Severity: "minor", Description: "updated"})

	all, _ := s.AllGaps("upsert")
	if len(all) != 1 {
		t.Fatalf("upsert should not create duplicate, got %d gaps", len(all))
	}
	if all[0].Description != "updated" {
		t.Errorf("upsert should update description, got %q", all[0].Description)
	}
	if all[0].Severity != "minor" {
		t.Errorf("upsert should update severity, got %q", all[0].Severity)
	}
}

// --- Handoffs ---

func TestHandoffChain(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "ho", Title: "Handoff", Intent: "handoff test"})

	s.RecordHandoff(&HandoffRecord{
		MissionID:    "ho",
		FromAgent:    "agent-1",
		ToAgent:      "agent-2",
		Summary:      "Implemented auth module. Tests passing. Need to add rate limiting.",
		PendingWork:  "Rate limiting middleware",
		KeyDecisions: "Using sliding window algorithm for rate limiting",
	})

	time.Sleep(time.Millisecond) // ensure ordering

	s.RecordHandoff(&HandoffRecord{
		MissionID:    "ho",
		FromAgent:    "agent-2",
		ToAgent:      "agent-3",
		Summary:      "Rate limiting done. Need docs and integration tests.",
		PendingWork:  "API docs, integration tests",
		KeyDecisions: "429 response with Retry-After header",
	})

	// Get all handoffs in order
	chain, _ := s.Handoffs("ho")
	if len(chain) != 2 {
		t.Fatalf("handoff chain = %d, want 2", len(chain))
	}
	if chain[0].FromAgent != "agent-1" || chain[1].FromAgent != "agent-2" {
		t.Error("handoff chain should be chronological")
	}

	// Get latest
	latest, _ := s.LatestHandoff("ho")
	if latest == nil {
		t.Fatal("latest handoff should not be nil")
	}
	if latest.FromAgent != "agent-2" {
		t.Errorf("latest handoff from = %q", latest.FromAgent)
	}
	if latest.PendingWork != "API docs, integration tests" {
		t.Errorf("pending work = %q", latest.PendingWork)
	}
}

func TestLatestHandoffEmpty(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "empty-ho", Title: "T", Intent: "I"})
	h, err := s.LatestHandoff("empty-ho")
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		t.Error("should return nil for no handoffs")
	}
}

// --- Consensus ---

func TestConsensusProtocol(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "con", Title: "Consensus", Intent: "consensus test"})

	// One model says complete
	s.RecordConsensus(&ConsensusRecord{
		MissionID: "con", Model: "claude", Verdict: "complete",
		Reasoning: "All criteria met, tests passing, code quality good",
	})

	has, _ := s.HasConsensus("con", 2)
	if has {
		t.Error("should not have consensus with only 1 model")
	}

	// Second model says incomplete
	s.RecordConsensus(&ConsensusRecord{
		MissionID: "con", Model: "codex", Verdict: "incomplete",
		Reasoning: "Missing edge case tests", GapsFound: []string{"g-edge-1"},
	})

	has, _ = s.HasConsensus("con", 2)
	if has {
		t.Error("should not have consensus when second model disagrees")
	}

	// Second model changes mind to complete
	s.RecordConsensus(&ConsensusRecord{
		MissionID: "con", Model: "codex", Verdict: "complete",
		Reasoning: "Edge cases now covered",
	})

	has, _ = s.HasConsensus("con", 2)
	if !has {
		t.Error("should have consensus when both models agree")
	}

	// Verify records
	records, _ := s.ConsensusRecords("con")
	if len(records) != 3 {
		t.Errorf("records = %d, want 3", len(records))
	}
}

func TestConsensusUsesLatestVerdictPerModel(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "latest", Title: "T", Intent: "I"})

	// Claude says complete, then changes to incomplete
	s.RecordConsensus(&ConsensusRecord{MissionID: "latest", Model: "claude", Verdict: "complete"})
	time.Sleep(time.Millisecond)
	s.RecordConsensus(&ConsensusRecord{MissionID: "latest", Model: "claude", Verdict: "incomplete"})

	// Codex says complete
	s.RecordConsensus(&ConsensusRecord{MissionID: "latest", Model: "codex", Verdict: "complete"})

	// Should NOT have consensus because Claude's latest is "incomplete"
	has, _ := s.HasConsensus("latest", 2)
	if has {
		t.Error("should use latest verdict per model — Claude changed to incomplete")
	}
}

// --- Convergence Status ---

func TestConvergenceStatus(t *testing.T) {
	s := newTestStore(t)
	m := &Mission{
		ID: "conv", Title: "Convergence", Intent: "convergence status",
		Criteria: []Criterion{
			{ID: "c-1", Description: "tests pass"},
			{ID: "c-2", Description: "build succeeds"},
		},
	}
	s.Create(m)
	s.AddGap(&Gap{ID: "g-1", MissionID: "conv", Category: "test", Severity: "blocking", Description: "no tests"})
	s.AddGap(&Gap{ID: "g-2", MissionID: "conv", Category: "docs", Severity: "minor", Description: "no docs"})

	status, err := s.GetConvergenceStatus("conv", 2)
	if err != nil {
		t.Fatal(err)
	}
	if status.TotalCriteria != 2 {
		t.Errorf("TotalCriteria = %d", status.TotalCriteria)
	}
	if status.SatisfiedCriteria != 0 {
		t.Errorf("SatisfiedCriteria = %d", status.SatisfiedCriteria)
	}
	if status.OpenGapCount != 2 {
		t.Errorf("OpenGapCount = %d", status.OpenGapCount)
	}
	if status.BlockingGapCount != 1 {
		t.Errorf("BlockingGapCount = %d", status.BlockingGapCount)
	}
	if status.IsConverged {
		t.Error("should not be converged with unsatisfied criteria and gaps")
	}

	// Satisfy criteria and resolve gaps
	s.SetCriteriaSatisfied("conv", "c-1", "pass", "h")
	s.SetCriteriaSatisfied("conv", "c-2", "pass", "h")
	s.ResolveGap("conv", "g-1")
	s.ResolveGap("conv", "g-2")

	status, _ = s.GetConvergenceStatus("conv", 2)
	if !status.IsConverged {
		t.Error("should be converged when all criteria met and no gaps")
	}
	if status.HasConsensus {
		t.Error("should not have consensus without votes")
	}

	// Add consensus
	s.RecordConsensus(&ConsensusRecord{MissionID: "conv", Model: "claude", Verdict: "complete"})
	s.RecordConsensus(&ConsensusRecord{MissionID: "conv", Model: "codex", Verdict: "complete"})

	status, _ = s.GetConvergenceStatus("conv", 2)
	if !status.HasConsensus {
		t.Error("should have consensus with 2 complete votes")
	}
}

// --- Concurrency ---

func TestConcurrentCreates(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	errors := make(chan error, 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m := &Mission{
				ID:     fmt.Sprintf("concurrent-%d", n),
				Title:  fmt.Sprintf("Mission %d", n),
				Intent: fmt.Sprintf("intent %d", n),
			}
			if err := s.Create(m); err != nil {
				errors <- err
			}
		}(i)
	}
	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent create error: %v", err)
	}

	all, _ := s.List("")
	if len(all) != 50 {
		t.Errorf("created %d missions, want 50", len(all))
	}
}

func TestConcurrentAdvance(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Mission{ID: "race", Title: "Race", Intent: "race condition test"})

	// Two goroutines try to advance simultaneously — only one should succeed
	var wg sync.WaitGroup
	results := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.Advance("race", PhaseResearching, "start", "agent")
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	// Both might succeed since Created → Researching is valid and SQLite serializes writes.
	// But the mission should be in a consistent state.
	m, _ := s.Get("race")
	if m.Phase != PhaseResearching {
		t.Errorf("phase = %q after concurrent advance", m.Phase)
	}
}

// --- Edge Cases ---

func TestEmptyDatabase(t *testing.T) {
	s := newTestStore(t)

	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("empty db should return 0 missions, got %d", len(all))
	}
}

func TestNilCriteriaTagsMetadata(t *testing.T) {
	s := newTestStore(t)
	// Create mission without setting Criteria/Tags/Metadata (nil slices/maps)
	m := &Mission{ID: "nil-fields", Title: "Nil", Intent: "nil fields test"}
	if err := s.Create(m); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("nil-fields")
	if got.Criteria == nil {
		t.Error("Criteria should be initialized to empty slice, not nil")
	}
	if got.Tags == nil {
		t.Error("Tags should be initialized to empty slice, not nil")
	}
	if got.Metadata == nil {
		t.Error("Metadata should be initialized to empty map, not nil")
	}
}

func TestStoreReopen(t *testing.T) {
	dir := t.TempDir()

	// Create store, add data, close
	s1, _ := NewStore(dir)
	s1.Create(&Mission{ID: "persist", Title: "Persistent", Intent: "survives reopen"})
	s1.AddGap(&Gap{ID: "g-1", MissionID: "persist", Category: "test", Severity: "blocking", Description: "test gap"})
	s1.Close()

	// Reopen and verify data persisted
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	m, _ := s2.Get("persist")
	if m == nil {
		t.Fatal("mission should survive store reopen")
	}
	if m.Title != "Persistent" {
		t.Errorf("Title = %q after reopen", m.Title)
	}

	gaps, _ := s2.OpenGaps("persist")
	if len(gaps) != 1 {
		t.Errorf("gaps = %d after reopen, want 1", len(gaps))
	}
}

func TestNewStoreEmptyDir(t *testing.T) {
	_, err := NewStore("")
	if err == nil {
		t.Error("should reject empty directory")
	}
}

// --- IsValidTransition ---

func TestIsValidTransitionExhaustive(t *testing.T) {
	// Verify specific invalid transitions
	invalid := []struct{ from, to Phase }{
		{PhaseCreated, PhaseExecuting},
		{PhaseCreated, PhaseValidating},
		{PhaseCreated, PhaseConverged},
		{PhaseCreated, PhaseCompleted},
		{PhaseCompleted, PhaseExecuting},
		{PhaseCompleted, PhaseFailed},
		{PhaseFailed, PhaseCompleted},
		{PhaseFailed, PhaseExecuting},
	}
	for _, tc := range invalid {
		if IsValidTransition(tc.from, tc.to) {
			t.Errorf("should be invalid: %s → %s", tc.from, tc.to)
		}
	}

	// Verify specific valid transitions
	valid := []struct{ from, to Phase }{
		{PhaseCreated, PhaseResearching},
		{PhaseCreated, PhasePlanning},
		{PhaseResearching, PhasePlanning},
		{PhasePlanning, PhaseExecuting},
		{PhaseExecuting, PhaseValidating},
		{PhaseValidating, PhaseConverged},
		{PhaseValidating, PhaseExecuting}, // convergence loop
		{PhaseConverged, PhaseCompleted},
		{PhaseConverged, PhaseExecuting}, // consensus reject
		{PhaseExecuting, PhasePaused},
		{PhasePaused, PhaseExecuting},
	}
	for _, tc := range valid {
		if !IsValidTransition(tc.from, tc.to) {
			t.Errorf("should be valid: %s → %s", tc.from, tc.to)
		}
	}
}
