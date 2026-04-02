package scheduler

import (
	"context"
	"fmt"
	"sync"

	"stoke/internal/plan"
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

	completed map[string]bool // task finished (success or failure)
	failed    map[string]bool // task failed -- dependents must NOT dispatch
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

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return allResults, ctx.Err()
		default:
		}

		// Drain results
		drained := true
		for drained {
			select {
			case r := <-results:
				wg.Done()
				active--
				allResults = append(allResults, r)
				s.releaseFiles(r.TaskID, tasks)
				delete(s.running, r.TaskID)
				s.completed[r.TaskID] = true
				if !r.Success {
					s.failed[r.TaskID] = true
				}
			default:
				drained = false
			}
		}

		if len(s.completed) == len(tasks) { break }

		// Find dispatchable tasks
		for _, t := range tasks {
			if s.completed[t.ID] || s.running[t.ID] { continue }
			if active >= s.maxWorkers { break }
			if !s.depsOK(t) || s.hasConflict(t) { continue }

			s.acquireFiles(t)
			s.running[t.ID] = true
			active++
			wg.Add(1)
			go func(task plan.Task) {
				results <- execFn(ctx, task)
			}(t)
		}

		// If nothing is running and nothing dispatchable, check why
		if active == 0 {
			remaining := len(tasks) - len(s.completed)
			if remaining > 0 {
				// Check if remaining tasks are blocked by failed dependencies
				blockedByFailure := 0
				for _, t := range tasks {
					if s.completed[t.ID] { continue }
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
				if blockedByFailure > 0 {
					// Re-check: there may be more cascading blocks
					continue
				}
				return allResults, fmt.Errorf("deadlock: %d tasks undispatchable (no failed deps, possible cycle)", remaining)
			}
			break
		}

		// Wait for at least one result before trying again
		if len(s.findDispatchable(tasks)) == 0 && active > 0 {
			r := <-results
			wg.Done()
			active--
			allResults = append(allResults, r)
			s.releaseFiles(r.TaskID, tasks)
			delete(s.running, r.TaskID)
			s.completed[r.TaskID] = true
			if !r.Success {
				s.failed[r.TaskID] = true
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
