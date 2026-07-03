package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/worktree"
)

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestSkipMergeLeavesVerifiedWorktree runs the full pipeline with
// SkipMerge against a real temp git repo and asserts the contract:
// the verified commit stays on a live branch, main is untouched, the
// returned Handle merges cleanly through the real manager afterwards.
func TestSkipMergeLeavesVerifiedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	mock := newMockRunner()
	mock.FilesToWrite = map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	}

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	mgr := worktree.NewManager(repo)
	preHead := gitOut(t, repo, "rev-parse", "HEAD")

	wf := Engine{
		RepoRoot:       repo,
		Task:           "Investigate the session model and add user authentication with token refresh, updating middleware, storage, and tests",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "skipmerge-t1",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		SkipMerge:      true,
		Worktrees:      mgr,
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("skipmerge-t1"),
		RunnerOverride: mock,
	}

	result, err := wf.Run(context.Background())
	if err != nil {
		t.Fatalf("SkipMerge run failed: %v", err)
	}
	if result.Handle.Name == "" {
		t.Fatal("Result.Handle is zero-valued; SkipMerge must return the live worktree")
	}
	if _, statErr := os.Stat(result.Handle.Path); statErr != nil {
		t.Fatalf("worktree path gone after SkipMerge run: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(result.Handle.Path, "main.go")); statErr != nil {
		t.Fatalf("verified file missing in kept worktree: %v", statErr)
	}
	if head := gitOut(t, repo, "rev-parse", "HEAD"); head != preHead {
		t.Fatalf("main HEAD moved (%s -> %s): SkipMerge must not merge", preHead, head)
	}
	if len(result.FilesChanged) == 0 {
		t.Error("FilesChanged empty; expected the validated file set")
	}

	// The caller-owned merge must land the verified commit on main and
	// self-clean the worktree (Manager.Merge contract).
	if mergeErr := mgr.Merge(context.Background(), result.Handle, "merge skip-merge winner"); mergeErr != nil {
		t.Fatalf("caller merge of returned handle failed: %v", mergeErr)
	}
	if head := gitOut(t, repo, "rev-parse", "HEAD"); head == preHead {
		t.Fatal("main HEAD unchanged after caller merge")
	}
	if _, statErr := os.Stat(filepath.Join(repo, "main.go")); statErr != nil {
		t.Fatalf("merged file missing on main: %v", statErr)
	}
}

// TestSkipMergeDiscardViaCleanup asserts the loser path: a kept
// worktree can be discarded with Manager.Cleanup without touching main.
func TestSkipMergeDiscardViaCleanup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := initTestRepo(t)
	mock := newMockRunner()
	mock.FilesToWrite = map[string]string{"loser.go": "package loser\n"}

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	mgr := worktree.NewManager(repo)
	preHead := gitOut(t, repo, "rev-parse", "HEAD")

	wf := Engine{
		RepoRoot:       repo,
		Task:           "Investigate the storage layer and add a second speculative implementation for comparison across strategies",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "skipmerge-loser",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		SkipMerge:      true,
		Worktrees:      mgr,
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("skipmerge-loser"),
		RunnerOverride: mock,
	}
	result, err := wf.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Handle.Name == "" {
		t.Fatal("expected live handle")
	}
	if cleanErr := mgr.Cleanup(context.Background(), result.Handle); cleanErr != nil {
		t.Fatalf("cleanup: %v", cleanErr)
	}
	if head := gitOut(t, repo, "rev-parse", "HEAD"); head != preHead {
		t.Fatal("main HEAD moved after discarding a loser rollout")
	}
	if _, statErr := os.Stat(filepath.Join(repo, "loser.go")); statErr == nil {
		t.Fatal("loser file leaked onto main")
	}
}
