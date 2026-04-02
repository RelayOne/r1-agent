package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeWriteFileRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	err := SafeWriteFile(dir, "../escape.txt", []byte("bad"), 0644)
	if err == nil {
		t.Fatal("expected path traversal rejection")
	}
}

func TestSafeWriteFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	os.WriteFile(target, []byte("original"), 0644)
	link := filepath.Join(dir, "link.txt")
	os.Symlink(target, link)

	err := SafeWriteFile(dir, "link.txt", []byte("overwrite"), 0644)
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
	// Original should be unchanged
	data, _ := os.ReadFile(target)
	if string(data) != "original" {
		t.Errorf("symlink target was modified: %q", data)
	}
}

func TestSafeWriteFileSuccess(t *testing.T) {
	dir := t.TempDir()
	err := SafeWriteFile(dir, "sub/file.txt", []byte("hello"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sub", "file.txt"))
	if string(data) != "hello" {
		t.Errorf("got %q, want %q", data, "hello")
	}
}

func TestSafeWriteFileRejectsParentSymlink(t *testing.T) {
	dir := t.TempDir()
	// Create a symlink directory: dir/evil -> /tmp
	os.Symlink(os.TempDir(), filepath.Join(dir, "evil"))

	err := SafeWriteFile(dir, "evil/escape.txt", []byte("bad"), 0644)
	if err == nil {
		t.Fatal("expected parent directory symlink rejection")
		// Cleanup in case test fails
		os.Remove(filepath.Join(os.TempDir(), "escape.txt"))
	}
}

func TestHashFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("world"), 0644)

	h := HashFiles(dir, []string{"a.txt", "b.txt", "missing.txt"})
	if h["a.txt"] == "" || h["a.txt"] == "MISSING" {
		t.Error("a.txt should have a hash")
	}
	if h["b.txt"] == "" || h["b.txt"] == "MISSING" {
		t.Error("b.txt should have a hash")
	}
	if h["missing.txt"] != "MISSING" {
		t.Errorf("missing.txt should be MISSING, got %q", h["missing.txt"])
	}
	if h["a.txt"] == h["b.txt"] {
		t.Error("different files should have different hashes")
	}
	// Same content = same hash
	h2 := HashFiles(dir, []string{"a.txt"})
	if h2["a.txt"] != h["a.txt"] {
		t.Error("same file should produce same hash")
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Add request ID middleware", "add-request-id-middleware"},
		{"Fix  CRITICAL  bug!!", "fix--critical--bug"},
		{"", ""},
		{"a/b/c", "a-b-c"},
	}
	for _, tt := range tests {
		got := slug(tt.input)
		if got != tt.want {
			t.Errorf("slug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
