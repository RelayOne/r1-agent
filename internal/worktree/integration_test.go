package worktree

// integration_test.go — R2: Manager.Merge must never leave a half-finished
// merge (MERGE_HEAD + conflicted index) wedging the user's RepoRoot. Because
// mergeMu serializes all merges, a single stale wedge would make every
// subsequent task's merge fail with "MERGE_HEAD exists" — a repo-wide stall
// that requires manual git surgery. These tests pin (a) the preflight self-heal
// that clears a wedge left by a crashed prior run, and (b) the generic
// real-merge-failure path leaving no wedge and permitting a follow-up merge.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitAllow runs git in dir WITHOUT failing the test on a nonzero exit, so tests
// can drive intentionally-failing commands (e.g. a conflicting merge).
func gitAllow(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com"}, os.Environ()...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mergeHeadExists(repo string) bool {
	_, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	return err == nil
}

// TestMergePreflightAbortsStaleWedge is the core R2 regression guard: a stale
// MERGE_HEAD left in RepoRoot by a crashed prior run must be aborted by the
// Merge preflight so the current merge can proceed. On pre-fix code the merge
// fails with "MERGE_HEAD exists" and the wedge persists.
func TestMergePreflightAbortsStaleWedge(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "file.txt", "base\n")
	run(repo, "add", "file.txt")
	run(repo, "commit", "-m", "base")

	m := NewManager(repo)
	// Prepare a clean, non-conflicting feature worktree from main@base BEFORE
	// we wedge main, so its branch only adds a new file.
	h, err := m.Prepare(ctx, "clean-feature")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = m.Cleanup(ctx, h) }()
	writeRepoFile(t, h.Path, "newfile.txt", "added by feature\n")
	run(h.Path, "add", "newfile.txt")
	run(h.Path, "commit", "-m", "feature adds newfile")

	// Wedge main: build a real content conflict and start (but don't finish) a
	// merge, leaving MERGE_HEAD + a conflicted index behind.
	run(repo, "checkout", "-b", "wedger")
	writeRepoFile(t, repo, "file.txt", "wedge side\n")
	run(repo, "add", "file.txt")
	run(repo, "commit", "-m", "wedge side")
	run(repo, "checkout", "main")
	writeRepoFile(t, repo, "file.txt", "main side\n")
	run(repo, "add", "file.txt")
	run(repo, "commit", "-m", "main side")
	if _, err := gitAllow(t, repo, "merge", "wedger"); err == nil {
		t.Fatal("setup: `git merge wedger` should have conflicted")
	}
	if !mergeHeadExists(repo) {
		t.Fatal("setup: expected a stale MERGE_HEAD wedge before Merge")
	}

	// The Merge preflight must self-heal the wedge, then merge clean-feature.
	if err := m.Merge(ctx, h, "merge clean-feature"); err != nil {
		t.Fatalf("Merge did not recover from a stale wedge: %v", err)
	}
	if mergeHeadExists(repo) {
		t.Error("MERGE_HEAD still present after Merge — wedge not cleared")
	}
	if _, statErr := os.Stat(filepath.Join(repo, "newfile.txt")); statErr != nil {
		t.Errorf("feature file not merged into main: %v", statErr)
	}
	got, err := os.ReadFile(filepath.Join(repo, "file.txt"))
	if err != nil {
		t.Fatalf("read file.txt: %v", err)
	}
	if string(got) != "main side\n" {
		t.Errorf("file.txt = %q, want %q (abort should have restored main side)", string(got), "main side\n")
	}
	// HEAD must be a merge commit (two parents).
	if _, err := gitAllow(t, repo, "rev-parse", "HEAD^2"); err != nil {
		t.Error("main HEAD is not a merge commit after Merge")
	}
}

// TestMergeGenericFailureDoesNotWedgeAndRecovers forces a real 'git merge'
// failure (dirty main working tree) and asserts the generic-failure path leaves
// no MERGE_HEAD behind and that a follow-up merge succeeds once the tree is
// clean.
func TestMergeGenericFailureDoesNotWedgeAndRecovers(t *testing.T) {
	ctx := context.Background()
	repo, run := initMergeTestRepo(t)

	writeRepoFile(t, repo, "file.txt", "base\n")
	run(repo, "add", "file.txt")
	run(repo, "commit", "-m", "base")

	m := NewManager(repo)
	h, err := m.Prepare(ctx, "feat")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = m.Cleanup(ctx, h) }()
	writeRepoFile(t, h.Path, "file.txt", "feat side\n")
	run(h.Path, "add", "file.txt")
	run(h.Path, "commit", "-m", "feat changes file")

	// Dirty the main working tree on the very file the merge must update, so
	// the real `git merge` refuses ("local changes would be overwritten").
	writeRepoFile(t, repo, "file.txt", "uncommitted local edit\n")

	if err := m.Merge(ctx, h, "merge feat"); err == nil {
		t.Fatal("Merge should fail against a dirty working tree")
	}
	if mergeHeadExists(repo) {
		t.Error("MERGE_HEAD present after a failed merge — generic-failure path did not abort")
	}

	// Clean the working tree and retry: the follow-up merge must succeed,
	// proving the failed merge did not wedge the repo.
	run(repo, "checkout", "--", "file.txt")
	if err := m.Merge(ctx, h, "merge feat retry"); err != nil {
		t.Fatalf("follow-up Merge failed — repo was wedged: %v", err)
	}
	if mergeHeadExists(repo) {
		t.Error("MERGE_HEAD present after successful follow-up merge")
	}
	got, err := os.ReadFile(filepath.Join(repo, "file.txt"))
	if err != nil {
		t.Fatalf("read file.txt: %v", err)
	}
	if string(got) != "feat side\n" {
		t.Errorf("file.txt = %q, want %q after successful merge", string(got), "feat side\n")
	}
}
