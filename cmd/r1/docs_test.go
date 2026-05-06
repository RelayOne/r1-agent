package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecurityMD_CrossLinks verifies the SECURITY.md augmentation from
// specs/finishing-touches.md §Part C: SECURITY.md must exist at repo root,
// it must cross-link to docs/security/prompt-injection.md, and that target
// file must exist on disk.
//
// Walks upward from the test working directory to locate the repo root
// (cmd/r1 is two levels deep), so this test does not depend on `go test`
// being invoked from any particular directory.
func TestSecurityMD_CrossLinks(t *testing.T) {
	repoRoot := findRepoRoot(t)

	securityPath := filepath.Join(repoRoot, "SECURITY.md")
	body, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("SECURITY.md not found at %s: %v", securityPath, err)
	}

	const linkTarget = "docs/security/prompt-injection.md"
	if !strings.Contains(string(body), linkTarget) {
		t.Errorf("SECURITY.md does not cross-link to %s", linkTarget)
	}

	linkedPath := filepath.Join(repoRoot, linkTarget)
	if _, err := os.Stat(linkedPath); err != nil {
		t.Errorf("cross-linked file %s missing on disk: %v", linkedPath, err)
	}
}

// findRepoRoot ascends from the current working directory until it finds a
// directory containing a `go.mod` file, returning that directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	// LINT-ALLOW chdir-test: test-only repo-root locator under `go test`.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	dir := cwd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root (go.mod) starting from %s", cwd)
	return ""
}
