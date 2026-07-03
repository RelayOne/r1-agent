package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/repomap"
	"github.com/RelayOne/r1/internal/worktree"
)

// TestRepomapRefreshingMergeNilPassthrough: a nil repomap makes the wrapper a
// transparent passthrough — the underlying merge runs and its result is
// returned verbatim.
func TestRepomapRefreshingMergeNilPassthrough(t *testing.T) {
	called := false
	sentinel := errors.New("boom")
	merge := func(ctx context.Context, h worktree.Handle, msg string) error {
		called = true
		return sentinel
	}
	wrapped := repomapRefreshingMerge(merge, nil)
	if err := wrapped(context.Background(), worktree.Handle{}, "m"); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
	if !called {
		t.Fatal("underlying merge was not invoked")
	}
}

// TestRepomapRefreshingMergeErrorSkipsRefresh: when the merge fails the error
// propagates and no invalidation is attempted (asserted indirectly: the
// wrapper returns before touching the map).
func TestRepomapRefreshingMergeErrorSkipsRefresh(t *testing.T) {
	rm := &repomap.RepoMap{Root: t.TempDir(), Files: map[string]*repomap.FileNode{}}
	sentinel := errors.New("merge failed")
	merge := func(ctx context.Context, h worktree.Handle, msg string) error { return sentinel }
	wrapped := repomapRefreshingMerge(merge, rm)
	if err := wrapped(context.Background(), worktree.Handle{Path: t.TempDir(), GitBinary: "git"}, "m"); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

// TestRepomapRefreshingMergeInvalidatesWinnerFiles is the end-to-end path:
// after a successful merge, the shared repomap reflects the winner's on-disk
// changes. Uses a real git worktree so worktree.ModifiedFiles resolves the
// changed set; skips when git is unavailable.
func TestRepomapRefreshingMergeInvalidatesWinnerFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "main")
	foo := filepath.Join(repo, "foo.go")
	if err := os.WriteFile(foo, []byte("package p\n\nfunc Old() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	// Build the shared repomap; it now reflects Old, not New.
	rm, err := repomap.Build(repo)
	if err != nil {
		t.Fatalf("repomap.Build: %v", err)
	}
	if symNames(rm, "foo.go")["New"] {
		t.Fatal("repomap unexpectedly already has New")
	}

	// Simulate the winner's on-disk change (uncommitted is enough for
	// ModifiedFiles to detect it).
	if err := os.WriteFile(foo, []byte("package p\n\nfunc Old() {}\nfunc New() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := worktree.Handle{Path: repo, RepoRoot: repo, GitBinary: "git", BaseCommit: "HEAD"}

	mergeCalled := false
	merge := func(ctx context.Context, hh worktree.Handle, msg string) error {
		mergeCalled = true
		return nil
	}
	wrapped := repomapRefreshingMerge(merge, rm)
	if err := wrapped(context.Background(), h, "merge winner"); err != nil {
		t.Fatalf("wrapped merge: %v", err)
	}
	if !mergeCalled {
		t.Fatal("underlying merge not called")
	}
	if !symNames(rm, "foo.go")["New"] {
		t.Errorf("repomap not refreshed after merge; foo.go symbols: %v", symNames(rm, "foo.go"))
	}
}

func symNames(rm *repomap.RepoMap, rel string) map[string]bool {
	names := map[string]bool{}
	if n, ok := rm.Files[rel]; ok {
		for _, s := range n.Symbols {
			names[s.Name] = true
		}
	}
	return names
}
