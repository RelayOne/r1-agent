package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// fixOrchestrator implements Level 2 concurrent fix-pipeline.
//
// The "big worker" — the Step-3 CC build call — keeps running.
// A commit watcher polls git HEAD every 30 s and reviews each
// new commit. When the reviewer flags issues, orchestrator:
//
//  1. Creates a git worktree off main at the flagged commit
//     (branch: fix-<id>).
//  2. Launches a fix-worker CC call in that worktree with the
//     reviewer feedback.
//  3. When the fix worker exits, re-reviews the fix-branch diff
//     against main.
//  4. If the reviewer signs off → merges fix-<id> back into main
//     (fast-forward if possible, else --no-ff). If the merge
//     conflicts, logs + abandons the branch. Worktree is removed.
//  5. If the reviewer still finds issues → dispatches another
//     fix-worker in the same worktree up to maxFixAttempts.
//  6. After maxFixAttempts, gives up and removes the worktree
//     without merging.
//
// This is the "smart merge trigger": merges happen on reviewer
// approval, not on a 60-second timer. Fix branches that never
// achieve approval are never merged into main — the big
// worker's branch stays clean of unreviewed fixes.

type fixOrchestrator struct {
	mainRepo     string // absolute path of the big worker's workdir
	worktreesDir string // parent dir for spawned worktrees
	claudeBin    string
	reviewer     string // codex / cc-opus / cc-sonnet
	maxAttempts  int    // per fix branch

	mu          sync.Mutex
	nextID      int
	activeFixes map[int]*fixAttempt
	merged      int
	abandoned   int

	done chan struct{}
}

type fixAttempt struct {
	id           int
	branch       string
	worktreePath string
	sourceCommit string // the commit on main that triggered this fix
	attempts     int
	feedback     string
	status       string // "running", "merged", "abandoned"
}

func newFixOrchestrator(mainRepo, claudeBin, reviewer string) *fixOrchestrator {
	wtDir := mainRepo + ".fixes"
	_ = os.MkdirAll(wtDir, 0o755)
	return &fixOrchestrator{
		mainRepo:     mainRepo,
		worktreesDir: wtDir,
		claudeBin:    claudeBin,
		reviewer:     reviewer,
		maxAttempts:  3,
		activeFixes:  map[int]*fixAttempt{},
		done:         make(chan struct{}),
	}
}

// dispatch starts a concurrent fix attempt for a flagged commit.
// Returns the attempt id. Runs to completion in its own
// goroutine; callers don't wait.
func (o *fixOrchestrator) dispatch(sourceCommit, feedback string) int {
	o.mu.Lock()
	o.nextID++
	id := o.nextID
	attempt := &fixAttempt{
		id:           id,
		branch:       fmt.Sprintf("fix-%d", id),
		worktreePath: filepath.Join(o.worktreesDir, fmt.Sprintf("wt-%d", id)),
		sourceCommit: sourceCommit,
		feedback:     feedback,
		status:       "running",
	}
	o.activeFixes[id] = attempt
	o.mu.Unlock()

	go o.run(attempt)
	return id
}

// run is the per-attempt goroutine: create worktree → CC fix →
// review → merge-or-retry → cleanup.
func (o *fixOrchestrator) run(a *fixAttempt) {
	defer func() {
		o.mu.Lock()
		delete(o.activeFixes, a.id)
		o.mu.Unlock()
	}()

	// 1. Create the worktree at the source commit.
	fmt.Printf("  🛠️  fix-%d: creating worktree at %s (from %s)\n",
		a.id, a.worktreePath, short(a.sourceCommit))
	if out := shellCmd(o.mainRepo,
		fmt.Sprintf("git worktree add -b %s %s %s 2>&1",
			a.branch, a.worktreePath, a.sourceCommit)); strings.Contains(out, "fatal") {
		fmt.Printf("  ⚠️  fix-%d: worktree create failed: %s\n", a.id, trimShort(out))
		return
	}

	currentFeedback := a.feedback

	for a.attempts < o.maxAttempts {
		a.attempts++
		fmt.Printf("  🛠️  fix-%d: attempt %d/%d — CC fix worker starting\n",
			a.id, a.attempts, o.maxAttempts)

		claudeCall(o.claudeBin, a.worktreePath, fmt.Sprintf(
			"You are fix worker %d. You are on branch `%s` of the repo, "+
				"isolated in your own git worktree. The main build branch "+
				"is still being built by a parallel worker — you will NOT "+
				"see its changes until your fixes are merged back. "+
				"Your ONLY job: fix the issues flagged below. Do not add "+
				"features. Make each fix minimal, commit it, and run the "+
				"build after each commit. When you believe all issues are "+
				"resolved, stop. Another reviewer will judge your work "+
				"before anything is merged back to main.\n\n"+
				"REVIEWER FEEDBACK:\n%s",
			a.id, a.branch, currentFeedback))

		// 2. Re-review: diff the fix branch vs main.
		diff := shellCmd(a.worktreePath,
			"git diff main..HEAD --stat 2>/dev/null")
		log := shellCmd(a.worktreePath,
			"git log main..HEAD --oneline 2>/dev/null")
		if diff == "" && log == "" {
			fmt.Printf("  ⚠️  fix-%d: CC made no commits on attempt %d — abandoning\n",
				a.id, a.attempts)
			break
		}

		fmt.Printf("  🔍 fix-%d: reviewing fix commits on attempt %d\n", a.id, a.attempts)
		review := reviewCall(a.worktreePath,
			"Review the fix commits listed below against the ORIGINAL feedback. "+
				"Did the worker resolve every flagged issue? Check: "+
				"does the build still pass, are there new regressions, "+
				"are the commits minimal and focused. Respond with "+
				"'NO ISSUES' or 'LGTM' only if you are fully satisfied.\n\n"+
				"ORIGINAL FEEDBACK:\n"+a.feedback+
				"\n\nFIX COMMITS:\n"+log+
				"\n\nFIX DIFF STAT:\n"+diff)

		if approvedReview(review) {
			fmt.Printf("  ✅ fix-%d: reviewer approved — merging to main\n", a.id)
			if o.mergeIntoMain(a) {
				a.status = "merged"
				o.mu.Lock()
				o.merged++
				o.mu.Unlock()
			} else {
				a.status = "abandoned-merge-conflict"
				o.mu.Lock()
				o.abandoned++
				o.mu.Unlock()
			}
			break
		}
		fmt.Printf("  ✗ fix-%d: reviewer still finding issues — next attempt\n", a.id)
		currentFeedback = review
	}
	if a.status == "running" {
		a.status = "abandoned-max-attempts"
		o.mu.Lock()
		o.abandoned++
		o.mu.Unlock()
	}

	// 3. Cleanup worktree. Keep the branch if merged (history);
	// remove branch if abandoned so it doesn't clutter.
	o.cleanupWorktree(a)
}

// mergeIntoMain performs the reviewer-approved merge back to
// main. Uses --ff-only if possible (fast-forward when main
// hasn't advanced); else --no-ff to create a merge commit. On
// conflict, aborts + returns false.
func (o *fixOrchestrator) mergeIntoMain(a *fixAttempt) bool {
	// First try fast-forward.
	out := shellCmd(o.mainRepo,
		fmt.Sprintf("git merge --ff-only %s 2>&1", a.branch))
	if !strings.Contains(out, "fatal") && !strings.Contains(out, "Aborting") {
		fmt.Printf("  🔀 fix-%d: fast-forward merge to main\n", a.id)
		return true
	}
	// Fall back to non-ff merge (generates merge commit).
	out = shellCmd(o.mainRepo,
		fmt.Sprintf("git merge --no-ff -m 'merge fix-%d (reviewer-approved)' %s 2>&1",
			a.id, a.branch))
	if strings.Contains(out, "CONFLICT") || strings.Contains(out, "Automatic merge failed") {
		fmt.Printf("  💥 fix-%d: merge conflict — aborting, not merging:\n%s\n",
			a.id, trimShort(out))
		_ = shellCmd(o.mainRepo, "git merge --abort 2>&1")
		return false
	}
	if strings.Contains(out, "fatal") {
		fmt.Printf("  💥 fix-%d: merge failed: %s\n", a.id, trimShort(out))
		return false
	}
	fmt.Printf("  🔀 fix-%d: merged to main via merge-commit\n", a.id)
	return true
}

func (o *fixOrchestrator) cleanupWorktree(a *fixAttempt) {
	_ = shellCmd(o.mainRepo,
		fmt.Sprintf("git worktree remove --force %s 2>&1", a.worktreePath))
	if a.status != "merged" {
		_ = shellCmd(o.mainRepo,
			fmt.Sprintf("git branch -D %s 2>&1", a.branch))
	}
	_ = os.RemoveAll(a.worktreePath)
}

// waitIdle blocks until no fix attempts are in flight. Used to
// drain the pipeline before returning from the build phase.
func (o *fixOrchestrator) waitIdle(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		n := len(o.activeFixes)
		o.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// stats returns current counts for reporting.
func (o *fixOrchestrator) stats() (active, merged, abandoned int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.activeFixes), o.merged, o.abandoned
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func trimShort(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
