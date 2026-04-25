package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1-agent/internal/atomicfs"
)

// gitHEAD is the conventional git reference for the tip of the current branch.
const gitHEAD = "HEAD"

// ModifiedFiles returns ALL files changed in a worktree vs the task branch base.
// Uses --name-status with -M for BOTH committed AND staged diffs to capture
// both sides of renames. FAIL-CLOSED: returns error if any git command fails.
func ModifiedFiles(ctx context.Context, handle Handle) ([]string, error) {
	base := handle.BaseCommit
	if base == "" {
		base = gitHEAD
	}

	seen := map[string]bool{}
	var errors []string

	parseNameStatus := func(label string, cmd *exec.Cmd) {
		out, err := cmd.Output()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", label, err))
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) >= 2 {
				seen[parts[1]] = true
			}
			if len(parts) >= 3 {
				seen[parts[2]] = true
			}
		}
	}

	collectNameOnly := func(label string, cmd *exec.Cmd) {
		out, err := cmd.Output()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", label, err))
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				seen[line] = true
			}
		}
	}

	// 1. Committed changes: --name-status -M captures rename old+new paths
	c1 := exec.CommandContext(ctx, handle.GitBinary, "diff", "--name-status", "-M", base+"..HEAD") // #nosec G204 -- git binary with Stoke-generated args (BaseCommit captured at worktree creation, literal subcommands) not external input.
	c1.Dir = handle.Path
	parseNameStatus("committed", c1)

	// 2. Staged changes: ALSO --name-status -M (catches staged renames like git mv)
	c2 := exec.CommandContext(ctx, handle.GitBinary, "diff", "--name-status", "-M", "--cached") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	c2.Dir = handle.Path
	parseNameStatus("staged", c2)

	// 3. Unstaged working-tree changes
	c3 := exec.CommandContext(ctx, handle.GitBinary, "diff", "--name-only") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	c3.Dir = handle.Path
	collectNameOnly("unstaged", c3)

	// 4. Untracked files
	c4 := exec.CommandContext(ctx, handle.GitBinary, "ls-files", "--others", "--exclude-standard") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	c4.Dir = handle.Path
	collectNameOnly("untracked", c4)

	if len(errors) > 0 {
		return nil, fmt.Errorf("incomplete diff (checks unsafe): %s", strings.Join(errors, "; "))
	}

	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	return files, nil
}

// IgnoredNewFiles returns files in the worktree that match .gitignore patterns
// and were NOT present at BaseCommit. These files are invisible to git add -A
// and won't be in the merged commit, but the agent's build/test may depend on them.
// Callers should warn if non-empty: the verified environment differs from the merge artifact.
func IgnoredNewFiles(ctx context.Context, handle Handle) []string {
	cmd := exec.CommandContext(ctx, handle.GitBinary, "ls-files", "--others", "--ignored", "--exclude-standard") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	cmd.Dir = handle.Path
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ignored []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ignored = append(ignored, line)
		}
	}
	return ignored
}

// DiffSummary returns a compressed summary of ALL changes for retry briefs.
// Includes tracked changes (committed + staged + unstaged) AND untracked files.
func DiffSummary(ctx context.Context, handle Handle) string {
	base := handle.BaseCommit
	if base == "" {
		base = gitHEAD
	}

	var parts []string

	// Tracked changes
	cmd := exec.CommandContext(ctx, handle.GitBinary, "diff", "--stat", base) // #nosec G204 -- git binary with Stoke-generated args (BaseCommit captured at worktree creation, literal subcommands) not external input.
	cmd.Dir = handle.Path
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		parts = append(parts, strings.TrimSpace(string(out)))
	}

	// Untracked files (new files the agent created but didn't stage)
	lsCmd := exec.CommandContext(ctx, handle.GitBinary, "ls-files", "--others", "--exclude-standard") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	lsCmd.Dir = handle.Path
	lsOut, err := lsCmd.Output()
	if err != nil {
		lsOut = nil
	}
	untracked := strings.TrimSpace(string(lsOut))
	if untracked != "" {
		lines := strings.Split(untracked, "\n")
		parts = append(parts, fmt.Sprintf("%d new file(s): %s", len(lines), strings.Join(lines, ", ")))
	}

	if len(parts) == 0 {
		return "(diff unavailable)"
	}
	return strings.Join(parts, "\n")
}

// ScopeCheck verifies that all modified files fall within the allowed set.
func ScopeCheck(files []string, allowed []string) []string {
	if len(allowed) == 0 {
		return nil
	}
	exactFiles := map[string]bool{}
	var dirPrefixes []string
	for _, f := range allowed {
		if len(f) > 0 && f[len(f)-1] == '/' {
			dirPrefixes = append(dirPrefixes, f)
		} else {
			exactFiles[f] = true
		}
	}
	var violations []string
	for _, f := range files {
		if exactFiles[f] {
			continue
		}
		inDir := false
		for _, prefix := range dirPrefixes {
			if strings.HasPrefix(f, prefix) {
				inDir = true
				break
			}
		}
		if !inDir {
			violations = append(violations, f)
		}
	}
	return violations
}

// SafeWriteFile writes data to root/name, rejecting symlinks at any path
// component and path traversal.
func SafeWriteFile(root, name string, data []byte, perm os.FileMode) error {
	target := filepath.Join(root, name)
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	absRoot, _ := filepath.Abs(root)
	if !strings.HasPrefix(abs, absRoot+string(filepath.Separator)) && abs != absRoot {
		return fmt.Errorf("path traversal rejected: %q escapes %q", name, root)
	}
	rel, _ := filepath.Rel(absRoot, abs)
	parts := strings.Split(rel, string(filepath.Separator))
	check := absRoot
	for _, part := range parts {
		check = filepath.Join(check, part)
		if info, err := os.Lstat(check); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink rejected at %q", check)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// ErrNothingToCommit signals the validated set produced no diff vs base.
// Callers MUST skip merge when they see this.
var ErrNothingToCommit = fmt.Errorf("nothing to commit")

// SnapshotWorkingTree captures the FULL working tree state (committed + staged
// + unstaged + untracked) into a snapshot commit using git plumbing commands.
// Returns the snapshot commit SHA.
//
// Uses write-tree + commit-tree instead of porcelain commit because:
//   - HEAD never moves (branch stays where it was)
//   - No hooks fire (bypasses pre-commit, commit-msg, post-commit)
//   - No branch history pollution (snapshot is a dangling commit)
//
// The snapshot is stored under refs/stoke-snapshots/ to protect from GC.
// git add -A correctly handles untracked files, binary files, symlinks, and
// executable permissions. Gaps: gitignored files are skipped, empty dirs vanish.
func SnapshotWorkingTree(ctx context.Context, handle Handle) (string, error) {
	// 1. Stage everything into the index (including untracked)
	addCmd := exec.CommandContext(ctx, handle.GitBinary, "add", "-A") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	addCmd.Dir = handle.Path
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("snapshot: git add -A: %w: %s", err, out)
	}

	// 2. Serialize the index into a tree object (does not touch HEAD)
	writeTreeCmd := exec.CommandContext(ctx, handle.GitBinary, "write-tree") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	writeTreeCmd.Dir = handle.Path
	treeOut, err := writeTreeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("snapshot: git write-tree: %w", err)
	}
	treeSHA := strings.TrimSpace(string(treeOut))

	// 3. Get current HEAD for parent linkage
	headCmd := exec.CommandContext(ctx, handle.GitBinary, "rev-parse", gitHEAD) // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	headCmd.Dir = handle.Path
	headOut, err := headCmd.Output()
	if err != nil {
		return "", fmt.Errorf("snapshot: git rev-parse HEAD: %w", err)
	}
	headSHA := strings.TrimSpace(string(headOut))

	// 4. Create a commit object from the tree (HEAD never moves, no hooks fire)
	// treeSHA and headSHA come from git's own output above (tree/commit object hashes); not external input.
	commitTreeCmd := exec.CommandContext(ctx, handle.GitBinary, // #nosec G204 -- git binary; treeSHA/headSHA are git-produced object hashes from preceding commands.
		"commit-tree", treeSHA, "-p", headSHA, "-m", "stoke: working tree snapshot")
	commitTreeCmd.Dir = handle.Path
	snapOut, err := commitTreeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("snapshot: git commit-tree: %w", err)
	}
	snapSHA := strings.TrimSpace(string(snapOut))

	// 5. Store under a ref to protect from GC (dangling commits expire in 14 days)
	// refName composed from Stoke-controlled prefix + handle.Name (set when worktree created).
	refName := "refs/stoke-snapshots/" + handle.Name
	refCmd := exec.CommandContext(ctx, handle.GitBinary, "update-ref", refName, snapSHA) // #nosec G204 -- git binary; refName has fixed Stoke prefix + internal handle name, snapSHA is git-produced.
	refCmd.Dir = handle.Path
	refCmd.CombinedOutput() // best effort; snapshot SHA is still valid even if ref fails

	// 6. Reset the index back to HEAD (so subsequent git operations see clean index)
	readTreeCmd := exec.CommandContext(ctx, handle.GitBinary, "read-tree", gitHEAD) // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	readTreeCmd.Dir = handle.Path
	readTreeCmd.CombinedOutput() // best effort

	return snapSHA, nil
}

// CommitVerifiedTree builds a single clean commit from BaseCommit containing
// exactly the validated files from the agent's working tree.
//
// Flow:
//  1. Snapshot full working tree (captures uncommitted work)
//  2. Hard-reset to BaseCommit
//  3. Checkout validated files from snapshot
//  4. Handle deletions and rename old-sides
//  5. Create one clean harness commit
//
// Works regardless of whether agent committed, staged, or left changes loose.
// CommitVerifiedTreeWithSigner is the signer-aware variant of
// CommitVerifiedTree. When signer is non-nil, the underlying git
// commit invocation receives the signing identity overlay (signing
// key + committer/author env). signer == nil falls back to the
// unsigned path so existing callers stay untouched.
func CommitVerifiedTreeWithSigner(ctx context.Context, handle Handle, validatedFiles []string, message string, signer interface{ ApplyTo(*exec.Cmd) }) error {
	return commitVerifiedTreeImpl(ctx, handle, validatedFiles, message, signer)
}

func CommitVerifiedTree(ctx context.Context, handle Handle, validatedFiles []string, message string) error {
	return commitVerifiedTreeImpl(ctx, handle, validatedFiles, message, nil)
}

func commitVerifiedTreeImpl(ctx context.Context, handle Handle, validatedFiles []string, message string, signer interface{ ApplyTo(*exec.Cmd) }) error {
	if len(validatedFiles) == 0 {
		return ErrNothingToCommit
	}

	// 1. Snapshot the full working tree.
	snapshot, err := SnapshotWorkingTree(ctx, handle)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	// 2. Classify: exists at snapshot vs deleted by agent.
	var existFiles []string
	var deletedFiles []string
	for _, f := range validatedFiles {
		// f is from validatedFiles (caller-validated path list); snapshot is a git-produced SHA.
		catCmd := exec.CommandContext(ctx, handle.GitBinary, "cat-file", "-e", snapshot+":"+f) // #nosec G204 -- git binary; snapshot is git-produced SHA, f is a pre-validated path from verify stage.
		catCmd.Dir = handle.Path
		if catCmd.Run() == nil {
			existFiles = append(existFiles, f)
		} else {
			deletedFiles = append(deletedFiles, f)
		}
	}

	// 3. Hard-reset to BaseCommit. BaseCommit is captured by Stoke at worktree creation.
	resetCmd := exec.CommandContext(ctx, handle.GitBinary, "reset", "--hard", handle.BaseCommit) // #nosec G204 -- git binary; BaseCommit captured at worktree creation from git rev-parse, not external input.
	resetCmd.Dir = handle.Path
	if out, rErr := resetCmd.CombinedOutput(); rErr != nil {
		return fmt.Errorf("reset to base: %w: %s", rErr, out)
	}

	// 4. Checkout validated files from snapshot.
	if len(existFiles) > 0 {
		coArgs := append([]string{"checkout", snapshot, "--"}, existFiles...)
		coCmd := exec.CommandContext(ctx, handle.GitBinary, coArgs...) // #nosec G204 -- git binary; coArgs has a literal subcommand leader plus git-produced snapshot SHA and pre-validated file paths.
		coCmd.Dir = handle.Path
		if out, coErr := coCmd.CombinedOutput(); coErr != nil {
			return fmt.Errorf("checkout from snapshot %s: %w: %s", snapshot[:8], coErr, out)
		}
	}

	// 5. Remove files the agent deleted.
	for _, f := range deletedFiles {
		rmCmd := exec.CommandContext(ctx, handle.GitBinary, "rm", "--cached", "--quiet", "--ignore-unmatch", f) // #nosec G204 -- git binary; f is a pre-validated file path from verify stage.
		rmCmd.Dir = handle.Path
		rmCmd.CombinedOutput()
		os.Remove(filepath.Join(handle.Path, f))
	}

	// 6. Handle old rename sides: base files not in validated set, gone at snapshot.
	validSet := make(map[string]bool, len(validatedFiles))
	for _, f := range validatedFiles {
		validSet[f] = true
	}
	lsCmd := exec.CommandContext(ctx, handle.GitBinary, "ls-tree", "-r", "--name-only", handle.BaseCommit) // #nosec G204 -- git binary; BaseCommit is Stoke-captured SHA, literal subcommand arguments otherwise.
	lsCmd.Dir = handle.Path
	lsOut, err := lsCmd.Output()
	if err != nil {
		return fmt.Errorf("ls-tree base: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(lsOut)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || validSet[line] {
			continue
		}
		// line comes from git ls-tree output (git-produced path listing of BaseCommit).
		catCmd := exec.CommandContext(ctx, handle.GitBinary, "cat-file", "-e", snapshot+":"+line) // #nosec G204 -- git binary; snapshot is git-produced SHA, line is a path from git's own ls-tree output.
		catCmd.Dir = handle.Path
		if catCmd.Run() != nil {
			rmCmd := exec.CommandContext(ctx, handle.GitBinary, "rm", "--cached", "--quiet", "--ignore-unmatch", line) // #nosec G204 -- git binary; line is a path from git's own ls-tree output.
			rmCmd.Dir = handle.Path
			rmCmd.CombinedOutput()
			os.Remove(filepath.Join(handle.Path, line))
		}
	}

	// 6b. Atomic validation: use atomicfs to verify no concurrent modifications
	// occurred during the checkout. Build a transaction over the validated files
	// and run conflict detection before committing.
	tx := atomicfs.NewTransaction(handle.Path)
	for _, f := range existFiles {
		data, readErr := os.ReadFile(filepath.Join(handle.Path, f))
		if readErr != nil {
			continue
		}
		_ = tx.Write(f, data) // stages file with original hash for conflict detection
	}
	if err := tx.Validate(); err != nil {
		return fmt.Errorf("atomic validation failed (concurrent modification): %w", err)
	}

	// 7. Stage everything.
	addCmd := exec.CommandContext(ctx, handle.GitBinary, "add", "-A") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	addCmd.Dir = handle.Path
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, out)
	}

	// 8. Check if anything is staged.
	statusCmd := exec.CommandContext(ctx, handle.GitBinary, "diff", "--cached", "--quiet") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	statusCmd.Dir = handle.Path
	if err := statusCmd.Run(); err == nil {
		return ErrNothingToCommit
	}

	// 9. Commit. message is the Stoke-authored harness commit message string.
	commitCmd := exec.CommandContext(ctx, handle.GitBinary, "commit", "-m", message) // #nosec G204 -- git binary; message is a Stoke-authored harness commit message, not external input.
	commitCmd.Dir = handle.Path
	if signer != nil {
		signer.ApplyTo(commitCmd)
	}
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, out)
	}
	return nil
}

// ValidateMerge runs git merge-tree to check for conflicts without side effects.
func ValidateMerge(ctx context.Context, handle Handle) error {
	cmd := exec.CommandContext(ctx, handle.GitBinary, "merge-tree", "--write-tree", gitHEAD, handle.Branch) // #nosec G204 -- git binary; Branch is a Stoke-generated worktree branch name (not external input).
	cmd.Dir = handle.RepoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge conflict: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// HashFiles computes SHA-256 hashes of file contents. Missing files get "MISSING".
func HashFiles(root string, files []string) map[string]string {
	hashes := make(map[string]string, len(files))
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			hashes[f] = "MISSING"
			continue
		}
		h := sha256.Sum256(data)
		hashes[f] = hex.EncodeToString(h[:])
	}
	return hashes
}

// TreeSHA returns the git tree object SHA for the current index.
// Captures content, modes, and structure in one hash. Two identical
// TreeSHA values guarantee the exact same tree (catches mode changes
// that HashFiles misses).
func TreeSHA(ctx context.Context, handle Handle) (string, error) {
	addCmd := exec.CommandContext(ctx, handle.GitBinary, "add", "-A") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	addCmd.Dir = handle.Path
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add -A: %w: %s", err, out)
	}

	cmd := exec.CommandContext(ctx, handle.GitBinary, "write-tree") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	cmd.Dir = handle.Path
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MainHeadSHA returns the current HEAD commit SHA of the main branch.
// Returns empty string on error (non-fatal).
func MainHeadSHA(ctx context.Context, repoRoot string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", gitHEAD) // #nosec G204 -- hardcoded git binary; repoRoot is a Stoke-owned repository path.
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResetMainTo resets the main branch HEAD to the given commit SHA.
// Used for rollback on merge failure. Best-effort — errors are not returned.
// Refuses to reset if the working tree has uncommitted changes to avoid data loss.
func ResetMainTo(ctx context.Context, repoRoot, commitSHA string) {
	// Check for dirty working tree — refuse to destroy uncommitted changes.
	status, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "status", "--porcelain").Output() // #nosec G204 -- hardcoded git binary; repoRoot is a Stoke-owned repository path.
	if err == nil && len(strings.TrimSpace(string(status))) > 0 {
		// Working tree is dirty — don't reset, it would destroy user's changes.
		return
	}
	// commitSHA is produced by Stoke's own MainHeadSHA() or merge pipeline (git-produced hash).
	_ = exec.CommandContext(ctx, "git", "-C", repoRoot, "reset", "--hard", commitSHA).Run() // #nosec G204 -- hardcoded git binary; commitSHA is a git-produced SHA captured by the merge pipeline.
}
