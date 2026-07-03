package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoteShellMutationInvalidatesIndex verifies that a shell mutation
// (simulated by writing a file directly on disk, bypassing the write hooks)
// is reflected by graph tools once noteShellMutation marks the index dirty.
// Without invalidation the lazily-built index would keep serving pre-command
// results.
func TestNoteShellMutationInvalidatesIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package p\n\nfunc Alpha() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(dir)

	ix, err := r.codeIndex()
	if err != nil {
		t.Fatalf("codeIndex: %v", err)
	}
	// Baseline: Beta does not exist yet.
	base, err := ix.SearchSymbols("Beta", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols baseline: %v", err)
	}
	// The no-match message echoes the query, so assert on the file location
	// (b.go) which only appears once the symbol is actually indexed.
	if strings.Contains(base, "b.go") {
		t.Fatalf("b.go present before it was written; got:\n%s", base)
	}

	// A "shell command" adds a new symbol without going through write hooks.
	if err := os.WriteFile(filepath.Join(dir, "b.go"),
		[]byte("package p\n\nfunc Beta() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.noteShellMutation()

	got, err := ix.SearchSymbols("Beta", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols after mutation: %v", err)
	}
	if !strings.Contains(got, "b.go") {
		t.Errorf("index not refreshed after noteShellMutation; got:\n%s", got)
	}
}

// TestNoteShellMutationKillSwitch verifies the R1_DISABLE_SHELL_INDEX_INVALIDATION
// kill switch: with it set, noteShellMutation is a no-op and the index keeps
// serving pre-command results.
func TestNoteShellMutationKillSwitch(t *testing.T) {
	t.Setenv(shellIndexInvalidationDisabledEnv, "1")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package p\n\nfunc Alpha() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(dir)
	ix, err := r.codeIndex()
	if err != nil {
		t.Fatalf("codeIndex: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "b.go"),
		[]byte("package p\n\nfunc Beta() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.noteShellMutation() // no-op under the kill switch

	got, err := ix.SearchSymbols("Beta", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if strings.Contains(got, "b.go") {
		t.Errorf("kill switch should have suppressed invalidation; got:\n%s", got)
	}
}
