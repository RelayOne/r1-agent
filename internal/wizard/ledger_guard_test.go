package wizard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a bare-minimum git repo in dir so snapshot.Take works.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	// Create a file and commit so HEAD exists.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmds = append(cmds,
		[]string{"git", "add", "."},
		[]string{"git", "commit", "-m", "init"},
	)
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}
}

func TestInstallLedgerGuardHook(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	if err := InstallLedgerGuardHook(dir); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("expected pre-commit hook to exist: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("expected pre-commit hook to be executable")
	}

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "STOKE LEDGER GUARD") {
		t.Error("expected hook to contain STOKE LEDGER GUARD marker")
	}

	// Idempotent: calling again should not error or duplicate content.
	if err := InstallLedgerGuardHook(dir); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(hookPath)
	if strings.Count(string(data2), "STOKE LEDGER GUARD") != strings.Count(string(data), "STOKE LEDGER GUARD") {
		t.Error("expected idempotent install — guard duplicated")
	}
}

func TestInstallPreservesExistingHook(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	customContent := "#!/bin/sh\necho 'custom hook'\n"
	if err := os.WriteFile(hookPath, []byte(customContent), 0755); err != nil {
		t.Fatal(err)
	}

	if err := InstallLedgerGuardHook(dir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(data)
	if !strings.Contains(contents, "custom hook") {
		t.Error("expected original custom content to be preserved")
	}
	if !strings.Contains(contents, "STOKE LEDGER GUARD") {
		t.Error("expected guard to be appended")
	}
}

func TestLedgerGuardRejectsModification(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	if err := InstallLedgerGuardHook(dir); err != nil {
		t.Fatal(err)
	}

	// Create ledger directory and a file, then commit (addition is allowed).
	ledgerDir := filepath.Join(dir, ".stoke", "ledger", "nodes")
	if err := os.MkdirAll(ledgerDir, 0755); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(ledgerDir, "abc123.json")
	if err := os.WriteFile(nodePath, []byte(`{"id":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	gitCmd := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}

	if out, err := gitCmd("add", ".stoke/ledger/nodes/abc123.json"); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	if out, err := gitCmd("commit", "-m", "add ledger node"); err != nil {
		t.Fatalf("git commit (add) should succeed: %v\n%s", err, out)
	}

	// Now modify the file — the hook should reject this.
	if err := os.WriteFile(nodePath, []byte(`{"id":"abc123","modified":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := gitCmd("add", ".stoke/ledger/nodes/abc123.json"); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	out, err := gitCmd("commit", "-m", "modify ledger node")
	if err == nil {
		t.Fatal("expected git commit to fail when modifying ledger file")
	}
	if !strings.Contains(string(out), "STOKE LEDGER GUARD") {
		t.Errorf("expected rejection message from ledger guard, got:\n%s", out)
	}
}
