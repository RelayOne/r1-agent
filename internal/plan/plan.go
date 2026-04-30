package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Plan is a structured collection of tasks to execute.
type Plan struct {
	ID                     string    `json:"id"`
	Description            string    `json:"description"`
	Tasks                  []Task    `json:"tasks"`
	CrossPhaseVerification []string  `json:"cross_phase_verification,omitempty"`
	ShipBlockers           []string  `json:"ship_blockers,omitempty"`
	Approval               *Approval `json:"approval,omitempty"`
	CreatedAt              time.Time `json:"-"`
}

// Task is one unit of work in a plan.
type Task struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Dependencies []string `json:"dependencies,omitempty"`
	Files        []string `json:"files,omitempty"`
	Type         string   `json:"type,omitempty"`
	Verification []string `json:"verification,omitempty"` // per-task verification checklist from planner
	Status       Status   `json:"status,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	PlanOnly     bool     `json:"plan_only,omitempty"` // when true, workflow runs plan phase only (no execute/verify/merge)

}

// Status represents the lifecycle state of a task (pending, active, verifying, done, failed, or blocked).
type Status int

const (
	StatusPending Status = iota
	StatusActive
	StatusVerifying
	StatusDone
	StatusFailed
	StatusBlocked
)

// Load reads a plan from the project root.
// Currently supports JSON only (stoke-plan.json).
// For YAML support, add gopkg.in/yaml.v3 as a dependency.
func Load(projectRoot string) (*Plan, error) {
	path := filepath.Join(projectRoot, "stoke-plan.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no stoke-plan.json found in %s (YAML not yet supported -- use JSON)", projectRoot)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse stoke-plan.json: %w", err)
	}
	p.CreatedAt = time.Now()
	return &p, nil
}

// LoadFile reads a plan from a specific path.
func LoadFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	p.CreatedAt = time.Now()
	return &p, nil
}

// Save writes a plan to disk as JSON.
func Save(projectRoot string, p *Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectRoot, "stoke-plan.json"), data, 0644) // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
}

// Validate checks a plan for structural problems.
func (p *Plan) Validate() []string {
	var errs []string

	if p.ID == "" {
		errs = append(errs, "plan has no ID")
	}
	if len(p.Tasks) == 0 {
		errs = append(errs, "plan has no tasks")
		return errs
	}

	// Check for duplicate IDs
	ids := map[string]bool{}
	for _, t := range p.Tasks {
		if t.ID == "" {
			errs = append(errs, "task has empty ID")
		}
		if ids[t.ID] {
			errs = append(errs, fmt.Sprintf("duplicate task ID: %s", t.ID))
		}
		ids[t.ID] = true
		if t.Description == "" {
			errs = append(errs, fmt.Sprintf("task %s has empty description", t.ID))
		}
	}

	// Check for missing dependencies
	for _, t := range p.Tasks {
		for _, dep := range t.Dependencies {
			if !ids[dep] {
				errs = append(errs, fmt.Sprintf("task %s depends on unknown task %s", t.ID, dep))
			}
		}
	}

	// Check for dependency cycles (DFS)
	if cycle := detectCycle(p.Tasks); cycle != "" {
		errs = append(errs, "dependency cycle: "+cycle)
	}

	return errs
}

// CleanPlanTaskDependenciesDrop records a single dep-drop the
// cleaner performed.
type CleanPlanTaskDependenciesDrop struct {
	TaskID  string
	Dropped string
}

// CleanPlanTaskDependencies removes task.Dependencies entries that
// reference task IDs not present in the plan. Returns one drop
// record per removed reference for audit-log rendering. Idempotent.
// Companion to CleanTaskDependencies for SOW shape — runs against a
// flat plan.Plan which the native fast path uses.
func CleanPlanTaskDependencies(p *Plan) []CleanPlanTaskDependenciesDrop {
	if p == nil {
		return nil
	}
	ids := map[string]bool{}
	for _, t := range p.Tasks {
		ids[t.ID] = true
	}
	var drops []CleanPlanTaskDependenciesDrop
	for i := range p.Tasks {
		kept := p.Tasks[i].Dependencies[:0]
		for _, dep := range p.Tasks[i].Dependencies {
			if ids[dep] {
				kept = append(kept, dep)
				continue
			}
			drops = append(drops, CleanPlanTaskDependenciesDrop{TaskID: p.Tasks[i].ID, Dropped: dep})
		}
		p.Tasks[i].Dependencies = kept
	}
	return drops
}

// ValidateFiles checks that files listed in tasks actually exist in the repo.
// Returns warnings (new files in non-existent dirs) and errors (modifications
// to non-existent files). This catches misplanned tasks before execution.
func (p *Plan) ValidateFiles(repoRoot string) (warnings, errors []string) {
	for _, t := range p.Tasks {
		for _, f := range t.Files {
			path := filepath.Join(repoRoot, f)
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					// Check if parent dir exists (new file in existing dir is fine)
					dir := filepath.Dir(path)
					if _, dirErr := os.Stat(dir); os.IsNotExist(dirErr) {
						warnings = append(warnings,
							fmt.Sprintf("task %s: file %q parent directory does not exist", t.ID, f))
					}
					// Non-existent file could be a new file — just warn
					warnings = append(warnings,
						fmt.Sprintf("task %s: file %q does not exist (will be created?)", t.ID, f))
				} else {
					errors = append(errors,
						fmt.Sprintf("task %s: cannot stat file %q: %v", t.ID, f, err))
				}
				continue
			}
			if info.IsDir() {
				warnings = append(warnings,
					fmt.Sprintf("task %s: %q is a directory, not a file", t.ID, f))
			}
		}
	}
	return warnings, errors
}

// AutoInferDependencies examines each task's declared files, builds a
// file→task index, and adds an implicit dependency from task A to task B
// when A reads a file that B writes. Existing explicit dependencies are
// preserved; only new implicit ones are added.
func (p *Plan) AutoInferDependencies() int {
	// Build file→writer index: which task "owns" (writes to) each file
	writers := map[string]string{} // file path → task ID
	for _, t := range p.Tasks {
		for _, f := range t.Files {
			writers[f] = t.ID
		}
	}

	added := 0
	for i := range p.Tasks {
		existing := map[string]bool{}
		for _, dep := range p.Tasks[i].Dependencies {
			existing[dep] = true
		}

		for _, f := range p.Tasks[i].Files {
			// If another task also declares this file, add a dependency
			// from the later task to the earlier one (by position)
			for j := range p.Tasks {
				if i == j {
					continue
				}
				for _, otherFile := range p.Tasks[j].Files {
					if otherFile == f && !existing[p.Tasks[j].ID] && j < i {
						p.Tasks[i].Dependencies = append(p.Tasks[i].Dependencies, p.Tasks[j].ID)
						existing[p.Tasks[j].ID] = true
						added++
					}
				}
			}
		}
	}

	return added
}

func detectCycle(tasks []Task) string {
	adj := map[string][]string{}
	for _, t := range tasks {
		// Check for self-loops
		for _, dep := range t.Dependencies {
			if dep == t.ID {
				return t.ID + " -> " + t.ID + " (self-loop)"
			}
		}
		adj[t.ID] = t.Dependencies
	}

	white, gray := map[string]bool{}, map[string]bool{}
	for _, t := range tasks {
		white[t.ID] = true
	}

	var dfs func(string) string
	dfs = func(id string) string {
		delete(white, id)
		gray[id] = true
		for _, dep := range adj[id] {
			if gray[dep] {
				return id + " -> " + dep
			}
			if white[dep] {
				if cycle := dfs(dep); cycle != "" {
					return cycle
				}
			}
		}
		delete(gray, id)
		return ""
	}

	for id := range white {
		if cycle := dfs(id); cycle != "" {
			return cycle
		}
	}
	return ""
}
