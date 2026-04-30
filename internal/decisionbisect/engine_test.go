package decisionbisect

import (
	"testing"
	"time"
)

func TestAnalyze(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	narrative, err := Analyze(Input{
		MissionID:   "MISSION-abc",
		Description: "Add JWT auth",
		Regression:  "test:auth/login_test.go::TestRateLimit",
		Decisions: []DecisionPoint{
			{Step: "AC-3", VerifierVerdict: "partial"},
			{Step: "policy fast preset", PolicyChange: "gates.fast.tier_max=T8"},
			{Step: "reviewer", Dissents: []string{"missing concurrent-request handling"}},
			{Step: "merge", Override: "hitl-9d8e7f6a"},
		},
	}, now)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if narrative.RootCause == "" {
		t.Fatal("expected root cause")
	}
	if narrative.Learning.FailurePattern == "" {
		t.Fatal("expected failure pattern")
	}
	if len(narrative.Steps) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(narrative.Steps))
	}
}
