package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestPrepareWorkDir_SealsBaseline: a bare tempdir (the default bench
// workdir) becomes a git repo with exactly one baseline commit, and
// pre-seeded mission files are inside that baseline — so the end-of-run
// diff shows only what the agent did.
func TestPrepareWorkDir_SealsBaseline(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	workDir := t.TempDir()
	seed := filepath.Join(workDir, "seed.txt")
	if err := os.WriteFile(seed, []byte("mission seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := prepareWorkDir(workDir); err != nil {
		t.Fatalf("prepareWorkDir: %v", err)
	}

	if got := gitOut(t, workDir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("baseline commit count = %s, want 1", got)
	}
	// The seed file is committed, so the working tree is clean.
	if status := gitOut(t, workDir, "status", "--porcelain"); status != "" {
		t.Errorf("working tree not clean after baseline: %q", status)
	}
}

// TestPrepareWorkDir_LeavesExistingRepoUntouched: a caller-supplied
// --workdir that is already a git checkout must not gain commits or
// have its HEAD moved.
func TestPrepareWorkDir_LeavesExistingRepoUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	workDir := t.TempDir()
	gitOut(t, workDir, "init", "-q")
	gitOut(t, workDir, "config", "user.email", "bench@example.com")
	gitOut(t, workDir, "config", "user.name", "Bench Bot")
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitOut(t, workDir, "add", "a.txt")
	gitOut(t, workDir, "commit", "-q", "-m", "user commit")
	before := gitOut(t, workDir, "rev-parse", "HEAD")

	if err := prepareWorkDir(workDir); err != nil {
		t.Fatalf("prepareWorkDir: %v", err)
	}

	if after := gitOut(t, workDir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved on existing repo: %s -> %s", before, after)
	}
	if got := gitOut(t, workDir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commit count on existing repo = %s, want 1", got)
	}
}
