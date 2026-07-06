package handoff

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/RelayOne/r1/internal/mission"
)

func newTestChain(t *testing.T) (*Chain, *mission.Store) {
	t.Helper()
	store, err := mission.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	chain, err := NewChain(store)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return chain, store
}

func setupMission(t *testing.T, store *mission.Store) {
	t.Helper()
	m := &mission.Mission{
		ID:     "m-1",
		Title:  "Implement JWT Auth",
		Intent: "Add JWT authentication to the API with rate limiting and tests",
		Criteria: []mission.Criterion{
			{ID: "c-1", Description: "JWT tokens issued on login"},
			{ID: "c-2", Description: "Invalid tokens return 401"},
			{ID: "c-3", Description: "Rate limiting returns 429"},
		},
	}
	if err := store.Create(m); err != nil {
		t.Fatalf("Create mission: %v", err)
	}
}

// --- Basic Handoff ---

func TestHandoffAndRetrieve(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	err := c.Handoff(Record{
		MissionID:    "m-1",
		FromAgent:    "agent-1",
		ToAgent:      "agent-2",
		Summary:      "Implemented JWT token generation and validation. Tests for happy path passing.",
		PendingWork:  []string{"Add rate limiting middleware", "Write edge case tests for expired tokens"},
		KeyDecisions: []string{"Using golang-jwt/jwt/v5 library", "Sliding window for rate limiting"},
		FilesChanged: []string{"internal/auth/jwt.go", "internal/auth/jwt_test.go"},
		TestStatus:   "passing",
	})
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}

	latest, err := c.Latest("m-1")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil {
		t.Fatal("Latest returned nil")
	}
	if latest.FromAgent != "agent-1" {
		t.Errorf("FromAgent = %q", latest.FromAgent)
	}
	if latest.ToAgent != "agent-2" {
		t.Errorf("ToAgent = %q", latest.ToAgent)
	}
	if !strings.Contains(latest.Summary, "JWT token generation") {
		t.Errorf("Summary = %q", latest.Summary)
	}
	if len(latest.PendingWork) != 2 {
		t.Errorf("PendingWork = %v", latest.PendingWork)
	}
	if len(latest.KeyDecisions) != 2 {
		t.Errorf("KeyDecisions = %v", latest.KeyDecisions)
	}
}

func TestHandoffValidation(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	err := c.Handoff(Record{MissionID: "", Summary: "no mission"})
	if err == nil {
		t.Error("should reject empty mission ID")
	}

	err = c.Handoff(Record{MissionID: "m-1", Summary: ""})
	if err == nil {
		t.Error("should reject empty summary")
	}
}

// --- Chain History ---

func TestHandoffChain(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	c.Handoff(Record{
		MissionID: "m-1", FromAgent: "agent-1", ToAgent: "agent-2",
		Summary: "Phase 1: JWT implementation done",
	})
	c.Handoff(Record{
		MissionID: "m-1", FromAgent: "agent-2", ToAgent: "agent-3",
		Summary: "Phase 2: Rate limiting done",
	})
	c.Handoff(Record{
		MissionID: "m-1", FromAgent: "agent-3", ToAgent: "agent-4",
		Summary: "Phase 3: Tests and docs",
	})

	history, err := c.History("m-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history = %d, want 3", len(history))
	}

	// Should be chronological
	if history[0].FromAgent != "agent-1" || history[2].FromAgent != "agent-3" {
		t.Error("history should be in chronological order")
	}

	count, _ := c.Count("m-1")
	if count != 3 {
		t.Errorf("count = %d", count)
	}
}

func TestLatestEmpty(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	latest, err := c.Latest("m-1")
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		t.Error("should return nil for no handoffs")
	}
}

// --- Context Building ---

func TestBuildContext(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	// Add a gap
	store.AddGap(&mission.Gap{
		ID: "g-1", MissionID: "m-1", Category: "test", Severity: "blocking",
		Description: "No tests for rate limiting",
	})

	// Satisfy one criterion
	store.SetCriteriaSatisfied("m-1", "c-1", "jwt.go:Login returns token", "agent-1")

	// Record a handoff
	c.Handoff(Record{
		MissionID:    "m-1",
		FromAgent:    "agent-1",
		ToAgent:      "agent-2",
		Summary:      "Implemented JWT generation. Login endpoint working.",
		PendingWork:  []string{"Rate limiting", "Edge case tests"},
		KeyDecisions: []string{"Using HS256 signing"},
	})

	ctx, err := c.BuildContext("m-1", 2000)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	// Verify context contains key information
	checks := []string{
		"Implement JWT Auth",       // mission title
		"JWT authentication",       // intent
		"1/3 satisfied",            // criteria progress
		"[x] JWT tokens issued",    // satisfied criterion
		"[ ] Invalid tokens",       // unsatisfied criterion
		"blocking",                 // gap severity
		"rate limiting",            // gap description (case-insensitive handled by content)
		"agent-1",                  // previous agent
		"JWT generation",           // summary
		"Rate limiting",            // pending work
		"HS256 signing",            // key decision
	}
	for _, check := range checks {
		if !strings.Contains(ctx, check) {
			t.Errorf("context missing %q\nFull context:\n%s", check, ctx)
		}
	}
}

func TestBuildContextTokenBudget(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	// Add many handoffs to test truncation
	for i := 0; i < 20; i++ {
		c.Handoff(Record{
			MissionID: "m-1",
			FromAgent: "agent",
			ToAgent:   "next",
			Summary:   strings.Repeat("detailed context about what happened in this phase ", 10),
		})
	}

	// Very small budget
	ctx, err := c.BuildContext("m-1", 200)
	if err != nil {
		t.Fatal(err)
	}

	// Should be truncated to roughly 200 tokens * 4 chars = 800 chars
	if len(ctx) > 900 {
		t.Errorf("context length = %d, should be truncated to ~800 chars", len(ctx))
	}
}

func TestBuildContextNoHandoffs(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	ctx, err := c.BuildContext("m-1", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx, "Implement JWT Auth") {
		t.Error("context should still show mission info even without handoffs")
	}
}

func TestBuildContextNonexistentMission(t *testing.T) {
	c, _ := newTestChain(t)
	_, err := c.BuildContext("ghost", 2000)
	if err == nil {
		t.Error("should error for nonexistent mission")
	}
}

// --- Edge Cases ---

func TestNewChainErrorsOnNilStore(t *testing.T) {
	_, err := NewChain(nil)
	if err == nil {
		t.Error("NewChain(nil) should return error")
	}
}

func TestParseList(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"- item one\n- item two", 2},
		{"single item", 1},
		{"- a\n- b\n- c", 3},
	}
	for _, tc := range tests {
		got := parseList(tc.input)
		if len(got) != tc.want {
			t.Errorf("parseList(%q) = %d items, want %d", tc.input, len(got), tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 100) != "short" {
		t.Error("should not truncate short strings")
	}
	long := strings.Repeat("a", 200)
	truncated := truncate(long, 50)
	if utf8.RuneCountInString(truncated) != 50 {
		t.Errorf("truncated rune count = %d, want 50", utf8.RuneCountInString(truncated))
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Error("truncated string should end with ...")
	}
}

// TestTruncateMultibyte proves truncate is rune-aware and bounds-safe: it must
// never panic on tiny limits and must never emit invalid (split) UTF-8.
func TestTruncateMultibyte(t *testing.T) {
	// 40 CJK runes = 120 bytes. A byte-slice at rune count 10 would land
	// mid-rune and corrupt the output.
	long := strings.Repeat("日本語漢字", 8) // 40 runes
	cases := []int{0, 1, 2, 3, 4, 5, 10, 39, 40, 41, 1000}
	for _, n := range cases {
		got := truncate(long, n)
		if !utf8.ValidString(got) {
			t.Errorf("truncate(cjk, %d) produced invalid UTF-8: %q", n, got)
		}
		if utf8.RuneCountInString(got) > n && n > 0 {
			// allow the "..." suffix to fit within n (maxLen-3 runes + 3)
			t.Errorf("truncate(cjk, %d) has %d runes, exceeds limit", n, utf8.RuneCountInString(got))
		}
	}
	// Also cover mixed em-dash / emoji content.
	mixed := "café — naïve — 🚀 rocket — über"
	for n := 0; n <= utf8.RuneCountInString(mixed)+2; n++ {
		if !utf8.ValidString(truncate(mixed, n)) {
			t.Errorf("truncate(mixed, %d) produced invalid UTF-8", n)
		}
	}
}

// TestBuildContextTinyBudgetMultibyte proves BuildContext no longer panics on a
// sub-marker budget and never returns invalid UTF-8 when the handoff text is
// multibyte. Before the fix, result[:charBudget-20] panicked (negative index)
// and/or split a multibyte rune.
func TestBuildContextTinyBudgetMultibyte(t *testing.T) {
	c, store := newTestChain(t)
	setupMission(t, store)

	// Multibyte-heavy summary so any byte-boundary slice would corrupt UTF-8.
	c.Handoff(Record{
		MissionID: "m-1",
		FromAgent: "エージェント一号",
		ToAgent:   "エージェント二号",
		Summary:   strings.Repeat("実装した機能について詳しく説明する — 進捗と決定事項 🚀 ", 20),
	})

	// Budgets that drive charBudget below the truncation marker length,
	// including values that made the old code slice with a negative index.
	for _, budget := range []int{-5, 0, 1, 2, 3, 4, 5, 10, 50, 200} {
		ctx, err := c.BuildContext("m-1", budget)
		if err != nil {
			t.Fatalf("BuildContext(%d): %v", budget, err)
		}
		if !utf8.ValidString(ctx) {
			t.Errorf("BuildContext(%d) returned invalid UTF-8", budget)
		}
	}
}
