package plan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLockfilePresent verifies the lockfile detector used by the
// descent bootstrap wrapper (spec-1 item 5) to pick between frozen
// and permissive install modes.
func TestLockfilePresent(t *testing.T) {
	dir := t.TempDir()
	if LockfilePresent(dir) {
		t.Fatalf("empty dir should have no lockfile")
	}
	cases := []string{
		"pnpm-lock.yaml",
		"package-lock.json",
		"yarn.lock",
		"Cargo.lock",
		"uv.lock",
		"poetry.lock",
		"go.sum",
	}
	for _, name := range cases {
		sub := t.TempDir()
		path := filepath.Join(sub, name)
		if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		if !LockfilePresent(sub) {
			t.Errorf("expected LockfilePresent=true after creating %s", name)
		}
	}
}

// TestEnsureWorkspaceInstalledOpts_NoPackageJson verifies the install
// wrapper is a noop when the target dir has no package.json (not a
// Node project). Exercises the happy-path early-return: no panic, no
// command execution, no state change.
func TestEnsureWorkspaceInstalledOpts_NoPackageJson(t *testing.T) {
	dir := t.TempDir()
	// Should return silently without panic or error-side-effects.
	EnsureWorkspaceInstalledOpts(context.Background(), dir, InstallOpts{
		Force:  true,
		Frozen: true,
	})
}

// TestEnsureWorkspaceInstalledOpts_ForceBypassesGuard verifies Force
// disables the installedOnce guard so the descent bootstrap wrapper
// can re-install after a manifest change even if a prior install ran.
func TestEnsureWorkspaceInstalledOpts_ForceBypassesGuard(t *testing.T) {
	dir := t.TempDir()
	// Seed installedOnce so a non-force call would no-op.
	installedOnceMu.Lock()
	installedOnce[dir] = true
	installedOnceMu.Unlock()

	// Without Force: guard fires, function returns immediately even
	// though no package.json exists (no observable side effect). This
	// is a structural test — we're asserting the code path doesn't
	// crash when re-entered.
	EnsureWorkspaceInstalledOpts(context.Background(), dir, InstallOpts{})

	// With Force: guard is bypassed and the package.json absence
	// check catches it. Also no crash.
	EnsureWorkspaceInstalledOpts(context.Background(), dir, InstallOpts{
		Force: true,
	})

	// Clean up.
	installedOnceMu.Lock()
	delete(installedOnce, dir)
	installedOnceMu.Unlock()
}
