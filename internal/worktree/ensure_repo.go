package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureRepo makes sure path is a git repository with at least one commit.
// If path doesn't exist, it's created. If path exists but isn't a git repo
// (no .git directory detected via git rev-parse), `git init` is run. If the
// repo has zero commits, an empty initial commit is created so HEAD is valid
// — this is what `git worktree add` requires.
//
// Returns (created bool, err): created is true when any initialization was
// performed, so callers can log it.
//
// Motivation: Stoke's worktree.Manager fails on `git rev-parse HEAD` when
// pointed at a fresh directory. Users running `stoke sow --file project.md`
// on a new target shouldn't have to run `git init && git commit --allow-empty`
// by hand — the orchestrator should do the right thing.
func EnsureRepo(ctx context.Context, path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("EnsureRepo: empty path")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return false, fmt.Errorf("create repo dir: %w", err)
	}

	// Is this already a git repo? `git rev-parse --is-inside-work-tree`
	// returns "true" if we're inside a work tree, errors otherwise.
	check := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	check.Dir = path
	if out, err := check.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		// Already a work tree. Make sure there's at least one commit so
		// `rev-parse HEAD` works for the worktree manager.
		if err := ensureInitialCommit(ctx, path); err != nil {
			return false, err
		}
		return false, nil
	}

	// Not a git repo — run `git init`. Use the default branch name
	// configured on the system (git init respects init.defaultBranch).
	initCmd := exec.CommandContext(ctx, "git", "init")
	initCmd.Dir = path
	if out, err := initCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git init: %w: %s", err, out)
	}

	// Set a local identity so the initial commit works on systems where
	// user.name / user.email aren't configured globally. Only set the
	// *local* config so we don't touch the user's global git config.
	_ = runGit(ctx, path, "config", "user.name", "Stoke Bot")
	_ = runGit(ctx, path, "config", "user.email", "stoke@local.invalid")

	if err := ensureInitialCommit(ctx, path); err != nil {
		return true, err
	}
	return true, nil
}

// ensureInitialCommit creates an empty initial commit if the repo has no
// commits yet. Safe to call on a repo that already has commits.
func ensureInitialCommit(ctx context.Context, path string) error {
	// Check if HEAD resolves. If yes, there's at least one commit.
	hc := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD")
	hc.Dir = path
	if err := hc.Run(); err == nil {
		return nil
	}

	// Create a .gitignore so the first commit isn't completely empty —
	// empty commits require --allow-empty and some tooling is picky about
	// them. A stub .gitignore is harmless and gives the user something to
	// customize.
	gi := filepath.Join(path, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		stub := "# Stoke-generated .gitignore\n.stoke/\nnode_modules/\nvendor/\ntarget/\n.venv/\n__pycache__/\n"
		if err := os.WriteFile(gi, []byte(stub), 0o644); err != nil { // #nosec G306 -- repo metadata; 0644 is standard.
			return fmt.Errorf("seed .gitignore: %w", err)
		}
	}

	if err := runGit(ctx, path, "add", ".gitignore"); err != nil {
		return err
	}
	// Ensure a local identity exists (idempotent — repeat for safety).
	_ = runGit(ctx, path, "config", "user.name", "Stoke Bot")
	_ = runGit(ctx, path, "config", "user.email", "stoke@local.invalid")
	if err := runGit(ctx, path, "commit", "--no-gpg-sign", "-m", "chore: initial commit (stoke)"); err != nil {
		// Fall back to --allow-empty if the add somehow failed
		if err2 := runGit(ctx, path, "commit", "--allow-empty", "--no-gpg-sign", "-m", "chore: initial commit (stoke)"); err2 != nil {
			return fmt.Errorf("initial commit: %w (also tried --allow-empty: %w)", err, err2)
		}
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- git binary with Stoke-generated args (refs, paths, SHAs) not external input.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}
