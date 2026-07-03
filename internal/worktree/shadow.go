// shadow.go — per-tool-use shadow-git checkpoints and file-level restore.
//
// ShadowCheckpoint generalizes SnapshotWorkingTree for mid-turn, per-
// tool-use frequency with two load-bearing differences:
//
//  1. It stages into a PRIVATE index (GIT_INDEX_FILE) so the agent's
//     real index is never touched — SnapshotWorkingTree's add -A +
//     trailing read-tree HEAD clobbers anything the agent staged via
//     bash git, which is acceptable at its pre-merge call site but not
//     while the agent is still working.
//  2. Checkpoints are per-seq refs under refs/r1-checkpoints/<name>/
//     so a run accumulates a rewindable series instead of one ref.
//
// The private index persists across checkpoints in the same run (under
// RuntimeDir when set) so git's cached stat info makes checkpoint N
// cost O(changed files), not O(repo).
//
// Linked worktrees share the parent repo's ref store, so these refs
// land repo-wide; Manager.Cleanup deletes refs/r1-checkpoints/<name>/*
// to avoid pinning dangling commits past the worktree's life.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// checkpointRefPrefix is the ref namespace for shadow checkpoints.
const checkpointRefPrefix = "refs/r1-checkpoints/"

// CheckpointRef describes one shadow checkpoint ref. Seq -1 marks the
// pre-restore safety checkpoint RestoreFiles takes before rewinding.
type CheckpointRef struct {
	Seq int
	Ref string
	SHA string
}

// checkpointRefDir returns the ref directory (trailing slash included)
// holding every checkpoint for a worktree name. Single source of the
// naming scheme so cleanup sweeps exactly what checkpointing wrote.
func checkpointRefDir(name string) string {
	base := slug(name)
	if base == "" {
		base = "task"
	}
	return checkpointRefPrefix + base + "/"
}

// CheckpointRefName returns the ref a checkpoint with the given seq is
// stored under for a worktree name. Exposed so callers recording
// checkpoint provenance (transcripts) can stamp the ref without
// re-deriving the naming scheme.
func CheckpointRefName(name string, seq int) string {
	dir := checkpointRefDir(name)
	if seq < 0 {
		return dir + "pre-restore"
	}
	return fmt.Sprintf("%s%04d", dir, seq)
}

// shadowIndexPath returns the private index file used for a handle's
// checkpoints. RuntimeDir is preferred (harness-owned, deleted on
// cleanup); os.TempDir is the fallback for in-place runs.
func shadowIndexPath(handle Handle) string {
	dir := handle.RuntimeDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := slug(handle.Name)
	if name == "" {
		name = "task"
	}
	return filepath.Join(dir, "r1-shadow-index-"+name)
}

// gitBinaryFor defaults the handle's git binary.
func gitBinaryFor(handle Handle) string {
	if handle.GitBinary != "" {
		return handle.GitBinary
	}
	return "git"
}

// ShadowCheckpoint captures the FULL working tree (committed + staged +
// unstaged + untracked; gitignored files excluded) as a dangling commit
// and pins it under refs/r1-checkpoints/<name>/<seq>. HEAD, the branch,
// and the agent's real index are untouched. Returns the commit SHA.
//
// NOT safe for concurrent calls on the same handle (the private index
// is shared); callers must serialize — see the engine's per-dispatch
// checkpoint mutex.
func ShadowCheckpoint(ctx context.Context, handle Handle, seq int) (string, error) {
	gitBin := gitBinaryFor(handle)
	idxPath := shadowIndexPath(handle)
	env := append(os.Environ(), "GIT_INDEX_FILE="+idxPath)

	// Seed the private index once per run. Later checkpoints reuse it so
	// add -A benefits from cached stat info instead of re-hashing the tree.
	if _, statErr := os.Stat(idxPath); statErr != nil {
		seedCmd := exec.CommandContext(ctx, gitBin, "read-tree", gitHEAD) // #nosec G204 -- git binary with literal subcommand arguments, no external input.
		seedCmd.Dir = handle.Path
		seedCmd.Env = env
		if out, err := seedCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("shadow checkpoint: seed index: %w: %s", err, out)
		}
	}

	addCmd := exec.CommandContext(ctx, gitBin, "add", "-A") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	addCmd.Dir = handle.Path
	addCmd.Env = env
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("shadow checkpoint: git add -A: %w: %s", err, out)
	}

	writeTreeCmd := exec.CommandContext(ctx, gitBin, "write-tree") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	writeTreeCmd.Dir = handle.Path
	writeTreeCmd.Env = env
	treeOut, err := writeTreeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("shadow checkpoint: git write-tree: %w", err)
	}
	treeSHA := strings.TrimSpace(string(treeOut))

	headCmd := exec.CommandContext(ctx, gitBin, "rev-parse", gitHEAD) // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	headCmd.Dir = handle.Path
	headOut, err := headCmd.Output()
	if err != nil {
		return "", fmt.Errorf("shadow checkpoint: git rev-parse HEAD: %w", err)
	}
	headSHA := strings.TrimSpace(string(headOut))

	// Fixed identity env so checkpoints work in worktrees without
	// user.name/user.email config (dangling snapshots — authorship is
	// meaningless, determinism is not).
	commitEnv := append(env,
		"GIT_AUTHOR_NAME=r1", "GIT_AUTHOR_EMAIL=r1@localhost",
		"GIT_COMMITTER_NAME=r1", "GIT_COMMITTER_EMAIL=r1@localhost",
	)
	commitTreeCmd := exec.CommandContext(ctx, gitBin, // #nosec G204 -- git binary; treeSHA/headSHA are git-produced object hashes from preceding commands.
		"commit-tree", treeSHA, "-p", headSHA, "-m", fmt.Sprintf("r1: shadow checkpoint %d", seq))
	commitTreeCmd.Dir = handle.Path
	commitTreeCmd.Env = commitEnv
	snapOut, err := commitTreeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("shadow checkpoint: git commit-tree: %w", err)
	}
	snapSHA := strings.TrimSpace(string(snapOut))

	// Pin against GC. Best-effort: the SHA stays valid ~14 days even if
	// the ref write fails, and the caller records the SHA directly.
	refName := CheckpointRefName(handle.Name, seq)
	refCmd := exec.CommandContext(ctx, gitBin, "update-ref", refName, snapSHA) // #nosec G204 -- git binary; refName has fixed r1 prefix + internal handle name, snapSHA is git-produced.
	refCmd.Dir = handle.Path
	_, _ = refCmd.CombinedOutput()

	return snapSHA, nil
}

// RestoreFiles rewinds the working tree to a shadow checkpoint's state:
// tracked files are reset to the checkpoint tree, files created after
// the checkpoint are removed (gitignored files survive — the checkpoint
// never captured them, and deleting a symlinked node_modules would be
// destructive), and files deleted after the checkpoint come back.
// HEAD and the branch never move; the index ends reset to HEAD so
// status / diff-vs-BaseCommit behave exactly as before the restore.
//
// A pre-restore safety checkpoint (seq -1) is attempted first so the
// restore itself is undoable; its failure is non-fatal.
func RestoreFiles(ctx context.Context, handle Handle, sha string) error {
	gitBin := gitBinaryFor(handle)

	// Fail closed before touching anything: the checkpoint commit must exist.
	catCmd := exec.CommandContext(ctx, gitBin, "cat-file", "-e", sha+"^{commit}") // #nosec G204 -- git binary; sha is a git-produced checkpoint SHA recorded by ShadowCheckpoint.
	catCmd.Dir = handle.Path
	if err := catCmd.Run(); err != nil {
		return fmt.Errorf("restore: checkpoint %s not found: %w", shortSHA(sha), err)
	}

	// Safety net (best-effort): snapshot the current state so a restore
	// can itself be rewound.
	_, _ = ShadowCheckpoint(ctx, handle, -1)

	// Make working tree + index match the checkpoint tree. -u updates
	// (and deletes) working-tree files; --reset overwrites index state.
	readTreeCmd := exec.CommandContext(ctx, gitBin, "read-tree", "-u", "--reset", sha) // #nosec G204 -- git binary; sha is a git-produced checkpoint SHA.
	readTreeCmd.Dir = handle.Path
	if out, err := readTreeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore: git read-tree -u --reset: %w: %s", err, out)
	}

	// Drop untracked files created after the checkpoint. The checkpoint
	// staged every then-untracked file (add -A), so anything untracked
	// now is post-checkpoint residue. No -x: ignored files survive.
	cleanCmd := exec.CommandContext(ctx, gitBin, "clean", "-fd") // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	cleanCmd.Dir = handle.Path
	if out, err := cleanCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore: git clean -fd: %w: %s", err, out)
	}

	// Reset the index back to HEAD so subsequent status / diff calls see
	// the restored files as ordinary working-tree changes (same
	// convention SnapshotWorkingTree leaves behind).
	resetIdxCmd := exec.CommandContext(ctx, gitBin, "read-tree", gitHEAD) // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	resetIdxCmd.Dir = handle.Path
	if out, err := resetIdxCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore: git read-tree HEAD: %w: %s", err, out)
	}
	return nil
}

// ListCheckpoints enumerates the shadow checkpoints recorded for a
// handle, sorted by Seq ascending (pre-restore safety checkpoints sort
// first with Seq -1).
func ListCheckpoints(ctx context.Context, handle Handle) ([]CheckpointRef, error) {
	gitBin := gitBinaryFor(handle)
	prefix := checkpointRefDir(handle.Name)
	cmd := exec.CommandContext(ctx, gitBin, "for-each-ref", "--format=%(refname) %(objectname)", prefix) // #nosec G204 -- git binary; prefix has fixed r1 prefix + internal handle name.
	cmd.Dir = handle.Path
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	var refs []CheckpointRef
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref, sha := fields[0], fields[1]
		seqStr := ref[strings.LastIndex(ref, "/")+1:]
		seq := -1
		if n, convErr := strconv.Atoi(seqStr); convErr == nil {
			seq = n
		}
		refs = append(refs, CheckpointRef{Seq: seq, Ref: ref, SHA: sha})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Seq < refs[j].Seq })
	return refs, nil
}

// shortSHA trims a SHA for error messages.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// HeadSHA returns the worktree branch's current HEAD commit SHA, or ""
// on error (non-fatal). Callers capture the pre-attempt branch tip so a
// rewind can undo intermediate agent commits, not just working-tree edits.
func HeadSHA(ctx context.Context, handle Handle) string {
	cmd := exec.CommandContext(ctx, gitBinaryFor(handle), "rev-parse", gitHEAD) // #nosec G204 -- git binary with literal subcommand arguments, no external input.
	cmd.Dir = handle.Path
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResetBranchSoft moves the worktree's branch pointer to sha WITHOUT
// touching the working tree or index (git reset --soft). RewindOnRetry
// uses it to drop intermediate commits an agent made during a failed
// attempt; a following RestoreFiles then normalizes the working tree and
// reindexes to the restored HEAD. Resetting to the current HEAD is a
// harmless no-op. sha is a git-produced SHA captured by HeadSHA.
func ResetBranchSoft(ctx context.Context, handle Handle, sha string) error {
	cmd := exec.CommandContext(ctx, gitBinaryFor(handle), "reset", "--soft", sha) // #nosec G204 -- git binary; sha is a git-produced HEAD SHA captured by HeadSHA.
	cmd.Dir = handle.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reset --soft %s: %w: %s", shortSHA(sha), err, out)
	}
	return nil
}
