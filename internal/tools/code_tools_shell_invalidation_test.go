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

	r.noteShellMutation("go generate ./...") // a writing command

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

	r.noteShellMutation("go generate ./...") // no-op under the kill switch

	got, err := ix.SearchSymbols("Beta", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if strings.Contains(got, "b.go") {
		t.Errorf("kill switch should have suppressed invalidation; got:\n%s", got)
	}
}

// TestCommandIsReadOnly pins FIX 3: provably read-only commands do not force
// a code-graph rebuild, while anything that could have written (chained,
// redirected, or an unrecognized/writing program) does.
func TestCommandIsReadOnly(t *testing.T) {
	readOnly := []string{
		"grep -rn foo .", "ls -la", "rg pattern", "go test ./...",
		"go build ./...", "go vet ./...", "git status", "git diff HEAD",
		"cat file.go", "  find . -name '*.go'  ", "GOFLAGS=-mod=mod go list ./...",
		"/usr/bin/grep x y", "",
	}
	for _, c := range readOnly {
		if !commandIsReadOnly(c) {
			t.Errorf("commandIsReadOnly(%q) = false, want true", c)
		}
	}
	writes := []string{
		"go generate ./...", "sed -i s/a/b/ f.go", "gofmt -w .",
		"git apply patch.diff", "grep x && rm y", "echo hi > f.go",
		"cat a | tee b.go", "make build", "python gen.py", "git commit -am x",
		"go run gen.go", "ls; touch new.go", "cp a.go b.go",
	}
	for _, c := range writes {
		if commandIsReadOnly(c) {
			t.Errorf("commandIsReadOnly(%q) = true, want false (could write)", c)
		}
	}
}

// TestNoteShellMutationSkipsReadOnly verifies a read-only command does NOT
// invalidate the index (the perf regression this fix addresses).
func TestNoteShellMutationSkipsReadOnly(t *testing.T) {
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
	// Write a new symbol on disk, then run a READ-ONLY command: the index
	// must NOT pick up b.go (no invalidation), proving read-only commands
	// don't trigger a rebuild.
	if err := os.WriteFile(filepath.Join(dir, "b.go"),
		[]byte("package p\n\nfunc Beta() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.noteShellMutation("go test ./...")
	got, err := ix.SearchSymbols("Beta", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if strings.Contains(got, "b.go") {
		t.Errorf("read-only command must not invalidate the index; got:\n%s", got)
	}
}
