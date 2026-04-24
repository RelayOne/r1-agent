package mission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/convergence"
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
	os.WriteFile(filepath.Join(repoDir, "auth.go"), []byte("package auth"), 0o600)
	os.WriteFile(filepath.Join(repoDir, "jwt.go"), []byte("package jwt"), 0o600)
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main"), 0o600)

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
		[]byte("package auth\n\n// TODO: implement JWT\nfunc Login() {}\n"), 0o600)

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

func TestConsensusHandlerRequiresModelFn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Consensus must never auto-approve — it requires a real model function
	handler := NewConsensusHandler(HandlerDeps{Store: store}, []string{"claude", "codex"})
	_, err = handler(context.Background(), m)
	if err == nil {
		t.Fatal("expected error when ConsensusModelFn is nil")
	}
	if !strings.Contains(err.Error(), "consensus requires") {
		t.Errorf("unexpected error: %v", err)
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

	// capturedPrompts is appended from concurrent consensus goroutines.
	// Guard with a mutex to prevent a -race report.
	var (
		capturedMu      sync.Mutex
		capturedPrompts []string
	)
	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: metrics,
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			capturedMu.Lock()
			capturedPrompts = append(capturedPrompts, prompt)
			capturedMu.Unlock()
			// Provide evidence-backed reasoning so anti-hallucination check passes
			return "complete", "Verified auth.go:42 implements JWT token issuance with proper expiry, jwt_test.go:18 covers the happy path and error cases thoroughly", nil, nil
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
	capturedMu.Lock()
	defer capturedMu.Unlock()
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

func TestConsensusHandlerCreatesGapObjects(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			return "incomplete", "Missing rate limiting", []string{"No rate limiter", "Missing input validation"}, nil
		},
	}, []string{"reviewer-1"})

	_, err = handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}

	// Consensus gaps should be stored as Gap objects
	gaps, _ := store.OpenGaps(m.ID)
	if len(gaps) < 2 {
		t.Fatalf("expected at least 2 gaps from consensus, got %d", len(gaps))
	}

	found := map[string]bool{}
	for _, g := range gaps {
		if g.Category == "consensus" {
			found[g.Description] = true
		}
	}
	if !found["No rate limiter"] {
		t.Error("expected 'No rate limiter' gap from consensus")
	}
	if !found["Missing input validation"] {
		t.Error("expected 'Missing input validation' gap from consensus")
	}
}

func TestConsensusRejectsVagueAffirmation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			return "complete", "looks good", nil, nil
		},
	}, []string{"reviewer-1"})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}

	// Vague "looks good" should be overridden to incomplete
	if !strings.Contains(result.Summary, "incomplete") {
		t.Errorf("expected incomplete verdict due to vague affirmation, got summary=%q", result.Summary)
	}

	// A rejection gap should have been created
	gaps, _ := store.OpenGaps(m.ID)
	foundRejection := false
	for _, g := range gaps {
		if strings.Contains(g.Description, "Consensus rejected") {
			foundRejection = true
		}
	}
	if !foundRejection {
		t.Error("expected a 'Consensus rejected' gap to be recorded")
	}
}

func TestConsensusRequiresEvidence(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			// Long enough to pass the terse check, no vague phrases, but no file references
			return "complete", "The implementation correctly handles all edge cases including error propagation and boundary conditions across all modules in the system", nil, nil
		},
	}, []string{"reviewer-1"})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Summary, "incomplete") {
		t.Errorf("expected incomplete verdict due to missing evidence, got summary=%q", result.Summary)
	}
}

func TestConsensusAcceptsEvidencedComplete(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	handler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			return "complete", "auth.go:42 verifies JWT signature with HMAC-SHA256, middleware.go:89 checks token expiry before allowing request through, auth_test.go:15 covers both valid and expired token scenarios", nil, nil
		},
	}, []string{"reviewer-1"})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Summary, "complete") {
		t.Errorf("expected complete verdict with evidence-backed reasoning, got summary=%q", result.Summary)
	}
	if strings.Contains(result.Summary, "incomplete") {
		t.Errorf("evidence-backed complete verdict should not be overridden, got summary=%q", result.Summary)
	}
}

func TestExecutePromptIncludesGaps(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Add an open gap
	store.AddGap(&Gap{
		ID:          "gap-exec-1",
		MissionID:   m.ID,
		Category:    "discovery-validation",
		Severity:    "blocking",
		Description: "Rate limiting not implemented",
	})

	var capturedPrompt string
	handler := NewExecuteHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		ExecuteFn: func(ctx context.Context, m *Mission, prompt, taskDesc string) ([]string, error) {
			capturedPrompt = prompt
			return nil, nil
		},
	})

	_, err = handler(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(capturedPrompt, "Rate limiting not implemented") {
		t.Error("execute prompt should include open gaps")
	}
	if !strings.Contains(capturedPrompt, "blocking") {
		t.Error("execute prompt should show gap severity")
	}
}

// --- Keyword Extraction ---

func TestResearchHandlerRecordsDiscovery(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	var recordedTopic, recordedContent string
	handler := NewResearchHandler(HandlerDeps{
		Store:   store,
		Metrics: NewMetrics(),
		DiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "FILE: handler.go\nGAP: Missing auth middleware", nil
		},
		RecordResearchFn: func(missionID, topic, content string) error {
			recordedTopic = topic
			recordedContent = content
			return nil
		},
	})

	handler(context.Background(), m)

	if recordedTopic == "" {
		t.Error("RecordResearchFn should be called with discovery results")
	}
	if !strings.Contains(recordedContent, "FILE: handler.go") {
		t.Error("recorded content should include discovery output")
	}
}

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
		[]byte("package jwt\n\nfunc IssueToken() {}\n"), 0o600)

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
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o600)

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
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o600)

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

func TestValidateHandlerLayer4ParsesJSON(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	// Pre-add an open gap to test JSON "fixed" resolution
	store.AddGap(&Gap{
		ID:          "json-fixed-gap",
		MissionID:   m.ID,
		Category:    "discovery-validation",
		Severity:    "blocking",
		Description: "Missing rate limiter",
	})

	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o600)

	jsonResponse := `Some preamble text before JSON.
{"gaps":[{"category":"security","severity":"blocking","file":"api.go","line":55,"description":"No input validation on user endpoint"}],"fixed":["Missing rate limiter"]}`

	handler := NewValidateHandler(HandlerDeps{
		Store:     store,
		Validator: convergence.NewValidator(),
		RepoRoot:  repoDir,
		Metrics:   NewMetrics(),
		ValidateDiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return jsonResponse, nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// The fixed gap should be resolved
	openAfter, _ := store.OpenGaps(m.ID)
	for _, g := range openAfter {
		if g.ID == "json-fixed-gap" {
			t.Error("json-fixed-gap should have been resolved via JSON fixed array")
		}
	}

	// New gap should be created with proper structured fields
	allGaps, _ := store.AllGaps(m.ID)
	found := false
	for _, g := range allGaps {
		if strings.Contains(g.Description, "No input validation") {
			found = true
			if g.File != "api.go" {
				t.Errorf("gap File should be 'api.go', got %q", g.File)
			}
			if g.Line != 55 {
				t.Errorf("gap Line should be 55, got %d", g.Line)
			}
			if g.Category != "security" {
				t.Errorf("gap Category should be 'security', got %q", g.Category)
			}
		}
	}
	if !found {
		t.Error("expected new gap from JSON parsing for 'No input validation'")
	}

	if !strings.Contains(result.Summary, "discovery-validation") {
		t.Errorf("summary should mention discovery-validation: %q", result.Summary)
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
		[]byte("package auth\n\n// TODO: refactor this\nvar api_key = \"abcdefghijklmnopqrstuvwxyz1234567890\"\n"), 0o600)

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

func TestValidateHandlerLayer3ParsesJSON(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := setupHandlerTestMission(t, store)

	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o600)

	jsonResponse := `{"verdict":"incomplete","gaps":[` +
		`{"category":"test","severity":"blocking","file":"auth.go","line":42,"description":"No test for token expiry","suggestion":"Add expiry test"},` +
		`{"category":"security","severity":"blocking","description":"SQL injection in user lookup"}` +
		`],"reasoning":"Missing critical tests"}`

	handler := NewValidateHandler(HandlerDeps{
		Store:    store,
		Validator: convergence.NewValidator(),
		RepoRoot: repoDir,
		Metrics:  NewMetrics(),
		ValidateFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return jsonResponse, nil
		},
	})

	result, err := handler(context.Background(), m)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if !strings.Contains(result.Summary, "adversarial: 2 gaps") {
		t.Errorf("summary should mention 2 gaps: %q", result.Summary)
	}

	// Verify individual gaps were created with correct metadata
	gaps, _ := store.OpenGaps(m.ID)
	var testGap, secGap *Gap
	for i := range gaps {
		if gaps[i].Category == "test" {
			testGap = &gaps[i]
		}
		if gaps[i].Category == "security" {
			secGap = &gaps[i]
		}
	}

	if testGap == nil {
		t.Error("should create gap with category 'test'")
	} else {
		if testGap.File != "auth.go" {
			t.Errorf("test gap file = %q, want 'auth.go'", testGap.File)
		}
		if testGap.Line != 42 {
			t.Errorf("test gap line = %d, want 42", testGap.Line)
		}
		if testGap.Suggestion != "Add expiry test" {
			t.Errorf("test gap suggestion = %q", testGap.Suggestion)
		}
	}

	if secGap == nil {
		t.Error("should create gap with category 'security'")
	} else if !strings.Contains(secGap.Description, "SQL injection") {
		t.Errorf("security gap description = %q", secGap.Description)
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
		[]byte("package auth\n\n// TODO: refactor this\nvar api_key = \"abcdefghijklmnopqrstuvwxyz1234567890\"\n"), 0o600)

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

// --- Full Pipeline Integration Test ---

func TestFullPipelineWithDiscovery(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := &Mission{
		ID:     "m-pipeline",
		Title:  "Add JWT Auth",
		Intent: "Add JWT authentication to the API with rate limiting",
		Criteria: []Criterion{
			{ID: "c-1", Description: "JWT tokens are issued on login"},
			{ID: "c-2", Description: "Rate limiting prevents abuse"},
		},
	}
	if err := store.Create(m); err != nil {
		t.Fatal(err)
	}

	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0o600)

	metrics := NewMetrics()

	// Phase 1: Research with DiscoveryFn
	researchHandler := NewResearchHandler(HandlerDeps{
		Store:   store,
		RepoRoot: repoDir,
		Metrics: metrics,
		DiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "FILE: internal/auth/jwt.go\nFILE: internal/auth/handler.go\nGAP: No auth middleware exists yet\n", nil
		},
		RecordResearchFn: func(missionID, topic, content string) error {
			return nil // skip persistence in this test
		},
	})

	researchResult, err := researchHandler(context.Background(), m)
	if err != nil {
		t.Fatalf("research: %v", err)
	}
	if len(researchResult.FilesChanged) == 0 {
		t.Error("research should find relevant files")
	}
	// Discovery should create a gap
	gaps, _ := store.OpenGaps(m.ID)
	if len(gaps) == 0 {
		t.Error("research discovery should create gaps")
	}

	// Phase 2: Plan
	store.Advance(m.ID, PhasePlanning, "research complete", "test")
	m, _ = store.Get(m.ID)

	planHandler := NewPlanHandler(HandlerDeps{
		Store:   store,
		Metrics: metrics,
	})
	planResult, err := planHandler(context.Background(), m)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if planResult.Artifacts["prompt"] == "" {
		t.Error("plan should produce a prompt artifact")
	}

	// Phase 3: Execute
	store.Advance(m.ID, PhaseExecuting, "plan ready", "test")
	m, _ = store.Get(m.ID)

	var executedPrompt string
	executeHandler := NewExecuteHandler(HandlerDeps{
		Store:   store,
		Metrics: metrics,
		ExecuteFn: func(ctx context.Context, m *Mission, prompt, taskDesc string) ([]string, error) {
			executedPrompt = prompt
			return []string{"internal/auth/jwt.go", "internal/auth/jwt_test.go"}, nil
		},
	})
	execResult, err := executeHandler(context.Background(), m)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(execResult.FilesChanged) != 2 {
		t.Errorf("execute should report 2 files changed, got %d", len(execResult.FilesChanged))
	}
	// Execute prompt should contain mission context
	if !strings.Contains(executedPrompt, "JWT") {
		t.Error("execute prompt should contain mission intent")
	}
	if !strings.Contains(executedPrompt, "search_symbols") {
		t.Error("execute prompt should mention MCP codebase tools")
	}

	// Phase 4: Validate with Layer 4
	store.Advance(m.ID, PhaseValidating, "execution done", "test")
	m, _ = store.Get(m.ID)

	validateHandler := NewValidateHandler(HandlerDeps{
		Store:     store,
		Validator: convergence.NewValidator(),
		RepoRoot:  repoDir,
		Metrics:   metrics,
		ValidateDiscoveryFn: func(ctx context.Context, m *Mission, prompt string) (string, error) {
			return "GAP: Rate limiting not implemented yet\nFIXED: No auth middleware exists yet\n", nil
		},
	})
	valResult, err := validateHandler(context.Background(), m)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Should have resolved the old gap and added a new one
	if !strings.Contains(valResult.Summary, "discovery-validation") {
		t.Errorf("validate summary should mention discovery-validation: %q", valResult.Summary)
	}
	if !strings.Contains(valResult.Summary, "security-only") {
		t.Errorf("validate summary should mention security-only mode: %q", valResult.Summary)
	}

	// Phase 5: Consensus
	store.Advance(m.ID, PhaseConverged, "validation done", "test")
	m, _ = store.Get(m.ID)

	consensusHandler := NewConsensusHandler(HandlerDeps{
		Store:   store,
		Metrics: metrics,
		ConsensusModelFn: func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
			return "incomplete", "Rate limiting still missing", []string{"No rate limiter"}, nil
		},
	}, []string{"claude", "codex"})
	consResult, err := consensusHandler(context.Background(), m)
	if err != nil {
		t.Fatalf("consensus: %v", err)
	}
	if !strings.Contains(consResult.Summary, "incomplete") {
		t.Errorf("consensus should be incomplete: %q", consResult.Summary)
	}

	// Verify metrics tracked across all phases
	snap := metrics.Snapshot()
	if snap.ResearchQueries == 0 {
		t.Error("should have recorded research queries")
	}
	if snap.ConsensusVotes != 2 {
		t.Errorf("should have 2 consensus votes, got %d", snap.ConsensusVotes)
	}
}
