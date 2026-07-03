package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareAndCleanupWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append([]string{"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com"}, os.Environ()...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")

	m := NewManager(dir)
	handle, err := m.Prepare(context.Background(), "feature-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.Path); err != nil {
		t.Fatalf("expected worktree path to exist: %v", err)
	}
	if err := m.Cleanup(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
}

func gitInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append([]string{"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com"}, os.Environ()...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	return dir
}

// HandleForName must reproduce exactly what Prepare derives, so cleanup by
// name targets the real worktree/branch/refs.
func TestHandleForNameMatchesPrepare(t *testing.T) {
	dir := gitInitRepo(t)
	m := NewManager(dir)
	const name = "Feature Test 123"

	prepared, err := m.Prepare(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Cleanup(context.Background(), prepared) }()

	got := m.HandleForName(name)
	if got.Name != prepared.Name || got.Branch != prepared.Branch || got.Path != prepared.Path {
		t.Errorf("HandleForName = {Name:%q Branch:%q Path:%q}, Prepare gave {Name:%q Branch:%q Path:%q}",
			got.Name, got.Branch, got.Path, prepared.Name, prepared.Branch, prepared.Path)
	}
}

// CleanupByName removes a worktree whose live Handle was lost, and is a
// no-op (nil error, no spurious git failures) when nothing was created.
func TestCleanupByName(t *testing.T) {
	dir := gitInitRepo(t)
	m := NewManager(dir)
	const name = "rollout-spec-strategy-1"

	// Absent worktree: idempotent no-op.
	if err := m.CleanupByName(context.Background(), name); err != nil {
		t.Errorf("CleanupByName on a nonexistent worktree = %v, want nil (idempotent)", err)
	}

	// Create one, then drop the Handle and clean up purely by name.
	prepared, err := m.Prepare(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prepared.Path); err != nil {
		t.Fatalf("worktree path should exist after Prepare: %v", err)
	}
	if err := m.CleanupByName(context.Background(), name); err != nil {
		t.Fatalf("CleanupByName on a live worktree: %v", err)
	}
	if _, err := os.Stat(prepared.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still present after CleanupByName: stat err = %v", err)
	}
	if m.branchExists(context.Background(), prepared.Branch) {
		t.Errorf("branch %q survived CleanupByName", prepared.Branch)
	}
	// Second sweep is again a no-op.
	if err := m.CleanupByName(context.Background(), name); err != nil {
		t.Errorf("second CleanupByName = %v, want nil", err)
	}
}
