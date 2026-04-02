package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BuildReport is the structured output of a complete stoke build run.
type BuildReport struct {
	Version     string        `json:"version"`
	PlanID      string        `json:"plan_id"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	DurationSec float64       `json:"duration_sec"`
	TotalCost   float64       `json:"total_cost_usd"`
	TasksDone   int           `json:"tasks_done"`
	TasksFailed int           `json:"tasks_failed"`
	TasksTotal  int           `json:"tasks_total"`
	Success     bool          `json:"success"`
	Tasks       []TaskReport  `json:"tasks"`
}

// TaskReport is the structured output of one task execution.
type TaskReport struct {
	ID           string         `json:"id"`
	Description  string         `json:"description"`
	Type         string         `json:"type"`
	Status       string         `json:"status"` // done, failed, skipped
	Attempts     int            `json:"attempts"`
	CostUSD      float64        `json:"cost_usd"`
	DurationSec  float64        `json:"duration_sec"`
	Error        string         `json:"error,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
	Failure      *FailureReport `json:"failure,omitempty"`
	Review       *ReviewReport  `json:"review,omitempty"`
	FilesChanged []string       `json:"files_changed,omitempty"`
}

// FailureReport captures why a task failed.
type FailureReport struct {
	Class     string   `json:"class"`
	Summary   string   `json:"summary"`
	RootCause string   `json:"root_cause,omitempty"`
	Details   []Detail `json:"details,omitempty"`
}

// Detail is one specific issue.
type Detail struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

// ReviewReport captures cross-model review results.
type ReviewReport struct {
	Engine   string `json:"engine"` // claude or codex
	Approved bool   `json:"approved"`
	Summary  string `json:"summary,omitempty"`
}

// Save writes the report as JSON to the .stoke directory.
func (r *BuildReport) Save(projectRoot string) error {
	dir := filepath.Join(projectRoot, ".stoke", "reports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create reports dir: %w", err)
	}
	filename := fmt.Sprintf("build-%s-%s.json", r.PlanID, r.StartedAt.Format("20060102-150405"))
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil { return err }
	path := filepath.Join(dir, filename)
	return os.WriteFile(path, data, 0644)
}

// SaveLatest writes the report as latest.json for easy CI access.
func (r *BuildReport) SaveLatest(projectRoot string) error {
	dir := filepath.Join(projectRoot, ".stoke", "reports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create reports dir: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil { return err }
	return os.WriteFile(filepath.Join(dir, "latest.json"), data, 0644)
}

// LoadLatest reads the most recent report.
func LoadLatest(projectRoot string) (*BuildReport, error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, ".stoke", "reports", "latest.json"))
	if err != nil { return nil, err }
	var r BuildReport
	if err := json.Unmarshal(data, &r); err != nil { return nil, err }
	return &r, nil
}
