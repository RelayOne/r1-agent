package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/autofix"
	"github.com/ericmacdougall/stoke/internal/boulder"
	"github.com/ericmacdougall/stoke/internal/checkpoint"
	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/env"
	"github.com/ericmacdougall/stoke/internal/critic"
	"github.com/ericmacdougall/stoke/internal/ctxpack"
	"github.com/ericmacdougall/stoke/internal/diffcomp"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/extract"
	"github.com/ericmacdougall/stoke/internal/failure"
	"github.com/ericmacdougall/stoke/internal/filewatcher"
	"github.com/ericmacdougall/stoke/internal/fileutil"
	"github.com/ericmacdougall/stoke/internal/gitblame"
	"github.com/ericmacdougall/stoke/internal/hooks"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/intent"
	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/logging"
	"github.com/ericmacdougall/stoke/internal/microcompact"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/patchapply"
	"github.com/ericmacdougall/stoke/internal/promptcache"
	stokeprompts "github.com/ericmacdougall/stoke/internal/prompts"
	"github.com/ericmacdougall/stoke/internal/replay"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/scan"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/semdiff"
	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/snapshot"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/testgen"
	"github.com/ericmacdougall/stoke/internal/testselect"
	"github.com/ericmacdougall/stoke/internal/tokenest"
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
	Recorder         *replay.Recorder    // session replay recording (nil = disabled)
	TestGraph        *testselect.Graph   // dependency-aware test selection (nil = run all)
	RepoMap          *repomap.RepoMap    // ranked codebase map for context (nil = disabled)
	RepoMapBudget    int                 // token budget for repomap (0 = default 2000)
	CriticConfig     *critic.Config      // per-project critic configuration (nil = defaults)
	PlanOnly         bool
	RunnerOverride   engine.CommandRunner // if set, used for all phases (testing only)
	Boulder          *boulder.Enforcer   // idle detection (nil = disabled)
	Convergence      *convergence.Validator // adversarial self-audit: blocks merge if blocking findings (nil = skip)
	EventBus         *hub.Bus               // unified event bus (nil = no events)
	SkillRegistry    *skill.Registry        // skill library for prompt injection (nil = auto-create from RepoRoot)
	StackMatches     []string               // pre-computed stack-matched skill names from RepoProfile
	RunnerMode       string                 // runner selection: "claude", "codex", "native", "hybrid" (default: "claude")
	Environ          env.Environment        // execution environment backend (nil = run on host)
	EnvHandle        *env.Handle            // provisioned environment handle (nil = run on host)
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
func (e Engine) Run(ctx context.Context) (result Result, retErr error) {
	name := firstNonEmpty(e.WorktreeName, string(e.TaskType)+"-"+slugFromTask(e.Task))
	log := logging.Task("workflow", name)

	// Emit task failure event on error (uses named return).
	defer func() {
		if retErr != nil {
			e.emitEventAsync(&hub.Event{
				Type:   hub.EventTaskFailed,
				TaskID: name,
				Lifecycle: &hub.LifecycleEvent{
					Entity: "task",
					State:  "failed",
				},
			})
		}
	}()

	// Wire replay recorder: capture all agent events for post-mortem debugging.
	// Uses named returns so the deferred closure sees the actual outcome.
	if e.Recorder != nil {
		e.Recorder.Record(replay.EventPhase, map[string]any{"phase": "start", "task": e.Task, "type": string(e.TaskType)})
		origOnEvent := e.OnEvent
		e.OnEvent = func(ev stream.Event) {
			e.Recorder.Record(replay.EventType(ev.Type), map[string]any{
				"delta": truncStr(ev.DeltaText, 500),
			})
			if origOnEvent != nil {
				origOnEvent(ev)
			}
		}
		defer func() {
			outcome := "success"
			if retErr != nil {
				outcome = "failure"
				e.Recorder.RecordError(retErr.Error(), nil)
			}
			rec := e.Recorder.Finish(outcome, string(e.TaskType))
			// Persist recording to disk for post-mortem analysis.
			replayDir := filepath.Join(e.RepoRoot, ".stoke", "replays")
			if mkErr := os.MkdirAll(replayDir, 0o755); mkErr == nil {
				replayPath := filepath.Join(replayDir, rec.ID+".json")
				_ = replay.Save(rec, replayPath)
			}
		}()
	}
	var handle worktree.Handle
	if e.DryRun {
		runtimeDir := filepath.Join(os.TempDir(), "stoke-runtime-dryrun-"+name)
		if err := fileutil.EnsureDir(runtimeDir, fileutil.DirPerms); err != nil {
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

	result = Result{WorktreePath: handle.Path, Branch: handle.Branch, TaskType: e.TaskType, DryRun: e.DryRun}

	// Emit worktree creation event
	e.emitEventAsync(&hub.Event{
		Type:       hub.EventGitWorktreeCreated,
		TaskID:     name,
		WorktreeID: handle.Name,
		Git: &hub.GitEvent{
			Operation: "worktree_add",
			Branch:    handle.Branch,
		},
	})

	// Start a file watcher on the worktree to detect external changes during execution.
	// Logs warnings when files are modified outside the agent's control (e.g., editor saves, git ops).
	watcher := filewatcher.New(filewatcher.Config{
		Root:         handle.Path,
		IgnoreHidden: true,
		Extensions:   []string{".go", ".ts", ".js", ".py", ".rs"},
	})
	watcher.OnChange(func(ev filewatcher.Event) {
		log.Warn("external file change detected during execution", "type", string(ev.Type), "path", ev.RelPath)
	})
	if watchErr := watcher.Start(); watchErr != nil {
		log.Warn("filewatcher failed to start (non-fatal)", "error", watchErr)
	}
	defer watcher.Stop()

	// Checkpoint store for saving state at phase boundaries.
	cpStore := checkpoint.NewStore(handle.RuntimeDir)

	// Fire BeforeTask hooks

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

	// --- PRE-PLAN: Socratic interview clarification ---
	// If a completed interview session is attached, synthesize the clarified scope
	// and prepend it to the task description so the planner has full context.
	// --- PLAN phase (no retry) ---
	// Advance state: Pending -> Claimed (harness takes ownership)
	if err := e.advanceState(taskstate.Claimed, "harness dispatching to plan phase"); err != nil {
		return result, err
	}

	// execCtx governs the lifetime of agent processes. Boulder may cancel it
	// when idle detection exceeds max nudges, killing the stalled agent.
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	// Boulder: track this task for idle detection and start background scanner.
	if e.Boulder != nil {
		e.Boulder.TrackTask(name, e.Task, handle.Name)
		e.Boulder.UpdateStatus(name, boulder.StatusInProgress)
		// Background scanner: periodically check for idle agents.
		// Track nudge count to escalate from warning to cancellation.
		var boulderNudgeCount int
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("boulder scanner panic (recovered)", "panic", r)
				}
			}()
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					e.Boulder.Scan(time.Now(), func(taskID, msg string) bool {
						boulderNudgeCount++
						log.Warn("boulder nudge", "task", taskID, "message", msg, "nudge_count", boulderNudgeCount)
						// Fire a system event so the TUI/observer sees the nudge.
						if e.OnEvent != nil {
							e.OnEvent(stream.Event{
								Type:      "system",
								DeltaText: fmt.Sprintf("[boulder] idle agent nudge #%d: %s", boulderNudgeCount, msg),
							})
						}
						// After max nudges, cancel execution to force retry with fresh context.
						if boulderNudgeCount >= e.Boulder.MaxNudges() {
							log.Error("boulder: max nudges exceeded, cancelling execution", "task", taskID)
							execCancel()
							return false // stop scanning
						}
						return true
					})
				case <-execCtx.Done():
					return
				}
			}
		}()
		// Wrap OnEvent to record activity on each agent event.
		origOnEvent := e.OnEvent
		e.OnEvent = func(ev stream.Event) {
			e.Boulder.RecordActivity()
			if origOnEvent != nil {
				origOnEvent(ev)
			}
		}
	}

	planPhase := phases[0]
	e.emitEvent(ctx, &hub.Event{
		Type:   hub.EventMissionPlanStart,
		TaskID: name, Phase: "plan",
		Lifecycle: &hub.LifecycleEvent{Entity: "task", State: "plan_start"},
	})
	planRunner, planEngine := pickRunner(e, planPhase.Name)
	planSpec := e.buildSpec(planPhase, handle)
	planResult, err := planEngine.Run(execCtx, planSpec, e.OnEvent)
	if err != nil {
		_ = e.advanceState(taskstate.Failed, "plan phase failed: "+err.Error())
		return result, fmt.Errorf("plan phase: %w", err)
	}
	result.Steps = append(result.Steps, StepResult{Phase: "plan", Engine: planRunner, Command: planResult.Prepared})
	result.TotalCostUSD += planResult.CostUSD
	e.emitEventAsync(&hub.Event{
		Type:   hub.EventMissionPlanDone,
		TaskID: name, Phase: "plan",
		Model: &hub.ModelEvent{
			Provider:     planRunner,
			InputTokens:  planResult.Tokens.Input,
			OutputTokens: planResult.Tokens.Output,
			CostUSD:      planResult.CostUSD,
		},
	})
	if e.CostTracker != nil && planResult.CostUSD > 0 {
		e.CostTracker.Record(planRunner, e.Task+"/plan", planResult.Tokens.Input, planResult.Tokens.Output, planResult.Tokens.CacheRead, planResult.Tokens.CacheCreation)
	}

	// Checkpoint after plan phase completion.
	cpStore.Save(checkpoint.Checkpoint{
		ID: checkpoint.IdempotencyKey(name, 1, 0), TaskID: name,
		Phase: checkpoint.PhaseCheckpointed, Step: 1,
		WorktreePath: handle.Path, Branch: handle.Branch,
		BaseCommit: handle.BaseCommit, CostUSD: result.TotalCostUSD,
	})

	// --- PLAN-ONLY MODE: structurally prevents execute/verify/commit/merge ---
	// This is not a prompt instruction. The harness does not call execute.
	if e.PlanOnly {
		result.PlanOutput = planResult.ResultText
		e.Worktrees.Cleanup(ctx, handle)
		return result, nil
	}

	// --- EXECUTE + VERIFY loop (with retry) ---
	maxAttempts := 3
	maxConvergenceRetries := 2 // separate budget for convergence-only failures
	convergenceRetries := 0
	executePhase := phases[1]
	verifyPhase := phases[2]
	var lastFailure *failure.Analysis
	var lastDiff string
	var priorFingerprints []failure.Fingerprint // track failure fingerprints across attempts

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Budget gate: stop before spending more if over budget.
		if e.CostTracker != nil && e.CostTracker.OverBudget() {
			log.Warn("budget exceeded, skipping attempt", "spent", e.CostTracker.Total(), "remaining", e.CostTracker.BudgetRemaining())
			e.emitEventAsync(&hub.Event{
				Type:   hub.EventCostBudgetExceeded,
				TaskID: name,
				Cost: &hub.CostEvent{
					TotalSpent:  e.CostTracker.Total(),
					BudgetLimit: e.CostTracker.Total() + e.CostTracker.BudgetRemaining(),
					PercentUsed: 100,
					Threshold:   "exceeded",
				},
			})
			e.Worktrees.Cleanup(ctx, handle)
			return result, fmt.Errorf("budget exceeded ($%.2f spent), aborting", e.CostTracker.Total())
		}
		log.Info("starting attempt", "attempt", attempt, "max", maxAttempts)
		e.emitEvent(ctx, &hub.Event{
			Type:   hub.EventMissionExecuteStart,
			TaskID: name, Phase: "execute",
			Lifecycle: &hub.LifecycleEvent{Entity: "task", State: "execute_start", Attempt: attempt},
		})

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
			prompt = buildRetryPrompt(prompt, attempt, lastFailure, lastDiff, handle.Path)
			// Invoke BeforeRetry hooks for additional prompt augmentation
			for _, h := range e.Hooks {
				aug, err := h.BeforeRetry(ctx, e.Task, attempt, lastFailure)
				if err != nil {
					log.Warn("BeforeRetry hook error", "error", err, "attempt", attempt)
					continue
				}
				if aug != "" {
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
			execResult, runErr = execRunner.Run(execCtx, execSpec, e.OnEvent)

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
		e.emitEventAsync(&hub.Event{
			Type:   hub.EventModelPostCall,
			TaskID: name, Phase: "execute",
			Model: &hub.ModelEvent{
				Provider:     execRunnerName,
				InputTokens:  execResult.Tokens.Input,
				OutputTokens: execResult.Tokens.Output,
				CachedTokens: execResult.Tokens.CacheRead,
				CostUSD:      execResult.CostUSD,
				Duration:     time.Duration(execResult.DurationMs) * time.Millisecond,
			},
		})

		// --- VERIFY ---
		// Honor Policy.Verification flags: only run enabled checks.
		verifier := e.Verifier
		filteredBuild, filteredTest, filteredLint := verifier.Commands()
		if !e.Policy.Verification.Build { filteredBuild = "" }
		if !e.Policy.Verification.Tests { filteredTest = "" }
		if !e.Policy.Verification.Lint { filteredLint = "" }

		// Targeted test selection: if a dependency graph is available, narrow
		// the test command to only packages affected by the changed files.
		if e.TestGraph != nil && filteredTest != "" {
			changedFiles, _ := worktree.ModifiedFiles(ctx, handle)
			if len(changedFiles) > 0 {
				sel := e.TestGraph.Select(changedFiles)
				if len(sel.Packages) > 0 {
					filteredTest = "go test " + strings.Join(sel.Packages, " ")
					log.Info("testselect narrowed test scope", "packages", len(sel.Packages), "skipped", len(sel.Skipped))
				}
			}
		}

		verifier = verify.NewPipeline(filteredBuild, filteredTest, filteredLint)
		if e.Environ != nil && e.EnvHandle != nil {
			verifier = verifier.WithEnvironment(e.Environ, e.EnvHandle)
		}
		outcomes, verifyErr := verifier.Run(execCtx, handle.Path)
		result.Verification = outcomes

		// Emit verification results
		for _, o := range outcomes {
			if !o.Skipped {
				e.emitEventAsync(&hub.Event{
					Type:   hub.EventVerifyBuildResult,
					TaskID: name, Phase: "verify",
					Test: &hub.TestEvent{Phase: o.Name},
				})
			}
		}

		// Build evidence for this attempt.
		// When a gate is disabled by policy (empty command), treat it as passing.
		evidence := taskstate.Evidence{
			DiffSummary: worktree.DiffSummary(ctx, handle),
			BuildPass:   !e.Policy.Verification.Build,
			TestPass:    !e.Policy.Verification.Tests,
			LintPass:    !e.Policy.Verification.Lint,
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

			// --- BLAME ATTRIBUTION: annotate modified files with authorship impact ---
			if len(modifiedFiles) > 0 {
				var blameNotes []string
				for _, f := range modifiedFiles {
					if fb, blameErr := gitblame.Blame(e.RepoRoot, f); blameErr == nil {
						authors := fb.Authors()
						if len(authors) > 0 {
							blameNotes = append(blameNotes, fmt.Sprintf("%s: %d authors, primary %s (%.0f%%)",
								f, len(authors), authors[0].Author, authors[0].Percentage))
						}
					}
				}
				if len(blameNotes) > 0 {
					evidence.Notes = append(evidence.Notes, "blame: "+strings.Join(blameNotes, "; "))
				}
			}

			// --- CRITIC: AST-aware quality gate on changed files ---
			if len(modifiedFiles) > 0 {
				changes := make(map[string]string, len(modifiedFiles))
				for _, f := range modifiedFiles {
					absPath := filepath.Join(handle.Path, f)
					if data, readErr := os.ReadFile(absPath); readErr == nil {
						changes[f] = string(data)
					}
				}
				if len(changes) > 0 {
					criticCfg := critic.Config{}
				if e.CriticConfig != nil {
					criticCfg = *e.CriticConfig
				}
				c := critic.New(criticCfg)
					verdict := c.Review(changes)
					if !verdict.Pass {
						evidence.Notes = append(evidence.Notes, "critic: "+verdict.Summary)
						for _, finding := range verdict.Findings {
							if finding.Severity == critic.SeverityBlock {
								evidence.ReviewPass = false
								e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)
								e.Worktrees.Cleanup(ctx, handle)
								_ = e.advanceState(taskstate.Failed, "critic blocked: "+verdict.Summary)
								return result, fmt.Errorf("critic blocked: %s", verdict.Summary)
							}
						}
					}
				}
			}

			// Detect gitignored files created by the agent. These are invisible
			// to git add -A and won't be in the merged commit, but build/test
			// may depend on them. FAIL CLOSED: if the verified environment
			// includes files that can't ship, the verification is invalid.
			if ignored := worktree.IgnoredNewFiles(ctx, handle); len(ignored) > 0 {
				evidence.Findings = append(evidence.Findings,
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
			if scanErr == nil && len(scanResult.Findings) > 0 {
				e.emitEventAsync(&hub.Event{
					Type:   hub.EventSecurityScanResult,
					TaskID: name, Phase: "verify",
					Security: &hub.SecurityEvent{
						Category: "scan",
						Severity: "info",
						Details:  scanResult.Summary(),
					},
				})
			}
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

			// --- CONVERGENCE GATE: adversarial self-audit before merge ---
			e.emitEvent(ctx, &hub.Event{
				Type:   hub.EventVerifyConvergenceStart,
				TaskID: name, Phase: "convergence",
			})
			if e.Convergence != nil && len(postReviewFiles) > 0 {
				var convFiles []convergence.FileInput
				for _, f := range postReviewFiles {
					absPath := filepath.Join(handle.Path, f)
					if data, readErr := os.ReadFile(absPath); readErr == nil {
						convFiles = append(convFiles, convergence.FileInput{Path: f, Content: data})
					}
				}
				if len(convFiles) > 0 {
					var convReport *convergence.Report
					if len(e.TaskVerification) > 0 {
						convReport = e.Convergence.ValidateWithCriteria(name, convFiles, e.TaskVerification)
					} else {
						convReport = e.Convergence.Validate(name, convFiles)
					}
					if convReport != nil && convReport.BlockingCount() > 0 {
						// Convergence failed: inject findings into failure context for retry.
						var convFindings strings.Builder
						convFindings.WriteString(fmt.Sprintf("Convergence audit BLOCKED merge: %d blocking findings (score=%.2f)\n",
							convReport.BlockingCount(), convReport.Score))
						for _, f := range convReport.Findings {
							if f.Severity == convergence.SevBlocking {
								convFindings.WriteString(fmt.Sprintf("  [%s] %s: %s", f.Category, f.File, f.Description))
								if f.Suggestion != "" {
									convFindings.WriteString(fmt.Sprintf(" (fix: %s)", f.Suggestion))
								}
								convFindings.WriteString("\n")
							}
						}
						log.Warn("convergence gate blocked merge",
							"blocking", convReport.BlockingCount(),
							"total_findings", len(convReport.Findings),
							"score", convReport.Score,
							"convergence_retry", convergenceRetries,
							"max_convergence_retries", maxConvergenceRetries)
						evidence.Findings = append(evidence.Findings, "convergence: "+convFindings.String())

						convergenceRetries++
						if convergenceRetries > maxConvergenceRetries {
							e.Worktrees.Cleanup(ctx, handle)
							_ = e.advanceState(taskstate.Failed, "convergence retry budget exhausted")
							return result, fmt.Errorf("convergence retry budget exhausted (%d retries): %d blocking findings remain",
								maxConvergenceRetries, convReport.BlockingCount())
						}

						// Inject findings into retry context WITHOUT consuming the main attempt budget.
						e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, evidence)

						// Transition back through the state machine for a clean retry.
						// Reviewed -> Claimed is the convergence retry path.
						_ = e.advanceState(taskstate.Claimed, "convergence retry: "+convFindings.String())

						lastFailure = &failure.Analysis{
							Class:     failure.Incomplete,
							Summary:   convFindings.String(),
							RootCause: "convergence audit found blocking issues",
						}
						attempt-- // convergence retries don't consume the build-failure budget
						continue
					}
					if convReport != nil {
						e.emitEventAsync(&hub.Event{
							Type:   hub.EventVerifyConvergenceResult,
							TaskID: name, Phase: "convergence",
						})
						log.Info("convergence gate passed", "score", convReport.Score, "findings", len(convReport.Findings))
					}
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
			// Take snapshot before merge for rollback on failure.
			snap, snapErr := snapshot.Take(handle.Path, "pre-merge-"+name)
			if snapErr != nil {
				log.Warn("snapshot failed (non-fatal)", "error", snapErr)
			}

			// Record validated file set in result for callers (e.g., mission bridge).
			result.FilesChanged = postReviewFiles

			// CommitVerifiedTree builds one clean commit from BaseCommit containing
			// ONLY the validated files. No intermediate agent commits survive.
			commitMsg := fmt.Sprintf("feat(%s): %s", slugFromTask(e.Task), e.Task)
			e.emitEvent(ctx, &hub.Event{
				Type:   hub.EventGitPreCommit,
				TaskID: name, WorktreeID: handle.Name,
				Git: &hub.GitEvent{
					Operation:    "commit",
					Branch:       handle.Branch,
					FilesChanged: postReviewFiles,
					Message:      commitMsg,
				},
			})
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
			// Save main branch HEAD before merge for potential rollback.
			mainHead := worktree.MainHeadSHA(ctx, e.RepoRoot)

			e.emitEvent(ctx, &hub.Event{
				Type:   hub.EventGitPreMerge,
				TaskID: name, WorktreeID: handle.Name,
				Git: &hub.GitEvent{
					Operation: "merge",
					Branch:    handle.Branch,
					Message:   commitMsg,
				},
			})
			if mergeErr := e.Worktrees.Merge(ctx, handle, commitMsg); mergeErr != nil {
				// Attempt to restore main to pre-merge state if it was modified.
				if mainHead != "" {
					currentHead := worktree.MainHeadSHA(ctx, e.RepoRoot)
					if currentHead != mainHead {
						log.Warn("merge failed mid-operation, restoring main HEAD",
							"pre_merge", mainHead, "current", currentHead)
						worktree.ResetMainTo(ctx, e.RepoRoot, mainHead)
					}
				}
				// Restore worktree snapshot.
				if snap != nil {
					if restoreErr := snapshot.Restore(snap); restoreErr != nil {
						log.Warn("snapshot restore failed", "error", restoreErr)
					}
				}
				e.Worktrees.Cleanup(ctx, handle)
				return result, fmt.Errorf("merge: %w", mergeErr)
			}

			// State: Reviewed -> Committed (Stoke merged to main)
			if err := e.advanceState(taskstate.Committed, "merged to main by harness"); err != nil {
				return result, err
			}

			e.emitEventAsync(&hub.Event{
				Type:   hub.EventGitPostMerge,
				TaskID: name, WorktreeID: handle.Name,
				Git: &hub.GitEvent{
					Operation:    "merge",
					Branch:       handle.Branch,
					FilesChanged: postReviewFiles,
				},
			})
			e.emitEventAsync(&hub.Event{
				Type:   hub.EventTaskCompleted,
				TaskID: name,
				Lifecycle: &hub.LifecycleEvent{
					Entity:  "task",
					State:   "completed",
					Attempt: attempt,
				},
				Model: &hub.ModelEvent{CostUSD: result.TotalCostUSD},
			})
			log.Info("task completed successfully", "attempt", attempt, "cost_usd", result.TotalCostUSD)
			if e.Boulder != nil {
				e.Boulder.UpdateStatus(name, boulder.StatusComplete)
			}
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
	reviewFilesRead := make(map[string]bool) // track unique files the reviewer Read
	triedReviewPools := map[string]bool{}
	maxReviewRotations := 5

	// Build a set of changed files for overlap checking.
	changedSet := make(map[string]bool, len(preReviewFiles))
	for _, f := range preReviewFiles {
		changedSet[f] = true
	}

	// Wrap OnEvent to track unique Read tool uses on changed files.
	reviewOnEvent := func(ev stream.Event) {
		for _, tu := range ev.ToolUses {
			if tu.Name == "Read" {
				if filePath, ok := tu.Input["file_path"].(string); ok {
					// Normalize: strip worktree prefix to get relative path.
					for cf := range changedSet {
						if strings.HasSuffix(filePath, cf) {
							reviewFilesRead[cf] = true
							break
						}
					}
				}
			}
		}
		if e.OnEvent != nil {
			e.OnEvent(ev)
		}
	}

	for range maxReviewRotations {
		verifySpec := e.buildSpec(verifyPhase, handle)
		diffText := worktree.DiffSummary(ctx, handle)
		verifySpec.Prompt = stokeprompts.BuildVerifyPrompt(e.Task, e.TaskVerification, preReviewFiles...) +
			"\n\n## Diff summary\n" + diffText
		// Enrich review prompt with semantic diff analysis (renames, breaking changes).
		if semAnalysis := semdiff.AnalyzeMultiFile(buildSemdiffInputs(ctx, handle)); len(semAnalysis.Changes) > 0 {
			verifySpec.Prompt += "\n\n## Semantic change summary\n" + semAnalysis.Summary
		}

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

	// Fail fast on agent-side errors (timeout, stream failure) — same as execute path.
	if verifyResult.IsError && verifyResult.Subtype != "rate_limited" {
		evidence.ReviewPass = false
		evidence.ReviewOutput = fmt.Sprintf("reviewer error (subtype=%s): %s", verifyResult.Subtype, truncate(verifyResult.ResultText, 200))
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "cross-model review: agent error ("+verifyResult.Subtype+")")
		return nil, fmt.Errorf("cross-model review agent error (subtype=%s)", verifyResult.Subtype)
	}

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
	if e.CostTracker != nil && verifyResult.CostUSD > 0 {
		e.CostTracker.Record(verifyRunnerName, e.Task+"/review", verifyResult.Tokens.Input, verifyResult.Tokens.Output, verifyResult.Tokens.CacheRead, verifyResult.Tokens.CacheCreation)
	}
	evidence.ReviewOutput = verifyResult.ResultText

	verdict, parseErr := parseReviewVerdict(verifyResult.ResultText)
	if parseErr != nil {
		evidence.ReviewPass = false
		e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
		e.Worktrees.Cleanup(ctx, handle)
		_ = e.advanceState(taskstate.Failed, "review returned invalid JSON")
		return nil, fmt.Errorf("cross-model review returned invalid JSON: %v", parseErr)
	}

	// Review quality gate: reviewer must have Read at least half the changed files.
	// Counts unique changed files actually read, not total Read events.
	//
	// Gate is skipped when no files were changed (preReviewFiles is empty).
	// For Codex, which doesn't emit ToolUse events, we use output-based validation
	// instead of file-read tracking.
	if len(preReviewFiles) > 0 {
		if verifyRunnerName == string(model.ProviderCodex) {
			// Codex-specific validation: verify the review output references changed files
			// and has substantive content (not just a bare pass/fail).
			referencedFiles := 0
			for _, f := range preReviewFiles {
				if strings.Contains(verifyResult.ResultText, f) || strings.Contains(verifyResult.ResultText, filepath.Base(f)) {
					referencedFiles++
				}
			}
			minReferenced := len(preReviewFiles) / 3
			if minReferenced < 1 {
				minReferenced = 1
			}
			if referencedFiles < minReferenced {
				evidence.ReviewPass = false
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Failed, fmt.Sprintf("codex review quality: referenced %d/%d changed files, need at least %d", referencedFiles, len(preReviewFiles), minReferenced))
				return nil, fmt.Errorf("codex review quality gate failed: referenced %d/%d changed files (need %d)", referencedFiles, len(preReviewFiles), minReferenced)
			}
		} else {
			minFilesRead := len(preReviewFiles) / 2
			if minFilesRead < 1 {
				minFilesRead = 1
			}
			uniqueReads := len(reviewFilesRead)
			if uniqueReads < minFilesRead {
				evidence.ReviewPass = false
				e.recordAttemptEvidence(attempt, attemptStart, execRunnerName, execResult.ResultText, *evidence)
				e.Worktrees.Cleanup(ctx, handle)
				_ = e.advanceState(taskstate.Failed, fmt.Sprintf("review quality: read %d/%d changed files, need at least %d", uniqueReads, len(preReviewFiles), minFilesRead))
				return nil, fmt.Errorf("cross-model review quality gate failed: read %d/%d changed files (need %d)", uniqueReads, len(preReviewFiles), minFilesRead)
			}
		}
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
	if len(e.AllowedFiles) > 0 && e.Policy.Verification.ScopeCheck {
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

func buildRetryPrompt(originalPrompt string, attempt int, analysis *failure.Analysis, diffSummary string, worktreeDir string) string {
	var sb strings.Builder
	sb.WriteString(originalPrompt)
	sb.WriteString(fmt.Sprintf("\n\n--- RETRY CONTEXT (attempt %d) ---\n", attempt))
	sb.WriteString("Previous attempt FAILED: " + analysis.Summary + "\n")
	if analysis.RootCause != "" {
		sb.WriteString("Root cause: " + analysis.RootCause + "\n")
	}

	// For lint failures, use autofix's structured parser for cleaner output.
	if analysis.Class == failure.LintFailed {
		var lintOutput string
		for _, d := range analysis.Specifics {
			lintOutput += fmt.Sprintf("%s:%d: %s\n", d.File, d.Line, d.Message)
		}
		if issues := autofix.ParseOutput(lintOutput); len(issues) > 0 {
			sb.WriteString("\n" + autofix.FormatFixPrompt(issues))
		}
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
		// Parse diff to provide structured stats (files/additions/deletions).
		if patch, parseErr := patchapply.Parse(diffSummary); parseErr == nil {
			files, adds, dels := patch.Stats()
			sb.WriteString(fmt.Sprintf("\nPREVIOUS ATTEMPT STATS: %d file(s), +%d/-%d lines\n", files, adds, dels))
		}
		// Compress diff to remove whitespace-only and comment-only changes before injection.
		compressedDiff := diffcomp.Compress(diffcomp.Diff("", diffSummary), diffcomp.CompressOpts{
			SkipWhitespace: true,
			SkipComments:   true,
			MaxContext:      3,
		})
		compressedText := diffcomp.Render(compressedDiff)
		if compressedText == "" {
			compressedText = diffSummary
		}
		// Truncate diff to last N lines to keep retry context bounded.
		lines := strings.Split(compressedText, "\n")
		if len(lines) > maxRetryContextLines {
			sb.WriteString(fmt.Sprintf("\nCHANGES FROM PREVIOUS ATTEMPT (last %d of %d lines):\n", maxRetryContextLines, len(lines)))
			sb.WriteString(strings.Join(lines[len(lines)-maxRetryContextLines:], "\n") + "\n")
		} else {
			sb.WriteString("\nCHANGES FROM PREVIOUS ATTEMPT:\n")
			sb.WriteString(compressedText + "\n")
		}
	}
	// For test failures, generate test scaffolds from changed Go files to guide the agent.
	if analysis.Class == failure.TestsFailed || analysis.Class == failure.Regression {
		for _, d := range analysis.Specifics {
			if strings.HasSuffix(d.File, ".go") && !strings.HasSuffix(d.File, "_test.go") {
				// Normalize file path: try absolute, then relative to worktree.
				filePath := d.File
				if !filepath.IsAbs(filePath) && worktreeDir != "" {
					filePath = filepath.Join(worktreeDir, filePath)
				}
				if src, readErr := os.ReadFile(filePath); readErr == nil {
					scaffold := testgen.GenerateFile(filepath.Base(filepath.Dir(filePath)), string(src))
					if scaffold != "" {
						sb.WriteString(fmt.Sprintf("\nSUGGESTED TEST SCAFFOLD for %s:\n%s\n", d.File, truncStr(scaffold, 2000)))
						break // one scaffold is enough guidance
					}
				}
			}
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

	// Classify task intent and generate a gate prompt to prepend to planning.
	// This forces the agent to verbalize understanding before implementation.
	intentGate := ""
	classification := intent.Classify(e.Task)
	if intent.RequiresGate(classification) {
		intentGate = intent.GatePrompt(e.Task, classification) + "\n\n"
	}

	return []engine.PhaseSpec{
		{
			Name:         "plan",
			BuiltinTools: plan.BuiltinTools,
			AllowedRules: plan.AllowedRules,
			DeniedRules:  plan.DeniedRules,
			MCPEnabled:   plan.MCPEnabled,
			MaxTurns:     10,
			Prompt:       intentGate + planPromptWithSkills(e),
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
			Prompt:       executePromptWithContext(e),
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
	if e.RunnerOverride != nil {
		if !e.DryRun {
			log := logging.Component("workflow")
			log.Warn("RunnerOverride active in non-dry-run mode — all phases use same runner, cross-model review is bypassed")
		}
		return "mock", e.RunnerOverride
	}

	// Honor explicit runner mode selection.
	switch e.RunnerMode {
	case "native":
		if e.Runners.Native != nil {
			return string(model.ProviderNative), e.Runners.Native
		}
		// Fall through to default routing if native isn't available.
	case "codex":
		if e.Runners.Codex != nil {
			if phase == "plan" {
				// Codex doesn't plan well — use Claude for planning, Codex for execution.
				return string(model.ProviderClaude), e.Runners.Claude
			}
			return string(model.ProviderCodex), e.Runners.Codex
		}
		// Fall through to default routing if codex isn't available.
	case "hybrid":
		// Hybrid = Claude for planning, Codex for execution, cross-model review.
		// This is the default model.Resolve() behavior when both runners exist,
		// so we fall through to the standard routing below.
	}

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
		case model.ProviderNative:
			return e.Runners.Native != nil
		default:
			return false // openrouter/direct-api not yet wired as runners
		}
	}

	// Use cost-aware routing when a budget tracker is available.
	resolve := func(tt model.TaskType) model.Provider {
		if e.CostTracker != nil {
			return model.CostAwareResolve(tt, e.CostTracker, isAvailable)
		}
		return model.Resolve(tt, isAvailable)
	}

	if phase == "verify" {
		execProvider := resolve(e.TaskType)
		reviewer := model.CrossModelReviewer(execProvider)
		return providerToRunner(e, reviewer)
	}

	resolved := resolve(e.TaskType)
	return providerToRunner(e, resolved)
}

func providerToRunner(e Engine, p model.Provider) (string, engine.CommandRunner) {
	switch p {
	case model.ProviderNative:
		if e.Runners.Native != nil {
			return string(p), e.Runners.Native
		}
		return string(model.ProviderClaude), e.Runners.Claude
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

func planPromptWithSkills(e Engine) string {
	prompt := stokeprompts.BuildPlanPrompt(e.Task, false, "")
	reg := e.SkillRegistry
	if reg == nil {
		reg = skill.DefaultRegistry(e.RepoRoot)
		_ = reg.Load()
	}
	prompt, _ = reg.InjectPromptBudgeted(prompt, e.StackMatches, 3000)
	return prompt
}

func executePromptWithContext(e Engine) string {
	prompt := executePrompt(e.Task, e.TaskType, e.TaskVerification)

	// Inject matching built-in skills (keyword-triggered prompt augmentation).
	// Use Engine's registry if available, otherwise auto-create from project root.
	reg := e.SkillRegistry
	if reg == nil {
		reg = skill.DefaultRegistry(e.RepoRoot)
		_ = reg.Load()
	}
	prompt, _ = reg.InjectPromptBudgeted(prompt, e.StackMatches, 3000)

	// Repository map is injected below via ctxpack (not here) to avoid
	// duplication and to respect context window constraints.

	// Optimize prompt for cache alignment: separate static instructions from
	// dynamic task content so API-level prefix caching works effectively.
	opt := promptcache.New()
	opt.AddSection(promptcache.Section{
		Label: "system", Content: stokeprompts.ScopeSystemPrompt(), Static: true, Priority: 0,
	})
	opt.AddSection(promptcache.Section{
		Label: "task", Content: prompt, Static: false, Priority: 10,
	})
	optimized := opt.Build(prompt)

	// Estimate token usage for budget tracking and context window management.
	promptTokens := tokenest.Estimate(optimized.System+optimized.User, tokenest.ContentMixed)

	// Pack context items into the available window using adaptive bin-packing.
	var contextItems []ctxpack.Item
	contextItems = append(contextItems, ctxpack.Item{
		ID: "prompt", Category: "system", Content: prompt,
		Tokens: promptTokens, Relevance: 1.0, Required: true,
	})
	if e.RepoMap != nil {
		budget := e.RepoMapBudget
		if budget <= 0 {
			budget = 2000
		}
		mapContent := e.RepoMap.RenderRelevant(e.AllowedFiles, budget)
		if mapContent != "" {
			contextItems = append(contextItems, ctxpack.Item{
				ID: "repomap", Category: "file", Content: mapContent,
				Tokens: tokenest.Estimate(mapContent, tokenest.ContentCode), Relevance: 0.7,
			})
		}
	}
	packed := ctxpack.Pack(contextItems, ctxpack.Config{
		MaxTokens: 180000, ReserveResponse: 8000,
	})

	// Apply microcompact if the prompt exceeds 80% of context window.
	if packed.TotalTokens > 144000 {
		compactor := microcompact.NewCompactor(microcompact.Config{MaxTokens: 144000})
		sections := []microcompact.Section{{
			Label: "prompt", Content: prompt, Static: false, Priority: 10,
			Tokens: promptTokens,
		}}
		compacted := compactor.Compact(sections)
		if len(compacted.Sections) > 0 {
			prompt = microcompact.Render(compacted)
		}
	}

	// Track prompt fingerprint for cache stability monitoring.
	stokeprompts.TrackPromptVersion(optimized.System)
	if packed.Utilization > 0.9 {
		log := logging.Component("workflow")
		log.Warn("context window nearly full", "utilization", fmt.Sprintf("%.0f%%", packed.Utilization*100), "tokens", packed.TotalTokens)
	}

	return prompt
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

	// Pre-validate JSON structure using schemaval before full parsing.
	// This catches missing fields and type errors with clear diagnostics.
	if jsonStr, ok := schemaval.ExtractJSON(s); ok {
		result := schemaval.Validate(jsonStr, schemaval.Schema{
			Name: "review_verdict",
			Fields: []schemaval.Field{
				{Name: "pass", Type: schemaval.TypeBool, Required: true},
			},
		})
		if !result.Valid {
			return nil, fmt.Errorf("review verdict schema validation: %s", result.Error())
		}
	}

	// Try jsonutil first — handles markdown fences, brace matching, mixed content.
	var v reviewVerdict
	if err := jsonutil.ExtractFromMarkdown(s, &v); err != nil {
		// Fallback: strip fences manually and try raw JSON.
		clean := strings.TrimPrefix(s, "```json")
		clean = strings.TrimPrefix(clean, "```")
		clean = strings.TrimSuffix(clean, "```")
		clean = strings.TrimSpace(clean)
		if err2 := json.Unmarshal([]byte(clean), &v); err2 != nil {
			// Final fallback: use extract package to find JSON in mixed LLM output.
			extracted := false
			if obj := extract.ExtractFirstJSON(s); obj != nil {
				if raw, mErr := json.Marshal(obj); mErr == nil {
					if json.Unmarshal(raw, &v) == nil {
						extracted = true
					}
				}
			}
			if !extracted {
				return nil, err2
			}
		}
	}

	// Minimum validity: a passing review with zero findings suggests the reviewer
	// didn't actually check anything (confused output, truncated response).
	// Require at least one finding entry for a valid review.
	// This check applies to ALL extraction paths — no bypass.
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

// emitEvent sends an event to the hub bus if configured. Nil-safe.
// Returns the hub's decision (Allow/Deny/Abstain). If no bus is configured,
// returns Allow.
func (e Engine) emitEvent(ctx context.Context, ev *hub.Event) hub.Decision {
	if e.EventBus != nil {
		resp := e.EventBus.Emit(ctx, ev)
		if resp != nil {
			return resp.Decision
		}
	}
	return hub.Allow
}

// emitEventAsync sends an event asynchronously if configured. Nil-safe.
func (e Engine) emitEventAsync(ev *hub.Event) {
	if e.EventBus != nil {
		e.EventBus.EmitAsync(ev)
	}
}

// buildSemdiffInputs builds old/new content pairs for semantic diff analysis
// by reading modified files from the worktree and their base versions.
func buildSemdiffInputs(ctx context.Context, handle worktree.Handle) map[string][2]string {
	files, err := worktree.ModifiedFiles(ctx, handle)
	if err != nil || len(files) == 0 {
		return nil
	}
	result := make(map[string][2]string, len(files))
	for _, f := range files {
		newContent := ""
		if data, readErr := os.ReadFile(filepath.Join(handle.Path, f)); readErr == nil {
			newContent = string(data)
		}
		oldContent := ""
		if handle.BaseCommit != "" {
			cmd := exec.CommandContext(ctx, "git", "show", handle.BaseCommit+":"+f)
			cmd.Dir = handle.Path
			if out, showErr := cmd.Output(); showErr == nil {
				oldContent = string(out)
			}
		}
		result[f] = [2]string{oldContent, newContent}
	}
	return result
}

