package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

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
}

// ExecuteFunc is the callback the scheduler invokes to run one task.
// The scheduler handles parallelism; this func handles the actual work.
// IMPORTANT: ExecuteFunc is called from multiple goroutines concurrently.
// Implementations must not mutate shared state without synchronization.
type ExecuteFunc func(ctx context.Context, task plan.Task) TaskResult

// Scheduler dispatches tasks in parallel, respecting dependencies and file conflicts.
type Scheduler struct {
	maxWorkers int

	fileLocks map[string]string // file -> writing task ID
	lockMu    sync.Mutex

	stateMu   sync.Mutex        // protects completed, failed, running maps
	completed map[string]bool   // task finished (success or failure)
	failed    map[string]bool   // task failed -- dependents must NOT dispatch
	running   map[string]bool
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
	tasks := sortByGRPW(p.Tasks)
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
// for tasks that match the predicate. For each speculative task, it forks
// multiple strategies (alternative prompts), runs them in parallel via
// specexec.Run, and returns the winning result.
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

		spec := specexec.Spec{
			Strategies:    strategies,
			MaxParallel:   cfg.MaxParallel,
			Timeout:       cfg.Timeout,
			EarlyStop:     true,
			StopThreshold: 0.9,
			Scorer:        specexec.DefaultScorer,
		}

		// Bridge specexec.Executor to our base ExecuteFunc
		executor := func(ctx context.Context, strategy specexec.Strategy) specexec.Outcome {
			// Replace the task description with the strategy prompt
			specTask := task
			specTask.Description = strategy.Prompt

			start := time.Now()
			result := base(ctx, specTask)

			outcome := specexec.Outcome{
				StrategyID: strategy.ID,
				Success:    result.Success,
				Duration:   time.Since(start),
			}
			if result.Error != nil {
				outcome.Error = result.Error.Error()
			}
			return outcome
		}

		result := specexec.Run(ctx, spec, executor)

		if result.Winner != nil {
			return TaskResult{
				TaskID:     task.ID,
				Success:    result.Winner.Success,
				DurationMs: result.Duration.Milliseconds(),
			}
		}

		// All strategies failed — return the best attempt's error
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
