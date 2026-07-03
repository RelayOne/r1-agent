package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/agentmsg"
	"github.com/RelayOne/r1/internal/branch"
	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/intent"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/specexec"
	"github.com/RelayOne/r1/internal/worktree"
)

// TaskResult is the outcome of one task execution.
type TaskResult struct {
	TaskID    string
	Success   bool
	CostUSD   float64
	DurationMs int64
	Error     error

	// Verification metrics (populated when verify phase runs).
	TestsPassed int
	TestsFailed int
	DiffLines   int

	// PlanOutput carries the plan text when the task ran in plan-only
	// mode (plan.Task.PlanOnly). Executors surface it from the
	// workflow's Result.PlanOutput; WithSpecExec feeds it into
	// specexec.Outcome.PlanText so the plan-aware scorer can rank
	// speculative strategies by plan quality instead of raw speed.
	PlanOutput string

	// Worktree is the live worktree handle carrying a verified,
	// committed, but UNMERGED change when the task ran with
	// plan.Task.NoMerge (full-execution rollouts). Zero-valued
	// (Worktree.Name == "") otherwise. The wrapper that requested the
	// rollout owns merge/discard of the handle.
	Worktree worktree.Handle
}

// ExecuteFunc is the callback the scheduler invokes to run one task.
// The scheduler handles parallelism; this func handles the actual work.
// IMPORTANT: ExecuteFunc is called from multiple goroutines concurrently.
// Implementations must not mutate shared state without synchronization.
type ExecuteFunc func(ctx context.Context, task plan.Task) TaskResult

// PriorityFunc returns the input tasks in the order they should be
// dispatched. Pure — must not modify the input slice in place. The
// default is sortByGRPW (Greatest Rank Positional Weight); alternative
// algorithms (Autellix PLAS, Continuum KV-cache affinity) can be
// registered via Algorithms and selected via Scheduler.PriorityName.
// When PriorityName is empty or unknown, the scheduler falls back to
// GRPW so behavior is byte-identical to the pre-pluggable build.
type PriorityFunc func(tasks []plan.Task) []plan.Task

// Algorithms is the registry of named PriorityFunc implementations.
// Seeded with "grpw" (the legacy default). Callers who bring an
// alternative (e.g. cmd/r1 at startup) can add entries before the
// scheduler runs. Safe for concurrent registration as long as writes
// happen before any Scheduler.Run call begins.
var Algorithms = map[string]PriorityFunc{
	"grpw": sortByGRPW,
}

// Scheduler dispatches tasks in parallel, respecting dependencies and file conflicts.
type Scheduler struct {
	maxWorkers int

	fileLocks map[string]string // file -> writing task ID
	lockMu    sync.Mutex

	stateMu   sync.Mutex        // protects completed, failed, running maps
	completed map[string]bool   // task finished (success or failure)
	failed    map[string]bool   // task failed -- dependents must NOT dispatch
	running   map[string]bool

	// PriorityName selects which Algorithms entry drives task ordering.
	// Empty string or unknown name → fallback to "grpw" without error,
	// so misconfiguration degrades gracefully rather than halting runs.
	PriorityName string

	// MessageBus enables inter-agent communication during parallel task execution.
	// When set, tasks can broadcast status updates and conflict alerts.
	MessageBus *agentmsg.Bus
}

// priority returns the resolved PriorityFunc for this Scheduler, always
// yielding a non-nil result. Unknown PriorityName degrades to GRPW.
func (s *Scheduler) priority() PriorityFunc {
	if s != nil && s.PriorityName != "" {
		if fn, ok := Algorithms[s.PriorityName]; ok && fn != nil {
			return fn
		}
	}
	return sortByGRPW
}

// New creates a scheduler with the given concurrency limit.
func New(maxWorkers int) *Scheduler {
	if maxWorkers < 1 { maxWorkers = 1 }
	return &Scheduler{
		maxWorkers: maxWorkers,
		fileLocks:  make(map[string]string),
		completed:  make(map[string]bool),
		failed:     make(map[string]bool),
		running:    make(map[string]bool),
	}
}

// Run executes all tasks in the plan. Calls execFn for each task.
// Tasks with StatusDone are skipped (resume support).
// Returns results for all tasks.
//
// Previous Scheduler state (completed/failed/running/fileLocks) is
// cleared at the start of each Run so the same Scheduler instance
// can be reused without stale entries from a prior plan leaking in
// — a subsequent Run would otherwise see old task IDs as
// "already completed" and skip them outright.
func (s *Scheduler) Run(ctx context.Context, p *plan.Plan, execFn ExecuteFunc) ([]TaskResult, error) {
	s.stateMu.Lock()
	s.completed = make(map[string]bool)
	s.failed = make(map[string]bool)
	s.running = make(map[string]bool)
	s.fileLocks = make(map[string]string)
	s.stateMu.Unlock()

	tasks := s.priority()(p.Tasks)
	results := make(chan TaskResult, len(tasks))
	var allResults []TaskResult
	var wg sync.WaitGroup
	active := 0

	// Pre-populate completed tasks (resume support)
	for _, t := range tasks {
		if t.Status == plan.StatusDone {
			s.completed[t.ID] = true
			allResults = append(allResults, TaskResult{TaskID: t.ID, Success: true})
		}
	}

	// recordResult updates state maps under stateMu
	recordResult := func(r TaskResult) {
		s.stateMu.Lock()
		s.releaseFiles(r.TaskID, tasks)
		delete(s.running, r.TaskID)
		s.completed[r.TaskID] = true
		if !r.Success {
			s.failed[r.TaskID] = true
		}
		s.stateMu.Unlock()

		// Broadcast task completion/failure via the message bus.
		if s.MessageBus != nil {
			status := "completed"
			if !r.Success {
				status = "failed"
			}
			s.MessageBus.Broadcast("scheduler", "task."+status, map[string]any{
				"task_id":  r.TaskID,
				"success":  r.Success,
				"cost_usd": r.CostUSD,
			})
		}
	}

	// drainResults collects all immediately-available results without blocking.
	drainResults := func() {
		for {
			select {
			case r := <-results:
				wg.Done()
				active--
				allResults = append(allResults, r)
				recordResult(r)
			default:
				return
			}
		}
	}

	for {
		// Non-blocking drain of any completed results.
		drainResults()

		s.stateMu.Lock()
		allDone := len(s.completed) == len(tasks)
		s.stateMu.Unlock()
		if allDone {
			break
		}

		// R5: once the context is cancelled (Ctrl-C / timeout), stop
		// dispatching queued tasks. Previously the dispatch section below
		// never checked ctx.Err(), so every still-ready task was launched
		// into a dead context, failed with "context canceled", and got
		// written up as a real task failure — corrupting attempt numbering,
		// failure-fingerprint escalation, and wisdom, and leaving resume
		// with phantom failed tasks. Fall through to the in-flight drain and
		// return results for genuinely-dispatched tasks only; undispatched
		// tasks stay untouched (StatusPending) for resume.
		if ctx.Err() != nil {
			for active > 0 {
				r := <-results
				wg.Done()
				active--
				allResults = append(allResults, r)
				recordResult(r)
			}
			wg.Wait()
			return allResults, ctx.Err()
		}

		// Dispatch all ready tasks (collect candidates, then launch outside lock).
		s.stateMu.Lock()
		var toDispatch []plan.Task
		for _, t := range tasks {
			if s.completed[t.ID] || s.running[t.ID] {
				continue
			}
			if active+len(toDispatch) >= s.maxWorkers {
				break
			}
			if !s.depsOK(t) || s.hasConflict(t) {
				continue
			}
			s.acquireFiles(t)
			s.running[t.ID] = true
			toDispatch = append(toDispatch, t)
		}
		s.stateMu.Unlock()

		for _, t := range toDispatch {
			active++
			wg.Add(1)
			go func(task plan.Task) {
				defer func() {
					if r := recover(); r != nil {
						results <- TaskResult{
							TaskID:  task.ID,
							Success: false,
							Error:   fmt.Errorf("panic in task %s: %v", task.ID, r),
						}
					}
				}()
				results <- execFn(ctx, task)
			}(t)
		}

		// If nothing is running and nothing was dispatched, check why.
		if active == 0 && len(toDispatch) == 0 {
			s.stateMu.Lock()
			remaining := len(tasks) - len(s.completed)
			if remaining > 0 {
				blockedByFailure := 0
				for _, t := range tasks {
					if s.completed[t.ID] {
						continue
					}
					for _, dep := range t.Dependencies {
						if s.failed[dep] {
							blockedByFailure++
							s.completed[t.ID] = true
							s.failed[t.ID] = true
							allResults = append(allResults, TaskResult{
								TaskID: t.ID,
								Error:  fmt.Errorf("blocked: dependency %s failed", dep),
							})
							break
						}
					}
				}
				s.stateMu.Unlock()
				if blockedByFailure > 0 {
					continue // re-check for cascading blocks
				}
				return allResults, fmt.Errorf("deadlock: %d tasks undispatchable (no failed deps, possible cycle)", remaining)
			}
			s.stateMu.Unlock()
			break
		}

		// Block until at least one result arrives or context is cancelled.
		// This is the key fix: no busy-wait. We only loop when there's a
		// result to process or new tasks to dispatch.
		if active > 0 && len(toDispatch) == 0 {
			select {
			case r := <-results:
				wg.Done()
				active--
				allResults = append(allResults, r)
				recordResult(r)
			case <-ctx.Done():
				// Workers only exit by delivering into results; wg.Done
				// happens on the receive side. Drain every in-flight
				// result (bounded: execFn honors ctx and returns once
				// its process group is torn down) or wg.Wait blocks
				// forever and the whole r1 process hangs on Ctrl-C.
				for active > 0 {
					r := <-results
					wg.Done()
					active--
					allResults = append(allResults, r)
					recordResult(r)
				}
				wg.Wait()
				return allResults, ctx.Err()
			}
		}
	}

	wg.Wait()
	return allResults, nil
}

func (s *Scheduler) depsOK(t plan.Task) bool {
	for _, dep := range t.Dependencies {
		if !s.completed[dep] { return false } // dep hasn't finished
		if s.failed[dep] { return false }     // dep failed -- block downstream
	}
	return true
}

func (s *Scheduler) hasConflict(t plan.Task) bool {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	for _, f := range t.Files {
		if owner, ok := s.fileLocks[f]; ok && owner != "" {
			return true
		}
	}
	return false
}

func (s *Scheduler) acquireFiles(t plan.Task) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	for _, f := range t.Files { s.fileLocks[f] = t.ID }
}

func (s *Scheduler) releaseFiles(taskID string, tasks []plan.Task) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	for f, owner := range s.fileLocks {
		if owner == taskID { delete(s.fileLocks, f) }
	}
}

// sortByGRPW returns tasks sorted by Greatest Rank Positional Weight.
//
// Cycle-safe: the recursive weight computation tracks a
// `visiting` set so a dependency cycle returns `weight=1`
// for the revisited ID instead of unbounded-recursing into
// a stack overflow. Production SOWs validate DAG acyclicity
// upstream, but the default priority runs against unvalidated
// task lists too, and the old implementation would hang the
// whole run on a cyclic plan.
func sortByGRPW(tasks []plan.Task) []plan.Task {
	sorted := make([]plan.Task, len(tasks))
	copy(sorted, tasks)

	dependents := map[string][]string{}
	for _, t := range sorted {
		for _, dep := range t.Dependencies {
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}

	weights := map[string]int{}
	visiting := map[string]bool{}
	var weight func(string) int
	weight = func(id string) int {
		if w, ok := weights[id]; ok {
			return w
		}
		if visiting[id] {
			// Cycle detected at `id`; short-circuit to 1 so
			// the caller's sum stays bounded. The memoization
			// entry is set AFTER the fan-out finishes.
			return 1
		}
		visiting[id] = true
		w := 1
		for _, d := range dependents[id] {
			w += weight(d)
		}
		delete(visiting, id)
		weights[id] = w
		return w
	}
	for _, t := range sorted {
		weight(t.ID)
	}

	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && weights[sorted[j].ID] > weights[sorted[j-1].ID]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}

// SpecExecConfig configures speculative execution integration.
type SpecExecConfig struct {
	// Approaches are the alternative strategy prompts to try.
	// Each creates a specexec.Strategy with a modified prompt.
	Approaches []string

	// MaxParallel limits concurrent speculative strategies. Default: 3.
	MaxParallel int

	// Timeout per strategy. Default: 5 minutes.
	Timeout time.Duration

	// ShouldSpeculate decides whether a task should use speculative execution.
	// If nil, all tasks use speculative execution.
	ShouldSpeculate func(task plan.Task) bool

	// FullExecution switches speculation from plan-only exploration to
	// best-of-N FULL rollouts: each strategy runs the real pipeline
	// (plan/execute/verify/commit) in its own isolated worktree with
	// plan.Task.NoMerge set, outcomes are scored on real build/test
	// signal (specexec.DefaultScorer), only the winning worktree is
	// merged via MergeWinner, and every other rollout is discarded.
	// Requires MergeWinner and DiscardRollout — when either is missing
	// the wrapper FAILS CLOSED to the plan-only behavior so no rollout
	// worktree can ever be created without a discard path.
	FullExecution bool

	// MergeWinner lands the winning rollout's verified branch on main.
	// Wired to (*worktree.Manager).Merge, which serializes all merges
	// on its mergeMu and self-cleans the worktree on success.
	MergeWinner func(ctx context.Context, h worktree.Handle, msg string) error

	// DiscardRollout removes a losing rollout's worktree and branch.
	// Wired to (*worktree.Manager).Cleanup.
	DiscardRollout func(ctx context.Context, h worktree.Handle) error

	// Tracker gates rollout spend: OverBudget() skips speculation for
	// the task entirely, and a nearly exhausted budget (<20% remaining)
	// clamps the rollout count to 2. Nil = no budget constraints.
	Tracker *costtrack.Tracker

	// Models are runner names ("claude"|"codex"|"native") round-robined
	// into Strategy.Model so parallel rollouts differ by engine as well
	// as by prompt. Empty = every rollout uses the build default.
	Models []string

	// Selector optionally overrides the deterministic score-sorted
	// winner with a comparative judgment over all outcomes (the seam
	// for an LLM judge; see specexec.Selector). It augments the Scorer,
	// never replaces it — nil or any selector failure keeps the
	// deterministic winner.
	Selector specexec.Selector
}

// WithSpecExec wraps an ExecuteFunc to use speculative parallel execution
// for tasks that match the predicate. For each speculative task, it runs
// parallel PLAN-ONLY explorations with different strategy prompts, scores
// the plans by deterministic structural quality plus speed
// (specexec.PlanScorer over TaskResult.PlanOutput — plan-only outcomes
// carry no test/diff signal, so DefaultScorer would reduce winner
// selection to wall-clock latency), and then executes the winning
// strategy through the real pipeline.
//
// SAFETY: Speculative strategies are plan-only (no execute, no verify, no merge).
// Only the winning strategy runs through the full pipeline with side effects.
//
// Non-speculative tasks pass through to the base ExecuteFunc unchanged.
func WithSpecExec(base ExecuteFunc, cfg SpecExecConfig) ExecuteFunc {
	if len(cfg.Approaches) == 0 {
		return base // no alternative approaches → no speculation
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 3
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	// Fail closed: full execution without both callbacks would leak
	// rollout worktrees (no discard path) or strand the winner (no
	// merge path). Degrade to plan-only speculation instead.
	if cfg.FullExecution && (cfg.MergeWinner == nil || cfg.DiscardRollout == nil) {
		cfg.FullExecution = false
	}

	return func(ctx context.Context, task plan.Task) TaskResult {
		if cfg.ShouldSpeculate != nil && !cfg.ShouldSpeculate(task) {
			return base(ctx, task)
		}

		// Best-of-N full rollouts. A task that is itself plan-only
		// never escalates into full execution (contract mirrored from
		// the plan-only branch below).
		if cfg.FullExecution && !task.PlanOnly {
			return runFullRollouts(ctx, base, cfg, task)
		}

		// Build strategies from approaches
		strategies := specexec.GenerateStrategies(task.Description, cfg.Approaches)

		// Create a branch explorer so each speculative strategy is tracked as a
		// conversation branch. This enables scoring, selection, and pruning of
		// failed exploration paths.
		explorer := branch.NewExplorer([]branch.Message{
			{Role: "system", Content: task.Description},
		})
		strategyBranches := make(map[string]string, len(strategies))
		for _, s := range strategies {
			b := explorer.Fork(s.ID)
			strategyBranches[s.ID] = b.ID
		}

		// Plan-only outcomes have TestsPassed=TestsFailed=DiffLines=0,
		// so DefaultScorer would award every successful strategy the
		// same flat fallbacks and the winner would degenerate to
		// "fastest plan" (its old 0.9 threshold was also unreachable —
		// plan-only scores capped at ~0.6, audit A024). PlanScorer
		// ranks by plan structure instead, and PlanStopThreshold is
		// reachable exactly when a plan shows full structural quality.
		spec := specexec.Spec{
			Strategies:    strategies,
			MaxParallel:   cfg.MaxParallel,
			Timeout:       cfg.Timeout,
			EarlyStop:     true,
			StopThreshold: specexec.PlanStopThreshold,
			Scorer:        specexec.PlanScorer,
			Selector:      cfg.Selector,
		}

		// PHASE 1: Run plan-only explorations in parallel.
		// Each strategy gets a unique task ID and PlanOnly=true, so the workflow
		// runs ONLY the plan phase (no execute, no verify, no merge).
		// This is structurally enforced: workflow.Engine.PlanOnly skips the
		// execute+verify loop entirely. No side effects, no worktree mutations.
		executor := func(ctx context.Context, strategy specexec.Strategy) specexec.Outcome {
			specTask := task
			specTask.ID = fmt.Sprintf("%s-spec-%s", task.ID, strategy.ID)
			specTask.Description = strategy.Prompt
			specTask.PlanOnly = true // CRITICAL: prevents execute/verify/merge

			start := time.Now()
			result := base(ctx, specTask)

			outcome := specexec.Outcome{
				StrategyID:  strategy.ID,
				Success:     result.Success,
				Duration:    time.Since(start),
				TestsPassed: result.TestsPassed,
				TestsFailed: result.TestsFailed,
				DiffLines:   result.DiffLines,
				PlanText:    result.PlanOutput,
			}
			if result.Error != nil {
				outcome.Error = result.Error.Error()
				if bid, ok := strategyBranches[strategy.ID]; ok {
					_ = explorer.Fail(bid, result.Error.Error())
				}
			} else if result.Success {
				if bid, ok := strategyBranches[strategy.ID]; ok {
					_ = explorer.Complete(bid, 1.0)
				}
			}
			return outcome
		}

		result := specexec.Run(ctx, spec, executor)
		// Prune failed branches to free memory after speculation completes.
		explorer.Prune()

		// Carry learnings from the explorations forward (the specexec
		// package promise "merge insights from failed approaches into
		// retry prompts"): the winner's real run sees what the other
		// approaches tripped over, and the all-failed error surfaces
		// the same learnings to the workflow retry loop.
		insights := specexec.ExtractInsights(result)

		// PHASE 2: Execute the winning strategy through the real pipeline.
		if result.Winner != nil {
			// Find the winning strategy's prompt
			var winningPrompt string
			for _, s := range strategies {
				if s.ID == result.Winner.StrategyID {
					winningPrompt = s.Prompt
					break
				}
			}
			if winningPrompt == "" {
				winningPrompt = task.Description // fallback
			}

			// Run the winner through the full pipeline (with merge)
			realTask := task
			realTask.Description = winningPrompt
			// Preserve original PlanOnly contract — specexec must not
			// escalate a plan-only task into full execution.
			realTask.PlanOnly = task.PlanOnly
			if len(insights) > 0 {
				realTask.Description += "\n\nLearnings from other explored approaches:\n- " +
					strings.Join(insights, "\n- ")
			}
			return base(ctx, realTask)
		}

		// All strategies failed — return error carrying the collected
		// insights so the workflow retry loop can learn from them.
		bestErr := fmt.Errorf("all %d speculative strategies failed", len(result.Outcomes))
		if len(insights) > 0 {
			bestErr = fmt.Errorf("all %d speculative strategies failed; insights: %s",
				len(result.Outcomes), strings.Join(insights, "; "))
		}
		return TaskResult{
			TaskID:     task.ID,
			Success:    false,
			DurationMs: result.Duration.Milliseconds(),
			Error:      bestErr,
		}
	}
}

// rolloutDiffCap bounds how much unified diff each rollout contributes
// to the comparative Selector prompt (the selector re-caps per block).
const rolloutDiffCap = 64 * 1024

// rolloutCount decides how many full-execution rollouts a task earns.
// Full rollouts cost N× tokens plus N× verify CPU, so N tracks task
// difficulty instead of being hardcoded: trivial tasks skip speculation
// entirely (0 = run the task normally), explicit specs get 2, tasks
// needing investigation or interpretation get 3, and open-ended tasks
// use every configured approach. When less than 20% of the cost budget
// remains, N clamps to 2 so best-of-N never burns the budget's tail.
func rolloutCount(desc string, tracker *costtrack.Tracker, max int) int {
	if max <= 0 {
		return 0
	}
	var n int
	switch intent.Classify(desc).Class {
	case intent.ClassTrivial:
		return 0
	case intent.ClassExplicit:
		n = 2
	case intent.ClassOpenEnded:
		n = max
	default: // exploratory / ambiguous: worth comparing interpretations
		n = 3
	}
	if n > max {
		n = max
	}
	if tracker != nil {
		// BudgetRemaining() returns -1 for unlimited budgets; only a
		// real cap participates in the clamp.
		if remaining := tracker.BudgetRemaining(); remaining >= 0 {
			if budget := remaining + tracker.Total(); budget > 0 && remaining/budget < 0.2 && n > 2 {
				n = 2
			}
		}
	}
	return n
}

// runFullRollouts executes the best-of-N full-execution branch: N
// strategies run the REAL pipeline in isolated worktrees (NoMerge, so
// each stops after merge validation), outcomes are scored on actual
// build/test results, the winner's already-verified branch is merged
// through the caller's mergeMu-serialized path, and the losers are
// discarded. Every failure mode degrades to a single normal execution
// so the task is never left unattempted.
func runFullRollouts(ctx context.Context, base ExecuteFunc, cfg SpecExecConfig, task plan.Task) TaskResult {
	// Over budget: no speculation spend at all.
	if cfg.Tracker != nil && cfg.Tracker.OverBudget() {
		return base(ctx, task)
	}
	n := rolloutCount(task.Description, cfg.Tracker, len(cfg.Approaches))
	if n <= 0 {
		return base(ctx, task)
	}

	strategies := specexec.GenerateStrategiesWithModels(task.Description, cfg.Approaches[:n], cfg.Models)

	// Stash each rollout's full TaskResult (including its worktree
	// handle) so the winner can be merged and the losers discarded.
	var mu sync.Mutex
	rollouts := make(map[string]TaskResult, len(strategies))

	executor := func(ctx context.Context, strategy specexec.Strategy) specexec.Outcome {
		specTask := task
		specTask.ID = fmt.Sprintf("%s-spec-%s", task.ID, strategy.ID)
		specTask.Description = strategy.Prompt
		specTask.PlanOnly = false
		specTask.NoMerge = true // rollout stops before merge; only the winner merges
		specTask.Runner = strategy.Model

		start := time.Now()
		res := base(ctx, specTask)
		mu.Lock()
		rollouts[strategy.ID] = res
		mu.Unlock()

		outcome := specexec.Outcome{
			StrategyID:  strategy.ID,
			Success:     res.Success,
			Duration:    time.Since(start),
			TestsPassed: res.TestsPassed,
			TestsFailed: res.TestsFailed,
			DiffLines:   res.DiffLines,
		}
		if res.Worktree.Name != "" {
			outcome.Artifacts = []string{res.Worktree.Path}
			// Real patch hunks for the comparative Selector; numeric
			// signals above are what DefaultScorer consumes.
			outcome.DiffText = worktree.DiffText(ctx, res.Worktree, rolloutDiffCap)
		}
		if res.Error != nil {
			outcome.Error = res.Error.Error()
		}
		return outcome
	}

	spec := specexec.Spec{
		Strategies:  strategies,
		MaxParallel: cfg.MaxParallel,
		Timeout:     cfg.Timeout,
		// EarlyStop stays OFF: every rollout must run to completion so
		// its worktree is deterministically tracked for discard below.
		Scorer:   specexec.DefaultScorer,
		Selector: cfg.Selector,
	}
	result := specexec.Run(ctx, spec, executor)
	insights := specexec.ExtractInsights(result)

	// Sum speculation spend so the returned TaskResult reports the true
	// cost of the task, not just the surviving rollout's share.
	var speculationCost float64
	mu.Lock()
	for _, r := range rollouts {
		speculationCost += r.CostUSD
	}
	mu.Unlock()

	discard := func(strategyID string) {
		mu.Lock()
		r, ok := rollouts[strategyID]
		mu.Unlock()
		if !ok || r.Worktree.Name == "" {
			return
		}
		if derr := cfg.DiscardRollout(ctx, r.Worktree); derr != nil {
			insights = append(insights, fmt.Sprintf("discard rollout %s: %v", strategyID, derr))
		}
	}

	if result.Winner != nil {
		winID := result.Winner.StrategyID
		mu.Lock()
		winRes, ok := rollouts[winID]
		mu.Unlock()

		// Losers are discarded regardless of what happens to the winner.
		for _, s := range strategies {
			if s.ID != winID {
				discard(s.ID)
			}
		}

		if ok && winRes.Worktree.Name != "" {
			msg := fmt.Sprintf("feat(specexec-bestof%d): %s [strategy %s]", n, task.ID, winID)
			mergeErr := cfg.MergeWinner(ctx, winRes.Worktree, msg)
			if mergeErr == nil {
				// MergeWinner (worktree.Manager.Merge) self-cleans the
				// winning worktree on success.
				winRes.TaskID = task.ID
				winRes.CostUSD = speculationCost
				winRes.Worktree = worktree.Handle{}
				return winRes
			}
			// Merge failure (e.g. a sibling task merged a conflicting
			// change while the rollouts ran): the rollout branch is
			// stale, so discard it and fall through to a normal
			// re-execution of the winning approach below.
			insights = append(insights, fmt.Sprintf("winning rollout %s failed to merge: %v", winID, mergeErr))
			discard(winID)
		}

		// No mergeable worktree (zero-diff rollout) or merge failure:
		// re-execute the winning approach through the normal pipeline
		// (with merge), carrying learnings from all rollouts.
		var winningPrompt string
		for _, s := range strategies {
			if s.ID == winID {
				winningPrompt = s.Prompt
				break
			}
		}
		if winningPrompt == "" {
			winningPrompt = task.Description
		}
		realTask := task
		realTask.Description = winningPrompt
		if len(insights) > 0 {
			realTask.Description += "\n\nLearnings from other explored approaches:\n- " +
				strings.Join(insights, "\n- ")
		}
		final := base(ctx, realTask)
		final.CostUSD += speculationCost
		return final
	}

	// All rollouts failed: discard every live worktree and surface the
	// collected insights to the workflow retry loop.
	for _, s := range strategies {
		discard(s.ID)
	}
	bestErr := fmt.Errorf("all %d full-execution rollouts failed", len(result.Outcomes))
	if len(insights) > 0 {
		bestErr = fmt.Errorf("all %d full-execution rollouts failed; insights: %s",
			len(result.Outcomes), strings.Join(insights, "; "))
	}
	return TaskResult{
		TaskID:     task.ID,
		Success:    false,
		CostUSD:    speculationCost,
		DurationMs: result.Duration.Milliseconds(),
		Error:      bestErr,
	}
}
