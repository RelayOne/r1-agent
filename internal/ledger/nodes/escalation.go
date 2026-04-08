package nodes

import (
	"fmt"
	"time"
)

// Escalation is a request to forward something upward in the supervisor hierarchy or to the user.
// ID prefix: esc-
type Escalation struct {
	EscalationType      string    `json:"escalation_type"`       // infeasible, blocked, deadlock, drift, budget, user_required, partner_exhaustion, cto_veto_disputed
	OriginatingLoopRef  string    `json:"originating_loop_ref"`
	Target              string    `json:"target"`                // parent_supervisor, mission_supervisor, user_via_po
	Context             string    `json:"context"`
	RequestedResolution string    `json:"requested_resolution"`
	CreatedAt           time.Time `json:"created_at"`
	CreatedBy           string    `json:"created_by"`

	// Optional fields.
	ResolutionStatus   string `json:"resolution_status,omitempty"`    // pending, resolved, withdrawn, superseded
	ResolutionNodeRef  string `json:"resolution_node_ref,omitempty"`

	Version int `json:"schema_version"`
}

var validEscalationTypes = map[string]bool{
	"infeasible": true, "blocked": true, "deadlock": true, "drift": true,
	"budget": true, "user_required": true, "partner_exhaustion": true, "cto_veto_disputed": true,
}

var validEscalationTargets = map[string]bool{
	"parent_supervisor": true, "mission_supervisor": true, "user_via_po": true,
}

func (e *Escalation) NodeType() string     { return "escalation" }
func (e *Escalation) SchemaVersion() int   { return e.Version }

func (e *Escalation) Validate() error {
	if e.EscalationType == "" {
		return fmt.Errorf("escalation: escalation_type is required")
	}
	if !validEscalationTypes[e.EscalationType] {
		return fmt.Errorf("escalation: invalid escalation_type %q", e.EscalationType)
	}
	if e.OriginatingLoopRef == "" {
		return fmt.Errorf("escalation: originating_loop_ref is required")
	}
	if e.Target == "" {
		return fmt.Errorf("escalation: target is required")
	}
	if !validEscalationTargets[e.Target] {
		return fmt.Errorf("escalation: invalid target %q", e.Target)
	}
	if e.Context == "" {
		return fmt.Errorf("escalation: context is required")
	}
	if e.RequestedResolution == "" {
		return fmt.Errorf("escalation: requested_resolution is required")
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("escalation: created_at is required")
	}
	if e.CreatedBy == "" {
		return fmt.Errorf("escalation: created_by is required")
	}
	return nil
}

func init() {
	Register("escalation", func() NodeTyper { return &Escalation{Version: 1} })
}

// JudgeVerdict is the Judge's output from an invocation.
// ID prefix: jv-
type JudgeVerdict struct {
	InvokingRule                string    `json:"invoking_rule"`
	LoopRef                     string    `json:"loop_ref"`
	Verdict                     string    `json:"verdict"` // keep_iterating, switch_approaches, return_to_prd, escalate_to_user
	Reasoning                   string    `json:"reasoning"`
	LoopHistoryConsulted        []string  `json:"loop_history_consulted"`
	OriginalIntentAtInvocation  string    `json:"original_intent_at_invocation"`
	CreatedAt                   time.Time `json:"created_at"`
	CreatedBy                   string    `json:"created_by"`

	// Optional fields.
	ResearchRefs []string `json:"research_refs,omitempty"`
	Caveats      string   `json:"caveats,omitempty"`

	Version int `json:"schema_version"`
}

var validVerdicts = map[string]bool{
	"keep_iterating": true, "switch_approaches": true,
	"return_to_prd": true, "escalate_to_user": true,
}

func (j *JudgeVerdict) NodeType() string     { return "judge_verdict" }
func (j *JudgeVerdict) SchemaVersion() int   { return j.Version }

func (j *JudgeVerdict) Validate() error {
	if j.InvokingRule == "" {
		return fmt.Errorf("judge_verdict: invoking_rule is required")
	}
	if j.LoopRef == "" {
		return fmt.Errorf("judge_verdict: loop_ref is required")
	}
	if j.Verdict == "" {
		return fmt.Errorf("judge_verdict: verdict is required")
	}
	if !validVerdicts[j.Verdict] {
		return fmt.Errorf("judge_verdict: invalid verdict %q", j.Verdict)
	}
	if j.Reasoning == "" {
		return fmt.Errorf("judge_verdict: reasoning is required")
	}
	if len(j.LoopHistoryConsulted) == 0 {
		return fmt.Errorf("judge_verdict: loop_history_consulted is required")
	}
	if j.OriginalIntentAtInvocation == "" {
		return fmt.Errorf("judge_verdict: original_intent_at_invocation is required")
	}
	if j.CreatedAt.IsZero() {
		return fmt.Errorf("judge_verdict: created_at is required")
	}
	if j.CreatedBy == "" {
		return fmt.Errorf("judge_verdict: created_by is required")
	}
	return nil
}

func init() {
	Register("judge_verdict", func() NodeTyper { return &JudgeVerdict{Version: 1} })
}

// StakeholderDirective is the Stakeholder's resolution of an escalation in full-auto mode.
// ID prefix: sd-
type StakeholderDirective struct {
	EscalationRef                       string    `json:"escalation_ref"`
	StakeholderStanceID                 string    `json:"stakeholder_stance_id"`
	PostureApplied                      string    `json:"posture_applied"` // absolute_completion_and_quality, balanced, pragmatic
	DirectiveType                       string    `json:"directive_type"`  // proceed_as_proposed, switch_to_alternative_approach, add_constraint_and_retry, return_to_prd_for_rescope, abort_mission_as_infeasible, dispatch_research_before_deciding, forward_to_user
	DirectiveContent                    string    `json:"directive_content"`
	Reasoning                           string    `json:"reasoning"`
	EvaluationSummary                   string    `json:"evaluation_summary"`
	PriorStakeholderDirectivesConsidered []string  `json:"prior_stakeholder_directives_considered"`
	OriginalIntentAtEvaluation          string    `json:"original_intent_at_evaluation"`
	CreatedAt                           time.Time `json:"created_at"`
	CreatedBy                           string    `json:"created_by"`

	// Optional fields.
	ResearchRefs                string `json:"research_refs,omitempty"`
	SecondStakeholderRef        string `json:"second_stakeholder_ref,omitempty"`
	SecondStakeholderDissentRef string `json:"second_stakeholder_dissent_ref,omitempty"`
	Caveats                     string `json:"caveats,omitempty"`

	Version int `json:"schema_version"`
}

var validPostures = map[string]bool{
	"absolute_completion_and_quality": true, "balanced": true, "pragmatic": true,
}

var validDirectiveTypes = map[string]bool{
	"proceed_as_proposed": true, "switch_to_alternative_approach": true,
	"add_constraint_and_retry": true, "return_to_prd_for_rescope": true,
	"abort_mission_as_infeasible": true, "dispatch_research_before_deciding": true,
	"forward_to_user": true,
}

func (s *StakeholderDirective) NodeType() string     { return "stakeholder_directive" }
func (s *StakeholderDirective) SchemaVersion() int   { return s.Version }

func (s *StakeholderDirective) Validate() error {
	if s.EscalationRef == "" {
		return fmt.Errorf("stakeholder_directive: escalation_ref is required")
	}
	if s.StakeholderStanceID == "" {
		return fmt.Errorf("stakeholder_directive: stakeholder_stance_id is required")
	}
	if s.PostureApplied == "" {
		return fmt.Errorf("stakeholder_directive: posture_applied is required")
	}
	if !validPostures[s.PostureApplied] {
		return fmt.Errorf("stakeholder_directive: invalid posture_applied %q", s.PostureApplied)
	}
	if s.DirectiveType == "" {
		return fmt.Errorf("stakeholder_directive: directive_type is required")
	}
	if !validDirectiveTypes[s.DirectiveType] {
		return fmt.Errorf("stakeholder_directive: invalid directive_type %q", s.DirectiveType)
	}
	if s.DirectiveContent == "" {
		return fmt.Errorf("stakeholder_directive: directive_content is required")
	}
	if s.Reasoning == "" {
		return fmt.Errorf("stakeholder_directive: reasoning is required")
	}
	if s.EvaluationSummary == "" {
		return fmt.Errorf("stakeholder_directive: evaluation_summary is required")
	}
	if s.OriginalIntentAtEvaluation == "" {
		return fmt.Errorf("stakeholder_directive: original_intent_at_evaluation is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("stakeholder_directive: created_at is required")
	}
	if s.CreatedBy == "" {
		return fmt.Errorf("stakeholder_directive: created_by is required")
	}
	// Directionality: if second stakeholder dissented, directive must forward to user.
	if s.SecondStakeholderDissentRef != "" && s.DirectiveType != "forward_to_user" {
		return fmt.Errorf("stakeholder_directive: directive_type must be forward_to_user when second_stakeholder_dissent_ref is set")
	}
	return nil
}

func init() {
	Register("stakeholder_directive", func() NodeTyper { return &StakeholderDirective{Version: 1} })
}
