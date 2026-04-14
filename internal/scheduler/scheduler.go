package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/agentmsg"
	"github.com/ericmacdougall/stoke/internal/branch"
	"github.com/ericmacdougall/stoke/internal/dispatch"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/specexec"
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
// alternative (e.g. cmd/stoke at startup) can add entries before the
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

	// DispatchQueue provides reliable message delivery with retry for task events.
	// When set, task completion/failure events are dispatched through the queue.
	DispatchQueue *dispatch.Queue
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
func (s *Scheduler) Run(ctx context.Context, p *plan.Plan, execFn ExecuteFunc) ([]TaskResult, error) {
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

		// Dispatch task result event through the reliable delivery queue.
		if s.DispatchQueue != nil {
			priority := dispatch.PriorityNormal
			if !r.Success {
				priority = dispatch.PriorityHigh
			}
			s.DispatchQueue.Enqueue("task.result", "orchestrator", priority, map[string]any{
				"task_id": r.TaskID,
				"success": r.Success,
			}, fmt.Sprintf("result-%s", r.TaskID))
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

func (s *Scheduler) findDispatchable(tasks []plan.Task) []plan.Task {
	var ready []plan.Task
	for _, t := range tasks {
		if s.completed[t.ID] || s.running[t.ID] { continue }
		if s.depsOK(t) && !s.hasConflict(t) {
			ready = append(ready, t)
		}
	}
	return ready
}

// sortByGRPW returns tasks sorted by Greatest Rank Positional Weight.
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
	var weight func(string) int
	weight = func(id string) int {
		if w, ok := weights[id]; ok { return w }
		w := 1
		for _, d := range dependents[id] { w += weight(d) }
		weights[id] = w
		return w
	}
	for _, t := range sorted { weight(t.ID) }

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
}

// WithSpecExec wraps an ExecuteFunc to use speculative parallel execution
// for tasks that match the predicate. For each speculative task, it runs
// parallel PLAN-ONLY explorations with different strategy prompts, scores
// the plans, and then executes the winning strategy through the real pipeline.
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

	return func(ctx context.Context, task plan.Task) TaskResult {
		if cfg.ShouldSpeculate != nil && !cfg.ShouldSpeculate(task) {
			return base(ctx, task)
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

		spec := specexec.Spec{
			Strategies:    strategies,
			MaxParallel:   cfg.MaxParallel,
			Timeout:       cfg.Timeout,
			EarlyStop:     true,
			StopThreshold: 0.9,
			Scorer:        specexec.DefaultScorer,
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
			return base(ctx, realTask)
		}

		// All strategies failed — return error
		bestErr := fmt.Errorf("all %d speculative strategies failed", len(result.Outcomes))
		for _, o := range result.Outcomes {
			if o.Error != "" {
				bestErr = fmt.Errorf("all %d strategies failed; last: %s", len(result.Outcomes), o.Error)
				break
			}
		}
		return TaskResult{
			TaskID:     task.ID,
			Success:    false,
			DurationMs: result.Duration.Milliseconds(),
			Error:      bestErr,
		}
	}
}
