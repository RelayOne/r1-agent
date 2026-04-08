package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}

	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("setup %v: %v", c, err)
		}
	}

	// Create initial commit
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial", "--no-gpg-sign")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	return dir
}

func TestTake(t *testing.T) {
	dir := setupGitRepo(t)

	snap, err := Take(dir, "test-snap")
	if err != nil {
		t.Fatal(err)
	}

	if snap.Branch == "" {
		t.Error("branch should not be empty")
	}
	if snap.Commit == "" {
		t.Error("commit should not be empty")
	}
	if snap.Label != "test-snap" {
		t.Errorf("expected label test-snap, got %s", snap.Label)
	}
}

func TestTakeWithModifiedFiles(t *testing.T) {
	dir := setupGitRepo(t)

	// Modify a tracked file
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Modified"), 0644)
	// Add an untracked file
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644)

	snap, err := Take(dir, "modified")
	if err != nil {
		t.Fatal(err)
	}

	if len(snap.Modified) == 0 {
		t.Error("should capture modified files")
	}
	if len(snap.Untracked) == 0 {
		t.Error("should capture untracked files")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := setupGitRepo(t)

	snap, _ := Take(dir, "save-test")
	path := filepath.Join(t.TempDir(), "snap.json")

	if err := Save(snap, path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Branch != snap.Branch {
		t.Errorf("branch mismatch: %s vs %s", loaded.Branch, snap.Branch)
	}
	if loaded.Commit != snap.Commit {
		t.Errorf("commit mismatch")
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path")
	if err == nil {
		t.Error("should error on missing file")
	}
}

func TestSummary(t *testing.T) {
	snap := &Snapshot{
		Label:    "test",
		Branch:   "main",
		Commit:   "abc123def456",
		Modified: []FileState{{Path: "a.go"}},
	}

	s := snap.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestDiff(t *testing.T) {
	a := &Snapshot{
		Branch:   "main",
		Commit:   "aaaa1111",
		Modified: []FileState{{Path: "a.go"}},
	}
	b := &Snapshot{
		Branch:   "feature",
		Commit:   "bbbb2222",
		Modified: []FileState{{Path: "a.go"}, {Path: "b.go"}},
	}

	diffs := Diff(a, b)
	if len(diffs) == 0 {
		t.Error("should detect differences")
	}

	hasBranch := false
	hasNew := false
	for _, d := range diffs {
		if d == "branch: main → feature" {
			hasBranch = true
		}
		if d == "new modified: b.go" {
			hasNew = true
		}
	}
	if !hasBranch {
		t.Error("should detect branch change")
	}
	if !hasNew {
		t.Error("should detect new modified file")
	}
}

func TestFileState(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a file with specific content
	content := []byte("package main\nfunc main() {}\n")
	os.WriteFile(filepath.Join(dir, "main.go"), content, 0644)

	snap, _ := Take(dir, "filestate")

	found := false
	for _, f := range snap.Modified {
		if f.Path == "main.go" {
			found = true
			if string(f.Content) != string(content) {
				t.Error("content mismatch")
			}
		}
	}
	if !found {
		t.Error("should capture main.go")
	}
}
