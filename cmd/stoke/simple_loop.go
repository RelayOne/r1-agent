package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// step8RegressionCap bounds how many consecutive outer rounds may
// end with "Step 8 audit says gaps remain AND the compliance sweep
// also found stubs/missing" before simple-loop bails out. This is a
// token-burn circuit breaker for the failure mode observed in
// D-opus-full (H-6, 2026-04-17) where Step 8 kept kicking the worker
// back to the builder for round after round without ever achieving
// a clean compliance pass.
//
// Counter semantics:
//   - incremented when an outer round completes with compliance-NOT-
//     clean (i.e. gaps truly remain, either a real audit-says-gaps
//     or a CC-claims-done-but-compliance-overrode-to-gaps);
//   - reset to zero the first time an outer round's compliance sweep
//     comes back clean (meaning we actually closed the gaps);
//   - when the counter reaches step8RegressionCap we print the
//     regression banner and terminate the simple-loop without
//     starting another builder call.
//
// Default is 2: a single failed closure is fine (the builder wasn't
// given a useful gap list the first time); two consecutive failures
// is evidence the audit/compliance feedback is not converging and
// further rounds will just burn tokens.
const step8RegressionCap = 2

// step8RegressionTracker counts consecutive outer rounds that ended
// with compliance NOT clean (i.e. Step 8 rejected the round and is
// about to kick us back to another builder call). See
// step8RegressionCap for the policy. Safe for single-goroutine use;
// the simple-loop is sequential per invocation.
type step8RegressionTracker struct {
	cap      int
	cycles   int
	lastGaps []string
}

// Observe reports one outer round's compliance outcome. `gaps` is
// the human-readable list of MISSING/STUB items (empty when
// compliance is clean). Returns true when the cap has been reached
// and the caller MUST abort the loop.
//
// Kept for backward compatibility with existing tests/callers; new
// call sites that need to distinguish "audit didn't actually run"
// from "audit ran and found gaps" should prefer ObserveAuditResult.
func (t *step8RegressionTracker) Observe(complianceClean bool, gaps []string) bool {
	return t.ObserveAuditResult(true, complianceClean, gaps)
}

// ObserveAuditResult reports one outer round's compliance outcome
// WITH an explicit signal for whether the upstream audit call
// actually produced a usable verdict. Three cases:
//
//   - auditRan=true,  complianceClean=true  → reset counter (no regression)
//   - auditRan=true,  complianceClean=false → increment counter (real regression)
//   - auditRan=false                        → skip increment, do NOT reset,
//                                             return false (don't terminate
//                                             on upstream infrastructure
//                                             failures like Claude rate-limit
//                                             or network blips)
//
// gaps is used only when auditRan=true && !complianceClean.
//
// Rationale (H-6 fix, 2026-04-17): in the hardened cohort, both
// H1-sonnet and H2-opus-full were killed by this guard at ~2h even
// though no REAL regression was happening — the claude CLI was
// rate-limited and every audit call returned an empty 55-char body,
// which the old Observe() counted as "not clean". The fix teaches
// the tracker the difference between "audit said gaps remain" and
// "audit couldn't run at all".
func (t *step8RegressionTracker) ObserveAuditResult(auditRan bool, complianceClean bool, gaps []string) bool {
	if !auditRan {
		// Upstream failure — leave the counter where it is. Don't
		// reset either (we don't KNOW the state is clean), and don't
		// increment (we don't KNOW it regressed).
		return false
	}
	if complianceClean {
		t.cycles = 0
		t.lastGaps = nil
		return false
	}
	t.cycles++
	t.lastGaps = gaps
	return t.cycles >= t.cap
}

// Cycles returns the current consecutive-failure count. Exposed for
// the final run summary and for tests.
func (t *step8RegressionTracker) Cycles() int { return t.cycles }

// LastGaps returns the gap list from the most recent failing round.
// Exposed for the final run summary and for the abort-banner log.
func (t *step8RegressionTracker) LastGaps() []string { return t.lastGaps }

// Load restores prior tracker state. Used by H-25 simple-loop resume
// so the H-6 regression counter survives a crash — otherwise a
// resume with cycles=1 would reset to 0 and give the builder an
// unearned extra chance, defeating the regression cap.
func (t *step8RegressionTracker) Load(cycles int, lastGaps []string) {
	if cycles < 0 {
		cycles = 0
	}
	t.cycles = cycles
	if lastGaps != nil {
		t.lastGaps = append([]string{}, lastGaps...)
	}
}

// gapCountProgressTracker implements H-29: progress-based termination.
//
// Problem observed on 2026-04-18 cohort: the sow harness and simple-
// loop kept iterating rounds even when the worker couldn't narrow the
// gap list. Round N: 36 gaps. Round N+1: 42 gaps. Round N+2: 54 gaps.
// Each round burns tokens; progress runs backward. H-6 catches the
// terminal case (2 consecutive non-clean), but often the loop limps
// along with flat gap count + no clean exit — burning hours without
// convergence.
//
// H-29 adds a lower bar: if the gap count doesn't DECREASE by the
// minimum delta for `window` consecutive rounds, declare "plateau"
// and exit with PARTIAL-SUCCESS status. Commits produced so far are
// preserved; the operator gets a clean report showing what landed vs
// what's still missing.
//
// Policy tuning:
//   - window=3: three plateau rounds before exit. Gives room for
//     one round to go sideways (worker got confused) before killing.
//   - minDelta=1: any gap reduction counts as progress. More nuanced
//     tunings could require percentage reduction, but 1 is the easiest
//     defensible bar and matches how humans think about "is this
//     moving forward."
type gapCountProgressTracker struct {
	window   int
	minDelta int
	history  []int // gap count observed at the end of each round
}

// Observe records the gap count at end of a round. Returns true when
// the tracker has seen `window` consecutive rounds without a minDelta
// drop — the caller MUST exit with PARTIAL-SUCCESS in that case.
// Clean rounds (gapCount == 0) reset the history since "done" beats
// "plateau" trivially.
func (g *gapCountProgressTracker) Observe(gapCount int) bool {
	if gapCount == 0 {
		g.history = nil
		return false
	}
	g.history = append(g.history, gapCount)
	if len(g.history) <= g.window {
		return false
	}
	// Check the last `window+1` entries: did any drop by at least
	// minDelta vs the one before it? If yes, we're progressing.
	tail := g.history[len(g.history)-g.window-1:]
	for i := 1; i < len(tail); i++ {
		if tail[i-1]-tail[i] >= g.minDelta {
			return false
		}
	}
	return true
}

// Best returns the lowest gap count ever observed — used in the
// PARTIAL-SUCCESS banner to show the high-water mark of progress.
func (g *gapCountProgressTracker) Best() int {
	if len(g.history) == 0 {
		return 0
	}
	best := g.history[0]
	for _, n := range g.history[1:] {
		if n < best {
			best = n
		}
	}
	return best
}

// History returns the full per-round gap count sequence. Used in the
// PARTIAL-SUCCESS banner + tests.
func (g *gapCountProgressTracker) History() []int {
	return append([]int{}, g.history...)
}

// ccPipeSilenceThreshold caps how long Claude Code's stdout pipe
// is allowed to sit idle before we assume the subprocess is wedged
// and SIGKILL its process group. This is STRICTER than the existing
// buffer-growth watchdog because it tracks activity on the pipe
// itself rather than the accumulated buffer length, which means the
// outer driver can't defeat it by touching unrelated log files.
// See H-4 (2026-04-17) — MS-full was stuck 17+ min with the old
// mtime-based watchdog because outer-loop heartbeats kept the file
// fresh even though the child process had gone silent.
const ccPipeSilenceThreshold = 5 * time.Minute

// argMaxStdinThreshold is the prompt-length cutoff above which we
// switch from passing the prompt on the command line to piping it
// through stdin. Linux's ARG_MAX is typically ~128 KiB (conservative
// kernels as low as 32 KiB); 64 KiB leaves a generous safety margin
// while still letting the common case use the faster arg path. See
// H-21 (2026-04-17) — R-deep scan-repair Phase 3 crashed with
// "argument list too long" when 31K+ security findings were
// concatenated into one codex prompt.
const argMaxStdinThreshold = 64 * 1024

// pipeWatcher wraps an io.Writer and records the timestamp of the
// most recent non-empty Write. Call SilenceDuration() to ask "how
// long has it been since bytes last flowed through?". Safe for
// concurrent Write() + SilenceDuration() — the underlying writer
// is assumed to be concurrent-safe with itself (bytes.Buffer is
// NOT, but a single goroutine writes to it via cmd.Stdout so that's
// fine here; the mutex guards only the timestamp).
type pipeWatcher struct {
	mu    sync.Mutex
	last  time.Time
	inner io.Writer
}

func newPipeWatcher(w io.Writer) *pipeWatcher {
	return &pipeWatcher{inner: w, last: time.Now()}
}

func (p *pipeWatcher) Write(b []byte) (int, error) {
	n, err := p.inner.Write(b)
	if n > 0 {
		p.mu.Lock()
		p.last = time.Now()
		p.mu.Unlock()
	}
	return n, err
}

// SilenceDuration returns how long it has been since the last
// non-empty Write. Monotonic-clock based.
func (p *pipeWatcher) SilenceDuration() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Since(p.last)
}

// killChildProcessGroup sends SIGTERM to the process group, waits
// gracePeriod, then SIGKILLs any survivors. Mirrors the pattern in
// internal/engine/claude.go killProcessGroup but with a tunable
// grace so tests can run fast. Returns true if the process was
// definitely signalled (pgid lookup succeeded).
func killChildProcessGroup(cmd *exec.Cmd, gracePeriod time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fall back to direct kill; Setpgid might have failed.
		_ = cmd.Process.Kill()
		return false
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(gracePeriod)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return true
}

// simpleLoopCmd implements the "just let claude code build it"
// approach. No chunked SOW planning, no session scheduler, no
// MiniMax, no refine loops. Just:
//
//  1. Claude Code reads the prose → produces a plan
//  2. Codex reviews + enhances the plan
//  3. Claude Code reads codex feedback → does one more plan round
//  4. Claude Code builds, committing as it goes
//  5. We watch for commits → codex reviews each one
//  6. Codex review feedback → back to Claude Code to fix
//  7. Loop until codex signs off
//  8. Claude Code self-audits against the original SOW
//  9. If gaps remain → new prose → loop back to step 4
//  10. Repeat until "no gaps" + everything builds
//
// Usage:
//   stoke simple-loop --repo /path --file SOW.md
func simpleLoopCmd(args []string) {
	fs := flag.NewFlagSet("simple-loop", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	sowFile := fs.String("file", "", "SOW prose file")
	maxRounds := fs.Int("max-rounds", 5, "Max outer loops (plan→build→audit)")
	claudeBin := fs.String("claude-bin", "claude", "Claude Code binary")
	claudeModel := fs.String("claude-model", "", "Claude Code worker model (sonnet, opus, etc)")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	reviewer := fs.String("reviewer", "codex", "Reviewer backend: codex | cc-opus | cc-sonnet")
	fixMode := fs.String("fix-mode", "sequential", "How to deliver reviewer findings to CC: sequential (one big prompt, iterate until clean post-build) | parallel (split into chunks, N workers concurrently post-build) | concurrent (reviewer-approved worktree merges fire while big worker still building — Level 2)")
	fixWorkers := fs.Int("fix-workers", 3, "Max concurrent CC fix workers when --fix-mode=parallel")
	// H-19: TIER filter flags. The filter engages after `tierFilterAfter`
	// consecutive Final-review rounds fail to converge; set to 0 to
	// disable entirely. `tierFilterThreshold` is the TIER-3 dominance
	// share required to drop noise (0.7 = 70% of gaps must be TIER 3).
	tierFilterAfter := fs.Int("tier-filter-after", 5, "Final-review rounds before TIER filter engages (0 = disabled)")
	tierFilterThreshold := fs.Float64("tier-filter-threshold", 0.7, "TIER-3 dominance share required to drop noise")
	// H-25: resume from a prior run's .stoke/simple-loop-state.json.
	// Resume is opt-in (default OFF) because a crashed run with
	// unclean state is safer to restart fresh than to silently pick
	// up mid-round with a stale H-6 counter. --fresh clears any
	// stored state before starting so a relaunch after an aborted
	// run always produces a clean slate.
	resume := fs.Bool("resume", false, "Resume from prior .stoke/simple-loop-state.json (skips completed rounds, preserves H-6 counter). Refuses to resume on SOW/reviewer/fix-mode mismatch or prior regression-cap abort.")
	fresh := fs.Bool("fresh", false, "Clear .stoke/simple-loop-state.json before starting. Use after a crash or when relaunching a new SOW. Incompatible with --resume.")
	// H-30: lenient compliance. Changes the success criterion from
	// "compliance gate clean" to "CC audit says ALL DELIVERABLES
	// COMPLETE + at least 1 commit landed + build-verify didn't
	// reject". Lets small-scope runs (R01/R02/R03) exit cleanly when
	// the compliance gate is merely finding residual regex false
	// positives or SOW prose the worker legitimately deferred. The
	// strict default still holds when this flag is off.
	lenient := fs.Bool("lenient-compliance", false, "Exit on CC-says-done + commits-landed, even if compliance gate still has minor findings. For small-scope proofs where the strict gate can falsely block convergence.")
	fs.Parse(args)

	if *sowFile == "" {
		fmt.Fprintln(os.Stderr, "usage: stoke simple-loop --file SOW.md --repo /path")
		os.Exit(2)
	}
	absRepo, _ := filepath.Abs(*repo)
	prose, err := os.ReadFile(*sowFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read SOW:", err)
		os.Exit(1)
	}

	fmt.Printf("🔄 simple-loop: %s (%d bytes prose)\n", *sowFile, len(prose))
	fmt.Printf("   repo: %s\n", absRepo)
	claudeModelArg := *claudeModel
	fmt.Printf("   claude worker: %s (model: %s)\n", *claudeBin, func() string { if claudeModelArg == "" { return "default" }; return claudeModelArg }())
	fmt.Printf("   reviewer: %s\n", *reviewer)
	fmt.Printf("   max rounds: %d\n\n", *maxRounds)

	globalClaudeModel = claudeModelArg
	globalReviewer = *reviewer
	globalClaudeBin = *claudeBin
	globalCodexBin = *codexBin
	globalFixMode = *fixMode
	globalFixWorkers = *fixWorkers
	if globalFixWorkers < 1 {
		globalFixWorkers = 1
	}
	// Wire the CC↔Codex fallback pairs so rate-limits on one
	// provider automatically route traffic through the other while
	// the primary recovers (see cmd/stoke/fallback.go).
	initFallbackPairs(absRepo)
	fmt.Printf("   fix-mode: %s (workers: %d)\n", globalFixMode, globalFixWorkers)
	fmt.Printf("   fallback: writer primary=%s secondary=%s; reviewer primary=%s secondary=%s\n",
		writerPair.primary.Name(), writerPair.secondary.Name(),
		reviewerPair.primary.Name(), reviewerPair.secondary.Name())
	// Show which quality-signal gates are active so the operator can
	// visually confirm feature-gate env vars were honored. Printed
	// once at startup; experimentals are marked so their absence is
	// obvious when STOKE_QS_ENABLE wasn't set.
	qsCfg := plan.LoadQualityConfigFromEnv()
	fmt.Printf("   quality gates: %s\n", strings.Join(qsCfg.Enabled(), ", "))
	currentProse := string(prose)

	// Step-8 regression guard — see step8RegressionCap. Tracks how
	// many consecutive outer rounds ended with compliance NOT clean;
	// aborts the loop once the cap is hit so we don't burn tokens
	// oscillating between audit + builder without convergence.
	step8Tracker := &step8RegressionTracker{cap: step8RegressionCap}
	// H-29 plateau tracker: 3 rounds without gap-count reduction → partial exit.
	// Sized conservatively so a 5-round run has 2 rounds of "real" tries before
	// the plateau check can fire; the H-6 regression cap still catches the
	// extreme "counter-productive" shape (gaps grow) in 2 rounds.
	gapProgress := &gapCountProgressTracker{window: 3, minDelta: 1}
	var step8Aborted bool
	var plateauAborted bool

	// H-25: resume / fresh handling. --fresh wins over --resume with
	// a warning so an ambiguous operator command can't silently resume
	// into pre-crash state. The state file is written at the top of
	// every round below so a crash after round N+1 starts resumes at
	// round N+1 (the round we were about to execute), not N.
	proseHash := hashProse(currentProse)
	startRound := 1
	if *fresh && *resume {
		fmt.Fprintln(os.Stderr, "  ⚠ --fresh overrides --resume: clearing cached state, starting from round 1")
		*resume = false
	}
	if *fresh {
		if err := ClearSimpleLoopState(absRepo); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ --fresh: could not remove simple-loop-state.json: %v\n", err)
		} else {
			fmt.Println("  🧹 --fresh: cleared simple-loop-state.json")
		}
	}
	if *resume {
		stored, err := LoadSimpleLoopState(absRepo)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "  ⚠ --resume: could not load state (%v) — starting fresh\n", err)
		case stored == nil:
			fmt.Fprintln(os.Stderr, "  ⚠ --resume: no saved state found — starting fresh")
		default:
			ok, reason := validateResumeCompat(stored, absRepo, proseHash, *reviewer, globalFixMode)
			if !ok {
				fmt.Fprintf(os.Stderr, "  ⚠ --resume refused: %s — starting fresh\n", reason)
			} else {
				startRound = stored.CurrentRound
				currentProse = stored.CurrentProse
				*maxRounds = stored.MaxRounds
				step8Tracker.Load(stored.Step8Cycles, stored.LastGaps)
				fmt.Printf("  🔁 resumed at round %d/%d (H-6 counter=%d, prior gaps=%d)\n",
					startRound, *maxRounds, stored.Step8Cycles, len(stored.LastGaps))
			}
		}
	}

	for round := startRound; round <= *maxRounds; round++ {
		// Persist the round we're ABOUT TO EXECUTE before doing
		// anything expensive. A crash before the save-point means
		// we lose the round that never started (fine). A crash
		// after means resume re-runs the round (idempotent — plan
		// + review + build are re-entrant; duplicate commits from
		// a partial builder remain in git and are treated as prior
		// progress by the continuation builder).
		if err := SaveSimpleLoopState(absRepo, &simpleLoopState{
			SOWHash:      proseHash,
			CurrentRound: round,
			MaxRounds:    *maxRounds,
			Reviewer:     *reviewer,
			FixMode:      globalFixMode,
			CurrentProse: currentProse,
			Step8Cycles:  step8Tracker.Cycles(),
			LastGaps:     step8Tracker.LastGaps(),
			RepoHead:     currentRepoHead(absRepo),
		}); err != nil {
			// Non-fatal: resume is a convenience, not a correctness
			// property. Log and continue so a locked .stoke/ doesn't
			// kill an otherwise-healthy run.
			fmt.Fprintf(os.Stderr, "  ⚠ could not save simple-loop-state.json: %v\n", err)
		}

		fmt.Printf("═══════════════════════════════════════\n")
		fmt.Printf("  ROUND %d/%d\n", round, *maxRounds)
		fmt.Printf("═══════════════════════════════════════\n\n")

		// Step 1: Claude Code plans (prose-only; route through
		// writerPair so a CC rate-limit falls back to codex).
		fmt.Println("📋 Step 1: Claude Code planning...")
		planText := writerCall(absRepo, fmt.Sprintf(
			"Read this project specification and create a CONCISE implementation plan. "+
				"List every file you need to create/modify, in order.\n\n"+
				"PLAN FORMAT (strict):\n"+
				"  - Use the compact form `path/to/file.ts: one-line description of exports + key behavior`\n"+
				"  - NO paragraphs, NO prose explanations, NO file-content snippets\n"+
				"  - Target <20KB total. A larger plan slows the reviewer significantly.\n"+
				"  - Group by phase but keep each entry to ONE line\n\n"+
				"SPECIFICATION:\n%s\n\n"+
				"CURRENT REPO STATE: quickly list existing top-level dirs with `ls` before planning, don't recurse.",
			currentProse))
		if planText == "" {
			fmt.Println("  ⚠ Claude Code planning failed, retrying...")
			continue
		}
		fmt.Printf("  ✓ plan: %d chars\n", len(planText))

		// Step 2: Reviewer reviews the plan
		fmt.Printf("📝 Step 2: %s reviewing plan...\n", *reviewer)
		codexReview := reviewCall(absRepo,
			"Review this implementation plan for a software project. "+
				"Flag any issues: missing files, wrong dependencies, unrealistic steps, "+
				"ordering problems. Suggest improvements. Be specific.\n\nPLAN:\n"+planText)
		fmt.Printf("  ✓ review: %d chars\n", len(codexReview))

		// Step 3: Claude Code builds (background) while we watch commits
		fmt.Println("🔧 Step 3: Claude Code building (watching commits)...")
		headBefore := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null || echo none")

		// In concurrent mode, a fixOrchestrator runs alongside the
		// big worker: flagged commits spawn fix-workers in git
		// worktrees that merge back only on reviewer approval.
		var orch *fixOrchestrator
		bigWorkerExtra := ""
		if globalFixMode == "concurrent" {
			orch = newFixOrchestrator(absRepo, *claudeBin, *reviewer)
			bigWorkerExtra = "\n\n⚠️ IMPORTANT — CONCURRENT FIX PIPELINE ACTIVE:\n" +
				"A reviewer is watching every commit you make. When it finds issues, " +
				"a parallel fix worker is spawned in a separate git worktree to repair " +
				"them; once the reviewer approves those fixes, they are MERGED INTO YOUR " +
				"BRANCH automatically. Before every Edit or Write:\n" +
				"  • Run `git status` and `git log --oneline -10` to see fix-worker merges.\n" +
				"  • Re-Read the file you're about to modify (someone may have just fixed it).\n" +
				"  • If a conflict appears after `git status`, run `git diff` and reconcile — " +
				"do NOT blow away merged fixes.\n" +
				"Never assume your in-memory view of a file is up-to-date. The merge " +
				"orchestrator is silent; only `git log` reveals its work."
		}

		// Launch Claude Code build in background — with continuation
		// support. A single CC call is capped at 100 turns; the SOW
		// is too big to fit in 100 turns. When the builder exits
		// (clean finish OR max-turns), we inspect git + disk state,
		// and if the SOW isn't obviously done we spawn a continuation
		// builder with "here's what's committed, keep going". Loop
		// terminates when: (a) CC signals completion in its result,
		// (b) a continuation made ZERO new commits (stuck), or
		// (c) maxBuildContinuations reached.
		buildDone := make(chan string, 1)
		go func() {
			// Builder continuation is PROGRESS-SIGNAL BOUNDED, not
			// count-bounded. We loop as long as:
			//   - each continuation produces at least 1 new commit
			//   - the worker has not explicitly reported completion
			// We stop when:
			//   (a) 2 CONSECUTIVE continuations produced zero new
			//       commits (worker is stuck — spinning on the same
			//       problem without making progress);
			//   (b) CC signals "ALL DELIVERABLES COMPLETE" in its
			//       final text;
			//   (c) absoluteCap rounds have fired — escape hatch so
			//       a truly pathological SOW can't run forever.
			// absoluteCap is deliberately high so normal big SOWs
			// are bounded by real progress, not an arbitrary counter.
			const absoluteCap = 40 // ~4000 turns — hard ceiling
			priorCommits := shellCmd(absRepo, "git rev-list --count HEAD 2>/dev/null")
			consecutiveStalls := 0
			var finalResult string
			cont := 0
			for cont < absoluteCap {
				var prompt string
				if cont == 0 {
					prompt = fmt.Sprintf(
						"Here's your implementation plan and codex's review. "+
							"Refine the plan addressing codex's feedback, then START BUILDING. "+
							"Implement step by step.\n\n"+
							"COMMIT CADENCE — commit on LOGICAL-UNIT-OF-WORK boundaries:\n"+
							"  • Commit when you FINISH something coherent a reviewer can evaluate as a "+
							"unit — a planned task, a fully-wired feature (e.g. 'login flow end-to-end'), "+
							"a completed module (e.g. 'packages/types Zod schemas'), a working refactor.\n"+
							"  • Each commit should compile + pass its local build at the boundary. "+
							"Run the relevant typecheck/build BEFORE committing; fix failures first.\n"+
							"  • DO NOT commit mid-function, mid-feature, or in a broken state — the "+
							"reviewer will reject unreviewable 'wip' commits.\n"+
							"  • DO NOT batch several unrelated features into one commit — if ANY piece "+
							"is wrong the whole commit has to be rejected or split. Keep scope tight.\n"+
							"  • Commit message should answer 'what unit of work did I just complete?' — "+
							"'feat(api-client): residents + alarms modules' IS a unit; 'wip: more stuff' "+
							"is NOT.\n"+
							"  • Aim for commits small enough that each one is a clean, standalone win — "+
							"not a time-sliced chunk. Multiple small commits beat one monster commit "+
							"every time.\n"+
							"  • Your turn budget is 100. Do not try to finish the whole SOW in one call. "+
							"Get as many COMPLETE units in cleanly as possible; a continuation builder "+
							"will pick up from your last good commit.\n\n"+
							"YOUR PLAN:\n%s\n\nCODEX REVIEW:\n%s\n\n"+
							"SPECIFICATION:\n%s\n\n"+
							"START BUILDING NOW.%s",
						planText, codexReview, currentProse, bigWorkerExtra)
				} else {
					// Continuation prompt — show what's been done, ask
					// CC to diff against the SOW and keep going from
					// wherever the previous builder left off.
					doneLog := shellCmd(absRepo, "git log --oneline "+headBefore+"..HEAD 2>/dev/null | head -40")
					tree := shellCmd(absRepo, "ls -la 2>/dev/null; echo ---; find . -maxdepth 3 -type d -not -path './node_modules*' -not -path './.git*' 2>/dev/null | sort")
					prompt = fmt.Sprintf(
						"CONTINUATION BUILDER (call %d, %d stalls so far) — the prior builder "+
							"call has exited (either cleanly or at the 100-turn budget). "+
							"The SOW is large; we're continuing where you left off. The "+
							"harness will keep spawning continuations AS LONG AS each one "+
							"produces new commits, so take your turn budget fully.\n\n"+
							"COMMITTED SO FAR (%d prior commits in this build phase):\n%s\n\n"+
							"CURRENT DIRECTORY TREE:\n%s\n\n"+
							"YOUR JOB:\n"+
							"  1. Run `git log --oneline -20` and `git status` first to see the latest state.\n"+
							"  2. Read the SOW below and identify what's missing or incomplete.\n"+
							"  3. KEEP BUILDING from there. Do NOT duplicate work already committed.\n"+
							"  4. Fix any compile/typecheck errors you encounter along the way.\n"+
							"  5. Commit on LOGICAL-UNIT-OF-WORK boundaries (completed tasks/features/modules, "+
							"not time chunks). Each commit must compile and represent something the reviewer "+
							"can evaluate as a standalone unit.\n"+
							"  6. If you genuinely finish everything, end your last message with the "+
							"phrase 'ALL DELIVERABLES COMPLETE'. Otherwise we'll spawn another continuation.\n\n"+
							"ORIGINAL SPECIFICATION:\n%s%s",
						cont+1, consecutiveStalls, cont, doneLog, tree, currentProse, bigWorkerExtra)
				}
				fmt.Printf("🔧 Step 3 builder call %d (absoluteCap=%d, stalls=%d/2)...\n",
					cont+1, absoluteCap, consecutiveStalls)
				finalResult = claudeCall(*claudeBin, absRepo, prompt)

				curCommits := shellCmd(absRepo, "git rev-list --count HEAD 2>/dev/null")
				if curCommits == priorCommits {
					consecutiveStalls++
					fmt.Printf("  ⚠ builder %d made no new commits (stall %d/2)\n", cont+1, consecutiveStalls)
					if consecutiveStalls >= 2 {
						fmt.Printf("  ⛔ 2 consecutive stalled continuations — stopping build phase\n")
						break
					}
				} else {
					consecutiveStalls = 0
				}
				priorCommits = curCommits

				lower := strings.ToLower(finalResult)
				if strings.Contains(lower, "all deliverables complete") ||
					strings.Contains(lower, "sow complete") ||
					strings.Contains(lower, "nothing left to build") {
					fmt.Printf("  ✓ builder %d reports completion — ending build phase\n", cont+1)
					break
				}
				cont++
			}
			if cont >= absoluteCap {
				fmt.Printf("  ⛔ hit absoluteCap=%d continuations — stopping (unusual, investigate)\n", absoluteCap)
			}
			buildDone <- finalResult
		}()

		// Step 4: Watch commits. Two behaviors:
		//   - sequential/parallel fix-modes: accumulate findings
		//     into pendingReviews; deliver in Step 4b after big
		//     worker finishes.
		//   - concurrent fix-mode: dispatch findings IMMEDIATELY
		//     to the orchestrator (worktree + CC fix worker + auto
		//     merge-on-approval). Big worker keeps running.
		if globalFixMode == "concurrent" {
			fmt.Println("👀 Step 4: Watching commits, dispatching fix workers concurrently...")
		} else {
			fmt.Println("👀 Step 4: Watching for commits, queueing reviewer feedback...")
		}
		lastReviewedHead := headBefore
		reviewRound := 0
		const maxReviewRounds = 20
		var pendingReviews []string

	commitWatch:
		for reviewRound < maxReviewRounds {
			select {
			case <-buildDone:
				fmt.Println("  📦 Claude Code build phase complete")
				break commitWatch

			case <-time.After(30 * time.Second):
				currentHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
				if currentHead != lastReviewedHead && currentHead != headBefore {
					diff := shellCmd(absRepo, "git diff "+lastReviewedHead+".."+currentHead+" --stat 2>/dev/null")
					commitMsg := shellCmd(absRepo, "git log --oneline "+lastReviewedHead+".."+currentHead+" 2>/dev/null")
					if diff != "" {
						reviewRound++
						fmt.Printf("  📝 New commits (round %d):\n%s\n", reviewRound, indent(commitMsg, "    "))

						// Deterministic per-commit quality sweep — earliest-
						// fire gate. Before the LLM reviewer even looks at
						// the diff, we scan the changed files for hollow
						// bodies, skipped tests, tautology assertions,
						// duplicate scaffolds, silent catches. Blocking
						// findings are prepended to the review feedback so
						// the fixer gets concrete file:line targets.
						var qualityAddendum string
						changedFiles := strings.Split(strings.TrimSpace(
							shellCmd(absRepo, "git diff --name-only "+lastReviewedHead+".."+currentHead+" 2>/dev/null")),
							"\n")
						var cleanChanged []string
						for _, f := range changedFiles {
							f = strings.TrimSpace(f)
							if f != "" {
								cleanChanged = append(cleanChanged, f)
							}
						}
						if len(cleanChanged) > 0 {
							// Pass the SOW prose as a synthetic SOW so the
							// SOW-scoped experimental gates (sow-endpoints,
							// sow-structural, package-scripts) can fire when
							// enabled via STOKE_QS_ENABLE. Without this, only
							// file-scoped scanners fire on per-commit watch.
							syntheticSOW := &plan.SOW{Description: currentProse}
							qual := plan.RunQualitySweepForSOW(absRepo, cleanChanged, syntheticSOW)
							// H-2 (declared-file-not-created) in
							// simple-loop: task.Files doesn't exist here,
							// so we extract explicit file paths from the
							// SOW prose and cross-check them against the
							// repo. Only fires when extraction finds at
							// least one candidate — silent otherwise to
							// avoid noise on SOWs that only talk in
							// narratives.
							if declared := plan.ExtractDeclaredFiles(currentProse); len(declared) > 0 {
								missing := plan.ScanDeclaredFilesNotCreated(absRepo, declared)
								if len(missing) > 0 {
									paths := make([]string, len(missing))
									for i, m := range missing {
										paths[i] = m.File
									}
									fmt.Printf("  ⛔ [gate-hit] declared-file-not-created on %d file(s): %s\n",
										len(paths), strings.Join(paths, ", "))
									if qual == nil {
										qual = &plan.QualityReport{}
									}
									qual.Findings = append(qual.Findings, missing...)
									qual.BlockingN += len(missing)
								}
							}
							if qual != nil {
								// Always log a summary so telemetry can
								// distinguish "ran and passed" from "didn't
								// run". Previously we only logged when there
								// were findings, making grep-based counts
								// misleading.
								fmt.Printf("  🕵 quality sweep on diff: %s\n", qual.Summary())
								if qual.Blocking() {
									qualityAddendum = plan.FormatQualityReport(qual)
									if len(qualityAddendum) > 3000 {
										qualityAddendum = qualityAddendum[:3000] + "\n... (truncated)"
									}
									fmt.Println(qualityAddendum)
								}
							}
						}

						fmt.Printf("  🔍 %s reviewing...\n", *reviewer)
						reviewPrompt := "Review these specific changes. Check for: compilation errors, " +
							"missing imports, skeleton code. Be specific about what to fix.\n\n" +
							"COMMITS:\n" + commitMsg + "\n\nDIFF STAT:\n" + diff
						if qualityAddendum != "" {
							reviewPrompt = "DETERMINISTIC QUALITY SWEEP FLAGGED THE FOLLOWING — fixing these is MANDATORY regardless of your other findings.\n\nIMPORTANT: Each finding is ONE example. The same issue likely exists in sibling files (the worker often copy-pastes patterns). When you fix a finding, ALSO grep the repo for the same pattern across all related files (e.g. if `apps/web/e2e/alert-rules.spec.ts` has `.skip(` at line 6, also check every `apps/web/e2e/*.spec.ts` file for `.skip(` and fix them too). One-shot fix-all-matches prevents the rescan from surfacing the same issue in the next round.\n\n" +
								qualityAddendum + "\n\n---\n\n" + reviewPrompt
						}
						codeReview := reviewCall(absRepo, reviewPrompt)
						// If the quality sweep found blocking issues, the
						// reviewer's verdict doesn't get to approve — we
						// force feedback into the pending-reviews queue
						// with the concrete gap list so the fixer addresses
						// them even if the LLM tries to rubber-stamp.
						if qualityAddendum != "" && (approvedReview(codeReview) || len(codeReview) < 100) {
							codeReview = "QUALITY SWEEP BLOCKING SIGNALS (reviewer attempted to approve but deterministic scan found these):\n\n" +
								qualityAddendum
						}
						if len(codeReview) > 100 && !approvedReview(codeReview) {
							if orch != nil {
								id := orch.dispatch(currentHead,
									fmt.Sprintf("Commits reviewed:\n%s\n\nFindings:\n%s",
										commitMsg, codeReview))
								active, merged, abandoned := orch.stats()
								fmt.Printf("  🚀 dispatched fix-%d concurrently (active:%d merged:%d abandoned:%d)\n",
									id, active, merged, abandoned)
							} else {
								pendingReviews = append(pendingReviews,
									fmt.Sprintf("Commits reviewed:\n%s\n\nFindings:\n%s",
										commitMsg, codeReview))
								fmt.Printf("  ✗ reviewer found issues — queued (%d pending)\n", len(pendingReviews))
							}
						} else {
							fmt.Printf("  ✓ reviewer approved commits\n")
						}
						lastReviewedHead = currentHead
					}
				} else {
					fmt.Printf("  ⏳ waiting for commits... (%ds)\n", (reviewRound+1)*30)
				}
			}
		}

		// Concurrent mode: drain the orchestrator before Step 4b.
		// Any still-in-flight fix attempts get up to 10 min to
		// complete their merge-or-abandon cycle. After that, if
		// they haven't reached an approved merge they stay
		// abandoned on their fix branches (not merged to main).
		if orch != nil {
			active, merged, abandoned := orch.stats()
			if active > 0 {
				fmt.Printf("  ⏳ draining %d in-flight fix attempts (merged:%d abandoned:%d so far)\n",
					active, merged, abandoned)
				orch.waitIdle(10 * time.Minute)
			}
			_, merged, abandoned = orch.stats()
			fmt.Printf("  🛠️  concurrent fix pipeline final: merged=%d abandoned=%d\n",
				merged, abandoned)
		}

		// Step 4b: Iterate-until-clean. Deliver queued findings +
		// do a fresh final review over the full diff. If the
		// reviewer approves, we're done. Otherwise send to CC for
		// fix, wait for those fix commits, re-review. Repeat up
		// to maxFixRounds. This is the gate that makes simple-loop
		// actually enforce reviewer sign-off instead of shipping
		// unreviewed code.
		const maxFixRounds = 5
		// H-19: track prior rounds' gaps so the TIER filter can measure
		// recurrence. Each entry is the extracted gap list from one
		// Final-review iteration — reset each outer round because the
		// codebase composition has changed.
		var priorRoundsGaps [][]string
		// tierFilterComplete is set when applyTierFilter declares
		// drop-tier3-complete, breaking out of the fix loop as a clean
		// sign-off. Handled the same as approvedReview(finalReview).
		tierFilterComplete := false
		for fixRound := 1; fixRound <= maxFixRounds; fixRound++ {
			currentHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
			if currentHead == headBefore {
				fmt.Println("  (no commits produced — skipping fix loop)")
				break
			}
			fullDiff := shellCmd(absRepo, "git diff "+headBefore+"..HEAD --stat 2>/dev/null")
			fmt.Printf("  🔍 Final review %d/%d (via %s)...\n", fixRound, maxFixRounds, *reviewer)
			// Adaptive prefix: self-aware reviewer. The first 3 rounds
			// ALWAYS get a normal review — no prompt tweaking, no quality
			// assessment, no auto-skip. Reviewer earns those rounds of
			// full authority before the loop starts meta-questioning.
			// After round 3, the bar progressively rises:
			//   rounds 1-3: normal review (catch real defects + medium polish)
			//   rounds 4-6: raise bar (blocking defects only)
			//   rounds 7-12: "only would-break-in-prod" + loud warning
			//   rounds 13-19: quality-layer skepticism ("are we still finding
			//                real stuff, or polish?")
			//   rounds 20+: auto-approve if prior findings have been
			//               polish-only across the assessed window
			qualityVerdict := assessPriorReviewQuality(priorRoundsGaps)
			adaptivePreamble := ""
			autoApprove := false
			switch {
			case fixRound >= 20 && qualityVerdict == "polish-only":
				fmt.Printf("  🧠 review-quality: %d rounds, prior findings look polish-only — auto-approving\n", fixRound)
				autoApprove = true
			case fixRound >= 13:
				adaptivePreamble = fmt.Sprintf(
					"⚠⚠ ROUND %d of %d. Prior review quality so far: %s.\n\n"+
						"Step back and ask yourself: are my findings actually making this "+
						"code NOT WORK, or am I finding things I'd mention as a nit in code "+
						"review but would still merge? If it's the latter, RETURN LGTM.\n\n"+
						"The loop is losing confidence in the review layer. At 20 rounds "+
						"of polish-only findings, the loop will auto-approve regardless.\n\n",
					fixRound, maxFixRounds, qualityVerdict)
			case fixRound >= 7:
				adaptivePreamble = fmt.Sprintf(
					"⚠ ROUND %d of %d. Prior review quality so far: %s.\n\n"+
						"Prior rounds have dispatched fix workers for everything you flagged. "+
						"At this depth, almost every new 'finding' is polish disguised as a defect. "+
						"RETURN 'LGTM' UNLESS you can cite a specific file:line where code that "+
						"WAS passing now FAILS a build, test, or security check. A vague 'could "+
						"be more robust' is NOT a blocker at round 7+. Would a reasonable senior "+
						"engineer actually block a PR on your finding? If no → LGTM.\n\n",
					fixRound, maxFixRounds, qualityVerdict)
			case fixRound >= 4:
				adaptivePreamble = fmt.Sprintf(
					"Note: round %d of %d. Earlier rounds have iterated on issues; raise "+
						"the rejection bar. Only return pass=false for concrete BLOCKING defects "+
						"(tests fail, build fails, declared file missing, security bug). Polish, "+
						"style, or 'could also add' → pass=true with a low-severity finding.\n\n",
					fixRound, maxFixRounds)
			}

			if autoApprove {
				fmt.Printf("  ✅ reviewer bypassed (round %d) — quality layer judged prior findings polish-only, auto-approved\n", fixRound)
				pendingReviews = nil
				break
			}
			finalPrompt := adaptivePreamble +
				"Review ALL changes in this branch for SHIPPABILITY. " +
				"Bias strongly toward LGTM — your job is to catch BLOCKING defects, " +
				"not polish. Say 'NO ISSUES' or 'LGTM' when the code works and meets the SOW.\n\n" +
				"REJECT ONLY for these blocking defects:\n" +
				"  • Skeleton / scaffold-only files: body is empty, has unresolved TODO/FIXME, " +
				"or is only mocked/hard-coded values where the SOW asked for real behavior\n" +
				"  • Fake returns: hard-coded stub values that pretend a feature works without real logic\n" +
				"  • Functions that throw 'not implemented' style errors\n" +
				"  • Compilation errors, missing imports, broken tests\n" +
				"  • Security bugs: actual injection, auth bypass, data leak\n" +
				"  • Scope violation: files modified outside declared task scope WITHOUT a clear reason\n\n" +
				"DO NOT reject for any of these (bias to pass):\n" +
				"  • Style preferences (naming, formatting, comment density, config nits)\n" +
				"  • 'Could also add X' suggestions for features not in the SOW\n" +
				"  • Test-coverage opinions when at least one real passing test exists\n" +
				"  • Documentation gaps unless the SOW required them\n" +
				"  • TypeScript config refinements when the baseline compiles\n" +
				"  • 'Could be more comprehensive' without a concrete failure case\n" +
				"  • Anything you flagged in a PRIOR review round that the worker has addressed — " +
				"do not re-raise the same issue twice with different wording\n\n" +
				"You MUST look INSIDE the changed files using the Read tool — a diff that " +
				"adds a file whose body is scaffolding IS rejectable; a diff that adds a file " +
				"whose body works but lacks polish is NOT.\n\n" +
				"FULL DIFF STAT:\n" + fullDiff
			if len(pendingReviews) > 0 {
				finalPrompt += "\n\nPREVIOUSLY FLAGGED ISSUES (must be verified fixed):\n" +
					strings.Join(pendingReviews, "\n\n---\n\n")
			}
			// Anti-nitpick: surface prior-round findings so the reviewer
			// can see what was already said and not re-raise the same
			// issues with new wording. `priorRoundsGaps` accumulates
			// gap lists from each Final-review round.
			if len(priorRoundsGaps) > 0 {
				var seen []string
				for _, g := range priorRoundsGaps {
					seen = append(seen, g...)
				}
				if len(seen) > 0 {
					dedupSeen := make([]string, 0, len(seen))
					seenMap := make(map[string]struct{}, len(seen))
					for _, g := range seen {
						if _, ok := seenMap[g]; ok {
							continue
						}
						seenMap[g] = struct{}{}
						dedupSeen = append(dedupSeen, g)
					}
					finalPrompt += "\n\nISSUES YOU OR A PRIOR REVIEWER RAISED IN EARLIER ROUNDS " +
						"(do NOT re-raise these unless they are visibly still broken; " +
						"the worker has had attempts to address them):\n  - " +
						strings.Join(dedupSeen, "\n  - ")
				}
			}
			finalReview := reviewCall(absRepo, finalPrompt)
			if len(finalReview) < 100 || approvedReview(finalReview) {
				fmt.Printf("  ✅ reviewer approved (round %d) — build sign-off obtained\n", fixRound)
				pendingReviews = nil
				break
			}
			fmt.Printf("  ✗ reviewer still finding issues (round %d, mode=%s)\n", fixRound, globalFixMode)

			// H-19: Extract gap list from this round's reviewer output,
			// archive it for future recurrence detection, then — if
			// we've hit the tier-filter-after threshold — route the
			// reviewer through applyTierFilter. The filter may drop
			// recurring TIER-3 noise and even declare the loop
			// complete; otherwise the normal fix worker dispatches.
			currentGaps := extractGapsFromReview(finalReview)
			priorRoundsGaps = append(priorRoundsGaps, currentGaps)

			fixFeedback := finalReview
			if *tierFilterAfter > 0 && fixRound >= *tierFilterAfter {
				fmt.Printf("  🎚 TIER filter engaging (fixRound=%d >= %d)\n",
					fixRound, *tierFilterAfter)
				tierCtx, tierCancel := context.WithTimeout(context.Background(), 20*time.Minute)
				review := func(ctx context.Context, prompt string) string {
					return reviewCall(absRepo, prompt)
				}
				// priorRoundsGaps[:len-1] excludes the current round
				// (we just appended it) so the filter measures
				// recurrence against TRULY prior data, not itself.
				priorOnly := priorRoundsGaps
				if len(priorOnly) > 0 {
					priorOnly = priorOnly[:len(priorOnly)-1]
				}
				tfResult, tfErr := applyTierFilter(
					tierCtx, review, currentGaps, priorOnly, fixRound, *tierFilterThreshold)
				tierCancel()
				if tfErr != nil {
					fmt.Fprintf(os.Stderr,
						"  ↻ TIER-3 filter: error, continuing normally: %v\n", tfErr)
				}
				switch tfResult.Decision {
				case "drop-tier3-complete":
					fmt.Printf("  ✓ TIER-3 drop: all remaining gaps were TIER-3 noise; declaring SIMPLE LOOP COMPLETE (dropped %d gaps: %s)\n",
						len(tfResult.Tier3Dropped),
						formatGapList(tfResult.Tier3Dropped))
					pendingReviews = nil
					tierFilterComplete = true
				case "drop-tier3-continue":
					fmt.Printf("  ⏭ TIER-3 drop: %d gaps dropped (recurring noise); continuing with %d TIER-1/2 gaps\n",
						len(tfResult.Tier3Dropped), len(tfResult.RemainingGaps))
					fmt.Printf("     dropped: %s\n", formatGapList(tfResult.Tier3Dropped))
					fmt.Printf("     remaining: %s\n", formatGapList(tfResult.RemainingGaps))
					// Replace reviewer feedback with ONLY the TIER-1/2
					// gaps so the fix worker focuses on real defects.
					fixFeedback = "REAL DEFECTS REMAINING (TIER-3 noise filtered out by convergence guard):\n\n" +
						strings.Join(tfResult.RemainingGaps, "\n")
				default:
					fmt.Printf("  ↻ TIER-3 filter: continue (tier1=%d tier2=%d tier3=%d, recurring=%v)\n",
						tfResult.Tier1Count, tfResult.Tier2Count,
						tfResult.Tier3Count, tfResult.Recurring)
				}
				if tierFilterComplete {
					break
				}
			}

			fixHeadBefore := currentHead
			if globalFixMode == "parallel" {
				dispatchParallelFix(*claudeBin, absRepo, fixFeedback, globalFixWorkers)
			} else {
				dispatchSequentialFix(*claudeBin, absRepo, fixFeedback)
			}
			pendingReviews = nil
			postFixHead := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null")
			if postFixHead == fixHeadBefore {
				fmt.Printf("  ⚠ CC made no fix commits — exiting fix loop\n")
				break
			}
			fmt.Printf("  📝 CC produced fix commits; re-reviewing...\n")
		}
		_ = tierFilterComplete // referenced inside the loop; nothing to do here

		// Step 5: Build verification
		fmt.Println("🏗️  Step 5: Build verification...")
		buildResult := shellCmd(absRepo, detectSimpleBuildCmd(absRepo))
		buildPassed := !strings.Contains(buildResult, "error") || strings.Contains(buildResult, "0 errors")
		if buildPassed {
			fmt.Println("  ✓ build passes")
		} else {
			fmt.Printf("  ✗ build failed, sending to CC...\n")
			claudeCall(*claudeBin, absRepo, fmt.Sprintf(
				"The build failed. Fix these errors and commit:\n\n%s", buildResult))
		}

		// Step 8: Self-audit against SOW (prose verdict; routed
		// through writerPair so a CC rate-limit falls back to codex).
		fmt.Println("📋 Step 8: Claude Code self-auditing against SOW...")
		audit := writerCall(absRepo, fmt.Sprintf(
			"Compare the current state of this repository against the original specification. "+
				"For EACH deliverable in the spec, state whether it's: DONE, PARTIAL, or MISSING. "+
				"BE BRUTALLY HONEST. A deliverable is NOT DONE if it is any of: "+
				"skeleton function body; hard-coded fake returns; empty handler/callback; "+
				"mock-only implementation where SOW asked for real behavior; file exists but logic is missing. "+
				"Report PARTIAL or MISSING for anything that is scaffolding only.\n\n"+
				"Then answer: IS THERE MORE WORK TO DO? If yes, describe EXACTLY what remains "+
				"(list each stub/missing item by name) as a new specification for the next round. "+
				"If no, say 'ALL DELIVERABLES COMPLETE'.\n\n"+
				"ORIGINAL SPECIFICATION:\n%s", currentProse))
		fmt.Printf("  audit: %d chars\n", len(audit))

		// Step 8b: Deterministic compliance sweep — anti-rubber-stamp
		// The CC audit above is circular (CC grading CC's work). This
		// sweep walks the SOW prose for named deliverables and checks
		// each against the actual repo via filename+content-definition
		// match + 80-byte + body-line thresholds. Authoritative: if
		// compliance finds stubs/missing, we override any "ALL
		// DELIVERABLES COMPLETE" claim from CC.
		ccSaysDone := strings.Contains(strings.ToUpper(audit), "ALL DELIVERABLES COMPLETE")
		tmpSOW := &plan.SOW{Description: currentProse}
		compReport := plan.RunSOWCompliance(absRepo, tmpSOW)
		complianceClean := compReport != nil && compReport.Passed()
		if compReport != nil && len(compReport.Findings) > 0 {
			fmt.Printf("  🕵 compliance sweep: %s\n", compReport.Summary())
			if !complianceClean {
				// Show what's missing/stub so CC has concrete feedback
				// for the next round's prose.
				shortReport := plan.FormatComplianceReport(compReport)
				if len(shortReport) > 4000 {
					shortReport = shortReport[:4000] + "\n... (truncated)"
				}
				fmt.Println(shortReport)
			}
		} else {
			fmt.Printf("  🕵 compliance sweep: no extractable deliverables from prose\n")
		}

		// Step 8c: H-6 audit-ran heuristic. The Step-8 `claude` call
		// above can fail silently when the CLI is rate-limited or the
		// account is cut off — every call returns `claude error: exit
		// status 1` after 1 turn at $0.0000 and produces a short
		// (<200 char) body. If that happens, counting the empty audit
		// as a compliance regression would kill a healthy run (see
		// H1-sonnet / H2-opus-full 2026-04-17). Heuristic: we consider
		// the audit to have actually RUN only when the output is long
		// enough to contain a real verdict AND does NOT contain the
		// "claude error" marker.
		auditRan := len(strings.TrimSpace(audit)) >= 200 && !strings.Contains(audit, "claude error")
		if !auditRan {
			fmt.Fprintf(os.Stderr,
				"  ⚠ Step-8 audit output looks incomplete (len=%d); not counting toward regression cap (upstream failure suspected)\n",
				len(strings.TrimSpace(audit)))
		}

		// Step 9: Check if done.
		// Default (strict): BOTH gates must agree — CC says complete
		// AND compliance sweep is clean.
		// H-30 lenient mode: CC says complete + at least 1 commit
		// landed this round is sufficient; compliance findings become
		// advisory, not blocking. Lets small-scope runs exit cleanly
		// when the regex-based compliance gate surfaces residual
		// false positives that don't correspond to real gaps.
		headAfterRound := shellCmd(absRepo, "git rev-parse HEAD 2>/dev/null || echo none")
		commitsLanded := headAfterRound != "none" && headAfterRound != headBefore
		if *lenient && ccSaysDone && commitsLanded {
			fmt.Printf("\n✅ ROUND %d: Lenient mode — CC reports complete + %d commits landed; residual compliance findings logged as advisory.\n",
				round, reviewRound)
			step8Tracker.ObserveAuditResult(true, true, nil)
			if err := ClearSimpleLoopState(absRepo); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ could not clear simple-loop-state.json on lenient pass: %v\n", err)
			}
			break
		}
		if ccSaysDone && complianceClean {
			fmt.Printf("\n✅ ROUND %d: All deliverables complete (CC audit + compliance sweep both clean)\n", round)
			// Reset the regression tracker on clean pass — future
			// rounds (if any) start from zero.
			step8Tracker.ObserveAuditResult(true, true, nil)
			// H-25 (codex P2-2): clear resume state BEFORE the break.
			// A kill between `break` and the post-loop save would
			// otherwise leave the round-start snapshot on disk and
			// cause --resume to re-run a round that already completed.
			if err := ClearSimpleLoopState(absRepo); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ could not clear simple-loop-state.json on clean pass: %v\n", err)
			}
			break
		}
		if ccSaysDone && !complianceClean {
			fmt.Printf("\n⚠ ROUND %d: CC claimed complete but compliance sweep found stubs/missing — overriding to gaps-remain\n", round)
		}

		// Build the gap list now so the regression tracker, the
		// abort banner, and the next-round prose all see the same
		// canonical list.
		var gaps []string
		if compReport != nil && !complianceClean {
			for _, f := range compReport.Findings {
				if f.Verdict == plan.VerdictMissing {
					gaps = append(gaps, fmt.Sprintf("MISSING: %s", f.Deliverable.Name))
				} else if f.Verdict == plan.VerdictFoundStub {
					gaps = append(gaps, fmt.Sprintf("STUB (must implement real logic): %s", f.Deliverable.Name))
				}
			}
		}

		// Step-8 regression guard — observe this round's outcome.
		// If the tracker says we've hit the cap, emit the banner and
		// bail out of the whole outer loop.
		if step8Tracker.ObserveAuditResult(auditRan, complianceClean, gaps) {
			// H-20: before killing the run, give the TIER filter one
			// last chance to rescue us. The H-6 cap counts OUTER ROUNDs
			// (plan→build→audit cycles), whereas H-19's `tierFilterAfter`
			// counts INNER Final-review rounds; on fast cycles the outer
			// cap can fire before the inner filter has any chance to
			// engage. If the operator enabled the filter (tierFilterAfter
			// > 0) AND the filter says the remaining gaps are TIER-3
			// noise, we either declare complete (all noise) or reset the
			// regression counter with only real gaps remaining. On filter
			// error / decision="continue" we fall through to the original
			// H-6 termination — fail-open so an unavailable filter never
			// silently swallows a real regression.
			rescued, rescueComplete := tierFilterRescueBeforeH6Cap(
				absRepo, gaps, *tierFilterAfter, *tierFilterThreshold, step8Tracker)
			if rescueComplete {
				fmt.Printf("\n✅ ROUND %d: TIER filter declared drop-tier3-complete before H-6 cap — all remaining gaps were TIER-3 noise\n", round)
				// H-25 (codex P2-2): clear resume state BEFORE the
				// short-circuit break, matching the Step 9 clean-pass path.
				if err := ClearSimpleLoopState(absRepo); err != nil {
					fmt.Fprintf(os.Stderr, "  ⚠ could not clear simple-loop-state.json on TIER-3 drop-complete: %v\n", err)
				}
				// Short-circuit the whole outer loop as a clean pass.
				break
			}
			if rescued {
				fmt.Printf("\n🛟 H-20 rescue: TIER filter dropped TIER-3 noise; regression counter reset, continuing with real gaps only\n")
				// Counter reset happened inside the rescue helper.
				// Fall through to the "gaps remain, iterate next round"
				// path below so the next round's prose gets the
				// filtered gap list.
				filtered := step8Tracker.LastGaps()
				if len(filtered) > 0 {
					// Replace the gap list used to build next-round prose
					// so the builder focuses on real defects.
					gaps = append([]string{}, filtered...)
				}
				// NB: do NOT break; we want another outer round.
			} else {
				fmt.Printf("\n⛔ Step-8 regression guard: %d consecutive cycles ended with gaps remaining after audit. Stopping to avoid token burn. Last gaps: %s.\n",
					step8Tracker.Cycles(), formatGapList(step8Tracker.LastGaps()))
				step8Aborted = true
				// H-25 (codex P2-2): mark the on-disk state as Aborted
				// BEFORE the break. Without this, a kill between the
				// break and the post-loop save would leave the round-
				// start snapshot (Aborted=false) as the last written
				// state; --resume would then happily continue past the
				// regression cap. Mark-then-break forecloses that.
				if err := SaveSimpleLoopState(absRepo, &simpleLoopState{
					SOWHash:      proseHash,
					CurrentRound: round,
					MaxRounds:    *maxRounds,
					Reviewer:     *reviewer,
					FixMode:      globalFixMode,
					CurrentProse: currentProse,
					Step8Cycles:  step8Tracker.Cycles(),
					LastGaps:     step8Tracker.LastGaps(),
					Aborted:      true,
					RepoHead:     currentRepoHead(absRepo),
				}); err != nil {
					fmt.Fprintf(os.Stderr, "  ⚠ could not mark simple-loop state as aborted before break: %v\n", err)
				}
				break
			}
		}

		// Extract remaining work as new prose for next round.
		// If compliance found specific missing/stub items, prepend
		// those to the audit text so the next round gets concrete
		// targets instead of vague CC-self-assessment.
		nextProse := audit
		if len(gaps) > 0 {
			nextProse = "COMPLIANCE GATE FOUND THE FOLLOWING GAPS — IMPLEMENT THEM FULLY (no scaffolds, no mocks, no filler values):\n\n" +
				strings.Join(gaps, "\n") + "\n\n---\n\nADDITIONAL CC AUDIT NOTES:\n" + audit
		}

		// Auto-extend rounds if we've hit the cap but compliance
		// still says gaps remain. One-time extension to avoid the
		// MS failure mode (exited at max-rounds with gaps). Logs
		// a loud warning so the user sees it.
		if round == *maxRounds && !complianceClean {
			newCap := *maxRounds + 3
			fmt.Printf("\n⚠ ROUND %d = max but compliance still failing — auto-extending max-rounds to %d (one-time)\n",
				round, newCap)
			*maxRounds = newCap
		}

		// H-29 plateau check — record this round's gap count and, if
		// 3 consecutive rounds failed to drop the count by >= 1, exit
		// with PARTIAL-SUCCESS status instead of burning another
		// round. Commits landed so far are preserved; the operator
		// gets a concrete report of what's still missing.
		if gapProgress.Observe(len(gaps)) {
			hist := gapProgress.History()
			fmt.Printf("\n⏸  ROUND %d: gap count plateau (%v over %d rounds, best=%d) — exiting with PARTIAL-SUCCESS to avoid burning tokens on non-convergent loop.\n",
				round, hist, len(hist), gapProgress.Best())
			plateauAborted = true
			if err := SaveSimpleLoopState(absRepo, &simpleLoopState{
				SOWHash:      proseHash,
				CurrentRound: round,
				MaxRounds:    *maxRounds,
				Reviewer:     *reviewer,
				FixMode:      globalFixMode,
				CurrentProse: currentProse,
				Step8Cycles:  step8Tracker.Cycles(),
				LastGaps:     step8Tracker.LastGaps(),
				Aborted:      true,
				RepoHead:     currentRepoHead(absRepo),
			}); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ could not persist plateau state: %v\n", err)
			}
			break
		}

		fmt.Printf("\n🔄 ROUND %d: gaps remain — extracting remaining work for next round\n", round)
		currentProse = nextProse // next round's input

		// H-25 (codex P2-3): save state at END of round too, after we've
		// committed to the next-round prose + absorbed this round's
		// step8-tracker result. A crash in the gap between this save and
		// the top-of-loop save of round+1 would otherwise leave the
		// round-N snapshot on disk; resume would replay round N with
		// stale prose/counters. With this end-of-round save, the last
		// persisted state always reflects the most recent completed
		// round: resume correctly starts at round+1.
		if err := SaveSimpleLoopState(absRepo, &simpleLoopState{
			SOWHash:      proseHash,
			CurrentRound: round + 1,
			MaxRounds:    *maxRounds,
			Reviewer:     *reviewer,
			FixMode:      globalFixMode,
			CurrentProse: currentProse,
			Step8Cycles:  step8Tracker.Cycles(),
			LastGaps:     step8Tracker.LastGaps(),
			RepoHead:     currentRepoHead(absRepo),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ could not save end-of-round state: %v\n", err)
		}
	}

	// H-25: persist terminal state so a subsequent --resume does the
	// right thing. On clean completion we delete the state file (nothing
	// left to resume). On regression abort we mark the state file
	// Aborted=true so --resume refuses until the operator explicitly
	// passes --fresh or extends the SOW (otherwise relaunching just
	// reproduces the same abort). Plateau aborts also mark Aborted=true
	// so --resume refuses; operator must re-scope or --fresh.
	if step8Aborted || plateauAborted {
		if err := SaveSimpleLoopState(absRepo, &simpleLoopState{
			SOWHash:      proseHash,
			CurrentRound: *maxRounds,
			MaxRounds:    *maxRounds,
			Reviewer:     *reviewer,
			FixMode:      globalFixMode,
			CurrentProse: currentProse,
			Step8Cycles:  step8Tracker.Cycles(),
			LastGaps:     step8Tracker.LastGaps(),
			Aborted:      true,
			RepoHead:     currentRepoHead(absRepo),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ could not mark simple-loop state as aborted: %v\n", err)
		}
	} else {
		if err := ClearSimpleLoopState(absRepo); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ could not clear simple-loop-state.json on completion: %v\n", err)
		}
	}

	// Final summary
	fmt.Println("\n═══════════════════════════════════════")
	if plateauAborted {
		fmt.Println("  SIMPLE LOOP PARTIAL-SUCCESS — plateau reached")
		fmt.Printf("  repo: %s\n", absRepo)
		fmt.Printf("  gap-count trajectory: %v (best=%d)\n",
			gapProgress.History(), gapProgress.Best())
		fmt.Printf("  remaining gaps at exit: %s\n",
			formatGapList(step8Tracker.LastGaps()))
		fmt.Println("  real code shipped to main; run `git log --oneline` to audit.")
		fmt.Println("  next step: review the diff, merge what's good, re-scope the")
		fmt.Println("  remaining gaps into a tighter SOW, and relaunch.")
	} else if step8Aborted {
		fmt.Println("  SIMPLE LOOP ABORTED — Step-8 regression cap reached")
		fmt.Printf("  repo: %s\n", absRepo)
		fmt.Printf("  consecutive gap-closure failures: %d (cap %d)\n",
			step8Tracker.Cycles(), step8RegressionCap)
		fmt.Printf("  last unresolved gaps: %s\n",
			formatGapList(step8Tracker.LastGaps()))
		fmt.Println("  next step: relaunch stoke simple-loop OR extend the SOW")
		fmt.Println("  to give the builder a materially different prompt.")
	} else {
		fmt.Println("  SIMPLE LOOP COMPLETE")
		fmt.Printf("  repo: %s\n", absRepo)
		fmt.Println("  run 'stoke sessions status' to see results")
	}
	fmt.Println("═══════════════════════════════════════")
	if step8Aborted {
		// Non-zero exit so outer orchestrators (CI, other stoke
		// commands) can detect the regression-cap abort without
		// having to scrape stdout.
		os.Exit(3)
	}
	if plateauAborted {
		// Distinct exit code from the regression-cap abort so outer
		// orchestrators can tell "ran a long time and bailed from
		// non-convergence" from "bailed quickly after obvious
		// regression". Both are PARTIAL-SUCCESS in the sense that
		// committed work still landed; only the exit signal differs.
		os.Exit(4)
	}
}

// tierFilterRescueBeforeH6Cap is the H-20 last-chance hook. Called
// when step8RegressionTracker says we're about to abort but the TIER
// filter is enabled (tierFilterAfter > 0). It re-applies the H-19
// classifier to the current gap list so TIER-3 noise dominating the
// outer regression loop can be filtered instead of terminating the run.
//
// Return values:
//
//	rescued=false, complete=false → filter declined / errored / real
//	    gaps remain; caller falls through to the H-6 abort banner.
//	rescued=true,  complete=false → TIER-3 noise filtered out; caller
//	    should reset the regression counter and loop another round with
//	    the TIER-1/2 gaps. The counter is reset INSIDE this helper so the
//	    caller doesn't need to know the implementation.
//	rescued=true,  complete=true  → every remaining gap was TIER-3; the
//	    caller should break out of the outer loop as a clean sign-off.
//
// Fail-open: any reviewer error, malformed JSON, unknown decision, or
// panic returns (false, false) so the outer H-6 termination still
// fires. Dropping real gaps on a filter failure would silently swallow
// a real regression.
func tierFilterRescueBeforeH6Cap(
	repoDir string,
	gaps []string,
	tierFilterAfter int,
	tierFilterThreshold float64,
	step8Tracker *step8RegressionTracker,
) (rescued bool, complete bool) {
	// Default reviewer = the production reviewCall routed through the
	// configured fallback pair. Tests inject via the exported *ForTest
	// variant below; production calls reach this thin wrapper.
	review := func(ctx context.Context, prompt string) string {
		return reviewCall(repoDir, prompt)
	}
	return tierFilterRescueBeforeH6CapWithReview(
		review, gaps, tierFilterAfter, tierFilterThreshold, step8Tracker)
}

// tierFilterRescueBeforeH6CapWithReview is the testable core of the
// H-20 rescue. Exposes the reviewer as a parameter so tests can stub
// it without spawning a real codex/CC subprocess. Production callers
// go through tierFilterRescueBeforeH6Cap, which plugs in the real
// reviewCall routed via reviewerPair.
func tierFilterRescueBeforeH6CapWithReview(
	review tierFilterReviewFunc,
	gaps []string,
	tierFilterAfter int,
	tierFilterThreshold float64,
	step8Tracker *step8RegressionTracker,
) (rescued bool, complete bool) {
	if tierFilterAfter <= 0 {
		// Filter disabled — nothing to do.
		return false, false
	}
	if len(gaps) == 0 {
		// No gaps means compliance passed; shouldn't reach here, but
		// treat as complete for safety.
		return true, true
	}
	// Guard the whole call with a panic recover so a bug in the filter
	// plumbing can never take down the outer loop. Fail-open to the
	// H-6 abort path.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ H-20 rescue panic: %v — falling through to H-6 abort\n", r)
			rescued = false
			complete = false
		}
	}()

	fmt.Printf("  🛟 H-20: TIER filter pre-H6-cap check (gaps=%d, threshold=%.2f)\n",
		len(gaps), tierFilterThreshold)

	// 20-minute ceiling matches the H-19 inline invocation.
	tierCtx, tierCancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer tierCancel()

	// No prior-rounds signal available at this layer — the H-6 cap is
	// an OUTER-loop counter and doesn't carry the inner-round gap
	// history. Pass the current gaps as a synthetic prior round so the
	// recurrence gate can fire (if the gaps repeat they'll match
	// themselves) without forcing a stricter dominance check. The
	// dominanceThreshold gate still has to pass on the raw reviewer
	// classification, so this doesn't weaken the safety bar.
	priorRounds := [][]string{append([]string{}, gaps...)}

	tfResult, tfErr := applyTierFilter(
		tierCtx, review, gaps, priorRounds, 0, tierFilterThreshold)
	if tfErr != nil {
		fmt.Fprintf(os.Stderr,
			"  ↻ H-20 rescue: filter error, falling through to H-6 abort: %v\n", tfErr)
		return false, false
	}
	switch tfResult.Decision {
	case "drop-tier3-complete":
		fmt.Printf("  ✓ H-20 rescue: TIER filter declared complete (dropped %d TIER-3 gaps: %s)\n",
			len(tfResult.Tier3Dropped), formatGapList(tfResult.Tier3Dropped))
		// Reset the tracker so if the outer loop continues for any
		// reason, the counter starts fresh.
		step8Tracker.ObserveAuditResult(true, true, nil)
		return true, true
	case "drop-tier3-continue":
		fmt.Printf("  ⏭ H-20 rescue: TIER filter dropped %d TIER-3 gaps; %d real gaps remain: %s\n",
			len(tfResult.Tier3Dropped), len(tfResult.RemainingGaps),
			formatGapList(tfResult.RemainingGaps))
		// Reset the regression counter and update lastGaps to the
		// filtered list so the next-round prose focuses on real
		// defects.
		step8Tracker.cycles = 0
		step8Tracker.lastGaps = append([]string{}, tfResult.RemainingGaps...)
		return true, false
	default:
		// "continue" or any unexpected value — fail-open to H-6 cap.
		fmt.Printf("  ↻ H-20 rescue: filter decision=%q (tier1=%d tier2=%d tier3=%d); falling through to H-6 abort\n",
			tfResult.Decision, tfResult.Tier1Count, tfResult.Tier2Count, tfResult.Tier3Count)
		return false, false
	}
}

// formatGapList renders a gap list for human-readable log lines.
// Truncates to 5 entries for the banner; the full list is already
// in the preceding compliance-sweep log output.
func formatGapList(gaps []string) string {
	if len(gaps) == 0 {
		return "(none recorded)"
	}
	const maxInline = 5
	if len(gaps) <= maxInline {
		return strings.Join(gaps, "; ")
	}
	return strings.Join(gaps[:maxInline], "; ") +
		fmt.Sprintf(" (+%d more)", len(gaps)-maxInline)
}

var (
	globalClaudeModel string // worker model override
	globalReviewer    string // "codex", "cc-opus", "cc-sonnet"
	globalClaudeBin   string // resolved claude binary path
	globalCodexBin    string // resolved codex binary path
	globalFixMode     string // "sequential" or "parallel"
	globalFixWorkers  int    // concurrency for parallel fix mode
)

// approvedReview returns true when the reviewer text looks
// like sign-off. Treats "no issues", "lgtm", "looks good",
// "approved" as approval. A short (<100 char) response is
// considered ambiguous and NOT approval — forces iteration.
func approvedReview(text string) bool {
	t := strings.ToLower(text)
	for _, marker := range []string{"no issues", "lgtm", "looks good", "approved", "no changes needed"} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}

// splitReviewIntoIssues breaks a reviewer's response into
// discrete actionable findings. Heuristic: lines starting with
// "-", "*", digit+dot, or "Issue:". When the reviewer writes
// free prose, returns the whole text as one issue. Returns at
// most maxChunks issues; extras are merged into the last chunk.
func splitReviewIntoIssues(text string, maxChunks int) []string {
	if maxChunks < 1 {
		maxChunks = 1
	}
	var issues []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			issues = append(issues, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		isNew := strings.HasPrefix(trimmed, "- ") ||
			strings.HasPrefix(trimmed, "* ") ||
			strings.HasPrefix(strings.ToLower(trimmed), "issue:") ||
			(len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && (trimmed[1] == '.' || trimmed[1] == ')'))
		if isNew && cur.Len() > 0 {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	if len(issues) <= 1 {
		return []string{strings.TrimSpace(text)}
	}
	if len(issues) > maxChunks {
		// Collapse overflow into the last chunk so nothing is dropped.
		head := issues[:maxChunks-1]
		tail := strings.Join(issues[maxChunks-1:], "\n\n")
		issues = append(head, tail)
	}
	return issues
}

// reviewCall dispatches the plan/code-review call to the
// configured reviewer backend. Reviewers run in TEXT-ONLY mode
// — no filesystem tools, no commits. The caller hands in a
// fully-formed prompt; we return the review text.
//
// For the codex reviewer, this function also gates the call through
// codexBackoff (H-7): when the rolling 5-min error rate exceeds
// 1/min, the NEXT call is delayed by 2x/4x/8x. A successful call
// resets the multiplier. This prevents tight loops of 429/turn.failed
// errors from wedging the final-review phase the way MS-full wedged.
func reviewCall(dir, prompt string) string {
	switch globalReviewer {
	case "cc-opus":
		return claudeReviewCall(globalClaudeBin, dir, prompt, "opus")
	case "cc-sonnet":
		return claudeReviewCall(globalClaudeBin, dir, prompt, "sonnet")
	case "cc", "claude":
		// Generic "claude code as reviewer" — uses its default model.
		return claudeReviewCall(globalClaudeBin, dir, prompt, "")
	default:
		// Codex path — apply rate-based backoff BEFORE the call,
		// then record the outcome. We treat an empty return as an
		// error (covers turn.failed, 429, watchdog-kill, crash) and
		// a non-empty return as success.
		//
		// The outer swap is done by reviewerPair (codex-primary,
		// CC-sonnet-secondary). When reviewerPair is nil (tests),
		// we fall back to calling codex directly. codexBackoff
		// remains the INNER throttle that slows tight error loops
		// while reviewerPair handles the whole-provider swap.
		applyCodexBackoff()
		var out string
		if reviewerPair != nil {
			out = reviewerCallViaPair(dir, prompt)
		} else {
			out = codexCall(globalCodexBin, dir, prompt)
		}
		if strings.TrimSpace(out) == "" {
			if codexBackoff.RecordError() {
				fmt.Fprintf(os.Stderr,
					"  ⏸ codex backoff activated: %dx next call (codex errors: %d in last 5min)\n",
					codexBackoff.Multiplier(), codexBackoff.ErrorCount())
			}
		} else {
			codexBackoff.RecordSuccess()
		}
		return out
	}
}

// claudeReviewCall invokes Claude Code in text-only mode for
// review purposes. No --dangerously-skip-permissions, no tools,
// no JSON wrapping — just --print with optional model override.
func claudeReviewCall(bin, dir, prompt, model string) string {
	// H-10: gate through the rate-limit detector just like claudeCall.
	claudeBackoff.WaitIfPaused()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// H-21: same ARG_MAX switch as codexCall. Claude's `-p` reads from
	// stdin when no prompt argument is supplied, so we simply drop the
	// positional arg and pipe via cmd.Stdin for oversized prompts.
	args := []string{
		"--print",
		"--no-session-persistence",
	}
	useStdin := len(prompt) > argMaxStdinThreshold
	if !useStdin {
		args = append(args, prompt)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  claude-reviewer error: %v\n", err)
		// Feed the error marker into the detector. --print mode has no
		// turns/cost metadata, so we pass (1, 0) to match the rate-
		// limit signature (turns<=1 && cost==0 && "claude error").
		claudeBackoff.RecordResult(fmt.Sprintf("claude error: %v", err), 1, 0)
		return ""
	}
	body := strings.TrimSpace(out.String())
	// Non-empty review body = success from the detector's perspective.
	// Pass turns=2 to cross the >1 bar; cost isn't known here.
	if body != "" {
		claudeBackoff.RecordResult(body, 2, 0)
	}
	return body
}

func claudeCall(bin, dir, prompt string) string {
	// H-10: Block if the rate-limit detector is in Active pause. No-op
	// in Normal/Suspected states, so this is cheap to call every time.
	claudeBackoff.WaitIfPaused()
	// Hard cap 40 min; previous 30-min was tight for big fix calls.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	// stream-json gives us live line-by-line tool-use events — we
	// scan its growth as the progress signal for the watchdog.
	// Without stream-json, the ONLY output is a single final JSON
	// blob at exit, which makes every long CC call look identical
	// to a hang. With stream-json, each tool call emits a line
	// immediately, so the watchdog can distinguish "CC is doing
	// work" from "CC is wedged".
	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--max-turns", "100",
	}
	if globalClaudeModel != "" {
		args = append(args, "--model", globalClaudeModel)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	// Process-group isolation lets the pipe-silence watchdog kill
	// the entire CC subtree (including any node/claude forks) via
	// `kill -PGID` when stdout goes silent. Without this, a SIGKILL
	// to the parent can leave orphans that keep writing to the log
	// and confuse the outer loop's mtime-based watchdog.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	// pipeWatcher wraps the buffer so every byte that arrives on
	// CC's stdout updates the lastActivity timestamp. The silence
	// watchdog below reads that timestamp — NOT the buffer length
	// or any file mtime — so outer-loop heartbeat writes cannot
	// defeat it (see H-4, 2026-04-17).
	pipeW := newPipeWatcher(&out)
	cmd.Stdout = pipeW
	cmd.Stderr = os.Stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "  claude start error: %v\n", err)
		return ""
	}
	go func() { done <- cmd.Wait() }()

	// Watchdog: 30-s ticker, 5 min of zero growth = hung.
	// This is the same pattern the provider package uses for
	// long-running CLI invocations (internal/provider/claudecode.go).
	// We keep it as a fallback — it catches modes the pipe-silence
	// watchdog can't (e.g. the buffer grew earlier but then nothing
	// further arrives even though Write was called recently with
	// empty bytes). Both watchdogs are additive; whichever trips
	// first kills the process.
	watchdog := time.NewTicker(30 * time.Second)
	defer watchdog.Stop()
	lastSize := 0
	stale := 0
	const maxStale = 10 // 10 × 30s = 5 min of silence
	running := true
	var runErr error
	for running {
		select {
		case err := <-done:
			runErr = err
			running = false
		case <-watchdog.C:
			// Pipe-silence watchdog (primary): operates on the
			// stdout pipe directly, independent of any log file.
			if silence := pipeW.SilenceDuration(); silence >= ccPipeSilenceThreshold {
				fmt.Fprintf(os.Stderr,
					"  ⏱ CC pipe silence watchdog: %d min of no stdout → SIGKILL\n",
					int(silence/time.Minute))
				killChildProcessGroup(cmd, 2*time.Second)
				running = false
				break
			}
			// Buffer-growth watchdog (fallback).
			cur := out.Len()
			if cur == lastSize {
				stale++
				if stale >= maxStale {
					fmt.Fprintf(os.Stderr, "  ⛔ claude: no stream output for %ds — killing\n", maxStale*30)
					killChildProcessGroup(cmd, 2*time.Second)
					running = false
				}
			} else {
				stale = 0
				lastSize = cur
			}
		}
	}
	// Capture any run error in a form RecordResult can classify.
	// The production rate-limit signature is literally the bytes
	// `claude error: exit status 1`, so we roll that string into the
	// effective output when cmd.Wait returned non-nil.
	var errMarker string
	if runErr != nil && !strings.Contains(runErr.Error(), "killed") {
		fmt.Fprintf(os.Stderr, "  claude error: %v\n", runErr)
		errMarker = fmt.Sprintf("claude error: %v", runErr)
	}

	// stream-json emits one JSON object per line. The final line
	// is a `result` event with the .result + usage. Scan backward
	// to find it. If we don't find one (watchdog kill / truncation),
	// fall back to the raw bytes so the caller still has something.
	raw := out.Bytes()
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var result struct {
			Type     string  `json:"type"`
			Result   string  `json:"result"`
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal([]byte(line), &result) != nil {
			continue
		}
		if result.Type == "result" || result.Result != "" {
			fmt.Printf("  [CC: %d turns, $%.4f]\n", result.NumTurns, result.Cost)
			// H-10: classify outcome. If cmd errored, the errMarker
			// string is passed so the detector sees the "claude error"
			// signature; otherwise the result body is used.
			classifyOutput := result.Result
			if errMarker != "" {
				classifyOutput = errMarker + "\n" + classifyOutput
			}
			claudeBackoff.RecordResult(classifyOutput, result.NumTurns, result.Cost)
			return result.Result
		}
	}
	// No parseable result line. Feed the raw bytes + error marker so
	// the rate-limit detector can still classify, with turns=0 cost=0.
	rawOut := strings.TrimSpace(string(raw))
	classifyOutput := rawOut
	if errMarker != "" {
		classifyOutput = errMarker + "\n" + rawOut
	}
	claudeBackoff.RecordResult(classifyOutput, 0, 0)
	return rawOut
}

// codexCall invokes `codex exec` with JSONL output (so we can
// detect turn.completed/turn.failed inline) plus an output-growth
// watchdog that kills the process if stdout goes silent for 5 min.
// Reviewer calls are --sandbox read-only; codex has no business
// editing files when we ask it to review.
func codexCall(bin, dir, prompt string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	lastMsg := fmt.Sprintf("/tmp/codex-simple-%d.txt", time.Now().UnixNano())
	defer os.Remove(lastMsg)
	// H-21: on large repos, the aggregate findings can exceed ARG_MAX
	// (~128KB on Linux) when passed as a command-line arg. Switch to
	// stdin via codex's "-" sentinel whenever the prompt is over 64KB —
	// plenty of headroom below ARG_MAX while still using the faster
	// arg-path for the common case. Also see argMaxStdinThreshold in
	// this file.
	args := []string{"exec",
		"--json",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--output-last-message", lastMsg,
	}
	useStdin := len(prompt) > argMaxStdinThreshold
	if useStdin {
		args = append(args, "-")
	} else {
		args = append(args, prompt)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	watchdog := time.NewTicker(30 * time.Second)
	defer watchdog.Stop()
	lastSize := 0
	stale := 0
	const maxStale = 10 // 10 × 30s = 5 min of silence

	turnFailed := false
	for running := true; running; {
		select {
		case err := <-done:
			running = false
			if err != nil {
				fmt.Fprintf(os.Stderr, "  codex error: %v (stderr: %s)\n",
					err, strings.TrimSpace(stderr.String()))
			}
		case <-watchdog.C:
			cur := stdout.Len() + stderr.Len()
			if cur == lastSize {
				stale++
				if stale >= maxStale {
					fmt.Fprintf(os.Stderr, "  codex: no output for %ds — killing\n", maxStale*30)
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					running = false
				}
			} else {
				stale = 0
				lastSize = cur
			}
			// Scan new JSONL events for turn.failed / usage_limit / 429
			for _, line := range strings.Split(stdout.String(), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "{") {
					continue
				}
				var ev struct{ Type string `json:"type"` }
				if json.Unmarshal([]byte(line), &ev) == nil {
					if ev.Type == "turn.failed" {
						turnFailed = true
					}
				}
			}
			if strings.Contains(stderr.String(), "429") ||
				strings.Contains(stderr.String(), "usage limit") {
				fmt.Fprintf(os.Stderr, "  codex rate-limited (stderr contains 429/usage-limit)\n")
			}
		}
	}

	if turnFailed {
		fmt.Fprintf(os.Stderr, "  codex reported turn.failed\n")
	}

	// Prefer the output-last-message file (clean final text).
	// Retry briefly — codex flushes the file slightly after exit.
	var data []byte
	for i := 0; i < 10; i++ {
		data, _ = os.ReadFile(lastMsg)
		if len(data) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(data) == 0 {
		// Fallback: extract final agent_message from JSONL stream.
		data = []byte(extractCodexFinalMessage(stdout.String()))
	}
	return strings.TrimSpace(string(data))
}

// extractCodexFinalMessage parses codex JSONL stdout and returns
// the text of the last `item.completed` event with type
// `agent_message`. Used as a fallback when --output-last-message
// hasn't flushed yet.
func extractCodexFinalMessage(jsonl string) string {
	var last string
	for _, line := range strings.Split(jsonl, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "item.completed" && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
			last = ev.Item.Text
		}
	}
	return last
}

func shellCmd(dir, cmd string) string {
	out, _ := exec.Command("bash", "-lc", "cd "+dir+" && "+cmd).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func detectSimpleBuildCmd(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err == nil {
		return "npx tsc --noEmit 2>&1 || echo 'tsc not available'"
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		// Check if there's a build script
		data, _ := os.ReadFile(filepath.Join(dir, "package.json"))
		var pkg map[string]interface{}
		if json.Unmarshal(data, &pkg) == nil {
			if scripts, ok := pkg["scripts"].(map[string]interface{}); ok {
				if _, ok := scripts["build"]; ok {
					return "pnpm build 2>&1 || npm run build 2>&1"
				}
				if _, ok := scripts["typecheck"]; ok {
					return "pnpm typecheck 2>&1"
				}
			}
		}
		return "echo 'no build script'"
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "go build ./... 2>&1"
	}
	return "echo 'no build detected'"
}

// dispatchSequentialFix sends the entire reviewer feedback as
// one prompt to a single CC worker. Simple, no concurrency, no
// git conflicts. One fat claudeCall; the worker iterates through
// every flagged item within its --max-turns budget.
func dispatchSequentialFix(bin, dir, feedback string) {
	fmt.Println("    → sequential: 1 CC worker fixing the full feedback")
	claudeCall(bin, dir, fmt.Sprintf(
		"The reviewer has flagged specific issues in your code. "+
			"Fix EVERY single one. Read each affected file carefully. "+
			"After each fix run the build (tsc --noEmit or the project's "+
			"build command). Commit each fix with a descriptive message. "+
			"Only fix what the reviewer flagged — do not add features.\n\n"+
			"REVIEWER FEEDBACK:\n%s", feedback))
}

// dispatchParallelFix splits the reviewer feedback into discrete
// issues and launches up to `workers` CC workers concurrently.
// Each worker owns one chunk. Git state is shared across
// workers — concurrent writes to different files are fine; the
// real contention is on commit. We do NOT serialize commits
// ourselves because `git commit` is atomic in the index and CC
// workers naturally stagger by processing different issues.
// On rare conflicts CC re-resolves via the build step. Returns
// once all workers finish.
func dispatchParallelFix(bin, dir, feedback string, workers int) {
	issues := splitReviewIntoIssues(feedback, workers)
	fmt.Printf("    → parallel: %d issue chunk(s) across up to %d worker(s)\n",
		len(issues), workers)
	if len(issues) == 0 {
		return
	}
	sem := make(chan struct{}, workers)
	done := make(chan struct{}, len(issues))
	for i, issue := range issues {
		go func(idx int, text string) {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			fmt.Printf("    [worker %d/%d] starting\n", idx+1, len(issues))
			claudeCall(bin, dir, fmt.Sprintf(
				"You are parallel worker %d of %d, all running concurrently on the same repo. "+
					"Other workers may be editing different files RIGHT NOW and committing "+
					"between your tool calls. Follow these rules exactly:\n"+
					"  1. BEFORE editing any file, read it fresh (Read tool) to see the latest state.\n"+
					"  2. BEFORE reading, run `git status` and `git log --oneline -10` to see what "+
					"other workers have committed since you started.\n"+
					"  3. If a file you planned to edit was just changed, re-read it and reconcile — "+
					"do NOT overwrite another worker's fix.\n"+
					"  4. Keep edits small and committed one at a time. After each commit run "+
					"`git pull --rebase origin HEAD 2>/dev/null || true` (no-op in single-branch "+
					"repos but safe to run).\n"+
					"  5. Stick strictly to files required by YOUR assigned issue. Do not touch "+
					"files you cannot justify from the issue description.\n"+
					"  6. Run the build (tsc --noEmit or project build) after each fix. If the "+
					"build breaks because of something NOT in your issue, that's another "+
					"worker's in-flight change — wait 30s and retry once before giving up.\n"+
					"  7. Commit with a message that starts with `fix(parallel-%d):` so humans can "+
					"see which worker made which change.\n"+
					"  8. Do not add new features. Only fix what the reviewer flagged below.\n\n"+
					"YOUR ASSIGNED ISSUE (%d of %d):\n%s",
				idx+1, len(issues), idx+1, idx+1, len(issues), text))
			fmt.Printf("    [worker %d/%d] done\n", idx+1, len(issues))
		}(i, issue)
	}
	for range issues {
		<-done
	}
}
