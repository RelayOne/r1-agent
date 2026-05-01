// Package worktree manages git worktree lifecycle: creation, merge-tree validation, serialized merges, and cleanup.
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
	"time"

	"github.com/RelayOne/r1/internal/conflictres"
	"github.com/RelayOne/r1/internal/r1dir"
)

// Manager creates, merges, and cleans up git worktrees, serializing merges via a mutex to prevent ref corruption.
type Manager struct {
	RepoRoot     string
	GitBinary    string
	WorktreeBase string
	mergeMu      sync.Mutex // serializes merges to main (parallel execution ok, parallel mutation of refs is not)

	// Signer, when non-nil, is applied to every git command this Manager
	// invokes that creates a commit (merge, conflict-resolution commit,
	// snapshot commits via helpers). Applies signing-key git config +
	// committer/author env so the resulting commit carries the stance's
	// cryptographic attestation. Closes anti-deception matrix row A1
	// (commit attribution).
	//
	// Defined as an interface (single-method) instead of importing
	// internal/stancesign directly to keep this package's import graph
	// small. Real implementations satisfy it via stancesign.Identity.
	// Unset = unsigned commits = pre-A1 behavior preserved.
	Signer CommitSigner
}

// CommitSigner is implemented by anything that can attach a signing
// identity to a git command — typically *stancesign.Identity. The
// interface is single-method by design so callers can pass either a
// real Identity or a no-op stub for tests / unsigned mode without
// pulling in the full stancesign package.
type CommitSigner interface {
	ApplyTo(cmd *exec.Cmd)
}

// Handle holds the paths, branch name, and base commit for a single git worktree created by the Manager.
type Handle struct {
	Name       string
	Branch     string
	Path       string // worktree path (agent-writable, untrusted)
	RuntimeDir string // harness runtime files (outside worktree, trusted)
	BaseCommit string // target branch HEAD at worktree creation (for diffing)
	RepoRoot   string
	GitBinary  string
}

// NewManager creates a Manager rooted at the given repository path with default worktree base under .stoke/worktrees.
func NewManager(repoRoot string) *Manager {
	return &Manager{RepoRoot: repoRoot, GitBinary: "git", WorktreeBase: r1dir.JoinFor(repoRoot, "worktrees")}
}

// Prepare creates a new git worktree and branch for a task, capturing the base commit for later diffing.
func (m *Manager) Prepare(ctx context.Context, explicitName string) (Handle, error) {
	name := slug(explicitName)
	if name == "" {
		name = "task"
	}
	branch := "r1/" + name
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
	baseCmd := exec.CommandContext(ctx, m.GitBinary, "rev-parse", "HEAD") // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	baseCmd.Dir = m.RepoRoot
	baseOut, err := baseCmd.Output()
	if err != nil {
		return Handle{}, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	baseCommit := strings.TrimSpace(string(baseOut))

	// Detect default branch (don't hardcode "main" -- repos may use "master" or custom names)
	defaultBranch := "main"
	branchCmd := exec.CommandContext(ctx, m.GitBinary, "rev-parse", "--abbrev-ref", "HEAD") // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	branchCmd.Dir = m.RepoRoot
	if branchOut, bErr := branchCmd.Output(); bErr == nil {
		if b := strings.TrimSpace(string(branchOut)); b != "" && b != "HEAD" {
			defaultBranch = b
		}
	}

	cmd := exec.CommandContext(ctx, m.GitBinary, "worktree", "add", path, "-b", branch, defaultBranch) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	cmd.Dir = m.RepoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(runtimeDir)
		return Handle{}, fmt.Errorf("git worktree add: %w: %s", err, string(out))
	}
	handle := Handle{
		Name: name, Branch: branch, Path: path, RuntimeDir: runtimeDir,
		BaseCommit: baseCommit, RepoRoot: m.RepoRoot, GitBinary: m.GitBinary,
	}

	// Symlink shared dependency directories from main into the worktree
	// to avoid reinstalling dependencies per worktree.
	symlinkSharedDeps(m.RepoRoot, path)

	return handle, nil
}

// sharedDepDirs returns dependency directories that can be safely shared
// across worktrees via symlinks. Returns a fresh slice each call to prevent
// mutation of a package-level variable.
func sharedDepDirs() []string {
	return []string{
		"node_modules",
		"vendor",
		".venv",
		"target",      // Rust
		"__pycache__",
		".gradle",
		".m2",
	}
}

// symlinkSharedDeps creates symlinks from the worktree to the main repo's
// dependency directories. This avoids reinstalling deps in each worktree.
// Only symlinks directories that exist in the source (verified via Lstat to
// avoid following existing symlinks) and don't exist in the target.
func symlinkSharedDeps(repoRoot, worktreePath string) {
	cleanRoot := filepath.Clean(repoRoot)
	for _, dir := range sharedDepDirs() {
		src := filepath.Join(repoRoot, dir)

		// Validate the resolved path is still under repoRoot
		cleanSrc := filepath.Clean(src)
		if !strings.HasPrefix(cleanSrc, cleanRoot+string(filepath.Separator)) && cleanSrc != cleanRoot {
			continue
		}

		dst := filepath.Join(worktreePath, dir)

		// Use Lstat to avoid following symlinks in the source repo
		info, err := os.Lstat(src)
		if err != nil || !info.IsDir() {
			continue
		}
		// Skip if source is itself a symlink (prevents indirection attacks)
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			os.Symlink(src, dst)
		}
	}
}

// Cleanup removes a worktree, its branch, runtime directory, and snapshot refs.
func (m *Manager) Cleanup(ctx context.Context, handle Handle) error {
	var errs []string

	// Remove RuntimeDir (trusted harness files)
	if handle.RuntimeDir != "" {
		os.RemoveAll(handle.RuntimeDir)
	}

	// Force-remove worktree (handles dirty worktrees with uncommitted changes)
	cmd := exec.CommandContext(ctx, m.GitBinary, "worktree", "remove", "--force", handle.Path) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	cmd.Dir = m.RepoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		errs = append(errs, fmt.Sprintf("worktree remove: %v: %s", err, strings.TrimSpace(string(out))))
		// Belt-and-suspenders: remove directory manually if git fails
		os.RemoveAll(handle.Path)
	}

	// Delete branch
	branchCmd := exec.CommandContext(ctx, m.GitBinary, "branch", "-D", handle.Branch) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	branchCmd.Dir = m.RepoRoot
	if out, err := branchCmd.CombinedOutput(); err != nil {
		errs = append(errs, fmt.Sprintf("branch -D: %v: %s", err, strings.TrimSpace(string(out))))
	}

	// Prune stale worktree refs
	pruneCmd := exec.CommandContext(ctx, m.GitBinary, "worktree", "prune") // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	pruneCmd.Dir = m.RepoRoot
	pruneCmd.Run() // best-effort

	// Delete snapshot refs created by SnapshotWorkingTree.
	// Without this, intermediate agent commits survive as reachable objects
	// under refs/stoke-snapshots/ and accumulate over time.
	snapRef := "refs/stoke-snapshots/" + handle.Name
	delRefCmd := exec.CommandContext(ctx, m.GitBinary, "update-ref", "-d", snapRef) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	delRefCmd.Dir = m.RepoRoot
	delRefCmd.Run() // best-effort

	if len(errs) > 0 {
		return fmt.Errorf("cleanup %s: %s", handle.Name, strings.Join(errs, "; "))
	}
	return nil
}

// mergeTimeout is the maximum time allowed for merge-tree validation and
// the actual merge operation. Prevents pathological merges from blocking forever.
const mergeTimeout = 2 * time.Minute

// Merge validates the worktree branch with merge-tree, then merges it into the target branch under a serializing mutex.
func (m *Manager) Merge(ctx context.Context, handle Handle, message string) error {
	// Serialize all merges -- parallel task execution is fine,
	// parallel mutation of main refs causes corruption.
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()

	mergeCtx, cancel := context.WithTimeout(ctx, mergeTimeout)
	defer cancel()

	// Validate with merge-tree first (zero side effects, Git 2.38+)
	var conflicts []conflictres.Conflict // hoisted for use in merge fallback
	validateCmd := exec.CommandContext(mergeCtx, m.GitBinary, "merge-tree", "--write-tree", "HEAD", handle.Branch) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	validateCmd.Dir = m.RepoRoot
	if out, err := validateCmd.CombinedOutput(); err != nil {
		if mergeCtx.Err() != nil {
			return fmt.Errorf("merge-tree timed out after %v", mergeTimeout)
		}
		// Attempt semantic conflict resolution before failing.
		mergeOutput := strings.TrimSpace(string(out))
		conflicts = conflictres.Parse(mergeOutput, "")
		if len(conflicts) > 0 {
			conflictres.AutoResolve(conflicts)
			// Count how many remain unresolved.
			unresolved := 0
			for _, c := range conflicts {
				if !c.AutoResolved {
					unresolved++
				}
			}
			if unresolved > 0 {
				return fmt.Errorf("merge conflict (%d/%d unresolved): %s",
					unresolved, len(conflicts), mergeOutput)
			}
			// All conflicts auto-resolved — log and proceed to real merge.
			// Note: merge-tree is a dry run; the actual merge below may still
			// succeed because git's merge strategies differ from merge-tree.
		} else {
			return fmt.Errorf("merge conflict detected: %s", mergeOutput)
		}
	}

	// Merge for real
	mergeCmd := exec.CommandContext(mergeCtx, m.GitBinary, "merge", "--no-ff", handle.Branch, "-m", message) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	mergeCmd.Dir = m.RepoRoot
	if m.Signer != nil {
		m.Signer.ApplyTo(mergeCmd)
	}
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		if mergeCtx.Err() != nil {
			return fmt.Errorf("git merge timed out after %v", mergeTimeout)
		}
		// If we previously auto-resolved all conflicts via merge-tree,
		// apply the resolutions to the working tree and complete the merge.
		if len(conflicts) > 0 && allAutoResolved(conflicts) {
			if applyErr := m.applyConflictResolutions(mergeCtx, handle, conflicts); applyErr != nil {
				// Abort the conflicted merge state before returning.
				abortCmd := exec.CommandContext(mergeCtx, m.GitBinary, "merge", "--abort") // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
				abortCmd.Dir = m.RepoRoot
				_ = abortCmd.Run()
				return fmt.Errorf("git merge conflict auto-resolution failed: %w (original: %s)", applyErr, string(out))
			}
		} else {
			return fmt.Errorf("git merge: %w: %s", err, string(out))
		}
	}

	// Clean up worktree and branch after successful merge
	m.Cleanup(ctx, handle)
	return nil
}

// allAutoResolved returns true if every conflict in the slice was auto-resolved.
func allAutoResolved(conflicts []conflictres.Conflict) bool {
	for _, c := range conflicts {
		if !c.AutoResolved {
			return false
		}
	}
	return len(conflicts) > 0
}

// applyConflictResolutions reads each conflicted file from the working tree
// (which contains git conflict markers after a failed merge), applies the
// auto-resolved content via conflictres.Resolve, writes the file back,
// stages it, and commits the merge.
//
// SAFETY: merge-tree and git-merge use different merge strategies, so the
// conflicts detected by merge-tree may not exactly match those in the working
// tree. This method guards against mismatch by verifying that each file
// actually contains conflict markers before applying resolutions. Files
// without markers are staged as-is (git may have resolved them differently).
func (m *Manager) applyConflictResolutions(ctx context.Context, handle Handle, conflicts []conflictres.Conflict) error {
	// Group conflicts by file.
	byFile := make(map[string][]conflictres.Conflict)
	for _, c := range conflicts {
		byFile[c.File] = append(byFile[c.File], c)
	}

	for file, fileConflicts := range byFile {
		filePath := filepath.Join(m.RepoRoot, file)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read conflicted file %s: %w", file, err)
		}

		contentStr := string(content)

		// Guard: only apply resolutions if the file actually has conflict markers.
		// merge-tree and git-merge use different strategies, so a file that
		// merge-tree flagged as conflicted may have been cleanly merged by git.
		// Applying Resolve() to a file without markers would corrupt it.
		if !strings.Contains(contentStr, "<<<<<<<") {
			// No conflict markers — git resolved this file on its own. Stage it.
			addCmd := exec.CommandContext(ctx, m.GitBinary, "add", file) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
			addCmd.Dir = m.RepoRoot
			if out, addErr := addCmd.CombinedOutput(); addErr != nil {
				return fmt.Errorf("git add %s: %w: %s", file, addErr, string(out))
			}
			continue
		}

		res := conflictres.Resolve(contentStr, fileConflicts)
		if !res.AllAuto {
			return fmt.Errorf("file %s has unresolved conflicts after apply", file)
		}

		if err := os.WriteFile(filePath, []byte(res.Resolved), 0644); err != nil { // #nosec G306 -- conflict-resolved source file; 0644 preserves source perms.
			return fmt.Errorf("write resolved file %s: %w", file, err)
		}

		// Stage the resolved file.
		addCmd := exec.CommandContext(ctx, m.GitBinary, "add", file) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
		addCmd.Dir = m.RepoRoot
		if out, err := addCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git add %s: %w: %s", file, err, string(out))
		}
	}

	// Complete the merge commit.
	commitCmd := exec.CommandContext(ctx, m.GitBinary, "commit", "--no-edit") // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	commitCmd.Dir = m.RepoRoot
	if m.Signer != nil {
		m.Signer.ApplyTo(commitCmd)
	}
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit (merge): %w: %s", err, string(out))
	}

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
