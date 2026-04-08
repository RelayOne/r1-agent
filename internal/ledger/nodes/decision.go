package nodes

import (
	"fmt"
	"strings"
	"time"
)

// minAckLength is the minimum character length for a substantive acknowledgment.
const minAckLength = 50

// placeholderPatterns are trivially short entries that indicate box-ticking.
var placeholderPatterns = []string{
	"ack", "noted", "considered", "n/a", "tbd", "todo",
	"acknowledged", "reviewed", "ok", "yes", "no",
}

// validateAcknowledgment checks that an acknowledgment entry is substantive:
// not empty, not trivially short, and not an obvious placeholder.
func validateAcknowledgment(idx int, ack string) error {
	trimmed := strings.TrimSpace(ack)
	if len(trimmed) < minAckLength {
		return fmt.Errorf("acknowledgment %d is too short (got %d chars, need at least %d): %q",
			idx, len(trimmed), minAckLength, trimmed)
	}
	lower := strings.ToLower(trimmed)
	for _, p := range placeholderPatterns {
		if lower == p {
			return fmt.Errorf("acknowledgment %d is a placeholder (%q) — write a substantive explanation", idx, trimmed)
		}
	}
	return nil
}

// DecisionParticipant records a stance that participated in a decision.
type DecisionParticipant struct {
	StanceRole string `json:"stance_role"`
	SessionID  string `json:"session_id"`
}

// DecisionInternal is the team's record of how it reached agreement during a task.
// ID prefix: dec-i-
type DecisionInternal struct {
	Who                          []DecisionParticipant `json:"who"`
	What                         string                `json:"what"`
	When                         time.Time             `json:"when"`
	WhenCommitSHA                string                `json:"when_commit_sha"`
	Why                          string                `json:"why"`
	WithWhatContext              string                `json:"with_what_context"`
	AffectsPreviousDecisions     []string              `json:"affects_previous_decisions"`
	PreviousContextsAcknowledged []string              `json:"previous_contexts_acknowledged"`
	TaskDAGScope                 string                `json:"task_dag_scope"`
	LoopRef                      string                `json:"loop_ref"`

	// Optional fields.
	IsSummary bool `json:"is_summary,omitempty"`

	Version int `json:"schema_version"`
}

func (d *DecisionInternal) NodeType() string   { return "decision_internal" }
func (d *DecisionInternal) SchemaVersion() int { return d.Version }

func (d *DecisionInternal) Validate() error {
	if len(d.Who) == 0 {
		return fmt.Errorf("decision_internal: who is required")
	}
	if d.What == "" {
		return fmt.Errorf("decision_internal: what is required")
	}
	if d.When.IsZero() {
		return fmt.Errorf("decision_internal: when is required")
	}
	if d.Why == "" {
		return fmt.Errorf("decision_internal: why is required")
	}
	if d.WithWhatContext == "" {
		return fmt.Errorf("decision_internal: with_what_context is required")
	}
	if d.TaskDAGScope == "" {
		return fmt.Errorf("decision_internal: task_dag_scope is required")
	}
	if d.LoopRef == "" {
		return fmt.Errorf("decision_internal: loop_ref is required")
	}
	if len(d.AffectsPreviousDecisions) != len(d.PreviousContextsAcknowledged) {
		return fmt.Errorf("decision_internal: affects_previous_decisions and previous_contexts_acknowledged must have matching lengths")
	}
	for i, ack := range d.PreviousContextsAcknowledged {
		if err := validateAcknowledgment(i, ack); err != nil {
			return fmt.Errorf("decision_internal: %w", err)
		}
	}
	return nil
}

func init() {
	Register("decision_internal", func() NodeTyper { return &DecisionInternal{Version: 1} })
}

// DecisionRepo is the codebase's record of why it looks the way it does.
// ID prefix: dec-r-
type DecisionRepo struct {
	Who                          []DecisionParticipant `json:"who"`
	What                         string                `json:"what"`
	When                         time.Time             `json:"when"`
	WhenCommitSHA                string                `json:"when_commit_sha"`
	Why                          string                `json:"why"`
	WithWhatContext              string                `json:"with_what_context"`
	AffectsPreviousDecisions     []string              `json:"affects_previous_decisions"`
	PreviousContextsAcknowledged []string              `json:"previous_contexts_acknowledged"`
	TaskDAGScope                 string                `json:"task_dag_scope"`
	LoopRef                      string                `json:"loop_ref"`
	Provenance                   string                `json:"provenance"`    // stoke_authored, inherited_human, inherited_stoke
	DistilledFrom                []string              `json:"distilled_from"` // source internal decision IDs

	// Optional fields.
	IsSummary bool `json:"is_summary,omitempty"`

	Version int `json:"schema_version"`
}

var validProvenances = map[string]bool{
	"stoke_authored": true, "inherited_human": true, "inherited_stoke": true,
}

func (d *DecisionRepo) NodeType() string   { return "decision_repo" }
func (d *DecisionRepo) SchemaVersion() int { return d.Version }

func (d *DecisionRepo) Validate() error {
	if d.Provenance == "" {
		return fmt.Errorf("decision_repo: provenance is required")
	}
	if !validProvenances[d.Provenance] {
		return fmt.Errorf("decision_repo: invalid provenance %q", d.Provenance)
	}
	// Inherited entries tolerate partial schemas.
	if d.Provenance == "stoke_authored" {
		if len(d.Who) == 0 {
			return fmt.Errorf("decision_repo: who is required for stoke_authored")
		}
		if d.What == "" {
			return fmt.Errorf("decision_repo: what is required for stoke_authored")
		}
		if d.When.IsZero() {
			return fmt.Errorf("decision_repo: when is required for stoke_authored")
		}
		if d.Why == "" {
			return fmt.Errorf("decision_repo: why is required for stoke_authored")
		}
		if d.WithWhatContext == "" {
			return fmt.Errorf("decision_repo: with_what_context is required for stoke_authored")
		}
		if d.TaskDAGScope == "" {
			return fmt.Errorf("decision_repo: task_dag_scope is required for stoke_authored")
		}
		if d.LoopRef == "" {
			return fmt.Errorf("decision_repo: loop_ref is required for stoke_authored")
		}
		if len(d.AffectsPreviousDecisions) != len(d.PreviousContextsAcknowledged) {
			return fmt.Errorf("decision_repo: affects_previous_decisions and previous_contexts_acknowledged must have matching lengths")
		}
		for i, ack := range d.PreviousContextsAcknowledged {
			if err := validateAcknowledgment(i, ack); err != nil {
				return fmt.Errorf("decision_repo: %w", err)
			}
		}
	}
	return nil
}

func init() {
	Register("decision_repo", func() NodeTyper { return &DecisionRepo{Version: 1} })
}
