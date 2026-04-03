package interview

import (
	"strings"
	"testing"
)

func TestNewSession(t *testing.T) {
	s := NewSession("add user authentication")
	if s.CurrentPhase() != PhaseGoal {
		t.Errorf("expected goal phase, got %s", s.CurrentPhase())
	}
	if s.IsComplete() {
		t.Error("new session should not be complete")
	}
	if s.Progress() != 0 {
		t.Error("new session should have 0 progress")
	}
}

func TestSessionFlow(t *testing.T) {
	s := NewSession("fix the login bug")

	// Answer all questions
	for !s.IsComplete() {
		q := s.NextQuestion()
		if q == nil {
			t.Fatal("NextQuestion returned nil before complete")
		}
		s.Answer("test response for: " + q.Question)
	}

	if !s.IsComplete() {
		t.Error("expected complete after answering all questions")
	}
	if s.Progress() != 1.0 {
		t.Errorf("expected progress 1.0, got %f", s.Progress())
	}
	if s.CurrentPhase() != PhaseApproval {
		t.Errorf("expected approval phase, got %s", s.CurrentPhase())
	}
}

func TestSessionSkip(t *testing.T) {
	s := NewSession("simple task")

	// Skip all questions
	for !s.IsComplete() {
		s.Skip()
	}

	scope := s.Synthesize()
	if scope.Confidence != 0 {
		t.Errorf("all-skipped session should have 0 confidence, got %f", scope.Confidence)
	}
}

func TestSynthesize(t *testing.T) {
	s := NewSession("add caching layer")

	// Answer questions by phase
	// Goal
	s.Answer("Add Redis caching for API responses to reduce latency below 50ms")
	s.Answer("Assumes Redis is already deployed")
	// Boundary
	s.Answer("Cache GET endpoints; invalidate on POST/PUT/DELETE")
	s.Answer("Don't cache auth endpoints; don't add cache warming")
	// Constraint
	s.Answer("Use go-redis library; TTL max 5 minutes")
	// Risk
	s.Answer("Cache stampede; stale data on crash")
	// Verify
	s.Answer("Latency benchmark; cache hit rate > 80%")

	scope := s.Synthesize()

	if scope.Goal == "" {
		t.Error("expected goal to be set")
	}
	if !strings.Contains(scope.Goal, "Redis") {
		t.Error("expected goal to mention Redis")
	}
	if len(scope.InScope) == 0 {
		t.Error("expected in-scope items")
	}
	if len(scope.OutOfScope) == 0 {
		t.Error("expected out-of-scope items")
	}
	if len(scope.Constraints) == 0 {
		t.Error("expected constraints")
	}
	if len(scope.Risks) == 0 {
		t.Error("expected risks")
	}
	if len(scope.SuccessCriteria) == 0 {
		t.Error("expected success criteria")
	}
	if scope.Confidence <= 0 {
		t.Error("expected positive confidence")
	}
}

func TestToPrompt(t *testing.T) {
	scope := &ClarifiedScope{
		OriginalRequest: "add auth",
		Goal:            "JWT-based authentication",
		InScope:         []string{"login endpoint", "token refresh"},
		OutOfScope:      []string{"OAuth", "social login"},
		Constraints:     []string{"use existing user table"},
		Risks:           []string{"token leakage"},
		SuccessCriteria: []string{"all auth tests pass", "no token in logs"},
		Assumptions:     []string{"bcrypt for passwords"},
	}

	prompt := scope.ToPrompt()

	checks := []string{
		"## Task: add auth",
		"### Goal",
		"JWT-based authentication",
		"### In Scope",
		"login endpoint",
		"### Out of Scope (DO NOT implement)",
		"OAuth",
		"### Constraints (MUST follow)",
		"use existing user table",
		"### Risks",
		"token leakage",
		"### Success Criteria",
		"- [ ] all auth tests pass",
		"### Assumptions",
		"bcrypt for passwords",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("expected prompt to contain %q", check)
		}
	}
}

func TestContextSpecificQuestions(t *testing.T) {
	// API task should get API-specific questions
	apiSession := NewSession("build a REST API for user management")
	hasAPIQuestion := false
	for _, q := range apiSession.questions {
		if strings.Contains(q.Question, "API contract") {
			hasAPIQuestion = true
		}
	}
	if !hasAPIQuestion {
		t.Error("API task should generate API-specific questions")
	}

	// Security task should get security-specific questions
	secSession := NewSession("add authentication to the admin panel")
	hasSecQuestion := false
	for _, q := range secSession.questions {
		if strings.Contains(q.Question, "threat model") {
			hasSecQuestion = true
		}
	}
	if !hasSecQuestion {
		t.Error("security task should generate security-specific questions")
	}

	// Refactor task should get refactor-specific questions
	refSession := NewSession("refactor the database layer")
	hasRefQuestion := false
	for _, q := range refSession.questions {
		if strings.Contains(q.Question, "external behavior") {
			hasRefQuestion = true
		}
	}
	if !hasRefQuestion {
		t.Error("refactor task should generate refactor-specific questions")
	}
}

func TestInterviewPrompt(t *testing.T) {
	prompt := InterviewPrompt("add caching")
	if !strings.Contains(prompt, "add caching") {
		t.Error("expected request in prompt")
	}
	if !strings.Contains(prompt, "Interview Protocol") {
		t.Error("expected protocol instructions")
	}
	if !strings.Contains(prompt, "DO NOT start implementing") {
		t.Error("expected implementation guard")
	}
}

func TestSplitItems(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"single item", 1},
		{"item1, item2, item3", 3},
		{"item1; item2", 2},
		{"- item1\n- item2\n- item3", 3},
		{"* first\n* second", 2},
	}
	for _, tc := range tests {
		got := splitItems(tc.input)
		if len(got) != tc.want {
			t.Errorf("splitItems(%q) = %d items, want %d", tc.input, len(got), tc.want)
		}
	}
}

func TestNextQuestionNilWhenComplete(t *testing.T) {
	s := NewSession("task")
	for !s.IsComplete() {
		s.Skip()
	}
	if s.NextQuestion() != nil {
		t.Error("expected nil after completion")
	}
}
