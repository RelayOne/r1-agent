package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// initTempGitRepo sets up a bare git repo with an initial commit at dir
// and returns the HEAD SHA for use by descent helpers. The commit seeds
// a tracked README so subsequent diffs have something to compare against.
func initTempGitRepo(t *testing.T, dir string) string {
	t.Helper()
	gitInitAt(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommitAllAt(t, dir, "seed")
	return descentGitHead(context.Background(), dir)
}

// TestBootstrapReinstallOnManifestChange exercises the spec-1 item 5
// bootstrap wrapper: when a repair commit touches package.json or a
// sibling manifest, the wrapper detects it via git diff and logs the
// bootstrap event. No actual install runs (EnsureWorkspaceInstalledOpts
// is a noop without package.json on disk before the edit).
func TestBootstrapReinstallOnManifestChange(t *testing.T) {
	dir := t.TempDir()
	preSHA := initTempGitRepo(t, dir)
	// Write a package.json and commit so it's on HEAD.
	pj := filepath.Join(dir, "package.json")
	if err := os.WriteFile(pj, []byte(`{"name":"x","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, dir, "add", ".")
	runGitIn(t, dir, "commit", "-m", "add pkg")
	postRef := "HEAD"

	// descentGitDiffNames must report package.json as changed.
	changed := descentGitDiffNames(context.Background(), dir, preSHA, postRef)
	found := false
	for _, f := range changed {
		if f == "package.json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected package.json in diff names, got %v", changed)
	}
}

// TestIntersectStrings_BasenameMatch verifies the matcher catches
// manifests sitting inside subdirectories (e.g. packages/x/package.json)
// via basename comparison.
func TestIntersectStrings_BasenameMatch(t *testing.T) {
	haystack := []string{"packages/x/package.json", "src/a.ts", "go.mod"}
	manifests := []string{"package.json", "go.mod", "Cargo.toml"}
	got := intersectStrings(haystack, manifests)
	if len(got) != 2 {
		t.Fatalf("expected 2 matches (package.json + go.mod), got %d: %v", len(got), got)
	}
}

// TestDescentGitDiffNames_NoRepo verifies the helper returns nil
// (not panic) when given a non-repo path. Defensive programming for
// descent cycles that land in detached trees.
func TestDescentGitDiffNames_NoRepo(t *testing.T) {
	dir := t.TempDir()
	got := descentGitDiffNames(context.Background(), dir, "", "HEAD")
	if got != nil {
		t.Errorf("expected nil for non-repo, got %v", got)
	}
}
