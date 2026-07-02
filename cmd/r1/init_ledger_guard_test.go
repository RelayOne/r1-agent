package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitCmdInstallsLedgerGuardHook is the integration test for the A075
// salvage wiring: `r1 init --auto` must install the ledger append-only
// guard into .git/hooks/pre-commit in addition to generating
// r1.policy.yaml via the auto-detect wizard.
func TestInitCmdInstallsLedgerGuardHook(t *testing.T) {
	dir := t.TempDir()
	git := exec.Command("git", "init")
	git.Dir = dir
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	initCmd([]string{dir, "--auto"})

	hook, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "pre-commit"))
	if err != nil {
		t.Fatalf("expected r1 init to install pre-commit hook: %v", err)
	}
	if !strings.Contains(string(hook), "STOKE LEDGER GUARD") {
		t.Errorf("pre-commit hook missing STOKE LEDGER GUARD marker:\n%s", hook)
	}

	if _, err := os.Stat(filepath.Join(dir, "r1.policy.yaml")); err != nil {
		t.Errorf("expected r1.policy.yaml from --auto wizard: %v", err)
	}
}

// TestInitCmdNonGitProjectSurvives verifies the hook install is
// best-effort: init in a directory without .git must not exit non-zero
// (initCmd would os.Exit(1) on a hard failure, killing the test binary).
func TestInitCmdNonGitProjectSurvives(t *testing.T) {
	dir := t.TempDir()

	initCmd([]string{dir, "--auto"})

	if _, err := os.Stat(filepath.Join(dir, "r1.policy.yaml")); err != nil {
		t.Errorf("expected r1.policy.yaml even without .git: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Errorf("expected no hook outside a git repo, stat err=%v", err)
	}
}
