package mission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/convergence"
)

func setupHandlerTestMission(t *testing.T, store *Store) *Mission {
	t.Helper()
	m := &Mission{
		ID:     "m-handler",
		Title:  "Handler Test",
		Intent: "Test the phase handlers for JWT authentication",
		Criteria: []Criterion{
			{ID: "c-1", Description: "JWT tokens issued"},
			{ID: "c-2", Description: "Tests pass"},
		},
	}
	if err := store.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return m
}

// --- Research Handler ---

func TestResearchHandler(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Create a temp repo with some files
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "auth.go"), []byte("package auth"), 0644)
	os.WriteFile(filepath.Join(repoDir, "jwt.go"), []byte("package jwt"), 0644)
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main"), 0644)

	handler := NewResearchHandler(HandlerDeps{
		Store:    store,
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Should find jwt.go and auth.go based on keywords from intent
	if !strings.Contains(result.Summary, "relevant files") {
		t.Errorf("summary = %q", result.Summary)
	}
}

func TestResearchHandlerNoRepo(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewResearchHandler(HandlerDeps{Store: store})
	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "0 relevant files") {
		t.Errorf("summary = %q, expected 0 files", result.Summary)
	}
}

// --- Plan Handler ---

func TestPlanHandler(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewPlanHandler(HandlerDeps{Store: store})
	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "2 tasks") {
		t.Errorf("summary = %q, expected 2 tasks", result.Summary)
	}
	if result.Artifacts["plan"] == "" {
		t.Error("should have plan artifact")
	}
}

func TestPlanHandlerAllSatisfied(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)
	store.SetCriteriaSatisfied(m.ID, "c-1", "done", "a")
	store.SetCriteriaSatisfied(m.ID, "c-2", "done", "a")

	// Re-fetch mission with updated criteria
	m, _ = store.Get(m.ID)

	handler := NewPlanHandler(HandlerDeps{Store: store})
	result, _ := handler(context.Background(), m)
	if !strings.Contains(result.Summary, "0 tasks") {
		t.Errorf("summary = %q, expected 0 tasks", result.Summary)
	}
}

// --- Execute Handler ---

func TestExecuteHandler(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	var capturedTask string
	handler := NewExecuteHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ExecuteFn: func(ctx context.Context, m *Mission, taskDesc string) ([]string, error) {
			capturedTask = taskDesc
			return []string{"auth.go", "auth_test.go"}, nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FilesChanged) != 2 {
		t.Errorf("FilesChanged = %d, want 2", len(result.FilesChanged))
	}
	if !strings.Contains(capturedTask, "JWT tokens") {
		t.Error("task description should include criteria")
	}
}

func TestExecuteHandlerNoFn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewExecuteHandler(HandlerDeps{Store: store})
	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesChanged != nil {
		t.Error("should have no files changed without ExecuteFn")
	}
}

// --- Validate Handler ---

func TestValidateHandler(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Create a repo with a file containing a TODO
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "auth.go"),
		[]byte("package auth\n\n// TODO: implement JWT\nfunc Login() {}\n"), 0644)

	handler := NewValidateHandler(HandlerDeps{
		Store:     store,
		Validator: convergence.NewValidator(),
		RepoRoot:  repoDir,
		Metrics:   NewMetrics(),
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "findings") {
		t.Errorf("summary = %q", result.Summary)
	}

	// Should have created gaps in the store
	gaps, _ := store.OpenGaps(m.ID)
	if len(gaps) == 0 {
		t.Error("should have persisted gaps from validation findings")
	}
}

func TestValidateHandlerNoValidator(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewValidateHandler(HandlerDeps{Store: store})
	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "skipped") {
		t.Errorf("should indicate validation was skipped, got %q", result.Summary)
	}
}

// --- Consensus Handler ---

func TestConsensusHandlerAutoApprove(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewConsensusHandler(HandlerDeps{Store: store}, []string{"claude", "codex"})
	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "Auto-approved") {
		t.Errorf("summary = %q", result.Summary)
	}

	// Should have recorded consensus
	has, _ := store.HasConsensus(m.ID, 2)
	if !has {
		t.Error("should have consensus after auto-approve")
	}
}

func TestConsensusHandlerWithFn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)
	metrics := NewMetrics()

	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: metrics,
		ConsensusModelFn: func(ctx context.Context, missionID, model string) (string, string, []string, error) {
			return "complete", "looks good", nil, nil
		},
	}, []string{"claude", "codex"})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "claude: complete") {
		t.Errorf("summary = %q", result.Summary)
	}

	snap := metrics.Snapshot()
	if snap.ConsensusVotes != 2 {
		t.Errorf("ConsensusVotes = %d, want 2", snap.ConsensusVotes)
	}
}

// --- Keyword Extraction ---

func TestExtractMissionKeywords(t *testing.T) {
	keywords := extractMissionKeywords("Add JWT authentication to the API with rate limiting")
	found := make(map[string]bool)
	for _, k := range keywords {
		found[k] = true
	}

	// Should have "jwt", "authentication", "api", "rate", "limiting"
	for _, expected := range []string{"jwt", "authentication", "api", "rate", "limiting"} {
		if !found[expected] {
			t.Errorf("missing keyword %q, got %v", expected, keywords)
		}
	}

	// Should not have stop words
	for _, stop := range []string{"the", "with"} {
		if found[stop] {
			t.Errorf("should not include stop word %q", stop)
		}
	}
}

func TestExtractMissionKeywordsEmpty(t *testing.T) {
	keywords := extractMissionKeywords("")
	if len(keywords) != 0 {
		t.Errorf("empty intent should produce no keywords, got %v", keywords)
	}
}
