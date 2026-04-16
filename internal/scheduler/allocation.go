// allocation.go implements weighted task allocation scoring.
// Inspired by OmX's allocation policy: score worker-task pairs using weighted
// factors to find the best assignment. This complements GRPW priority ordering
// (which determines WHAT to do next) with affinity scoring (which determines
// WHO should do it).
//
// OmX weights: +18 role match, +12 worker role, +4 expertise overlap,
// -4 workload penalty, -3 mismatch penalty.
package scheduler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// Worker represents an available execution slot with capabilities.
type Worker struct {
	ID         string   `json:"id"`
	Provider   string   `json:"provider"`    // "claude", "codex"
	Roles      []string `json:"roles"`       // e.g., "executor", "reviewer", "security"
	Expertise  []string `json:"expertise"`   // file patterns or domains
	ActiveLoad int      `json:"active_load"` // number of currently assigned tasks
}

// AllocationScore is the computed score for a worker-task pair.
type AllocationScore struct {
	WorkerID string  `json:"worker_id"`
	TaskID   string  `json:"task_id"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason"`
}

// AllocationWeights controls the scoring factors.
type AllocationWeights struct {
	RoleMatch       float64 // bonus when worker's primary role matches task type
	RolePartial     float64 // bonus for secondary role match
	ExpertiseMatch  float64 // bonus per matching expertise domain/file
	WorkloadPenalty float64 // penalty per existing active task
	MismatchPenalty float64 // penalty when task domain doesn't match worker
}

// DefaultWeights returns OmX-inspired allocation weights.
func DefaultWeights() AllocationWeights {
	return AllocationWeights{
		RoleMatch:       18.0,
		RolePartial:     12.0,
		ExpertiseMatch:  4.0,
		WorkloadPenalty: 4.0,
		MismatchPenalty: 3.0,
	}
}

// ScoreAllocation computes a score for assigning a task to a worker.
func ScoreAllocation(worker Worker, task plan.Task, weights AllocationWeights) AllocationScore {
	score := 0.0
	var reasons []string

	taskType := strings.ToLower(task.Type)
	if taskType == "" {
		taskType = "general"
	}

	// Role matching
	roleMatched := false
	if len(worker.Roles) > 0 {
		primary := strings.ToLower(worker.Roles[0])
		if matchesTaskType(primary, taskType) {
			score += weights.RoleMatch
			reasons = append(reasons, "primary role match")
			roleMatched = true
		} else {
			for _, role := range worker.Roles[1:] {
				if matchesTaskType(strings.ToLower(role), taskType) {
					score += weights.RolePartial
					reasons = append(reasons, "secondary role match")
					roleMatched = true
					break
				}
			}
		}
	}

	// Mismatch penalty
	if !roleMatched && len(worker.Roles) > 0 {
		score -= weights.MismatchPenalty
		reasons = append(reasons, "role mismatch")
	}

	// Expertise overlap
	expertiseHits := 0
	for _, exp := range worker.Expertise {
		for _, file := range task.Files {
			if matchExpertise(exp, file) {
				expertiseHits++
			}
		}
		// Also check task description for domain keywords
		if strings.Contains(strings.ToLower(task.Description), strings.ToLower(exp)) {
			expertiseHits++
		}
	}
	score += float64(expertiseHits) * weights.ExpertiseMatch
	if expertiseHits > 0 {
		reasons = append(reasons, fmt.Sprintf("%d expertise matches", expertiseHits))
	}

	// Workload penalty
	score -= float64(worker.ActiveLoad) * weights.WorkloadPenalty
	if worker.ActiveLoad > 0 {
		reasons = append(reasons, fmt.Sprintf("-%d workload penalty", worker.ActiveLoad))
	}

	return AllocationScore{
		WorkerID: worker.ID,
		TaskID:   task.ID,
		Score:    score,
		Reason:   strings.Join(reasons, ", "),
	}
}

// BestWorker finds the best worker for a task from available workers.
// Returns the worker ID with the highest allocation score.
//
// Tie-break correctness: pair each score with its source
// worker's ActiveLoad up front so the permutation in
// sort.Slice doesn't desync the tie-break lookup. The
// previous implementation read workers[i]/workers[j] after
// sorting `scores`, which picked unrelated workers' loads
// as the tiebreaker.
func BestWorker(workers []Worker, task plan.Task, weights AllocationWeights) (string, AllocationScore) {
	if len(workers) == 0 {
		return "", AllocationScore{}
	}
	type scored struct {
		s    AllocationScore
		load int
	}
	rows := make([]scored, len(workers))
	for i, w := range workers {
		rows[i] = scored{s: ScoreAllocation(w, task, weights), load: w.ActiveLoad}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].s.Score != rows[j].s.Score {
			return rows[i].s.Score > rows[j].s.Score
		}
		return rows[i].load < rows[j].load
	})
	return rows[0].s.WorkerID, rows[0].s
}

// AssignTasks assigns a batch of tasks to workers optimally.
// Uses a greedy approach: assign highest-scored pair first, then update loads.
func AssignTasks(workers []Worker, tasks []plan.Task, weights AllocationWeights) map[string]string {
	assignments := make(map[string]string) // task ID -> worker ID

	// Copy workers to track load updates
	wc := make([]Worker, len(workers))
	copy(wc, workers)

	// Sort tasks by dependency count (fewer deps = schedule first)
	sorted := make([]plan.Task, len(tasks))
	copy(sorted, tasks)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i].Dependencies) < len(sorted[j].Dependencies)
	})

	for _, task := range sorted {
		bestID, _ := BestWorker(wc, task, weights)
		if bestID != "" {
			assignments[task.ID] = bestID
			// Update load
			for i := range wc {
				if wc[i].ID == bestID {
					wc[i].ActiveLoad++
					break
				}
			}
		}
	}

	return assignments
}

// --- Internal ---

// matchesTaskType checks if a worker role aligns with a task type.
func matchesTaskType(role, taskType string) bool {
	// Direct match
	if role == taskType {
		return true
	}

	// Semantic mappings
	mappings := map[string][]string{
		"executor":  {"refactor", "implement", "general", "typesafety"},
		"reviewer":  {"review", "audit"},
		"security":  {"security", "auth"},
		"architect": {"architecture", "design"},
		"devops":    {"devops", "ci", "deploy"},
		"tester":    {"test", "qa"},
		"debugger":  {"debug", "fix", "concurrency"},
		"planner":   {"plan"},
		"docs":      {"docs", "documentation"},
	}

	if types, ok := mappings[role]; ok {
		for _, t := range types {
			if t == taskType {
				return true
			}
		}
	}
	return false
}

// matchExpertise checks if a worker's expertise matches a file path.
func matchExpertise(expertise, filePath string) bool {
	// Directory/path prefix match
	if strings.HasSuffix(expertise, "/") {
		return strings.HasPrefix(filePath, expertise)
	}
	// Extension match
	if strings.HasPrefix(expertise, "*.") {
		ext := expertise[1:]
		return strings.HasSuffix(filePath, ext)
	}
	// Substring match
	return strings.Contains(filePath, expertise)
}
