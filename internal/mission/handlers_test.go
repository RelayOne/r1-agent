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
	var capturedPrompt string
	handler := NewExecuteHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ExecuteFn: func(ctx context.Context, m *Mission, prompt, taskDesc string) ([]string, error) {
			capturedTask = taskDesc
			capturedPrompt = prompt
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
	// Verify the prompt was built from BuildMissionExecutePrompt (not ad-hoc)
	if capturedPrompt == "" {
		t.Error("execute prompt should be built and passed to ExecuteFn")
	}
	if !strings.Contains(capturedPrompt, "implementation agent") {
		t.Error("execute prompt should come from BuildMissionExecutePrompt")
	}
	if !strings.Contains(capturedPrompt, "No stubs, no TODOs") {
		t.Error("execute prompt should include anti-stub rules")
	}
	if !strings.Contains(capturedPrompt, "JWT tokens") {
		t.Error("execute prompt should include criteria")
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

	var capturedPrompts []string
	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: metrics,
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			capturedPrompts = append(capturedPrompts, prompt)
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

	// Verify each model received the adversarial consensus prompt
	if len(capturedPrompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(capturedPrompts))
	}
	for i, p := range capturedPrompts {
		if !strings.Contains(p, "DISPROVE Completeness") {
			t.Errorf("prompt %d missing adversarial framing", i)
		}
		if !strings.Contains(p, "Anti-rationalization") {
			t.Errorf("prompt %d missing anti-rationalization protocol", i)
		}
		if !strings.Contains(p, m.ID) {
			t.Errorf("prompt %d missing mission ID", i)
		}
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

// --- Research Handler with DiscoveryFn ---

func TestResearchHandlerWithDiscoveryFn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	var capturedPrompt string
	handler := NewResearchHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		DiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			capturedPrompt = prompt
			return "FILE: internal/auth/jwt.go\nFILE: internal/auth/handler.go\nGAP: Missing token refresh endpoint\nGAP:MAJOR: No rate limiting on auth endpoint\n", nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Should have captured the discovery prompt
	if capturedPrompt == "" {
		t.Error("DiscoveryFn should receive a prompt")
	}

	// Should include discovered files
	foundJWT := false
	foundHandler := false
	for _, f := range result.FilesChanged {
		if f == "internal/auth/jwt.go" {
			foundJWT = true
		}
		if f == "internal/auth/handler.go" {
			foundHandler = true
		}
	}
	if !foundJWT {
		t.Error("should include jwt.go from FILE: output")
	}
	if !foundHandler {
		t.Error("should include handler.go from FILE: output")
	}

	// Should have stored discovery artifact
	if result.Artifacts["discovery"] == "" {
		t.Error("should store discovery result as artifact")
	}

	// Should have created gaps from GAP: lines
	gaps, _ := store.OpenGaps(m.ID)
	blockingCount := 0
	majorCount := 0
	for _, g := range gaps {
		if g.Category != "discovery-research" {
			t.Errorf("gap category should be discovery-research, got %q", g.Category)
		}
		if g.Severity == "blocking" {
			blockingCount++
		}
		if g.Severity == "major" {
			majorCount++
		}
	}
	if blockingCount != 1 {
		t.Errorf("expected 1 blocking gap, got %d", blockingCount)
	}
	if majorCount != 1 {
		t.Errorf("expected 1 major gap, got %d", majorCount)
	}
}

func TestResearchHandlerDiscoveryFnSkipsFallback(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Create a temp repo with files that would match the fallback search
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "jwt.go"),
		[]byte("package jwt\n\nfunc IssueToken() {}\n"), 0644)

	discoveryRan := false
	handler := NewResearchHandler(HandlerDeps{
		Store:    store,
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
		DiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			discoveryRan = true
			return "FILE: custom/path.go\n", nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if !discoveryRan {
		t.Error("DiscoveryFn should have been called")
	}

	// With DiscoveryFn present, fallback TF-IDF/symbol search should NOT run.
	// The only file should be the one from DiscoveryFn.
	for _, f := range result.FilesChanged {
		if f == "jwt.go" {
			t.Error("fallback search should not run when DiscoveryFn is configured")
		}
	}
}

func TestResearchHandlerDiscoveryFnError(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewResearchHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		DiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "", context.DeadlineExceeded
		},
	})

	// Should not fail fatally even if DiscoveryFn errors
	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler should not fail: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
}

// --- Validate Handler with ValidateDiscoveryFn ---

func TestValidateHandlerLayer4GapParsing(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644)

	handler := NewValidateHandler(HandlerDeps{
		Store:    store,
		Validator: convergence.NewValidator(),
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
		ValidateDiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "GAP: Auth handler not wired to router at cmd/server/main.go:45\nGAP:MAJOR: No pagination on /api/users endpoint\nSome other line that should be ignored\n", nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if !strings.Contains(result.Summary, "discovery-validation") {
		t.Errorf("summary should mention discovery-validation: %q", result.Summary)
	}

	gaps, _ := store.OpenGaps(m.ID)

	// Find gaps by category
	var discGaps []Gap
	for _, g := range gaps {
		if g.Category == "discovery-validation" {
			discGaps = append(discGaps, g)
		}
	}

	blocking := 0
	major := 0
	for _, g := range discGaps {
		switch g.Severity {
		case "blocking":
			blocking++
			if !strings.Contains(g.Description, "Auth handler not wired") {
				t.Errorf("blocking gap description wrong: %q", g.Description)
			}
		case "major":
			major++
			if !strings.Contains(g.Description, "No pagination") {
				t.Errorf("major gap description wrong: %q", g.Description)
			}
		}
	}

	if blocking != 1 {
		t.Errorf("expected 1 blocking discovery gap, got %d", blocking)
	}
	if major != 1 {
		t.Errorf("expected 1 major discovery gap, got %d", major)
	}
}

func TestValidateHandlerLayer4FixedParsing(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Pre-add an open gap that the model will report as FIXED
	store.AddGap(&Gap{
		ID:          "existing-gap-1",
		MissionID:   m.ID,
		Category:    "discovery-validation",
		Severity:    "blocking",
		Description: "Auth handler not wired to router",
	})

	// Verify it's open
	openBefore, _ := store.OpenGaps(m.ID)
	if len(openBefore) != 1 {
		t.Fatalf("expected 1 open gap before, got %d", len(openBefore))
	}

	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644)

	handler := NewValidateHandler(HandlerDeps{
		Store:    store,
		Validator: convergence.NewValidator(),
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
		ValidateDiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "FIXED: Auth handler not wired to router\n", nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Gap should now be resolved
	openAfter, _ := store.OpenGaps(m.ID)
	for _, g := range openAfter {
		if g.ID == "existing-gap-1" {
			t.Error("existing-gap-1 should have been resolved by FIXED: line")
		}
	}

	if !strings.Contains(result.Summary, "1 fixed") {
		t.Errorf("summary should mention fixed count: %q", result.Summary)
	}
}

func TestValidateHandlerSecurityOnlyWithDiscoveryFn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Create a repo with both TODO (non-security) and a hardcoded secret (security)
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "auth.go"),
		[]byte("package auth\n\n// TODO: refactor this\nvar api_key = \"abcdefghijklmnopqrstuvwxyz1234567890\"\n"), 0644)

	handler := NewValidateHandler(HandlerDeps{
		Store:    store,
		Validator: convergence.NewValidator(),
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
		ValidateDiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "", nil // clean discovery result
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Should run in security-only mode
	if !strings.Contains(result.Summary, "security-only") {
		t.Errorf("summary should say security-only mode: %q", result.Summary)
	}

	// Should catch the hardcoded secret
	gaps, _ := store.OpenGaps(m.ID)
	foundSecret := false
	foundTodo := false
	for _, g := range gaps {
		if strings.Contains(g.Description, "secret") || strings.Contains(g.Description, "credential") {
			foundSecret = true
		}
		if strings.Contains(g.Description, "TODO") {
			foundTodo = true
		}
	}
	if !foundSecret {
		t.Error("should still detect hardcoded secrets in security-only mode")
	}
	if foundTodo {
		t.Error("should NOT flag TODO markers when ValidateDiscoveryFn is present (security-only mode)")
	}
}

func TestValidateHandlerFullRulesWithoutDiscoveryFn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Same files as above, but without ValidateDiscoveryFn
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "auth.go"),
		[]byte("package auth\n\n// TODO: refactor this\nvar api_key = \"abcdefghijklmnopqrstuvwxyz1234567890\"\n"), 0644)

	handler := NewValidateHandler(HandlerDeps{
		Store:    store,
		Validator: convergence.NewValidator(),
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
		// No ValidateDiscoveryFn → full rules
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Should run in full mode
	if !strings.Contains(result.Summary, "static(full)") {
		t.Errorf("summary should say full mode: %q", result.Summary)
	}

	// Should catch both TODO and secret
	gaps, _ := store.OpenGaps(m.ID)
	foundSecret := false
	foundTodo := false
	for _, g := range gaps {
		if strings.Contains(g.Description, "secret") || strings.Contains(g.Description, "credential") {
			foundSecret = true
		}
		if strings.Contains(g.Description, "TODO") || strings.Contains(g.Description, "FIXME") {
			foundTodo = true
		}
	}
	if !foundSecret {
		t.Error("full mode should detect hardcoded secrets")
	}
	if !foundTodo {
		t.Error("full mode should flag TODO markers")
	}
}
