package nodes

import (
	"fmt"
	"time"
)

// Task represents a node in the task DAG.
// ID prefix: task- (with granularity tag, e.g. task-mission-, task-feat-, task-tic-)
type Task struct {
	Granularity        string   `json:"granularity"`         // mission, feature, milestone, branch, ticket, sub_ticket
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	State              string   `json:"state"`               // proposed, assigned, in_progress, in_review, done, superseded, cancelled
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	CreatedAt          time.Time `json:"created_at"`
	CreatedBy          string   `json:"created_by"`

	// Optional fields.
	AssignedToStanceRole string    `json:"assigned_to_stance_role,omitempty"`
	AssignedAt           time.Time `json:"assigned_at,omitempty"`
	ParentTaskRef        string    `json:"parent_task_ref,omitempty"`
	Dependencies         []string  `json:"dependencies,omitempty"`
	ClosedAt             time.Time `json:"closed_at,omitempty"`
	ClosedBy             string    `json:"closed_by,omitempty"`

	Version int `json:"schema_version"`
}

var validTaskGranularities = map[string]bool{
	"mission": true, "feature": true, "milestone": true,
	"branch": true, "ticket": true, "sub_ticket": true,
}

var validTaskStates = map[string]bool{
	"proposed": true, "assigned": true, "in_progress": true,
	"in_review": true, "done": true, "superseded": true, "cancelled": true,
}

func (t *Task) NodeType() string   { return "task" }
func (t *Task) SchemaVersion() int { return t.Version }

func (t *Task) Validate() error {
	if t.Granularity == "" {
		return fmt.Errorf("task: granularity is required")
	}
	if !validTaskGranularities[t.Granularity] {
		return fmt.Errorf("task: invalid granularity %q", t.Granularity)
	}
	if t.Title == "" {
		return fmt.Errorf("task: title is required")
	}
	if t.Description == "" {
		return fmt.Errorf("task: description is required")
	}
	if t.State == "" {
		return fmt.Errorf("task: state is required")
	}
	if !validTaskStates[t.State] {
		return fmt.Errorf("task: invalid state %q", t.State)
	}
	if len(t.AcceptanceCriteria) == 0 {
		return fmt.Errorf("task: acceptance_criteria is required")
	}
	if t.CreatedAt.IsZero() {
		return fmt.Errorf("task: created_at is required")
	}
	if t.CreatedBy == "" {
		return fmt.Errorf("task: created_by is required")
	}
	return nil
}

func init() {
	Register("task", func() NodeTyper { return &Task{Version: 1} })
}
