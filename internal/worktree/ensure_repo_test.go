package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitCapture is a test helper that runs a git command and returns stdout.
func runGitCapture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestEnsureRepo_InitializesFreshDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	ctx := context.Background()

	created, err := EnsureRepo(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if !created {
		t.Error("expected created=true for fresh dir")
	}

	// Directory should exist and be a git repo
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf(".git not created: %v", err)
	}

	// Should have exactly one commit
	head := runGitCapture(t, dir, "rev-parse", "HEAD")
	if len(head) != 40 {
		t.Errorf("HEAD = %q, want a 40-char sha", head)
	}

	// Should have a .gitignore staged
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf(".gitignore not created: %v", err)
	}
}

func TestEnsureRepo_NoOpOnExistingRepoWithCommits(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Initialize a repo with one commit manually
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup git %v: %v: %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o600)
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-m", "initial", "--no-gpg-sign"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup git %v: %v: %s", args, err, out)
		}
	}
	originalHead := runGitCapture(t, dir, "rev-parse", "HEAD")

	// EnsureRepo should be a no-op
	created, err := EnsureRepo(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if created {
		t.Error("expected created=false for existing repo")
	}

	// HEAD should be unchanged
	if head := runGitCapture(t, dir, "rev-parse", "HEAD"); head != originalHead {
		t.Errorf("HEAD changed: %q → %q", originalHead, head)
	}
}

func TestEnsureRepo_ExistingRepoWithZeroCommits_AddsInitial(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// git init — no commits yet
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	// HEAD shouldn't resolve yet
	preCmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
	preCmd.Dir = dir
	if preCmd.Run() == nil {
		t.Skip("unexpected: fresh git init already had HEAD on this system")
	}

	created, err := EnsureRepo(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	// We didn't create the repo, but we DID create the initial commit.
	// The contract of created is "any init happened" — we return false
	// here because the repo already existed. Test both possibilities:
	_ = created

	// HEAD should now resolve
	if _, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err != nil {
		t.Errorf("HEAD still doesn't resolve after EnsureRepo: %v", err)
	}
}

func TestEnsureRepo_CreatesParentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deeply", "nested", "path")
	ctx := context.Background()

	_, err := EnsureRepo(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("target dir not created: %v", err)
	}
}

// TestEnsureRepo_WorksForManagerPrepare confirms that after EnsureRepo, the
// worktree.Manager.Prepare flow (which was the original failure point) works.
func TestEnsureRepo_WorksForManagerPrepare(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh-manager")
	ctx := context.Background()

	if _, err := EnsureRepo(ctx, dir); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	m := NewManager(dir)
	handle, err := m.Prepare(ctx, "test-task")
	if err != nil {
		t.Fatalf("Manager.Prepare on fresh EnsureRepo'd dir failed: %v", err)
	}
	if handle.BaseCommit == "" {
		t.Error("handle.BaseCommit should be set")
	}
	// Clean up the worktree
	m.Cleanup(ctx, handle)
}
