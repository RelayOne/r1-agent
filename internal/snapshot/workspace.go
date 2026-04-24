// Package snapshot implements workspace snapshot and restore.
// Inspired by OmX's workspace state capture and SWE-agent's state management:
//
// Before dangerous operations (risky refactors, large merges), snapshot the
// workspace state so it can be fully restored on failure:
// - Git state (HEAD, branch, staged changes, stash)
// - Modified files (content + permissions)
// - Environment variables relevant to the build
//
// This enables fearless experimentation: try bold changes knowing
// you can always revert to a known-good state.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Snapshot captures workspace state at a point in time.
type Snapshot struct {
	ID        string            `json:"id"`
	Dir       string            `json:"dir"`
	Branch    string            `json:"branch"`
	Commit    string            `json:"commit"`
	Staged    []string          `json:"staged,omitempty"`
	Modified  []FileState       `json:"modified,omitempty"`
	Untracked []string          `json:"untracked,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Label     string            `json:"label,omitempty"`
}

// FileState captures a single file's state.
type FileState struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Mode    uint32 `json:"mode"`
}

// Take captures the current workspace state.
func Take(dir, label string) (*Snapshot, error) {
	snap := &Snapshot{
		ID:        fmt.Sprintf("snap-%d", time.Now().UnixNano()),
		Dir:       dir,
		Label:     label,
		CreatedAt: time.Now(),
		Env:       make(map[string]string),
	}

	// Git branch
	branch, err := gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("get branch: %w", err)
	}
	snap.Branch = strings.TrimSpace(branch)

	// Git commit
	commit, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("get commit: %w", err)
	}
	snap.Commit = strings.TrimSpace(commit)

	// Modified and staged files
	status, err := gitOutput(dir, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		indicator := line[:2]
		filePath := strings.TrimSpace(line[2:])

		if strings.Contains(indicator, "?") {
			snap.Untracked = append(snap.Untracked, filePath)
		}
		if indicator[0] != ' ' && indicator[0] != '?' {
			snap.Staged = append(snap.Staged, filePath)
		}

		// Capture file content
		fullPath := filepath.Join(dir, filePath)
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			data, err := os.ReadFile(fullPath)
			if err == nil {
				snap.Modified = append(snap.Modified, FileState{
					Path:    filePath,
					Content: data,
					Mode:    uint32(info.Mode()),
				})
			}
		}
	}

	// Capture build-relevant env vars
	for _, key := range []string{"GOPATH", "GOROOT", "PATH", "NODE_PATH", "VIRTUAL_ENV"} {
		if val := os.Getenv(key); val != "" {
			snap.Env[key] = val
		}
	}

	return snap, nil
}

// Save writes the snapshot to a file.
func Save(snap *Snapshot, path string) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads a snapshot from a file.
func Load(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &snap, nil
}

// Restore applies a snapshot to the workspace.
// This is a best-effort restore: git state is reset, files are overwritten.
func Restore(snap *Snapshot) error {
	dir := snap.Dir

	// Checkout the branch
	if _, err := gitOutput(dir, "checkout", snap.Branch); err != nil {
		return fmt.Errorf("checkout branch: %w", err)
	}

	// Reset to the commit
	if _, err := gitOutput(dir, "reset", "--hard", snap.Commit); err != nil {
		return fmt.Errorf("reset to commit: %w", err)
	}

	// Restore modified files
	for _, f := range snap.Modified {
		fullPath := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(fullPath, f.Content, os.FileMode(f.Mode)); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}

	// Re-stage files that were staged
	for _, f := range snap.Staged {
		gitOutput(dir, "add", f)
	}

	return nil
}

// Diff compares two snapshots and returns the differences.
func Diff(a, b *Snapshot) []string {
	var diffs []string

	if a.Branch != b.Branch {
		diffs = append(diffs, fmt.Sprintf("branch: %s → %s", a.Branch, b.Branch))
	}
	if a.Commit != b.Commit {
		diffs = append(diffs, fmt.Sprintf("commit: %s → %s", a.Commit[:8], b.Commit[:8]))
	}

	// Compare modified files
	aFiles := make(map[string]bool)
	for _, f := range a.Modified {
		aFiles[f.Path] = true
	}
	bFiles := make(map[string]bool)
	for _, f := range b.Modified {
		bFiles[f.Path] = true
	}

	for path := range bFiles {
		if !aFiles[path] {
			diffs = append(diffs, fmt.Sprintf("new modified: %s", path))
		}
	}
	for path := range aFiles {
		if !bFiles[path] {
			diffs = append(diffs, fmt.Sprintf("no longer modified: %s", path))
		}
	}

	return diffs
}

// Summary returns a human-readable description.
func (s *Snapshot) Summary() string {
	return fmt.Sprintf("[%s] %s@%s (%d modified, %d staged, %d untracked)",
		s.Label, s.Branch, s.Commit[:minLen(8, len(s.Commit))],
		len(s.Modified), len(s.Staged), len(s.Untracked))
}

func minLen(a, b int) int {
	if a < b {
		return b
	}
	return a
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
