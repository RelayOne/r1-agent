package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// simpleLoopStateVersion is bumped whenever the on-disk schema for
// simpleLoopState changes in a way that's not backwards-compatible.
// LoadSimpleLoopState treats version mismatch as "no state" so old
// state files don't crash new binaries.
const simpleLoopStateVersion = 1

// simpleLoopStateFile is the canonical relative path inside the
// repository's .stoke/ directory. Kept as a function so callers don't
// duplicate the join.
func simpleLoopStateFile(repoRoot string) string {
	return filepath.Join(repoRoot, ".stoke", "simple-loop-state.json")
}

// simpleLoopState is the minimum persistent state needed to resume
// `r1 simple-loop` mid-run after a crash or account rotation. Only
// things that would be silently corrupted by a naive "just start over"
// go here; everything that's re-derivable (plan, review, last commit)
// is recomputed by the round it belongs to.
//
// Policy (H-25):
//   - Saved at the TOP of every outer round — before Step 1 plan — so
//     a resume always lands at the start of a round, never mid-step.
//     Mid-step resume would require persisting the H-6 counter across
//     builder/audit/fix sub-phases, an order of magnitude more state
//     for a marginal win.
//   - On SIMPLE LOOP COMPLETE the file is deleted so a subsequent
//     `--resume` with a new SOW doesn't falsely match.
//   - On H-6 / regression abort the file is LEFT IN PLACE so the
//     operator can inspect it — but resume will refuse to continue on
//     top of an aborted state (the operator must decide: rerun fresh,
//     or extend the SOW and resume). This avoids the failure mode
//     where the operator relaunches blindly and gets the same abort.
type simpleLoopState struct {
	Version      int       `json:"version"`
	SOWHash      string    `json:"sow_hash"`        // sha256(prose bytes) — prevents resume with a different SOW
	CurrentRound int       `json:"current_round"`   // round number to START with on resume (1-indexed)
	MaxRounds    int       `json:"max_rounds"`      // may have been auto-extended from the CLI flag
	Reviewer     string    `json:"reviewer"`        // e.g. "codex"; resume refuses if flag differs
	FixMode      string    `json:"fix_mode"`        // "sequential" | "parallel" | "concurrent"
	CurrentProse string    `json:"current_prose"`   // the (possibly gap-extracted) prose for the round to resume
	Step8Cycles  int       `json:"step8_cycles"`    // preserved H-6 regression counter
	LastGaps     []string  `json:"last_gaps"`       // preserved gap list for the H-6 tracker
	Aborted      bool      `json:"aborted"`         // set true on regression-cap exit; resume refuses
	RepoHead     string    `json:"repo_head"`       // git HEAD sha at save time — codex P2-5; resume verifies fast-forward
	SavedAt      time.Time `json:"saved_at"`        // wall-clock for operator triage
}

// currentRepoHead returns the git HEAD SHA of repoRoot, or empty on
// any error (untracked tree, missing git binary, etc.). Empty is
// treated as "not recorded" — resume compat will skip the HEAD check
// when neither side is populated, preserving backwards compat with
// state files written before RepoHead existed.
func currentRepoHead(repoRoot string) string {
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD") // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// repoTreeIsDirty returns true when repoRoot has staged, unstaged, or
// untracked changes — i.e. a build that half-wrote files before the
// crash. Codex P1-1: resume must refuse on a dirty tree, otherwise the
// saved CurrentProse replays on top of partial edits that HEAD doesn't
// reflect. Returns (false, err) on git failure so the caller can treat
// "couldn't verify" as refuse-to-resume.
func repoTreeIsDirty(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain=v1") // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// repoHeadIsAncestor reports whether savedHead is an ancestor of (or
// equal to) currentHead in repoRoot. Returns (false, err) on any git
// error so the caller can surface the reason. Used by validateResumeCompat
// to ensure resume only proceeds when the tree has moved strictly
// FORWARD from the save point — not diverged onto an unrelated
// branch or been reset/rewritten.
func repoHeadIsAncestor(repoRoot, savedHead, currentHead string) (bool, error) {
	if savedHead == "" || currentHead == "" {
		return true, nil
	}
	if savedHead == currentHead {
		return true, nil
	}
	cmd := exec.Command("git", "-C", repoRoot, "merge-base", "--is-ancestor", savedHead, currentHead) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			// Git returns exit 1 when the first commit is NOT an
			// ancestor. Not an error in the usual sense.
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// hashProse returns a short SHA-256 hex of the SOW bytes — used to
// detect "don't resume a different SOW." 16 hex chars is plenty to
// distinguish real SOWs; collision isn't a safety property here, only
// an accidental-mismatch guard.
func hashProse(prose string) string {
	sum := sha256.Sum256([]byte(prose))
	return hex.EncodeToString(sum[:])[:16]
}

// SaveSimpleLoopState atomically writes state to repoRoot/.stoke/.
// Parent directory is created when missing. Write-then-rename so a
// crash mid-write doesn't leave a truncated file that kills the next
// resume attempt.
func SaveSimpleLoopState(repoRoot string, state *simpleLoopState) error {
	if repoRoot == "" || state == nil {
		return errors.New("SaveSimpleLoopState: empty repo or nil state")
	}
	state.Version = simpleLoopStateVersion
	state.SavedAt = time.Now().UTC()
	dir := filepath.Join(repoRoot, ".stoke")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir .stoke: %w", err)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	target := simpleLoopStateFile(repoRoot)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil { // #nosec G306 -- CLI output artefact; user-readable.
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// LoadSimpleLoopState returns the persisted state, or (nil, nil) when
// no state file exists. A malformed file (bad JSON, wrong version) is
// treated as "no state" — we log via the returned err for the caller
// to surface. Callers decide whether to resume or start fresh.
func LoadSimpleLoopState(repoRoot string) (*simpleLoopState, error) {
	if repoRoot == "" {
		return nil, errors.New("LoadSimpleLoopState: empty repo")
	}
	body, err := os.ReadFile(simpleLoopStateFile(repoRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s simpleLoopState
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if s.Version != simpleLoopStateVersion {
		return nil, fmt.Errorf("state version %d does not match current %d; refusing to resume",
			s.Version, simpleLoopStateVersion)
	}
	return &s, nil
}

// ClearSimpleLoopState removes the state file. Silent when already
// absent. Called on clean completion and on --fresh.
func ClearSimpleLoopState(repoRoot string) error {
	if repoRoot == "" {
		return errors.New("ClearSimpleLoopState: empty repo")
	}
	err := os.Remove(simpleLoopStateFile(repoRoot))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// validateResumeCompat decides whether a loaded state is safe to
// resume under the given fresh invocation parameters. Returns a
// human-readable reason when NOT safe so the caller can print and
// degrade to a fresh run (or abort, depending on operator preference).
// Never mutates the state.
//
// repoRoot is used only for the codex-P2-5 HEAD ancestor check; pass
// "" to skip git verification (some unit tests).
func validateResumeCompat(state *simpleLoopState, repoRoot, proseHash, reviewer, fixMode string) (ok bool, reason string) {
	if state == nil {
		return false, "no prior state"
	}
	if state.Aborted {
		return false, "prior run ended via regression-cap abort; relaunch with --fresh or extend the SOW"
	}
	if state.SOWHash != proseHash {
		return false, fmt.Sprintf("SOW hash changed (%s -> %s); refusing to resume on a different spec",
			state.SOWHash, proseHash)
	}
	if state.Reviewer != reviewer {
		return false, fmt.Sprintf("reviewer changed (%s -> %s); resume across reviewer swaps may silently change gating",
			state.Reviewer, reviewer)
	}
	if state.FixMode != fixMode {
		return false, fmt.Sprintf("fix-mode changed (%s -> %s); resume across modes would corrupt the H-6 counter",
			state.FixMode, fixMode)
	}
	if state.CurrentRound < 1 {
		return false, fmt.Sprintf("state has invalid round %d", state.CurrentRound)
	}
	// Codex P1-1: reject resume when the working tree is dirty. A
	// mid-round crash leaves HEAD unchanged but the working tree with
	// partial builder edits; resuming would replay the saved prose on
	// top of those half-written files and likely corrupt the run. The
	// operator must either commit the partials (advancing HEAD — which
	// the fast-forward check below then accepts) or `git stash`/reset
	// before resuming. --fresh always bypasses this path.
	if repoRoot != "" {
		dirty, dirtyErr := repoTreeIsDirty(repoRoot)
		if dirtyErr != nil {
			return false, fmt.Sprintf("could not read git working-tree state (%v); refusing to resume without verification", dirtyErr)
		}
		if dirty {
			return false, "working tree has uncommitted changes since the last save; commit/stash/reset then retry --resume, or relaunch --fresh"
		}
	}
	// Codex P2-5: reject resume when the repo has diverged from the
	// save point. Fast-forward progress (worker committed between
	// saves) is allowed; cherry-picks, branch switches, or history
	// rewrites that leave saved_head NOT an ancestor of current are
	// refused so a stale CurrentProse / Step8Cycles can't be re-
	// applied to an unrelated tree.
	if repoRoot != "" && state.RepoHead != "" {
		curHead := currentRepoHead(repoRoot)
		if curHead == "" {
			return false, "could not read current git HEAD; refusing to resume without verification"
		}
		ancestor, err := repoHeadIsAncestor(repoRoot, state.RepoHead, curHead)
		if err != nil {
			return false, fmt.Sprintf("git ancestry check failed (%v); refusing to resume", err)
		}
		if !ancestor {
			return false, fmt.Sprintf("repo HEAD diverged from save-point %s (current %s); refusing to resume on an unrelated tree",
				state.RepoHead, curHead)
		}
	}
	return true, ""
}
