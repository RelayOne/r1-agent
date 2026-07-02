package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
)

// buildRecallEngine wires an Engine whose execute always "succeeds" (writes a
// file) but whose verify always fails, so every attempt reaches the
// failure->wisdom path. wisdom is a Recorder so the caller can inject either
// the in-memory or the persistent store.
func buildRecallEngine(t *testing.T, repo, name string, ws wisdom.Recorder) (Engine, *mockRunner) {
	t.Helper()
	mock := newMockRunner()
	mock.FilesToWrite = map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	}

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = true // the failing gate
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	wf := Engine{
		RepoRoot:     repo,
		Task:         "Fix the flaky handler",
		TaskType:     model.TaskTypeRefactor,
		WorktreeName: name,
		AuthMode:     engine.AuthModeMode1,
		Policy:       policy,
		Worktrees:    stubManager{repo: repo},
		Runners:      engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		// A test command that always fails with a stable, non-empty message
		// so failure.Compute yields the same fingerprint across both runs.
		Verifier:       verify.NewPipeline("", "echo 'ZZTESTFAIL: assertion mismatch'; exit 1", ""),
		State:          taskstate.NewTaskState(name),
		Wisdom:         ws,
		RunnerOverride: mock,
	}
	return wf, mock
}

// TestWisdomRecallAcrossRuns is the P4 regression: the retry loop RECORDS a
// failure fingerprint into wisdom but never RECALLED it. Against a persistent
// SQLite store, a fix/learning recorded for a failure pattern on one run must
// be surfaced into the retry prompt when the same pattern recurs on a later
// run. Two Engine runs share one on-disk DB (separate store instances, as
// separate processes would); the second run's retry prompt must carry the
// first run's learning.
func TestWisdomRecallAcrossRuns(t *testing.T) {
	repo := initTestRepo(t)
	dbPath := filepath.Join(t.TempDir(), "wisdom.db")

	// --- Run 1: populate the shared store with a failure learning. ---
	store1, err := wisdom.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store1: %v", err)
	}
	wf1, _ := buildRecallEngine(t, repo, "p4-run1", store1)
	_, _ = wf1.Run(context.Background()) // expected to fail (verify never passes)

	learnings := store1.Learnings()
	var learnedDesc string
	for _, l := range learnings {
		if l.Category == wisdom.Gotcha && l.FailurePattern != "" {
			learnedDesc = l.Description
			break
		}
	}
	store1.Close()
	if learnedDesc == "" {
		t.Fatalf("run 1 did not record a fingerprinted gotcha; learnings=%+v", learnings)
	}

	// --- Run 2: a fresh store instance on the SAME db must recall run 1. ---
	store2, err := wisdom.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store2: %v", err)
	}
	defer store2.Close()
	wf2, mock2 := buildRecallEngine(t, repo, "p4-run2", store2)
	_, _ = wf2.Run(context.Background())

	execPrompts := mock2.Prompts["execute"]
	if len(execPrompts) < 2 {
		t.Fatalf("run 2 did not reach a retry attempt; execute prompts=%d", len(execPrompts))
	}
	retryPrompt := execPrompts[1] // attempt 2
	if !strings.Contains(retryPrompt, "CROSS-RUN RECALL") {
		t.Errorf("retry prompt missing cross-run recall marker; prompt:\n%s", retryPrompt)
	}
	if !strings.Contains(retryPrompt, learnedDesc) {
		t.Errorf("retry prompt does not carry run 1's learning %q; prompt:\n%s", learnedDesc, retryPrompt)
	}
}

// TestSQLiteStoreSatisfiesRecorder is a lightweight guard that the persistent
// store is usable as the Engine.Wisdom Recorder (the P4 interface change).
func TestSQLiteStoreSatisfiesRecorder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "w.db")
	s, err := wisdom.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	var _ wisdom.Recorder = s
	e := Engine{Wisdom: s}
	if e.Wisdom == nil {
		t.Fatal("SQLiteStore not accepted as Engine.Wisdom")
	}
}
