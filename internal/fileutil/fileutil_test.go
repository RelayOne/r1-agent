package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := EnsureDir(dir, DirPerms); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if !DirExists(dir) {
		t.Error("directory should exist after EnsureDir")
	}
}

func TestEnsureDir_Idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "idem")
	if err := EnsureDir(dir, DirPerms); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureDir(dir, DirPerms); err != nil {
		t.Fatalf("second call should be idempotent: %v", err)
	}
}

func TestEnsureRuntimeDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := EnsureRuntimeDir(dir); err != nil {
		t.Fatalf("EnsureRuntimeDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

func TestEnsurePrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != PrivateDirPerms {
		t.Errorf("perm = %o, want %o", info.Mode().Perm(), PrivateDirPerms)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "file.txt")
	if err := WriteFileAtomic(path, []byte("hello"), FilePerms); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("got %q, want %q", string(data), "hello")
	}
	// Verify no .tmp file left behind
	if FileExists(path + ".tmp") {
		t.Error(".tmp file should not exist after atomic write")
	}
}

func TestWriteFileAtomic_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.txt")
	for i := 0; i < 3; i++ {
		if err := WriteFileAtomic(path, []byte("data"), FilePerms); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	if FileExists(filepath.Join(dir, "nope")) {
		t.Error("non-existent file should not exist")
	}
	path := filepath.Join(dir, "exists.txt")
	os.WriteFile(path, []byte("hi"), 0o600)
	if !FileExists(path) {
		t.Error("written file should exist")
	}
	if FileExists(dir) {
		t.Error("directory should not count as file")
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	if !DirExists(dir) {
		t.Error("temp dir should exist")
	}
	if DirExists(filepath.Join(dir, "nope")) {
		t.Error("non-existent dir should not exist")
	}
}

func TestSafePath(t *testing.T) {
	dir := t.TempDir()

	// Normal path
	abs, err := SafePath(dir, "sub/file.txt")
	if err != nil {
		t.Fatalf("SafePath: %v", err)
	}
	if abs == "" {
		t.Error("expected non-empty path")
	}

	// Traversal attack
	_, err = SafePath(dir, "../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}

	// Another traversal
	_, err = SafePath(dir, "../../../tmp/evil")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestSafePath_RootItself(t *testing.T) {
	dir := t.TempDir()
	abs, err := SafePath(dir, ".")
	if err != nil {
		t.Fatalf("SafePath for root itself: %v", err)
	}
	if abs == "" {
		t.Error("expected non-empty result for root path")
	}
}
