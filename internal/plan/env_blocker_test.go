package plan

import (
	"context"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// TestEnvBlockerScratch_RecordGetClear exercises the basic scratch
// lifecycle: record, get, clear, clear-session.
func TestEnvBlockerScratch_RecordGetClear(t *testing.T) {
	s := NewEnvBlockerScratch()
	if _, ok := s.Get("S1", "AC-1"); ok {
		t.Fatalf("empty scratch should not have entry")
	}
	s.Record(EnvBlockerReport{SessionID: "S1", ACID: "AC-1", Issue: "pnpm not on PATH"})
	r, ok := s.Get("S1", "AC-1")
	if !ok {
		t.Fatalf("expected record after Record")
	}
	if r.Issue != "pnpm not on PATH" {
		t.Errorf("issue=%q, want %q", r.Issue, "pnpm not on PATH")
	}
	s.Clear("S1", "AC-1")
	if _, ok := s.Get("S1", "AC-1"); ok {
		t.Errorf("expected cleared")
	}
	// ClearSession
	s.Record(EnvBlockerReport{SessionID: "S2", ACID: "AC-1", Issue: "x"})
	s.Record(EnvBlockerReport{SessionID: "S2", ACID: "AC-2", Issue: "y"})
	s.Record(EnvBlockerReport{SessionID: "S3", ACID: "AC-1", Issue: "z"})
	s.ClearSession("S2")
	if _, ok := s.Get("S2", "AC-1"); ok {
		t.Errorf("S2/AC-1 should be cleared")
	}
	if _, ok := s.Get("S2", "AC-2"); ok {
		t.Errorf("S2/AC-2 should be cleared")
	}
	if _, ok := s.Get("S3", "AC-1"); !ok {
		t.Errorf("S3/AC-1 should survive S2 clear")
	}
}

// TestEnvBlockerFastPath verifies that when the scratch has an
// env-blocked marker for the active AC, the descent engine T3
// classifier sets Category="environment" without consulting the
// Provider. The fixture injects a provider whose every call would
// panic — if it runs, the test fails.
func TestEnvBlockerFastPath(t *testing.T) {
	sess := Session{
		ID:    "S-FAST",
		Title: "env blocker fast path",
		Tasks: []Task{{ID: "T1", Files: []string{"src/a.ts"}}},
		AcceptanceCriteria: []AcceptanceCriterion{
			{ID: "AC-1", Description: "env blocker fixture", Command: "exit 127"},
		},
	}
	ac := sess.AcceptanceCriteria[0]

	// Pre-record an env blocker before descent runs.
	DefaultEnvBlockerScratch().Record(EnvBlockerReport{
		SessionID: sess.ID,
		TaskID:    "T1",
		ACID:      ac.ID,
		Issue:     "pnpm not on PATH",
	})
	defer DefaultEnvBlockerScratch().Clear(sess.ID, ac.ID)

	// Panicking provider: if runDescentReasoning runs, the test
	// fails because the fast-path should skip the LLM entirely.
	provider := &panicOnCallProvider{t: t}

	envFixCalled := false
	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session:  sess,
		Provider: provider,
		// Intent confirmed so we reach T3 instead of bailing at T1.
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "intent confirmed"
		},
		// EnvFix succeeds so T5 returns to pass (exercises the full
		// fast-path → T5 flow).
		EnvFixFunc: func(ctx context.Context, cause, stderr string) bool {
			envFixCalled = true
			return false // report failure so we fall through to T6
		},
	}

	result := VerificationDescent(context.Background(), ac, "bash: pnpm: command not found\nexit status 127\n", cfg)

	if result.Category != EnvBlockerFastPathCategory {
		t.Errorf("expected Category=environment, got %q", result.Category)
	}
	if !envFixCalled {
		t.Errorf("expected EnvFixFunc to be called (T5), it was not")
	}
}

// panicOnCallProvider is a Provider stub whose every method panics.
// Used by TestEnvBlockerFastPath to assert the multi-analyst path is
// NOT taken.
type panicOnCallProvider struct {
	t *testing.T
}

func (p *panicOnCallProvider) Name() string { return "panic-on-call" }

func (p *panicOnCallProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	p.t.Fatalf("provider.Chat called in fast-path fixture — envBlocker should have short-circuited")
	return nil, nil
}

func (p *panicOnCallProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	p.t.Fatalf("provider.ChatStream called in fast-path fixture — envBlocker should have short-circuited")
	return nil, nil
}
