package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSharedDepDirsExcludesBuildOutputs guards the fix for the concurrent-build
// corruption gap: build-OUTPUT / build-STATE directories must never appear in
// the shared-symlink list, because symlinking one shared output dir into every
// parallel worktree makes concurrent compilers race to write the same files.
// Only read-mostly dependency caches are allowed.
func TestSharedDepDirsExcludesBuildOutputs(t *testing.T) {
	// Directories a package manager populates once, then only reads: safe to share.
	allowed := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		".venv":        true,
		".m2":          true,
	}
	// Directories a build tool rewrites on every build: sharing corrupts them.
	forbidden := map[string]bool{
		"target":      true, // Cargo/sbt compiled output
		"__pycache__": true, // CPython bytecode
		".gradle":     true, // Gradle per-project build state + locks
	}

	for _, d := range sharedDepDirs() {
		if forbidden[d] {
			t.Errorf("sharedDepDirs() includes build-output dir %q; sharing it across parallel worktrees corrupts concurrent builds", d)
		}
		if !allowed[d] {
			t.Errorf("sharedDepDirs() includes unexpected dir %q; only read-mostly dependency caches may be shared", d)
		}
	}
}

// TestSymlinkSharedDepsSkipsBuildOutputDirs proves the behavior end to end: a
// build-output dir present in the source repo is NOT symlinked into a worktree,
// while a genuine dependency cache is.
func TestSymlinkSharedDepsSkipsBuildOutputDirs(t *testing.T) {
	repoRoot := t.TempDir()
	worktree := t.TempDir()

	// Create both a dependency cache and a build-output dir in the source repo.
	for _, d := range []string{"node_modules", "target", "__pycache__", ".gradle"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	symlinkSharedDeps(repoRoot, worktree)

	// node_modules (dependency cache) should be symlinked in.
	if _, err := os.Lstat(filepath.Join(worktree, "node_modules")); err != nil {
		t.Errorf("expected node_modules to be symlinked into the worktree, got: %v", err)
	}

	// Build-output dirs must NOT be symlinked — each worktree gets its own.
	for _, d := range []string{"target", "__pycache__", ".gradle"} {
		if _, err := os.Lstat(filepath.Join(worktree, d)); !os.IsNotExist(err) {
			t.Errorf("build-output dir %q must not be symlinked into the worktree (would corrupt concurrent builds); Lstat err=%v", d, err)
		}
	}
}
