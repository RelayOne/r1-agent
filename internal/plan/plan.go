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
}

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
	return os.WriteFile(filepath.Join(projectRoot, "stoke-plan.json"), data, 0644)
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
