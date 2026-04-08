package nodes

import (
	"fmt"
	"time"
)

// Loop represents the state machine for any decision in Stoke.
// ID prefix: loop-
type Loop struct {
	State                string   `json:"state"`                  // proposing, drafted, convening, reviewing, resolving_dissents, converged, escalated
	LoopType             string   `json:"loop_type"`              // prd, sow, ticket, pr_review, refactor_proposal, fix_cycle, escalation, research
	ArtifactRef          string   `json:"artifact_ref"`           // ID of the node currently being reviewed
	ConvenedPartners     []string `json:"convened_partners"`      // stance roles for consensus
	IterationCount       int      `json:"iteration_count"`        // incremented on each new draft
	ProposingStanceRole  string   `json:"proposing_stance_role"`  // which stance role produces drafts
	TaskDAGScope         string   `json:"task_dag_scope"`         // task DAG node ID
	CreatedAt            time.Time `json:"created_at"`
	CreatedBy            string   `json:"created_by"`             // supervisor instance

	// Optional fields.
	ParentLoopRef        string   `json:"parent_loop_ref,omitempty"`         // parent loop ID
	JudgeInvocationCount int      `json:"judge_invocation_count,omitempty"`
	TerminalReason       string   `json:"terminal_reason,omitempty"`

	Version int `json:"schema_version"`
}

var validLoopStates = map[string]bool{
	"proposing": true, "drafted": true, "convening": true,
	"reviewing": true, "resolving_dissents": true, "converged": true, "escalated": true,
}

var validLoopTypes = map[string]bool{
	"prd": true, "sow": true, "ticket": true, "pr_review": true,
	"refactor_proposal": true, "fix_cycle": true, "escalation": true, "research": true,
}

func (l *Loop) NodeType() string     { return "loop" }
func (l *Loop) SchemaVersion() int   { return l.Version }

func (l *Loop) Validate() error {
	if l.State == "" {
		return fmt.Errorf("loop: state is required")
	}
	if !validLoopStates[l.State] {
		return fmt.Errorf("loop: invalid state %q", l.State)
	}
	if l.LoopType == "" {
		return fmt.Errorf("loop: loop_type is required")
	}
	if !validLoopTypes[l.LoopType] {
		return fmt.Errorf("loop: invalid loop_type %q", l.LoopType)
	}
	if l.ArtifactRef == "" {
		return fmt.Errorf("loop: artifact_ref is required")
	}
	if l.LoopType != "research" && len(l.ConvenedPartners) == 0 {
		return fmt.Errorf("loop: convened_partners cannot be empty unless loop_type is research")
	}
	if l.ProposingStanceRole == "" {
		return fmt.Errorf("loop: proposing_stance_role is required")
	}
	if l.TaskDAGScope == "" {
		return fmt.Errorf("loop: task_dag_scope is required")
	}
	if l.CreatedBy == "" {
		return fmt.Errorf("loop: created_by is required")
	}
	if l.CreatedAt.IsZero() {
		return fmt.Errorf("loop: created_at is required")
	}
	return nil
}

func init() {
	Register("loop", func() NodeTyper { return &Loop{Version: 1} })
}
