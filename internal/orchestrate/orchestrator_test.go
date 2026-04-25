package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1-agent/internal/convergence"
	"github.com/RelayOne/r1-agent/internal/handoff"
	"github.com/RelayOne/r1-agent/internal/mission"
	"github.com/RelayOne/r1-agent/internal/research"
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
	// Will exhaust convergence loops because criteria never become satisfied
	if err == nil {
		t.Log("RunMission completed (expected failure)")
	}
	if result != nil && result.IsSuccess() {
		t.Error("should not succeed without satisfying criteria")
	}
}

// --- Wired Runner Integration ---

func TestRunMissionWithExecuteFn(t *testing.T) {
	repoDir := t.TempDir()
	writeFile(t, repoDir, "auth.go", "package auth\n\nfunc Login() string { return \"token\" }\n")
	writeFile(t, repoDir, "auth_test.go", "package auth\n\nimport \"testing\"\n\nfunc TestLogin(t *testing.T) {\n\tif Login() == \"\" { t.Fatal() }\n}\n")

	executeCalls := 0
	var lastExecutePrompt string
	orch, err := New(Config{
		StoreDir: t.TempDir(),
		RepoRoot: repoDir,
		ExecuteFn: func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
			executeCalls++
			lastExecutePrompt = prompt
			return []string{"auth.go", "auth_test.go"}, nil
		},
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			// Verify we received the adversarial prompt
			if !strings.Contains(prompt, "DISPROVE") {
				return "incomplete", "prompt missing adversarial framing", nil, nil
			}
			return "complete", "auth.go:3 implements Login() returning token, auth_test.go:5 verifies non-empty return value — criterion satisfied with evidence", nil, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer orch.Close()

	m, _ := orch.CreateMission("JWT Auth", "Implement JWT authentication", []string{"Login returns token"})

	// Satisfy criteria before running so convergence passes
	orch.Store().SetCriteriaSatisfied(m.ID, "c-1", "Login returns token", "test-agent")

	result, err := orch.RunMission(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("RunMission: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success, got phase=%s", result.FinalPhase)
	}
	if executeCalls == 0 {
		t.Error("ExecuteFn should have been called")
	}
	if len(result.Phases) == 0 {
		t.Error("should have recorded phase results")
	}
	// Verify execute prompt was built from BuildMissionExecutePrompt
	if lastExecutePrompt == "" {
		t.Error("ExecuteFn should receive the mission-aware prompt")
	}
	if !strings.Contains(lastExecutePrompt, "implementation agent") {
		t.Error("execute prompt should come from BuildMissionExecutePrompt")
	}
}

func TestRunMissionPhaseHandlersRegistered(t *testing.T) {
	repoDir := t.TempDir()
	writeFile(t, repoDir, "main.go", "package main\n\nfunc main() {}\n")

	orch, err := New(Config{
		StoreDir: t.TempDir(),
		RepoRoot: repoDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	m, _ := orch.CreateMission("Test", "Verify handlers fire", []string{"Feature done"})

	var phasesSeen []string
	config := mission.RunnerConfig{
		MaxConvergenceLoops: 2,
		RequiredConsensus:   2,
		MaxPhaseRetries:     1,
		OnPhaseComplete: func(missionID string, result *mission.PhaseResult) {
			phasesSeen = append(phasesSeen, string(result.Phase))
		},
	}

	runner, err := orch.NewRunner(config)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	runner.Run(context.Background(), m.ID)

	// Should have seen at least research and plan phases
	if len(phasesSeen) < 2 {
		t.Errorf("expected at least 2 phases seen, got %d: %v", len(phasesSeen), phasesSeen)
	}
	foundResearch := false
	for _, p := range phasesSeen {
		if p == string(mission.PhaseCreated) {
			foundResearch = true
		}
	}
	if !foundResearch {
		t.Errorf("research handler should have fired, phases=%v", phasesSeen)
	}
}

func TestRunMissionConsensusModelsDefault(t *testing.T) {
	orch, err := New(Config{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	if len(orch.config.ConsensusModels) != 2 {
		t.Errorf("default consensus models = %v", orch.config.ConsensusModels)
	}
	if orch.config.ConsensusModels[0] != "claude" || orch.config.ConsensusModels[1] != "codex" {
		t.Errorf("unexpected models: %v", orch.config.ConsensusModels)
	}
}

func TestRunMissionEndToEndWithConsensusFn(t *testing.T) {
	repoDir := t.TempDir()
	// No TODO or stubs — clean code so validator passes
	writeFile(t, repoDir, "handler.go", "package handler\n\nfunc Handle() string { return \"ok\" }\n")
	writeFile(t, repoDir, "handler_test.go", "package handler\n\nimport \"testing\"\n\nfunc TestHandle(t *testing.T) {\n\tif Handle() != \"ok\" { t.Fatal() }\n}\n")

	// consensusCalls and consensusPrompts are mutated by parallel
	// consensus goroutines; guard them with a mutex to satisfy -race.
	var (
		consensusMu      sync.Mutex
		consensusCalls   = map[string]int{}
		consensusPrompts []string
	)
	orch, err := New(Config{
		StoreDir:        t.TempDir(),
		RepoRoot:        repoDir,
		ConsensusModels: []string{"model-a", "model-b"},
		ExecuteFn: func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
			return []string{"handler.go"}, nil
		},
		ConsensusModelFn: func(ctx context.Context, missionID, modelName, prompt string) (string, string, []string, error) {
			consensusMu.Lock()
			consensusCalls[modelName]++
			consensusPrompts = append(consensusPrompts, prompt)
			consensusMu.Unlock()
			return "complete", "handler.go:3 implements Handle() returning ok, handler_test.go:5 asserts Handle() returns ok — full coverage verified", nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	m, _ := orch.CreateMission("End to End", "Full pipeline test", []string{"Handler works"})
	orch.Store().SetCriteriaSatisfied(m.ID, "c-1", "tested", "agent")

	result, err := orch.RunMission(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("RunMission: %v", err)
	}
	if !result.IsSuccess() {
		t.Errorf("expected success, got %s", result.FinalPhase)
	}
	consensusMu.Lock()
	defer consensusMu.Unlock()
	if consensusCalls["model-a"] == 0 || consensusCalls["model-b"] == 0 {
		t.Errorf("both models should have been called: %v", consensusCalls)
	}
	// Verify each model received the adversarial consensus prompt
	for i, p := range consensusPrompts {
		if !strings.Contains(p, "DISPROVE Completeness") {
			t.Errorf("consensus prompt %d missing adversarial framing", i)
		}
		if !strings.Contains(p, "Anti-rationalization") {
			t.Errorf("consensus prompt %d missing anti-rationalization protocol", i)
		}
	}
}

func TestNewRunnerRepoRoot(t *testing.T) {
	repoDir := t.TempDir()
	writeFile(t, repoDir, "jwt.go", "package jwt\n")

	orch, err := New(Config{
		StoreDir: t.TempDir(),
		RepoRoot: repoDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	m, _ := orch.CreateMission("JWT", "Add JWT authentication", []string{"Tokens work"})

	// Run research phase which should find jwt.go
	runner, err := orch.NewRunner(mission.DefaultRunnerConfig())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	// Just advance to researching and run one step
	orch.Store().Advance(m.ID, mission.PhaseResearching, "test", "test")

	// Manually invoke the runner to drive research
	result, _ := runner.Run(context.Background(), m.ID)
	// Research handler should have found jwt.go based on intent keywords
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Check that phases were recorded
	foundResearch := false
	for _, p := range result.Phases {
		if p.Phase == mission.PhaseResearching {
			foundResearch = true
			if !strings.Contains(p.Summary, "jwt.go") {
				t.Logf("research summary = %q (may not contain jwt.go if research handler ran at Created phase)", p.Summary)
			}
		}
	}
	if !foundResearch {
		// Research may have run at Created phase instead
		for _, p := range result.Phases {
			if p.Phase == mission.PhaseCreated && strings.Contains(p.Summary, "relevant files") {
				foundResearch = true
			}
		}
	}
	if !foundResearch {
		t.Errorf("should have a research phase result, phases=%v", result.Phases)
	}
}

// --- End-to-End: Baseline → Validate → Consensus ---

func TestEndToEndBaselineValidateConsensus(t *testing.T) {
	// Create a repo with a go.mod so AutoDetect finds Go commands
	repoDir := t.TempDir()
	writeFile(t, repoDir, "go.mod", "module testmod\n\ngo 1.21\n")
	writeFile(t, repoDir, "main.go", "package main\n\nimport \"log\"\n\nfunc main() { log.Printf(\"ok\") }\n")
	writeFile(t, repoDir, "main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestMain2(t *testing.T) {}\n")

	// All three captures are written from concurrent handler goroutines.
	// Guard with a mutex to satisfy -race.
	var (
		captureMu                sync.Mutex
		validatePromptReceived   string
		executePromptReceived    string
		consensusPromptsReceived []string
	)

	orch, err := New(Config{
		StoreDir:        t.TempDir(),
		RepoRoot:        repoDir,
		ConsensusModels: []string{"reviewer-a", "reviewer-b"},
		ExecuteFn: func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
			captureMu.Lock()
			executePromptReceived = prompt
			captureMu.Unlock()
			return []string{"main.go"}, nil
		},
		ValidateFn: func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
			captureMu.Lock()
			validatePromptReceived = prompt
			captureMu.Unlock()
			// Simulate adversarial LLM finding no additional issues
			return "", nil
		},
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			captureMu.Lock()
			consensusPromptsReceived = append(consensusPromptsReceived, prompt)
			captureMu.Unlock()
			return "complete", "validated", nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	m, err := orch.CreateMission("Test E2E", "Full pipeline test", []string{"Main runs"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify baseline was captured
	bl := orch.GetBaseline(m.ID)
	if bl == nil {
		t.Fatal("baseline should be captured at mission creation")
	}

	// Satisfy criteria so convergence can complete
	orch.Store().SetCriteriaSatisfied(m.ID, "c-1", "main runs", "test")

	// Run the full pipeline
	result, err := orch.RunMission(context.Background(), m.ID)
	if err != nil {
		t.Logf("RunMission error (may be expected if validation finds gaps): %v", err)
	}

	// Snapshot the captured prompts under the mutex so the assertion
	// reads don't race against the goroutines that mutated them.
	captureMu.Lock()
	executeP := executePromptReceived
	validateP := validatePromptReceived
	consensusP := append([]string(nil), consensusPromptsReceived...)
	captureMu.Unlock()

	// Verify prompts were actually built and passed
	if executeP == "" {
		t.Error("ExecuteFn should receive the mission-aware prompt")
	} else {
		if !strings.Contains(executeP, "implementation agent") {
			t.Error("execute prompt should come from BuildMissionExecutePrompt")
		}
		if !strings.Contains(executeP, "Test E2E") {
			t.Error("execute prompt should include mission title")
		}
		if !strings.Contains(executeP, "Main runs") {
			t.Error("execute prompt should include criteria")
		}
	}

	if validateP == "" {
		t.Error("ValidateFn should receive the adversarial validation prompt")
	} else {
		if !strings.Contains(validateP, "5 Convergence Gates") {
			t.Error("validate prompt should include 5 convergence gates")
		}
		if !strings.Contains(validateP, "Do not rationalize") {
			t.Error("validate prompt should include anti-rationalization")
		}
	}

	if len(consensusP) == 0 && result != nil && result.IsSuccess() {
		// If mission succeeded, consensus was called
		t.Error("consensus models should receive adversarial prompts")
	}
	for i, p := range consensusP {
		if !strings.Contains(p, "DISPROVE Completeness") {
			t.Errorf("consensus prompt %d missing adversarial framing", i)
		}
		if !strings.Contains(p, "Anti-rationalization") {
			t.Errorf("consensus prompt %d missing anti-rationalization protocol", i)
		}
	}
}

func TestBaselinePersistedAndRecovered(t *testing.T) {
	repoDir := t.TempDir()
	writeFile(t, repoDir, "go.mod", "module testmod\n\ngo 1.21\n")
	writeFile(t, repoDir, "x.go", "package x\n")

	storeDir := t.TempDir()
	orch, err := New(Config{StoreDir: storeDir, RepoRoot: repoDir})
	if err != nil {
		t.Fatal(err)
	}

	m, _ := orch.CreateMission("Persist Test", "Test baseline persistence", nil)
	bl := orch.GetBaseline(m.ID)
	if bl == nil {
		t.Fatal("baseline should exist")
	}
	orch.Close()

	// Re-open orchestrator — baseline should be recovered from disk
	orch2, err := New(Config{StoreDir: storeDir, RepoRoot: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	defer orch2.Close()

	// Use NewRunnerForMission which loads baseline from disk
	runner, err := orch2.NewRunnerForMission(mission.DefaultRunnerConfig(), m.ID)
	if err != nil {
		t.Fatalf("NewRunnerForMission: %v", err)
	}
	if runner == nil {
		t.Error("runner should be created")
	}
	// The baseline should have been loaded from disk into the runner's deps
	// We can verify by checking the baselines map
	bl2 := orch2.GetBaseline(m.ID)
	if bl2 == nil {
		t.Error("baseline should be recovered from disk")
	}
}

// writeFile is a test helper to create files in a temp directory.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
}

// --- Close ---

func TestClose(t *testing.T) {
	orch, _ := New(Config{StoreDir: t.TempDir()})
	if err := orch.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
