package atomicfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndCommit(t *testing.T) {
	dir := t.TempDir()

	// Create an existing file
	origPath := filepath.Join(dir, "test.txt")
	os.WriteFile(origPath, []byte("original"), 0644)

	tx := NewTransaction(dir)
	tx.Write("test.txt", []byte("updated"))

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	data, _ := os.ReadFile(origPath)
	if string(data) != "updated" {
		t.Errorf("expected 'updated', got %q", string(data))
	}
}

func TestCreateAndCommit(t *testing.T) {
	dir := t.TempDir()

	tx := NewTransaction(dir)
	tx.Create("new.txt", []byte("new content"))

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(data) != "new content" {
		t.Errorf("expected 'new content', got %q", string(data))
	}
}

func TestDeleteAndCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "delete-me.txt")
	os.WriteFile(path, []byte("bye"), 0644)

	tx := NewTransaction(dir)
	tx.Delete("delete-me.txt")

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	if _, err := os.Stat(path); err == nil {
		t.Error("file should have been deleted")
	}
}

func TestConflictDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conflict.txt")
	os.WriteFile(path, []byte("v1"), 0644)

	tx := NewTransaction(dir)
	tx.Write("conflict.txt", []byte("v2"))

	// Modify the file behind the transaction's back
	os.WriteFile(path, []byte("v1-modified"), 0644)

	err := tx.Commit()
	if err == nil {
		t.Error("expected conflict error")
	}
}

func TestCreateExistingFails(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "exists.txt"), []byte("hi"), 0644)

	tx := NewTransaction(dir)
	tx.Create("exists.txt", []byte("new"))

	err := tx.Commit()
	if err == nil {
		t.Error("expected error creating existing file")
	}
}

func TestDryRun(t *testing.T) {
	dir := t.TempDir()

	tx := NewTransaction(dir)
	tx.Write("a.txt", []byte("aaa"))
	tx.Create("b.txt", []byte("bbb"))
	tx.Delete("c.txt")

	summary := tx.DryRun()
	if len(summary) != 3 {
		t.Errorf("expected 3 ops, got %d", len(summary))
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("data"), 0644)

	tx := NewTransaction(dir)
	tx.Write("a.txt", []byte("new"))

	err := tx.Validate()
	if err != nil {
		t.Errorf("should validate clean: %v", err)
	}

	// Modify behind back
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0644)
	err = tx.Validate()
	if err == nil {
		t.Error("should detect conflict")
	}
}

func TestDoubleCommit(t *testing.T) {
	dir := t.TempDir()
	tx := NewTransaction(dir)
	tx.Create("x.txt", []byte("x"))
	tx.Commit()

	err := tx.Commit()
	if err == nil {
		t.Error("double commit should fail")
	}
}

func TestLen(t *testing.T) {
	tx := NewTransaction(t.TempDir())
	tx.Create("a.txt", []byte("a"))
	tx.Create("b.txt", []byte("b"))
	if tx.Len() != 2 {
		t.Errorf("expected 2, got %d", tx.Len())
	}
}

func TestSummary(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "e.txt"), []byte("x"), 0644)

	tx := NewTransaction(dir)
	tx.Write("e.txt", []byte("y"))
	tx.Create("n.txt", []byte("z"))

	s := tx.Summary()
	if s != "1 writes, 1 creates" {
		t.Errorf("unexpected summary: %s", s)
	}
}

func TestFiles(t *testing.T) {
	tx := NewTransaction("/tmp")
	tx.Create("a.txt", []byte("a"))
	tx.Create("b.txt", []byte("b"))

	files := tx.Files()
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

func TestMultiFileAtomicCommit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f1.txt"), []byte("orig1"), 0644)
	os.WriteFile(filepath.Join(dir, "f2.txt"), []byte("orig2"), 0644)

	tx := NewTransaction(dir)
	tx.Write("f1.txt", []byte("new1"))
	tx.Write("f2.txt", []byte("new2"))
	tx.Create("f3.txt", []byte("new3"))

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	for _, tc := range []struct {
		file, want string
	}{
		{"f1.txt", "new1"},
		{"f2.txt", "new2"},
		{"f3.txt", "new3"},
	} {
		data, _ := os.ReadFile(filepath.Join(dir, tc.file))
		if string(data) != tc.want {
			t.Errorf("%s: expected %q, got %q", tc.file, tc.want, string(data))
		}
	}
}
