package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/handoff"
	"github.com/ericmacdougall/stoke/internal/mission"
	"github.com/ericmacdougall/stoke/internal/research"
)

func newTestOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	orch, err := New(Config{
		StoreDir:            t.TempDir(),
		RequiredConsensus:   2,
		MaxConvergenceLoops: 5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { orch.Close() })
	return orch
}

// --- Create Mission ---

func TestCreateMission(t *testing.T) {
	orch := newTestOrchestrator(t)

	m, err := orch.CreateMission("JWT Auth", "Add JWT to API", []string{
		"Tokens issued on login",
		"Invalid tokens return 401",
	})
	if err != nil {
		t.Fatalf("CreateMission: %v", err)
	}
	if m.ID == "" {
		t.Error("mission ID should be generated")
	}
	if m.Title != "JWT Auth" {
		t.Errorf("Title = %q", m.Title)
	}
	if len(m.Criteria) != 2 {
		t.Errorf("Criteria = %d, want 2", len(m.Criteria))
	}
	if m.Criteria[0].ID != "c-1" {
		t.Errorf("first criterion ID = %q", m.Criteria[0].ID)
	}
	if m.Phase != mission.PhaseCreated {
		t.Errorf("Phase = %s, want created", m.Phase)
	}

	got, _ := orch.GetMission(m.ID)
	if got == nil || got.Title != "JWT Auth" {
		t.Error("mission should be retrievable")
	}
}

func TestCreateMissionValidation(t *testing.T) {
	orch := newTestOrchestrator(t)
	_, err := orch.CreateMission("", "intent", nil)
	if err == nil {
		t.Error("should reject empty title")
	}
	_, err = orch.CreateMission("title", "", nil)
	if err == nil {
		t.Error("should reject empty intent")
	}
}

// --- List ---

func TestListMissions(t *testing.T) {
	orch := newTestOrchestrator(t)
	orch.CreateMission("M1", "intent 1", nil)
	orch.CreateMission("M2", "intent 2", nil)

	all, _ := orch.ListMissions("")
	if len(all) != 2 {
		t.Errorf("all = %d, want 2", len(all))
	}
}

// --- Convergence ---

func TestRunConvergence(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Test", "Test convergence", []string{"Feature works"})

	files := []convergence.FileInput{
		{Path: "main.go", Content: []byte("// TODO: implement feature\nfunc main() {}\n")},
	}

	report, err := orch.RunConvergence(m.ID, files)
	if err != nil {
		t.Fatalf("RunConvergence: %v", err)
	}
	if len(report.Findings) == 0 {
		t.Error("should find TODO issue")
	}

	gaps, _ := orch.Store().OpenGaps(m.ID)
	if len(gaps) == 0 {
		t.Error("findings should be persisted as gaps")
	}
}

func TestCheckConvergence(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Test", "Check convergence", []string{"Done"})

	status, _ := orch.CheckConvergence(m.ID)
	if status.IsConverged {
		t.Error("should not be converged with unsatisfied criteria")
	}

	orch.Store().SetCriteriaSatisfied(m.ID, "c-1", "evidence", "agent")
	status, _ = orch.CheckConvergence(m.ID)
	if !status.IsConverged {
		t.Error("should be converged with all criteria satisfied")
	}
}

// --- Research ---

func TestResearch(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Test", "Research test", nil)

	err := orch.AddResearch(m.ID, &research.Entry{
		ID: "r-1", Topic: "JWT", Query: "How JWT works",
		Content: "JWT uses HMAC signatures for token validation",
	})
	if err != nil {
		t.Fatalf("AddResearch: %v", err)
	}

	results, _ := orch.SearchResearch("JWT", 10)
	if len(results) == 0 {
		t.Error("should find JWT research")
	}
}

// --- Handoffs ---

func TestHandoff(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Test", "Handoff test", []string{"Done"})

	err := orch.RecordHandoff(handoff.Record{
		MissionID: m.ID, FromAgent: "agent-1", ToAgent: "agent-2",
		Summary: "Implemented JWT generation", PendingWork: []string{"Rate limiting"},
	})
	if err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}

	ctx, _ := orch.GetHandoffContext(m.ID, 2000)
	if !strings.Contains(ctx, "JWT generation") {
		t.Error("handoff context should contain the summary")
	}
}

// --- Agent Context ---

func TestBuildAgentContext(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("JWT Auth", "Add JWT authentication", []string{
		"Tokens issued", "401 on invalid",
	})

	orch.AddResearch(m.ID, &research.Entry{
		ID: "r-1", Topic: "JWT", Query: "JWT validation",
		Content: "Use golang-jwt for parsing",
	})
	orch.RecordHandoff(handoff.Record{
		MissionID: m.ID, FromAgent: "a1", ToAgent: "a2",
		Summary: "Started JWT work",
	})
	orch.Store().SetCriteriaSatisfied(m.ID, "c-1", "jwt.go works", "a1")

	ctx, err := orch.BuildAgentContext(m.ID, mission.DefaultContextConfig())
	if err != nil {
		t.Fatalf("BuildAgentContext: %v", err)
	}

	for _, check := range []string{"JWT Auth", "1/2", "[x] Tokens issued", "[ ] 401 on invalid"} {
		if !strings.Contains(ctx, check) {
			t.Errorf("context missing %q", check)
		}
	}
}

// --- Consensus ---

func TestConsensus(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Test", "Consensus test", nil)

	has, _ := orch.HasConsensus(m.ID)
	if has {
		t.Error("no consensus initially")
	}

	orch.RecordConsensus(m.ID, "claude", "complete", "ok", nil)
	has, _ = orch.HasConsensus(m.ID)
	if has {
		t.Error("need 2 votes")
	}

	orch.RecordConsensus(m.ID, "codex", "complete", "confirmed", nil)
	has, _ = orch.HasConsensus(m.ID)
	if !has {
		t.Error("should have consensus with 2 votes")
	}
}

// --- Advance ---

func TestAdvanceMission(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Test", "Advance test", nil)

	err := orch.AdvanceMission(m.ID, mission.PhaseResearching, "starting", "test")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := orch.GetMission(m.ID)
	if got.Phase != mission.PhaseResearching {
		t.Errorf("phase = %s", got.Phase)
	}

	err = orch.AdvanceMission(m.ID, mission.PhaseCompleted, "skip", "test")
	if err == nil {
		t.Error("should reject invalid transition")
	}
}

// --- Store Accessors ---

func TestStoreAccessors(t *testing.T) {
	orch := newTestOrchestrator(t)
	if orch.Store() == nil {
		t.Error("Store nil")
	}
	if orch.ResearchStore() == nil {
		t.Error("ResearchStore nil")
	}
	if orch.HandoffChain() == nil {
		t.Error("HandoffChain nil")
	}
	if orch.Validator() == nil {
		t.Error("Validator nil")
	}
}

// --- Config Defaults ---

func TestConfigDefaults(t *testing.T) {
	orch, _ := New(Config{StoreDir: t.TempDir()})
	defer orch.Close()
	if orch.config.RequiredConsensus != 2 {
		t.Errorf("RequiredConsensus = %d", orch.config.RequiredConsensus)
	}
	if orch.config.MaxConvergenceLoops != 5 {
		t.Errorf("MaxConvergenceLoops = %d", orch.config.MaxConvergenceLoops)
	}
}

// --- Empty Store Dir ---

func TestEmptyStoreDir(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("should reject empty store dir")
	}
}

// --- RunMission ---

func TestRunMission(t *testing.T) {
	orch := newTestOrchestrator(t)
	m, _ := orch.CreateMission("Quick", "Quick test", []string{"Works"})

	result, err := orch.RunMission(context.Background(), m.ID)
	// Will fail because criteria not satisfied and no handlers
	if err == nil {
		t.Log("RunMission completed (expected failure)")
	}
	if result != nil && result.IsSuccess() {
		t.Error("should not succeed without satisfying criteria")
	}
}

// --- Close ---

func TestClose(t *testing.T) {
	orch, _ := New(Config{StoreDir: t.TempDir()})
	if err := orch.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
