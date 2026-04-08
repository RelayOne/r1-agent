package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manifest is the protected baseline of the user's pre-existing code.
type Manifest struct {
	SnapshotCommitSHA        string            `json:"snapshot_commit_sha"`
	SnapshotCreatedAt        time.Time         `json:"snapshot_created_at"`
	SnapshotCreatedByMission string            `json:"snapshot_created_by_mission"`
	Files                    map[string]string `json:"files"`      // path → content hash
	Directories              []string          `json:"directories"`
	IgnoredPatterns          []string          `json:"ignored_patterns"`
}

const manifestRelPath = "snapshot/manifest.json"

// TakeManifest captures the current git-tracked state of the repo at repoRoot.
func TakeManifest(repoRoot string, missionID string) (*Manifest, error) {
	m := &Manifest{
		SnapshotCreatedAt:        time.Now(),
		SnapshotCreatedByMission: missionID,
		Files:                    make(map[string]string),
	}

	// 1. Get current HEAD commit SHA.
	sha, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	m.SnapshotCommitSHA = strings.TrimSpace(sha)

	// 2. List all tracked files.
	lsOut, err := gitOutput(repoRoot, "ls-files")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	dirs := make(map[string]bool)

	for _, relPath := range strings.Split(strings.TrimSpace(lsOut), "\n") {
		relPath = strings.TrimSpace(relPath)
		if relPath == "" {
			continue
		}

		// 3. Hash each file's content with SHA256.
		absPath := filepath.Join(repoRoot, relPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			// File listed by git but unreadable (e.g. submodule); skip.
			continue
		}
		hash := sha256.Sum256(data)
		m.Files[relPath] = hex.EncodeToString(hash[:])

		// 4. Collect directory paths.
		dir := filepath.Dir(relPath)
		for dir != "." && dir != "" {
			dirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}

	// Sort directories for deterministic output.
	for d := range dirs {
		m.Directories = append(m.Directories, d)
	}
	sort.Strings(m.Directories)

	// 5. Read .gitignore patterns.
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if data, err := os.ReadFile(gitignorePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				m.IgnoredPatterns = append(m.IgnoredPatterns, line)
			}
		}
	}

	return m, nil
}

// LoadManifest reads an existing manifest from <stokeDir>/snapshot/manifest.json.
func LoadManifest(stokeDir string) (*Manifest, error) {
	path := filepath.Join(stokeDir, manifestRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return &m, nil
}

// Save writes the manifest to <stokeDir>/snapshot/manifest.json.
func (m *Manifest) Save(stokeDir string) error {
	dir := filepath.Join(stokeDir, "snapshot")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
}

// InSnapshot checks if a file path is in the snapshot manifest.
func (m *Manifest) InSnapshot(filePath string) bool {
	// Normalize to forward-slash relative path.
	cleaned := filepath.ToSlash(filepath.Clean(filePath))
	_, ok := m.Files[cleaned]
	return ok
}

// Promote adds new files to the snapshot manifest (user-explicit action).
// Each path is resolved relative to repoRoot and its content is hashed.
func (m *Manifest) Promote(paths []string, repoRoot string) error {
	for _, p := range paths {
		relPath := filepath.ToSlash(filepath.Clean(p))
		absPath := filepath.Join(repoRoot, relPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}
		hash := sha256.Sum256(data)
		m.Files[relPath] = hex.EncodeToString(hash[:])
	}
	return nil
}

// Summary returns a human-readable summary.
func (m *Manifest) Summary() string {
	return fmt.Sprintf("Snapshot %s: %d files, %d directories (commit %s, mission %s)",
		m.SnapshotCreatedAt.Format(time.RFC3339),
		len(m.Files),
		len(m.Directories),
		truncSHA(m.SnapshotCommitSHA),
		m.SnapshotCreatedByMission,
	)
}

func truncSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
