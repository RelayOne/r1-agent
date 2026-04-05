package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/failure"
	"github.com/ericmacdougall/stoke/internal/hooks"
	"github.com/ericmacdougall/stoke/internal/logging"
	"github.com/ericmacdougall/stoke/internal/model"
	stokeprompts "github.com/ericmacdougall/stoke/internal/prompts"
	"github.com/ericmacdougall/stoke/internal/scan"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/wisdom"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

// WorktreeManager abstracts git worktree operations for creating, merging, and cleaning up isolated workspaces.
type WorktreeManager interface {
	Prepare(ctx context.Context, explicitName string) (worktree.Handle, error)
	Merge(ctx context.Context, handle worktree.Handle, message string) error
	Cleanup(ctx context.Context, handle worktree.Handle) error
}

// TaskHook is the plugin interface for extending the workflow lifecycle.
// Dead packages become hooks: wisdom implements AfterTask/BeforeRetry,
// costtrack implements AfterTask, critic implements BeforeRetry, etc.
// All methods are optional — implement only the ones you need.
type TaskHook interface {
	// BeforeTask is called before execution starts. Can inject context or
	// reject a task. Return a non-nil error to abort the task.
	BeforeTask(ctx context.Context, task string, state *taskstate.TaskState) error

	// AfterTask is called after execution completes (success or failure).
	// Used for recording costs, learning patterns, or cleanup.
	AfterTask(ctx context.Context, task string, state *taskstate.TaskState, result Result) error

	// BeforeRetry is called before a retry attempt. Returns additional prompt
	// context to inject (e.g., learned fixes from prior failures).
	BeforeRetry(ctx context.Context, task string, attempt int, analysis *failure.Analysis) (promptAugment string, err error)
}

// Engine drives the plan/execute/verify workflow loop for a single task, including retries and merge.
type Engine struct {
	RepoRoot         string
	Task             string
	TaskType         model.TaskType
	TaskVerification []string // per-task verification checklist from planner
	WorktreeName     string
	AllowedFiles     []string
	AuthMode         engine.AuthMode
	Policy           config.Policy
	DryRun           bool
	Pools            *subscriptions.Manager
	Worktrees        WorktreeManager
	Runners          engine.Registry
	Verifier         *verify.Pipeline
	ClaudeConfigDir  string
	CodexHome        string
	OnEvent          engine.OnEventFunc
	State            *taskstate.TaskState
	Wisdom           *wisdom.Store       // cross-task learning accumulator (nil = disabled)
	CostTracker      *costtrack.Tracker  // per-session cost tracking (nil = disabled)
	Hooks            []TaskHook          // lifecycle hooks (nil = no hooks)
	PlanOnly         bool
}

// Result captures the outcome of a complete workflow execution, including steps, verification, and cost.
type Result struct {
	WorktreePath string
	Branch       string
	TaskType     model.TaskType
	DryRun       bool
	PlanOutput   string // populated in PlanOnly mode
	Steps        []StepResult
	Verification []verify.Outcome
	TotalCostUSD float64
	FilesChanged []string // files modified by the workflow (post-review validated set)
}

// StepResult records the phase name, engine used, and prepared command for one workflow step.
type StepResult struct {
	Phase   string
	Engine  string
	Command engine.PreparedCommand
}

// Render formats the workflow result as a human-readable summary string.
func (r Result) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Stoke workflow result\n")
	fmt.Fprintf(&b, "Task type: %s\n", r.TaskType)
	fmt.Fprintf(&b, "Worktree: %s\n", r.WorktreePath)
	fmt.Fprintf(&b, "Branch: %s\n", r.Branch)
	if r.TotalCostUSD > 0 {
		fmt.Fprintf(&b, "Cost: $%.4f\n", r.TotalCostUSD)
	}
	if r.DryRun {
		fmt.Fprintf(&b, "Mode: dry-run\n")
	}
	fmt.Fprintf(&b, "\nSteps:\n")
	for _, step := range r.Steps {
		fmt.Fprintf(&b, "- [%s via %s] %s %s\n", step.Phase, step.Engine, step.Command.Binary, strings.Join(step.Command.Args, " "))
	}
	if len(r.Verification) > 0 {
		fmt.Fprintf(&b, "\nVerification:\n")
		for _, outcome := range r.Verification {
			status := "ok"
			if outcome.Skipped {
				status = "skipped"
			} else if !outcome.Success {
				status = "failed"
			}
			fmt.Fprintf(&b, "- %s: %s\n", outcome.Name, status)
		}
	}
	return b.String()
}

// Run executes the full workflow: creates a worktree, runs plan/execute/verify phases with retries, and merges on success.
func (e Engine) Run(ctx context.Context) (Result, error) {
	name := firstNonEmpty(e.WorktreeName, string(e.TaskType)+"-"+slugFromTask(e.Task))
	log := logging.Task("workflow", name)
	var handle worktree.Handle
	if e.DryRun {
		runtimeDir := filepath.Join(os.TempDir(), "stoke-runtime-dryrun-"+name)
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			return Result{}, fmt.Errorf("create runtime dir: %w", err)
		}
		handle = worktree.Handle{
			Name:       name,
			Branch:     "stoke/" + name,
			Path:       filepath.Join(e.RepoRoot, ".stoke", "worktrees", name),
			RuntimeDir: runtimeDir,
		}
	} else {
		var err error
		handle, err = e.Worktrees.Prepare(ctx, name)
		if err != nil {
			return Result{}, err
		}
		// Install enforcer hooks in the worktree (§9 Layer 9)
		if hookErr := hooks.Install(handle.RuntimeDir); hookErr != nil {
			e.Worktrees.Cleanup(ctx, handle)
			return Result{}, fmt.Errorf("hook install failed (safety boundary): %w", hookErr)
		}
	}

	result := Result{WorktreePath: handle.Path, Branch: handle.Branch, TaskType: e.TaskType, DryRun: e.DryRun}

	// Invoke BeforeTask hooks
	for _, h := range e.Hooks {
		if err := h.BeforeTask(ctx, e.Task, e.State); err != nil {
			e.Worktrees.Cleanup(ctx, handle)
			return result, fmt.Errorf("hook BeforeTask: %w", err)
		}
	}

	// Invoke AfterTask hooks on exit (best-effort, errors logged not fatal)
	defer func() {
		for _, h := range e.Hooks {
			_ = h.AfterTask(ctx, e.Task, e.State, result)
		}
	}()

	phases := buildPhases(e)

	// Dry run: just prepare commands, don't execute
	if e.DryRun {
		for _, phase := range phases {
			runnerName, runner := pickRunner(e, phase.Name)
			spec := e.buildSpec(phase, handle)
			prepared, err := runner.Prepare(spec)
			if err != nil {
				os.RemoveAll(handle.RuntimeDir)
				return result, err
			}
			result.Steps = append(result.Steps, StepResult{Phase: phase.Name, Engine: runnerName, Command: prepared})
		}
		os.RemoveAll(handle.RuntimeDir)
		return result, nil
	}

	// --- PLAN phase (no retry) ---
	// Advance state: Pending -> Claimed (harness takes ownership)
	if err := e.advanceState(taskstate.Claimed, "harness dispatching to plan phase"); err != nil {
		return result, err
	}

	planPhase := phases[0]
	planRunner, planEngine := pickRunner(e, planPhase.Name)
	planSpec := e.buildSpec(planPhase, handle)
	planResult, err := planEngine.Run(ctx, planSpec, e.OnEvent)
	if err != nil {
		_ = e.advanceState(taskstate.Failed, "plan phase failed: "+err.Error())
		return result, fmt.Errorf("plan phase: %w", err)
	}
	result.Steps = append(result.Steps, StepResult{Phase: "plan", Engine: planRunner, Command: planResult.Prepared})
	result.TotalCostUSD += planResult.CostUSD

	// --- PLAN-ONLY MODE: structurally prevents execute/verify/commit/merge ---
	// This is not a prompt instruction. The harness does not call execute.
	if e.PlanOnly {
		result.PlanOutput = planResult.ResultText
		e.Worktrees.Cleanup(ctx, handle)
		return result, nil
	}

	// --- EXECUTE + VERIFY loop (with retry) ---
	maxAttempts := 3
	executePhase := phases[1]
	verifyPhase := phases[2]
	var lastFailure *failure.Analysis
	var lastDiff string
	var priorFingerprints []failure.Fingerprint // track failure fingerprints across attempts

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Info("starting attempt", "attempt", attempt, "max", maxAttempts)

		// §7: "Each retry starts from a clean worktree (fresh copy of main)."
		// The learning is in the INSTRUCTIONS (retry brief), not in code state.
		if attempt > 1 {
			lastDiff = worktree.DiffSummary(ctx, handle)
			e.Worktrees.Cleanup(ctx, handle)
			retryName := fmt.Sprintf("%s-attempt-%d", name, attempt)
			var prepErr error
			handle, prepErr = e.Worktrees.Prepare(ctx, retryName)
			if prepErr != nil {
				return result, fmt.Errorf("prepare retry worktree: %w", prepErr)
			}
			if hookErr := hooks.Install(handle.RuntimeDir); hookErr != nil {
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("hook install failed (safety boundary): %w", hookErr)
			}
			result.WorktreePath = handle.Path
			result.Branch = handle.Branch
		}

		// Build execute prompt
		prompt := executePhase.Prompt
		if attempt > 1 && lastFailure != nil {
			prompt = buildRetryPrompt(prompt, attempt, lastFailure, lastDiff)
			// Invoke BeforeRetry hooks for additional prompt augmentation
			for _, h := range e.Hooks {
				if aug, err := h.BeforeRetry(ctx, e.Task, attempt, lastFailure); err == nil && aug != "" {
					prompt += "\n\n" + aug
				}
			}
		}
		// Inject cross-task learnings from previous tasks in this session.
		if e.Wisdom != nil {
			if wisdomCtx := e.Wisdom.ForPrompt(); wisdomCtx != "" {
				prompt = prompt + "\n\n" + wisdomCtx
			}
		}

		// Run execute phase
		currentPhase := executePhase
		currentPhase.Prompt = prompt
		execRunnerName, execRunner := pickRunner(e, currentPhase.Name)

		// Pool-aware execution with rotation on rate limit
		var execResult engine.RunResult
		var acquiredPoolID string
		triedPools := map[string]bool{} // track which pools we've tried this attempt
		maxPoolRotations := 5            // don't spin forever

		// Determine provider for pool acquisition and rotation (must be outside rotation loop scope)
		execProvider := subscriptions.ProviderClaude
		if execRunnerName == string(model.ProviderCodex) {
			execProvider = subscriptions.ProviderCodex
		}

		for rotation := 0; rotation < maxPoolRotations; rotation++ {
			execSpec := e.buildSpec(currentPhase, handle)

			// Acquire a pool (excluding already-tried ones)
			if e.Pools != nil {
				var pool subscriptions.Pool
				var acqErr error
				if len(triedPools) == 0 {
					pool, acqErr = e.Pools.Acquire(execProvider, fmt.Sprintf("%s-attempt-%d", name, attempt))
				} else {
					pool, acqErr = e.Pools.AcquireExcluding(execProvider, fmt.Sprintf("%s-attempt-%d", name, attempt), triedPools)
				}

				if acqErr != nil {
					// All pools tried -- wait for one to come back
					if e.Pools.PoolCount(execProvider) > 1 {
						waitCtx, waitCancel := context.WithTimeout(ctx, 6*time.Minute)
						pool, acqErr = e.Pools.WaitForPool(waitCtx, execProvider, fmt.Sprintf("%s-attempt-%d-wait", name, attempt))
						waitCancel()
					}
					if acqErr != nil {
						return result, fmt.Errorf("all pools exhausted for %s: %w", execProvider, acqErr)
					}
				}
				acquiredPoolID = pool.ID
				triedPools[pool.ID] = true
				execSpec.PoolConfigDir = pool.ConfigDir
			}

			var runErr error
			execResult, runErr = execRunner.Run(ctx, execSpec, e.OnEvent)

			// Release pool
			if acquiredPoolID != "" && e.Pools != nil {
				rateLimited := execResult.Subtype == "rate_limited"
				e.Pools.Release(acquiredPoolID, rateLimited)
			}

			if runErr != nil {
				_ = e.advanceState(taskstate.Failed, fmt.Sprintf("execute phase attempt %d failed: %s", attempt, runErr))
				return result, fmt.Errorf("execute phase (attempt %d): %w", attempt, runErr)
			}

			// Treat non-rate-limit error states (timeout, stream failure, etc.) as
			// execution failures. The agent may have produced partial output, but
			// we must not verify/review an incomplete execution.
			if execResult.IsError && execResult.Subtype != "rate_limited" {
				_ = e.advanceState(taskstate.Failed, fmt.Sprintf("execute phase attempt %d: agent error (%s): %s",
					attempt, execResult.Subtype, truncate(execResult.ResultText, 200)))
				return result, fmt.Errorf("execute phase (attempt %d): agent reported error (subtype=%s)", attempt, execResult.Subtype)
			}

			// Rate limited? Rotate to another pool (using the actual provider, not hardcoded Claude)
			if execResult.Subtype == "rate_limited" {
				if e.Pools != nil && e.Pools.PoolCount(execProvider) > 1 {
					// Clean worktree and retry with different pool
					e.Worktrees.Cleanup(ctx, handle)
					retryName := fmt.Sprintf("%s-attempt-%d-rot-%d", name, attempt, rotation+1)
					var prepErr error
					handle, prepErr = e.Worktrees.Prepare(ctx, retryName)
					if prepErr != nil {
						return result, fmt.Errorf("prepare rotation worktree: %w", prepErr)
					}
					if hookErr := hooks.Install(handle.RuntimeDir); hookErr != nil {
						e.Worktrees.Cleanup(ctx, handle)
						return result, fmt.Errorf("hook install failed (safety boundary): %w", hookErr)
					}
					result.WorktreePath = handle.Path
					result.Branch = handle.Branch
					continue // try next pool
				}
				// Single pool, no rotation possible
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("rate limited during execute phase (single pool, no rotation available)")
			}

			break // success or non-rate-limit failure -- exit rotation loop
		}

		// State: agent CLAIMS done (not verified yet -- model proposes, harness decides)
		attemptStart := time.Now().Add(-time.Duration(execResult.DurationMs) * time.Millisecond)
		logging.Attempt(log, name, attempt, true, execResult.DurationMs)

		result.Steps = append(result.Steps, StepResult{
			Phase: fmt.Sprintf("execute (attempt %d)", attempt), Engine: execRunnerName, Command: execResult.Prepared,
		})
		result.TotalCostUSD += execResult.CostUSD
		if e.CostTracker != nil && execResult.CostUSD > 0 {
			e.CostTracker.Record(execRunnerName, e.Task, execResult.Tokens.Input, execResult.Tokens.Output, execResult.Tokens.CacheRead, execResult.Tokens.CacheCreation)
		}

		// --- VERIFY ---
		// Honor Policy.Verification flags: only run enabled checks.
		verifier := e.Verifier
		if !e.Policy.Verification.Build || !e.Policy.Verification.Tests || !e.Policy.Verification.Lint {
			// Build a filtered verifier respecting policy flags
			filteredBuild, filteredTest, filteredLint := verifier.Commands()
			if !e.Policy.Verification.Build { filteredBuild = "" }
			if !e.Policy.Verification.Tests { filteredTest = "" }
			if !e.Policy.Verification.Lint { filteredLint = "" }
			verifier = verify.NewPipeline(filteredBuild, filteredTest, filteredLint)
		}
		outcomes, verifyErr := verifier.Run(ctx, handle.Path)
		result.Verification = outcomes

		// Build evidence for this attempt
		evidence := taskstate.Evidence{
			DiffSummary: worktree.DiffSummary(ctx, handle),
		}
		for _, o := range outcomes {
			switch o.Name {
			case "build":
				evidence.BuildOutput = o.Output
				evidence.BuildPass = o.Success
			case "test":
				evidence.TestOutput = o.Output
				evidence.TestPass = o.Success
			case "lint":
				evidence.LintOutput = o.Output
				evidence.LintPass = o.Success
			}
		}

		if verifyErr == nil {
			// --- SCOPE ENFORCEMENT (pre-review validation) ---
			// Harness runtime files are now in RuntimeDir (outside worktree).
			// Any .stoke/ path in the worktree is agent-created and suspicious.
			modifiedFiles, modErr := worktree.ModifiedFiles(ctx, handle)
			if modErr != nil {
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Failed, "cannot enumerate modified files: "+modErr.Error())
				return result, fmt.Errorf("modified files check failed: %w", modErr)
			}

			// Detect gitignored files created by the agent. These are invisible
			// to git add -A and won't be in the merged commit, but build/test
			// may depend on them. FAIL CLOSED: if the verified environment
			// includes files that can't ship, the verification is invalid.
			if ignored := worktree.IgnoredNewFiles(ctx, handle); len(ignored) > 0 {
				evidence.Warnings = append(evidence.Warnings,
					fmt.Sprintf("agent created %d gitignored file(s) not in merge: %v", len(ignored), ignored))
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Failed, fmt.Sprintf(
					"verified environment diverges from merge artifact: %d gitignored file(s) would be lost: %v",
					len(ignored), ignored))
				return result, fmt.Errorf("gitignored files in verified tree (verified != merged): %v", ignored)
			}

			protectedViolations := verify.CheckProtectedFiles(modifiedFiles, e.Policy.Files.Protected)
			evidence.ProtectedClean = len(protectedViolations) == 0
			if !evidence.ProtectedClean {
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Failed, fmt.Sprintf("protected files modified: %v", protectedViolations))
				return result, fmt.Errorf("protected file(s) modified: %v", protectedViolations)
			}

			scopeClean := true
			if len(e.AllowedFiles) > 0 && e.Policy.Verification.ScopeCheck {
				scopeViolations := verify.CheckScope(modifiedFiles, e.AllowedFiles)
				scopeClean = len(scopeViolations) == 0
				if !scopeClean {
					evidence.ScopeClean = false
					e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
					e.Worktrees.Cleanup(ctx, handle)
					_ = e.advanceState(taskstate.Failed, fmt.Sprintf("out-of-scope: %v", scopeViolations))
					return result, fmt.Errorf("out-of-scope file(s) modified: %v", scopeViolations)
				}
			}
			evidence.ScopeClean = scopeClean

			// --- FORBIDDEN-PATTERN SCAN ---
			scanResult, scanErr := scan.ScanFiles(handle.Path, scan.DefaultRules(), modifiedFiles)
			if scanErr == nil && scanResult.HasBlocking() {
				evidence.ReviewPass = false
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Failed, "blocking scan findings: "+scanResult.Summary())
				return result, fmt.Errorf("forbidden patterns found: %s", scanResult.Summary())
			}

			// State: Executed -> Verified (harness confirmed build+test+lint+scope)
			if err := e.advanceState(taskstate.Executed, "agent returned, harness verifying"); err != nil {
				return result, err
			}
			if err := e.advanceState(taskstate.Verified, "build pass, test pass, lint pass, scope clean"); err != nil {
				return result, err
			}

			// Save pre-review state: file list + tree SHA.
			// TreeSHA captures content, modes, and structure in one hash.
			preReviewFiles := make([]string, len(modifiedFiles))
			copy(preReviewFiles, modifiedFiles)
			preReviewTree, treeErr := worktree.TreeSHA(ctx, handle)
			if treeErr != nil {
				preReviewTree = "" // fall back to file-set-only comparison
			}

			// --- CROSS-MODEL REVIEW ---
			// When enabled, run a cross-model review and post-review revalidation.
			// When disabled by policy, skip straight to commit with pre-review files.
			postReviewFiles := preReviewFiles
			if e.Policy.Verification.CrossModelReview {
				reviewFiles, reviewErr := e.runCrossModelReview(ctx, name, handle, verifyPhase, preReviewFiles, preReviewTree, &evidence, &result, attempt, attemptStart, execRunnerName, execResult)
				if reviewErr != nil {
					return result, reviewErr
				}
				postReviewFiles = reviewFiles
			} else {
				evidence.ReviewPass = true
				evidence.ReviewOutput = "cross-model review disabled by policy"
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
				if err := e.advanceState(taskstate.Reviewed, "review skipped (policy)"); err != nil {
					return result, err
				}
			}

			// --- MERGE GATES: evidence AND state must agree ---
			if !evidence.AllGatesPass() {
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("merge blocked: gates failed: %v", evidence.FailedGates())
			}
			if !e.State.CanCommit() {
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("merge blocked: state not committable (phase=%s)", e.State.Phase())
			}
			// --- COMMIT AND MERGE ---
			// Record validated file set in result for callers (e.g., mission bridge).
			result.FilesChanged = postReviewFiles

			// CommitVerifiedTree builds one clean commit from BaseCommit containing
			// ONLY the validated files. No intermediate agent commits survive.
			commitMsg := fmt.Sprintf("feat(%s): %s", slugFromTask(e.Task), e.Task)
			commitErr := worktree.CommitVerifiedTree(ctx, handle, postReviewFiles, commitMsg)
			if errors.Is(commitErr, worktree.ErrNothingToCommit) {
				// True no-op: validated file set is empty. Skip merge entirely.
				// This prevents net-zero-diff branches (add then remove) from
				// leaking unverified agent commits through merge.
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Committed, "no changes to merge (empty validated set)")
				break
			}
			if commitErr != nil {
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("commit: %w", commitErr)
			}
			if valErr := worktree.ValidateMerge(ctx, handle); valErr != nil {
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("merge validation: %w", valErr)
			}
			if mergeErr := e.Worktrees.Merge(ctx, handle, commitMsg); mergeErr != nil {
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("merge: %w", mergeErr)
			}

			// State: Reviewed -> Committed (Stoke merged to main)
			if err := e.advanceState(taskstate.Committed, "merged to main by harness"); err != nil {
				return result, err
			}

			log.Info("task completed successfully", "attempt", attempt, "cost_usd", result.TotalCostUSD)
			logging.Cost(log, name, result.TotalCostUSD, execRunnerName)

			// Record successful completion as a wisdom pattern.
			if e.Wisdom != nil && attempt > 1 {
				e.Wisdom.Record(e.Task, wisdom.Learning{
					Category:    wisdom.Decision,
					Description: fmt.Sprintf("succeeded on attempt %d after retry", attempt),
				})
			}
			break
		}

		// Verification FAILED -- record evidence BEFORE analyzing
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)

		analysis := verify.AnalyzeOutcomes(outcomes, evidence.DiffSummary)
		if analysis == nil {
			break
		}
		log.Warn("verification failed", "attempt", attempt, "class", string(analysis.Class), "summary", analysis.Summary)

		// Compute failure fingerprint for dedup across retries and tasks.
		fp := failure.Compute(analysis)
		if matched, count := failure.MatchHistory(fp, priorFingerprints); matched != nil && count > 0 {
			log.Error("fingerprint dedup: same failure repeated", "pattern", fp.Pattern, "count", count+1)
			_ = e.advanceState(taskstate.Failed,
				fmt.Sprintf("same failure repeated (%s, seen %dx) -- escalating to human", fp.Pattern, count+1))
			e.Worktrees.Cleanup(ctx, handle)
			return result, fmt.Errorf("fingerprint dedup: same failure %q repeated %d times", fp.Pattern, count+1)
		}
		priorFingerprints = append(priorFingerprints, fp)

		// Use failure.ShouldRetry for the retry/escalate decision
		decision := failure.ShouldRetry(analysis, attempt, lastFailure)
		if decision.Action == failure.Escalate {
			_ = e.advanceState(taskstate.Failed, "escalating to human: "+decision.Reason)
			e.Worktrees.Cleanup(ctx, handle)
			return result, fmt.Errorf("escalating: %s", decision.Reason)
		}

		lastFailure = analysis

		// Record failure as a wisdom gotcha for subsequent tasks, with fingerprint
		// so cross-task dedup can detect if task B hits the same pattern as task A.
		if e.Wisdom != nil {
			desc := analysis.Summary
			if analysis.RootCause != "" {
				desc = analysis.RootCause
			}
			e.Wisdom.Record(e.Task, wisdom.Learning{
				Category:       wisdom.Gotcha,
				Description:    desc,
				FailurePattern: fp.Hash,
			})
		}
	}

	return result, nil
}

// runCrossModelReview executes the cross-model review phase including
// post-review revalidation. Returns the validated file list or an error.
// This is extracted from Run() to allow the review to be policy-gated.
func (e Engine) runCrossModelReview(
	ctx context.Context,
	name string,
	handle worktree.Handle,
	verifyPhase engine.PhaseSpec,
	preReviewFiles []string,
	preReviewTree string,
	evidence *taskstate.Evidence,
	result *Result,
	attempt int,
	attemptStart time.Time,
	execRunnerName string,
	execResult engine.RunResult,
) ([]string, error) {
	verifyRunnerName, verifyRunner := pickRunner(e, verifyPhase.Name)

	reviewProvider := subscriptions.ProviderCodex
	if verifyRunnerName == string(model.ProviderClaude) {
		reviewProvider = subscriptions.ProviderClaude
	}

	var verifyResult engine.RunResult
	var reviewErr error
	var reviewReadCount int // track how many Read tool uses the reviewer made
	triedReviewPools := map[string]bool{}
	maxReviewRotations := 5

	// Wrap OnEvent to count Read tool uses during review
	reviewOnEvent := func(ev stream.Event) {
		for _, tu := range ev.ToolUses {
			if tu.Name == "Read" {
				reviewReadCount++
			}
		}
		if e.OnEvent != nil {
			e.OnEvent(ev)
		}
	}

	for reviewRot := 0; reviewRot < maxReviewRotations; reviewRot++ {
		_ = reviewRot // used only as loop counter
		verifySpec := e.buildSpec(verifyPhase, handle)
		verifySpec.Prompt = stokeprompts.BuildVerifyPrompt(e.Task, e.TaskVerification, preReviewFiles...) +
			"\n\n## Diff summary\n" +
			worktree.DiffSummary(ctx, handle)

		var verifyPoolID string
		if e.Pools != nil {
			var pool subscriptions.Pool
			var acqErr error
			if len(triedReviewPools) == 0 {
				pool, acqErr = e.Pools.Acquire(reviewProvider, "review-"+name)
			} else {
				pool, acqErr = e.Pools.AcquireExcluding(reviewProvider, "review-"+name, triedReviewPools)
			}
			if acqErr != nil && e.Pools.PoolCount(reviewProvider) > 1 {
				waitCtx, waitCancel := context.WithTimeout(ctx, 6*time.Minute)
				pool, acqErr = e.Pools.WaitForPool(waitCtx, reviewProvider, "review-wait-"+name)
				waitCancel()
			}
			if acqErr == nil {
				verifyPoolID = pool.ID
				triedReviewPools[pool.ID] = true
				verifySpec.PoolConfigDir = pool.ConfigDir
			}
		}

		verifyResult, reviewErr = verifyRunner.Run(ctx, verifySpec, reviewOnEvent)

		if verifyPoolID != "" && e.Pools != nil {
			rateLimited := verifyResult.Subtype == "rate_limited"
			e.Pools.Release(verifyPoolID, rateLimited)
			if rateLimited && e.Pools.PoolCount(reviewProvider) > 1 {
				continue
			}
		}
		break
	}

	evidence.ReviewEngine = verifyRunnerName
	if reviewErr != nil {
		evidence.ReviewPass = false
		evidence.ReviewOutput = reviewErr.Error()
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "cross-model review failed to execute")
		return nil, fmt.Errorf("cross-model review failed: %w", reviewErr)
	}
	result.Steps = append(result.Steps, StepResult{
		Phase: "verify", Engine: verifyRunnerName, Command: verifyResult.Prepared,
	})
	result.TotalCostUSD += verifyResult.CostUSD
	evidence.ReviewOutput = verifyResult.ResultText

	verdict, parseErr := parseReviewVerdict(verifyResult.ResultText)
	if parseErr != nil {
		evidence.ReviewPass = false
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "review returned invalid JSON")
		return nil, fmt.Errorf("cross-model review returned invalid JSON: %v", parseErr)
	}

	// Review quality gate: reviewer must have used Read tools proportional
	// to the number of changed files. A reviewer that reads nothing is invalid.
	minReads := len(preReviewFiles)
	if minReads < 1 {
		minReads = 1
	}
	if reviewReadCount < minReads {
		evidence.ReviewPass = false
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, fmt.Sprintf("review quality: %d Read tool uses, need at least %d (one per changed file)", reviewReadCount, minReads))
		return nil, fmt.Errorf("cross-model review quality gate failed: %d Read uses < %d changed files", reviewReadCount, minReads)
	}

	evidence.ReviewPass = verdict.Pass
	if !verdict.Pass {
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "cross-model review rejected")
		return nil, fmt.Errorf("cross-model review rejected: %s severity, %d findings", verdict.Severity, len(verdict.Findings))
	}

	// --- POST-REVIEW REVALIDATION ---
	postReviewFiles, postModErr := worktree.ModifiedFiles(ctx, handle)
	if postModErr != nil {
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "post-review file check failed: "+postModErr.Error())
		return nil, fmt.Errorf("post-review validation failed: %w", postModErr)
	}

	preSet := make(map[string]bool, len(preReviewFiles))
	for _, f := range preReviewFiles {
		preSet[f] = true
	}
	postSet := make(map[string]bool, len(postReviewFiles))
	for _, f := range postReviewFiles {
		postSet[f] = true
	}

	var setDiffs []string
	for f := range postSet {
		if !preSet[f] {
			setDiffs = append(setDiffs, "+"+f)
		}
	}
	for f := range preSet {
		if !postSet[f] {
			setDiffs = append(setDiffs, "-"+f)
		}
	}
	if len(setDiffs) > 0 {
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, fmt.Sprintf("review mutated file set: %v", setDiffs))
		return nil, fmt.Errorf("post-review validation failed: file set changed: %v", setDiffs)
	}

	if preReviewTree != "" {
		postReviewTree, postTreeErr := worktree.TreeSHA(ctx, handle)
		if postTreeErr == nil && postReviewTree != preReviewTree {
			e.Worktrees.Cleanup(ctx, handle)
			_ = e.advanceState(taskstate.Failed, "review mutated working tree (tree SHA mismatch)")
			return nil, fmt.Errorf("post-review validation failed: tree SHA changed (pre=%s post=%s)", preReviewTree[:12], postReviewTree[:12])
		}
	}

	postProtected := verify.CheckProtectedFiles(postReviewFiles, e.Policy.Files.Protected)
	if len(postProtected) > 0 {
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, fmt.Sprintf("post-review protected violation: %v", postProtected))
		return nil, fmt.Errorf("post-review protected file violation: %v", postProtected)
	}
	if len(e.AllowedFiles) > 0 {
		postScope := verify.CheckScope(postReviewFiles, e.AllowedFiles)
		if len(postScope) > 0 {
			e.Worktrees.Cleanup(ctx, handle)
			_ = e.advanceState(taskstate.Failed, fmt.Sprintf("post-review scope violation: %v", postScope))
			return nil, fmt.Errorf("post-review scope violation: %v", postScope)
		}
	}
	postScan, postScanErr := scan.ScanFiles(handle.Path, scan.DefaultRules(), postReviewFiles)
	if postScanErr == nil && postScan.HasBlocking() {
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "post-review scan: "+postScan.Summary())
		return nil, fmt.Errorf("post-review forbidden patterns: %s", postScan.Summary())
	}

	e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
	if err := e.advanceState(taskstate.Reviewed, fmt.Sprintf("%s review: approved", verifyRunnerName)); err != nil {
		return nil, err
	}

	return postReviewFiles, nil
}

// advanceState transitions the task state machine.
// State is required (no legacy mode). Invalid transitions are fatal.
func (e Engine) advanceState(to taskstate.Phase, reason string) error {
	return e.State.Advance(to, reason)
}

// recordAttemptEvidence records an attempt with evidence on the state machine.
// ProposedSummary is the model's UNTRUSTED claim. FailureCodes are derived from evidence.
func (e Engine) recordAttemptEvidence(number int, startedAt time.Time, engineName string, proposedSummary string, ev taskstate.Evidence) {
	if e.State != nil {
		// Derive failure codes from evidence (harness decides, not model)
		var codes []taskstate.FailureCode
		var details []taskstate.FailureDetail
		if !ev.BuildPass {
			codes = append(codes, taskstate.FailureBuildFailed)
			details = append(details, taskstate.FailureDetail{Code: taskstate.FailureBuildFailed, Message: truncStr(ev.BuildOutput, 200)})
		}
		if !ev.TestPass {
			codes = append(codes, taskstate.FailureTestsFailed)
			details = append(details, taskstate.FailureDetail{Code: taskstate.FailureTestsFailed, Message: truncStr(ev.TestOutput, 200)})
		}
		if !ev.LintPass {
			codes = append(codes, taskstate.FailureLintFailed)
			details = append(details, taskstate.FailureDetail{Code: taskstate.FailureLintFailed, Message: truncStr(ev.LintOutput, 200)})
		}
		if !ev.ScopeClean {
			codes = append(codes, taskstate.FailureWrongFiles)
		}
		if !ev.ProtectedClean {
			codes = append(codes, taskstate.FailureProtectedPathTouched)
		}
		if !ev.ReviewPass && ev.ReviewEngine != "" {
			codes = append(codes, taskstate.FailureReviewRejected)
			details = append(details, taskstate.FailureDetail{Code: taskstate.FailureReviewRejected, Message: truncStr(ev.ReviewOutput, 200)})
		}
		if ev.DiffSummary == "" || ev.DiffSummary == "(diff unavailable)" {
			codes = append(codes, taskstate.FailureNoDiff)
		}

		e.State.RecordAttempt(taskstate.Attempt{
			Number:          number,
			StartedAt:       startedAt,
			Duration:        time.Since(startedAt),
			Engine:          engineName,
			ProposedSummary: proposedSummary,
			Evidence:        ev,
			FailureCodes:    codes,
			FailureDetails:  details,
		})
	}
}

func truncStr(s string, n int) string {
	if len(s) <= n { return s }
	return s[:n] + "..."
}

// buildSpec creates a RunSpec for a phase and worktree handle.
func (e Engine) buildSpec(phase engine.PhaseSpec, handle worktree.Handle) engine.RunSpec {
	return engine.RunSpec{
		Prompt:            phase.Prompt,
		WorktreeDir:       handle.Path,
		RuntimeDir:        handle.RuntimeDir,
		Mode:              e.AuthMode,
		Phase:             phase,
		PoolConfigDir:     poolConfigForRunner(e, pickRunnerName(e, phase.Name)),
		SandboxEnabled:    phase.Sandbox,
		SandboxDomains:    sandboxDomainsForPhase(phase.Name),
		SandboxAllowRead:  []string{filepath.Clean(handle.Path), handle.RuntimeDir},
		SandboxAllowWrite: []string{filepath.Clean(handle.Path)}, // NO .stoke -- harness writes go to RuntimeDir
	}
}

func pickRunnerName(e Engine, phase string) string {
	name, _ := pickRunner(e, phase)
	return name
}

// buildRetryPrompt injects failure analysis and diff context into the next attempt.
// maxRetryContextLines caps the diff summary in retry prompts to prevent
// unbounded prompt growth across retry attempts.
const maxRetryContextLines = 100

func buildRetryPrompt(originalPrompt string, attempt int, analysis *failure.Analysis, diffSummary string) string {
	var sb strings.Builder
	sb.WriteString(originalPrompt)
	sb.WriteString(fmt.Sprintf("\n\n--- RETRY CONTEXT (attempt %d) ---\n", attempt))
	sb.WriteString("Previous attempt FAILED: " + analysis.Summary + "\n")
	if analysis.RootCause != "" {
		sb.WriteString("Root cause: " + analysis.RootCause + "\n")
	}
	if len(analysis.Specifics) > 0 {
		sb.WriteString("\nSPECIFIC ISSUES:\n")
		// Cap specifics to first 10 to avoid unbounded growth
		specifics := analysis.Specifics
		if len(specifics) > 10 {
			specifics = specifics[:10]
		}
		for _, d := range specifics {
			sb.WriteString(fmt.Sprintf("  %s:%d -- %s\n", d.File, d.Line, d.Message))
			if d.Fix != "" {
				sb.WriteString(fmt.Sprintf("    Suggested fix: %s\n", d.Fix))
			}
		}
		if len(analysis.Specifics) > 10 {
			sb.WriteString(fmt.Sprintf("  ... and %d more issue(s)\n", len(analysis.Specifics)-10))
		}
	}
	if diffSummary != "" && diffSummary != "(diff unavailable)" {
		// Truncate diff to last N lines to keep retry context bounded.
		lines := strings.Split(diffSummary, "\n")
		if len(lines) > maxRetryContextLines {
			sb.WriteString(fmt.Sprintf("\nCHANGES FROM PREVIOUS ATTEMPT (last %d of %d lines):\n", maxRetryContextLines, len(lines)))
			sb.WriteString(strings.Join(lines[len(lines)-maxRetryContextLines:], "\n") + "\n")
		} else {
			sb.WriteString("\nCHANGES FROM PREVIOUS ATTEMPT:\n")
			sb.WriteString(diffSummary + "\n")
		}
	}
	sb.WriteString("\nDO NOT:\n")
	switch analysis.Class {
	case failure.PolicyViolation:
		sb.WriteString("  - Use @ts-ignore, as any, or eslint-disable\n")
	case failure.WrongFiles:
		sb.WriteString("  - Modify files outside the task scope\n")
	case failure.Regression:
		sb.WriteString("  - Break existing passing tests\n")
	default:
		sb.WriteString("  - Repeat the same approach that just failed\n")
	}
	return sb.String()
}

func buildPhases(e Engine) []engine.PhaseSpec {
	plan := e.Policy.Phases["plan"]
	execute := e.Policy.Phases["execute"]
	verifyPhase := e.Policy.Phases["verify"]
	return []engine.PhaseSpec{
		{
			Name:         "plan",
			BuiltinTools: plan.BuiltinTools,
			AllowedRules: plan.AllowedRules,
			DeniedRules:  plan.DeniedRules,
			MCPEnabled:   plan.MCPEnabled,
			MaxTurns:     10,
			Prompt:       planPrompt(e.Task),
			Sandbox:      false,
			ReadOnly:     true,
		},
		{
			Name:         "execute",
			BuiltinTools: execute.BuiltinTools,
			AllowedRules: execute.AllowedRules,
			DeniedRules:  execute.DeniedRules,
			MCPEnabled:   execute.MCPEnabled,
			MaxTurns:     20,
			Prompt:       executePrompt(e.Task, e.TaskType, e.TaskVerification),
			Sandbox:      true,
			ReadOnly:     false,
		},
		{
			Name:         "verify",
			BuiltinTools: verifyPhase.BuiltinTools,
			AllowedRules: verifyPhase.AllowedRules,
			DeniedRules:  verifyPhase.DeniedRules,
			MCPEnabled:   verifyPhase.MCPEnabled,
			MaxTurns:     5,
			Prompt:       stokeprompts.BuildVerifyPrompt(e.Task, e.TaskVerification),
			Sandbox:      true,
			ReadOnly:     true,
		},
	}
}

func pickRunner(e Engine, phase string) (string, engine.CommandRunner) {
	if phase == "plan" {
		return string(model.ProviderClaude), e.Runners.Claude
	}

	// Use Resolve() with fallback chain -- checks pool availability
	isAvailable := func(p model.Provider) bool {
		switch p {
		case model.ProviderClaude:
			return e.Runners.Claude != nil
		case model.ProviderCodex:
			return e.Runners.Codex != nil
		default:
			return false // openrouter/direct-api not yet wired as runners
		}
	}

	if phase == "verify" {
		execProvider := model.Resolve(e.TaskType, isAvailable)
		reviewer := model.CrossModelReviewer(execProvider)
		return providerToRunner(e, reviewer)
	}

	resolved := model.Resolve(e.TaskType, isAvailable)
	return providerToRunner(e, resolved)
}

func providerToRunner(e Engine, p model.Provider) (string, engine.CommandRunner) {
	switch p {
	case model.ProviderCodex:
		if e.Runners.Codex != nil {
			return string(p), e.Runners.Codex
		}
		return string(model.ProviderClaude), e.Runners.Claude
	default:
		return string(model.ProviderClaude), e.Runners.Claude
	}
}

func poolConfigForRunner(e Engine, runner string) string {
	switch runner {
	case string(model.ProviderClaude):
		return e.ClaudeConfigDir
	case string(model.ProviderCodex):
		return e.CodexHome
	default:
		return ""
	}
}

func sandboxDomainsForPhase(phase string) []string {
	if phase == "execute" || phase == "verify" {
		return []string{"github.com", "*.npmjs.org", "registry.yarnpkg.com"}
	}
	return nil
}

func planPrompt(task string) string {
	return stokeprompts.BuildPlanPrompt(task, false, "")
}

func executePrompt(task string, taskType model.TaskType, verification []string) string {
	verificationStr := ""
	if len(verification) > 0 {
		verificationStr = strings.Join(verification, "\n")
	}
	return stokeprompts.BuildExecutePrompt(task, verificationStr, "")
}

// reviewVerdict is the parsed output of a cross-model review.
type reviewVerdict struct {
	Pass     bool   `json:"pass"`
	Severity string `json:"severity"`
	Findings []struct {
		Severity string `json:"severity"`
		File     string `json:"file"`
		Line     string `json:"line"`
		Message  string `json:"message"`
		Fix      string `json:"fix"`
	} `json:"findings"`
}

// parseReviewVerdict parses the reviewer's JSON response.
// If the response is not valid JSON, the review is considered failed.
func parseReviewVerdict(s string) (*reviewVerdict, error) {
	s = strings.TrimSpace(s)
	// Strip markdown fences if present
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var v reviewVerdict
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}

	// Minimum validity: a passing review with zero findings suggests the reviewer
	// didn't actually check anything (confused output, truncated response).
	// Require at least one finding entry for a valid review.
	if v.Pass && len(v.Findings) == 0 && v.Severity == "" {
		return nil, fmt.Errorf("review verdict invalid: pass=true with no findings and no severity (reviewer may not have checked)")
	}
	// A failing review with no findings is also suspect.
	if !v.Pass && len(v.Findings) == 0 {
		return nil, fmt.Errorf("review verdict invalid: pass=false with no findings (reviewer may not have checked)")
	}

	return &v, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func slugFromTask(task string) string {
	cleaned := strings.ToLower(task)
	repl := strings.NewReplacer(" ", "-", "/", "-", "_", "-")
	cleaned = repl.Replace(cleaned)
	if len(cleaned) > 32 {
		cleaned = cleaned[:32]
	}
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "task"
	}
	return cleaned
}
