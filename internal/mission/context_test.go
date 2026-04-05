package mission

import (
	"fmt"
	"strings"
	"testing"
)

// mockContextSource implements ContextSource for testing.
type mockContextSource struct {
	research []ResearchEntry
	handoff  string
}

func (m *mockContextSource) SearchResearch(query string, limit int) ([]ResearchEntry, error) {
	if limit > len(m.research) {
		return m.research, nil
	}
	return m.research[:limit], nil
}

func (m *mockContextSource) GetResearchByMission(missionID string) ([]ResearchEntry, error) {
	var entries []ResearchEntry
	for _, e := range m.research {
		entries = append(entries, e)
	}
	return entries, nil
}

func (m *mockContextSource) GetHandoffContext(missionID string, maxTokens int) (string, error) {
	return m.handoff, nil
}

func newTestContextBuilder(t *testing.T) (*ContextBuilder, *Store) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	source := &mockContextSource{
		research: []ResearchEntry{
			{Topic: "JWT Auth", Query: "How does JWT work?", Content: "JWT uses base64 encoding with HMAC signatures", Source: "https://jwt.io"},
			{Topic: "Rate Limiting", Query: "Token bucket algorithm", Content: "Token bucket allows bursts while maintaining average rate", Source: "docs"},
		},
		handoff: "## Previous Agent (agent-1)\nImplemented JWT generation. Login endpoint working.\n\n**Pending:**\n- Rate limiting\n- Edge case tests\n",
	}

	cb, err := NewContextBuilder(store, source)
	if err != nil {
		t.Fatalf("NewContextBuilder: %v", err)
	}
	return cb, store
}

func createContextTestMission(t *testing.T, store *Store) {
	t.Helper()
	m := &Mission{
		ID:     "m-ctx",
		Title:  "Implement JWT Auth",
		Intent: "Add JWT authentication to the API",
		Tags:   []string{"auth", "security"},
		Criteria: []Criterion{
			{ID: "c-1", Description: "JWT tokens issued on login"},
			{ID: "c-2", Description: "Invalid tokens return 401"},
			{ID: "c-3", Description: "Rate limiting returns 429"},
		},
	}
	if err := store.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// --- Full Context ---

func TestBuildContextFull(t *testing.T) {
	cb, store := newTestContextBuilder(t)
	createContextTestMission(t, store)

	// Satisfy one criterion
	store.SetCriteriaSatisfied("m-ctx", "c-1", "jwt.go:Login returns token", "agent-1")

	// Add a gap
	store.AddGap(&Gap{
		ID: "g-1", MissionID: "m-ctx", Category: "test", Severity: "blocking",
		Description: "No tests for rate limiting", Suggestion: "Add rate_limit_test.go",
	})

	ctx, err := cb.BuildContext("m-ctx", DefaultContextConfig())
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	// Verify all sections present
	checks := []string{
		"# Mission: Implement JWT Auth",    // mission title
		"**Intent:** Add JWT",              // intent
		"**Phase:** created",               // phase
		"auth, security",                   // tags
		"Convergence Status",               // convergence section
		"1/3 satisfied",                    // criteria progress
		"Acceptance Criteria (1/3)",         // criteria header
		"[x] JWT tokens issued",            // satisfied criterion
		"[ ] Invalid tokens",               // unsatisfied criterion
		"Open Gaps (1)",                    // gaps header
		"[blocking]",                        // gap severity
		"rate limiting",                     // gap description (case-insensitive in content)
		"Add rate_limit_test.go",           // gap suggestion
		"Research Context",                  // research section
		"JWT Auth",                          // research topic
		"base64 encoding",                   // research content
		"Previous Agent",                    // handoff section
	}
	for _, check := range checks {
		if !strings.Contains(ctx, check) {
			t.Errorf("context missing %q\nFull context:\n%s", check, ctx)
		}
	}
}

// --- Minimal Config ---

func TestBuildContextMinimal(t *testing.T) {
	cb, store := newTestContextBuilder(t)
	createContextTestMission(t, store)

	config := ContextConfig{
		MaxTokens:          4000,
		IncludeMissionInfo: true,
		// Everything else off
	}

	ctx, err := cb.BuildContext("m-ctx", config)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	if !strings.Contains(ctx, "Implement JWT Auth") {
		t.Error("should include mission title")
	}
	if strings.Contains(ctx, "Acceptance Criteria") {
		t.Error("should not include criteria when disabled")
	}
	if strings.Contains(ctx, "Research Context") {
		t.Error("should not include research when disabled")
	}
}

// --- No Source ---

func TestBuildContextNoSource(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cb, err := NewContextBuilder(store, nil) // nil source
	if err != nil {
		t.Fatalf("NewContextBuilder: %v", err)
	}

	store.Create(&Mission{
		ID: "m-nosrc", Title: "No Source", Intent: "test",
		Criteria: []Criterion{{ID: "c-1", Description: "done"}},
	})

	ctx, err := cb.BuildContext("m-nosrc", DefaultContextConfig())
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	if !strings.Contains(ctx, "No Source") {
		t.Error("should include mission info even without source")
	}
	// Research and handoff sections should be absent
	if strings.Contains(ctx, "Research Context") {
		t.Error("should not include research without source")
	}
}

// --- Token Budget ---

func TestBuildContextTokenBudget(t *testing.T) {
	cb, store := newTestContextBuilder(t)
	createContextTestMission(t, store)

	// Add many gaps to inflate context
	for i := 0; i < 50; i++ {
		store.AddGap(&Gap{
			ID: fmt.Sprintf("g-%d", i), MissionID: "m-ctx",
			Category: "code", Severity: "minor",
			Description: strings.Repeat("long description about code quality ", 5),
		})
	}

	config := DefaultContextConfig()
	config.MaxTokens = 200 // Very small budget: ~800 chars

	ctx, err := cb.BuildContext("m-ctx", config)
	if err != nil {
		t.Fatal(err)
	}

	// Should be truncated
	if len(ctx) > 850 {
		t.Errorf("context length = %d, should be truncated to ~800 chars", len(ctx))
	}
}

// --- Nonexistent Mission ---

func TestBuildContextNonexistent(t *testing.T) {
	cb, _ := newTestContextBuilder(t)
	_, err := cb.BuildContext("ghost", DefaultContextConfig())
	if err == nil {
		t.Error("should error for nonexistent mission")
	}
}

// --- Evidence in Criteria ---

func TestBuildContextCriteriaEvidence(t *testing.T) {
	cb, store := newTestContextBuilder(t)
	createContextTestMission(t, store)

	store.SetCriteriaSatisfied("m-ctx", "c-1", "jwt.go:42 — token generation verified", "claude")

	config := DefaultContextConfig()
	config.IncludeResearch = false
	config.IncludeHandoffs = false

	ctx, err := cb.BuildContext("m-ctx", config)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(ctx, "Evidence: jwt.go:42") {
		t.Errorf("should include evidence for satisfied criteria\nFull context:\n%s", ctx)
	}
}

// --- Default Config ---

func TestDefaultContextConfig(t *testing.T) {
	cfg := DefaultContextConfig()
	if cfg.MaxTokens != 4000 {
		t.Errorf("MaxTokens = %d", cfg.MaxTokens)
	}
	if !cfg.IncludeMissionInfo {
		t.Error("IncludeMissionInfo should be true")
	}
	if !cfg.IncludeCriteria {
		t.Error("IncludeCriteria should be true")
	}
	if !cfg.IncludeGaps {
		t.Error("IncludeGaps should be true")
	}
	if !cfg.IncludeResearch {
		t.Error("IncludeResearch should be true")
	}
	if !cfg.IncludeHandoffs {
		t.Error("IncludeHandoffs should be true")
	}
	if cfg.MaxResearchEntries != 5 {
		t.Errorf("MaxResearchEntries = %d", cfg.MaxResearchEntries)
	}
}

// --- Nil Store Panics ---

func TestNewContextBuilderErrorsOnNilStore(t *testing.T) {
	_, err := NewContextBuilder(nil, nil)
	if err == nil {
		t.Error("NewContextBuilder(nil) should return error")
	}
}

// --- truncateStr ---

func TestTruncateStr(t *testing.T) {
	if truncateStr("short", 100) != "short" {
		t.Error("should not truncate short strings")
	}
	long := strings.Repeat("a", 200)
	tr := truncateStr(long, 50)
	if len(tr) != 50 {
		t.Errorf("truncated length = %d, want 50", len(tr))
	}
	if !strings.HasSuffix(tr, "...") {
		t.Error("should end with ...")
	}
}
