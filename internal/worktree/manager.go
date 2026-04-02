package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Manager struct {
	RepoRoot     string
	GitBinary    string
	WorktreeBase string
	mergeMu      sync.Mutex // serializes merges to main (parallel execution ok, parallel mutation of refs is not)
}

type Handle struct {
	Name       string
	Branch     string
	Path       string // worktree path (agent-writable, untrusted)
	RuntimeDir string // harness runtime files (outside worktree, trusted)
	BaseCommit string // target branch HEAD at worktree creation (for diffing)
	RepoRoot   string
	GitBinary  string
}

func NewManager(repoRoot string) *Manager {
	return &Manager{RepoRoot: repoRoot, GitBinary: "git", WorktreeBase: filepath.Join(repoRoot, ".stoke", "worktrees")}
}

func (m *Manager) Prepare(ctx context.Context, explicitName string) (Handle, error) {
	name := slug(explicitName)
	if name == "" {
		name = "task"
	}
	branch := "stoke/" + name
	path := filepath.Join(m.WorktreeBase, name)

	// RuntimeDir is OUTSIDE the worktree. The agent cannot influence harness
	// writes here (no symlink attacks, no staged-path tricks).
	runtimeDir := filepath.Join(os.TempDir(), "stoke-runtime-"+name)
	os.RemoveAll(runtimeDir) // clean previous run
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return Handle{}, fmt.Errorf("create runtime dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Handle{}, err
	}

	// Capture target branch HEAD before creating the worktree
	baseCmd := exec.CommandContext(ctx, m.GitBinary, "rev-parse", "HEAD")
	baseCmd.Dir = m.RepoRoot
	baseOut, err := baseCmd.Output()
	if err != nil {
		return Handle{}, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	baseCommit := strings.TrimSpace(string(baseOut))

	// Detect default branch (don't hardcode "main" -- repos may use "master" or custom names)
	defaultBranch := "main"
	branchCmd := exec.CommandContext(ctx, m.GitBinary, "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = m.RepoRoot
	if branchOut, err := branchCmd.Output(); err == nil {
		if b := strings.TrimSpace(string(branchOut)); b != "" && b != "HEAD" {
			defaultBranch = b
		}
	}

	cmd := exec.CommandContext(ctx, m.GitBinary, "worktree", "add", path, "-b", branch, defaultBranch)
	cmd.Dir = m.RepoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(runtimeDir)
		return Handle{}, fmt.Errorf("git worktree add: %w: %s", err, string(out))
	}
	return Handle{
		Name: name, Branch: branch, Path: path, RuntimeDir: runtimeDir,
		BaseCommit: baseCommit, RepoRoot: m.RepoRoot, GitBinary: m.GitBinary,
	}, nil
}

func (m *Manager) Cleanup(ctx context.Context, handle Handle) error {
	var errs []string

	// Remove RuntimeDir (trusted harness files)
	if handle.RuntimeDir != "" {
		os.RemoveAll(handle.RuntimeDir)
	}

	// Force-remove worktree (handles dirty worktrees with uncommitted changes)
	cmd := exec.CommandContext(ctx, m.GitBinary, "worktree", "remove", "--force", handle.Path)
	cmd.Dir = m.RepoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		errs = append(errs, fmt.Sprintf("worktree remove: %v: %s", err, strings.TrimSpace(string(out))))
		// Belt-and-suspenders: remove directory manually if git fails
		os.RemoveAll(handle.Path)
	}

	// Delete branch
	branchCmd := exec.CommandContext(ctx, m.GitBinary, "branch", "-D", handle.Branch)
	branchCmd.Dir = m.RepoRoot
	if out, err := branchCmd.CombinedOutput(); err != nil {
		errs = append(errs, fmt.Sprintf("branch -D: %v: %s", err, strings.TrimSpace(string(out))))
	}

	// Prune stale worktree refs
	pruneCmd := exec.CommandContext(ctx, m.GitBinary, "worktree", "prune")
	pruneCmd.Dir = m.RepoRoot
	pruneCmd.Run() // best-effort

	// Delete snapshot refs created by SnapshotWorkingTree.
	// Without this, intermediate agent commits survive as reachable objects
	// under refs/stoke-snapshots/ and accumulate over time.
	snapRef := "refs/stoke-snapshots/" + handle.Name
	delRefCmd := exec.CommandContext(ctx, m.GitBinary, "update-ref", "-d", snapRef)
	delRefCmd.Dir = m.RepoRoot
	delRefCmd.Run() // best-effort

	if len(errs) > 0 {
		return fmt.Errorf("cleanup %s: %s", handle.Name, strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) Merge(ctx context.Context, handle Handle, message string) error {
	// Serialize all merges -- parallel task execution is fine,
	// parallel mutation of main refs causes corruption.
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()

	// Validate with merge-tree first (zero side effects, Git 2.38+)
	validateCmd := exec.CommandContext(ctx, m.GitBinary, "merge-tree", "--write-tree", "HEAD", handle.Branch)
	validateCmd.Dir = m.RepoRoot
	if out, err := validateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("merge conflict detected: %s", strings.TrimSpace(string(out)))
	}

	// Merge for real
	mergeCmd := exec.CommandContext(ctx, m.GitBinary, "merge", "--no-ff", handle.Branch, "-m", message)
	mergeCmd.Dir = m.RepoRoot
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git merge: %w: %s", err, string(out))
	}

	// Clean up worktree and branch after successful merge
	m.Cleanup(ctx, handle)
	return nil
}

var invalidSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = invalidSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}
