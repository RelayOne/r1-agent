package nodes

import (
	"fmt"
	"time"
)

// SupervisorStateCheckpoint is the mission supervisor's serialized state at a point in time.
// ID prefix: sv-ckpt-
type SupervisorStateCheckpoint struct {
	SupervisorInstanceID string    `json:"supervisor_instance_id"`
	SupervisorConfig     string    `json:"supervisor_config"` // mission, branch, sdm
	BusCursor            int64     `json:"bus_cursor"`
	ActiveLoops          []string  `json:"active_loops"`
	PausedWorkers        []string  `json:"paused_workers"`
	PendingDelayedEvents []string  `json:"pending_delayed_events"`
	CreatedAt            time.Time `json:"created_at"`

	// Optional fields.
	ParentSupervisorRef string `json:"parent_supervisor_ref,omitempty"`

	Version int `json:"schema_version"`
}

var validSupervisorConfigs = map[string]bool{
	"mission": true, "branch": true, "sdm": true,
}

func (s *SupervisorStateCheckpoint) NodeType() string     { return "supervisor_state_checkpoint" }
func (s *SupervisorStateCheckpoint) SchemaVersion() int   { return s.Version }

func (s *SupervisorStateCheckpoint) Validate() error {
	if s.SupervisorInstanceID == "" {
		return fmt.Errorf("supervisor_state_checkpoint: supervisor_instance_id is required")
	}
	if s.SupervisorConfig == "" {
		return fmt.Errorf("supervisor_state_checkpoint: supervisor_config is required")
	}
	if !validSupervisorConfigs[s.SupervisorConfig] {
		return fmt.Errorf("supervisor_state_checkpoint: invalid supervisor_config %q", s.SupervisorConfig)
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("supervisor_state_checkpoint: created_at is required")
	}
	return nil
}

func init() {
	Register("supervisor_state_checkpoint", func() NodeTyper { return &SupervisorStateCheckpoint{Version: 1} })
}

// BranchCompletionProposal is a branch supervisor's proposal that its branch is done.
// ID prefix: bcp-
type BranchCompletionProposal struct {
	BranchSupervisorID string    `json:"branch_supervisor_id"`
	BranchTaskRef      string    `json:"branch_task_ref"`
	MissionSupervisorID string   `json:"mission_supervisor_id"`
	SummaryOfWork      string    `json:"summary_of_work"`
	UnresolvedConcerns string    `json:"unresolved_concerns"`
	CreatedAt          time.Time `json:"created_at"`

	// Optional fields.
	CrossBranchAdvisoriesConsulted []string `json:"cross_branch_advisories_consulted,omitempty"`

	Version int `json:"schema_version"`
}

func (b *BranchCompletionProposal) NodeType() string     { return "branch_completion_proposal" }
func (b *BranchCompletionProposal) SchemaVersion() int   { return b.Version }

func (b *BranchCompletionProposal) Validate() error {
	if b.BranchSupervisorID == "" {
		return fmt.Errorf("branch_completion_proposal: branch_supervisor_id is required")
	}
	if b.BranchTaskRef == "" {
		return fmt.Errorf("branch_completion_proposal: branch_task_ref is required")
	}
	if b.MissionSupervisorID == "" {
		return fmt.Errorf("branch_completion_proposal: mission_supervisor_id is required")
	}
	if b.SummaryOfWork == "" {
		return fmt.Errorf("branch_completion_proposal: summary_of_work is required")
	}
	if b.UnresolvedConcerns == "" {
		return fmt.Errorf("branch_completion_proposal: unresolved_concerns is required")
	}
	if b.CreatedAt.IsZero() {
		return fmt.Errorf("branch_completion_proposal: created_at is required")
	}
	return nil
}

func init() {
	Register("branch_completion_proposal", func() NodeTyper { return &BranchCompletionProposal{Version: 1} })
}

// BranchCompletionAgreement is the mission supervisor's agreement on a branch completion proposal.
// ID prefix: bca-
type BranchCompletionAgreement struct {
	ProposalRef          string    `json:"proposal_ref"`
	MissionSupervisorID  string    `json:"mission_supervisor_id"`
	AgreementReasoning   string    `json:"agreement_reasoning"`
	CreatedAt            time.Time `json:"created_at"`

	Version int `json:"schema_version"`
}

func (b *BranchCompletionAgreement) NodeType() string     { return "branch_completion_agreement" }
func (b *BranchCompletionAgreement) SchemaVersion() int   { return b.Version }

func (b *BranchCompletionAgreement) Validate() error {
	if b.ProposalRef == "" {
		return fmt.Errorf("branch_completion_agreement: proposal_ref is required")
	}
	if b.MissionSupervisorID == "" {
		return fmt.Errorf("branch_completion_agreement: mission_supervisor_id is required")
	}
	if b.AgreementReasoning == "" {
		return fmt.Errorf("branch_completion_agreement: agreement_reasoning is required")
	}
	if b.CreatedAt.IsZero() {
		return fmt.Errorf("branch_completion_agreement: created_at is required")
	}
	return nil
}

func init() {
	Register("branch_completion_agreement", func() NodeTyper { return &BranchCompletionAgreement{Version: 1} })
}

// BranchCompletionDissent is the mission supervisor's disagreement with a branch completion proposal.
// ID prefix: bcd-
type BranchCompletionDissent struct {
	ProposalRef          string    `json:"proposal_ref"`
	MissionSupervisorID  string    `json:"mission_supervisor_id"`
	DissentReasoning     string    `json:"dissent_reasoning"`
	RequestedActions     []string  `json:"requested_actions"`
	CreatedAt            time.Time `json:"created_at"`

	Version int `json:"schema_version"`
}

func (b *BranchCompletionDissent) NodeType() string     { return "branch_completion_dissent" }
func (b *BranchCompletionDissent) SchemaVersion() int   { return b.Version }

func (b *BranchCompletionDissent) Validate() error {
	if b.ProposalRef == "" {
		return fmt.Errorf("branch_completion_dissent: proposal_ref is required")
	}
	if b.MissionSupervisorID == "" {
		return fmt.Errorf("branch_completion_dissent: mission_supervisor_id is required")
	}
	if b.DissentReasoning == "" {
		return fmt.Errorf("branch_completion_dissent: dissent_reasoning is required")
	}
	if len(b.RequestedActions) == 0 {
		return fmt.Errorf("branch_completion_dissent: requested_actions is required")
	}
	if b.CreatedAt.IsZero() {
		return fmt.Errorf("branch_completion_dissent: created_at is required")
	}
	return nil
}

func init() {
	Register("branch_completion_dissent", func() NodeTyper { return &BranchCompletionDissent{Version: 1} })
}

// SDMAdvisory is a structured warning emitted by the SDM supervisor.
// ID prefix: sdm-adv-
type SDMAdvisory struct {
	AdvisoryType          string    `json:"advisory_type"` // collision_file_modification, dependency_crossed, duplicate_work_detected, schedule_risk_critical_path, cross_branch_drift
	DetectedCondition     string    `json:"detected_condition"`
	BranchesInvolved      []string  `json:"branches_involved"`
	AffectedWorkers       []string  `json:"affected_workers"`
	SuggestedCoordination string    `json:"suggested_coordination"`
	OriginatingEventRef   string    `json:"originating_event_ref"`
	CreatedAt             time.Time `json:"created_at"`

	// Optional fields.
	Severity      string `json:"severity,omitempty"`        // informational, coordinate_soon, urgent_block
	ResolvedByRef string `json:"resolved_by_ref,omitempty"`

	Version int `json:"schema_version"`
}

var validAdvisoryTypes = map[string]bool{
	"collision_file_modification": true, "dependency_crossed": true,
	"duplicate_work_detected": true, "schedule_risk_critical_path": true,
	"cross_branch_drift": true,
}

var validAdvisorySeverities = map[string]bool{
	"informational": true, "coordinate_soon": true, "urgent_block": true,
}

func (s *SDMAdvisory) NodeType() string     { return "sdm_advisory" }
func (s *SDMAdvisory) SchemaVersion() int   { return s.Version }

func (s *SDMAdvisory) Validate() error {
	if s.AdvisoryType == "" {
		return fmt.Errorf("sdm_advisory: advisory_type is required")
	}
	if !validAdvisoryTypes[s.AdvisoryType] {
		return fmt.Errorf("sdm_advisory: invalid advisory_type %q", s.AdvisoryType)
	}
	if s.DetectedCondition == "" {
		return fmt.Errorf("sdm_advisory: detected_condition is required")
	}
	if len(s.BranchesInvolved) == 0 {
		return fmt.Errorf("sdm_advisory: branches_involved is required")
	}
	if s.SuggestedCoordination == "" {
		return fmt.Errorf("sdm_advisory: suggested_coordination is required")
	}
	if s.OriginatingEventRef == "" {
		return fmt.Errorf("sdm_advisory: originating_event_ref is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("sdm_advisory: created_at is required")
	}
	if s.Severity != "" && !validAdvisorySeverities[s.Severity] {
		return fmt.Errorf("sdm_advisory: invalid severity %q", s.Severity)
	}
	return nil
}

func init() {
	Register("sdm_advisory", func() NodeTyper { return &SDMAdvisory{Version: 1} })
}
