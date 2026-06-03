package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitIdentityEnv is the fixed, deterministic author/committer identity used by
// every test-local git invocation. Setting it on each git call (rather than
// relying on `git config`) makes commits reproducible and immune to the
// operator's global git config bleeding into hermetic tests.
func gitIdentityEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
}

// runGitIn shells out to real git with cmd.Dir pinned to dir and the fixed
// identity env applied. It t.Fatalf's on any git error so callers can stay
// terse. cmd.Dir is always set explicitly — never relative to the test cwd.
func runGitIn(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitIdentityEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%q): %v\n%s", args, dir, err, out)
	}
	return out
}

// gitHeadFull returns the full 40-hex HEAD SHA for the repo at dir.
func gitHeadFull(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	cmd.Env = gitIdentityEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD (dir=%q): %v", dir, err)
	}
	sha := string(out)
	// Trim trailing newline; keep exactly the 40-hex SHA.
	for len(sha) > 0 && (sha[len(sha)-1] == '\n' || sha[len(sha)-1] == '\r') {
		sha = sha[:len(sha)-1]
	}
	return sha
}

// gitInitAt initializes a fresh git repo at an existing dir (created by the
// caller, e.g. via t.TempDir()) with the default branch "main". Every git
// invocation sets cmd.Dir and the fixed identity env.
func gitInitAt(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGitIn(t, dir, "init", "-q", "-b", "main")
}

// gitCommitAllAt stages everything under dir and creates a commit with msg,
// returning the resulting full HEAD SHA. Use with gitInitAt for callers that
// own the directory; use newTempGitRepo when you just want a ready repo.
func gitCommitAllAt(t *testing.T, dir, msg string) string {
	t.Helper()
	runGitIn(t, dir, "add", "-A")
	runGitIn(t, dir, "commit", "-q", "-m", msg)
	return gitHeadFull(t, dir)
}

// newTempGitRepo creates a fresh git repository under t.TempDir(), seeds a
// single initial commit (a tracked README.md), and returns the repo directory
// plus the full 40-hex HEAD SHA. It shells out to real git, sets cmd.Dir on
// every invocation, applies a fixed author/committer identity, and t.Fatalf's
// on any git error. This is the canonical temp-git helper for cmd/r1 tests.
func newTempGitRepo(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	gitInitAt(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("seed README.md: %v", err)
	}
	head = gitCommitAllAt(t, dir, "initial commit")
	return dir, head
}
