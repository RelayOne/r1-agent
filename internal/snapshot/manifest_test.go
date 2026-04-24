package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestTakeManifest(t *testing.T) {
	dir := setupGitRepo(t)

	m, err := TakeManifest(dir, "mission-001")
	if err != nil {
		t.Fatal(err)
	}

	if m.SnapshotCommitSHA == "" {
		t.Error("commit SHA should not be empty")
	}
	if m.SnapshotCreatedByMission != "mission-001" {
		t.Errorf("expected mission-001, got %s", m.SnapshotCreatedByMission)
	}
	if m.SnapshotCreatedAt.IsZero() {
		t.Error("created_at should not be zero")
	}

	// README.md was committed in setupGitRepo
	hash, ok := m.Files["README.md"]
	if !ok {
		t.Fatal("README.md should be in manifest")
	}

	// Verify hash matches actual content
	content := []byte("# Test")
	expected := sha256.Sum256(content)
	if hash != hex.EncodeToString(expected[:]) {
		t.Errorf("hash mismatch for README.md")
	}
}

func TestInSnapshot(t *testing.T) {
	dir := setupGitRepo(t)

	m, err := TakeManifest(dir, "mission-002")
	if err != nil {
		t.Fatal(err)
	}

	if !m.InSnapshot("README.md") {
		t.Error("README.md should be in snapshot")
	}
	if m.InSnapshot("nonexistent.go") {
		t.Error("nonexistent.go should NOT be in snapshot")
	}

	// Create a new file after snapshot — should not be in snapshot
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o600)
	if m.InSnapshot("new.txt") {
		t.Error("new.txt should NOT be in snapshot (created after)")
	}
}

func TestPromote(t *testing.T) {
	dir := setupGitRepo(t)

	m, err := TakeManifest(dir, "mission-003")
	if err != nil {
		t.Fatal(err)
	}

	// Create a new file and promote it
	newPath := filepath.Join(dir, "promoted.go")
	os.WriteFile(newPath, []byte("package main\n"), 0o600)

	if m.InSnapshot("promoted.go") {
		t.Error("promoted.go should not be in snapshot yet")
	}

	err = m.Promote([]string{"promoted.go"}, dir)
	if err != nil {
		t.Fatal(err)
	}

	if !m.InSnapshot("promoted.go") {
		t.Error("promoted.go should be in snapshot after Promote")
	}

	// Verify the hash is correct
	data := []byte("package main\n")
	expected := sha256.Sum256(data)
	if m.Files["promoted.go"] != hex.EncodeToString(expected[:]) {
		t.Error("promoted.go hash mismatch")
	}
}

func TestManifestSaveAndLoad(t *testing.T) {
	dir := setupGitRepo(t)

	m, err := TakeManifest(dir, "mission-004")
	if err != nil {
		t.Fatal(err)
	}

	stokeDir := t.TempDir()
	if err := m.Save(stokeDir); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadManifest(stokeDir)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.SnapshotCommitSHA != m.SnapshotCommitSHA {
		t.Errorf("commit SHA mismatch: got %s, want %s", loaded.SnapshotCommitSHA, m.SnapshotCommitSHA)
	}
	if loaded.SnapshotCreatedByMission != m.SnapshotCreatedByMission {
		t.Errorf("mission mismatch: got %s, want %s", loaded.SnapshotCreatedByMission, m.SnapshotCreatedByMission)
	}
	if len(loaded.Files) != len(m.Files) {
		t.Errorf("files count mismatch: got %d, want %d", len(loaded.Files), len(m.Files))
	}
	for path, hash := range m.Files {
		if loaded.Files[path] != hash {
			t.Errorf("hash mismatch for %s", path)
		}
	}
}

func TestManifestLoadNotFound(t *testing.T) {
	_, err := LoadManifest("/nonexistent/stoke")
	if err == nil {
		t.Error("should error on missing manifest")
	}
}

func TestManifestSummary(t *testing.T) {
	dir := setupGitRepo(t)

	m, err := TakeManifest(dir, "mission-005")
	if err != nil {
		t.Fatal(err)
	}

	s := m.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
	if len(s) < 20 {
		t.Errorf("summary too short: %s", s)
	}
}

func TestManifestGitignorePatterns(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a .gitignore
	gitignore := "*.log\nbuild/\n# comment\n\n.env\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o600)

	// Add and commit .gitignore so it appears in ls-files
	gitOutput(dir, "add", ".gitignore")
	gitOutput(dir, "commit", "-m", "add gitignore", "--no-gpg-sign")

	m, err := TakeManifest(dir, "mission-006")
	if err != nil {
		t.Fatal(err)
	}

	// Should have 3 patterns (*.log, build/, .env) — not the comment or blank line
	if len(m.IgnoredPatterns) != 3 {
		t.Errorf("expected 3 ignored patterns, got %d: %v", len(m.IgnoredPatterns), m.IgnoredPatterns)
	}
}
