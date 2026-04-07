package nodes

import (
	"fmt"
	"time"
)

// Skill represents a proven pattern, shipped library entry, or imported external skill.
// ID prefix: skill-
type Skill struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Applicability []string `json:"applicability"`
	Content       string   `json:"content"`
	Provenance    string   `json:"provenance"`  // shipped_with_stoke, manufactured, imported_external, inherited_stoke
	Confidence    string   `json:"confidence"`  // proven, tentative, candidate
	Category      string   `json:"category"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     string   `json:"created_by"`

	// Optional fields.
	SupersededBy       string   `json:"superseded_by,omitempty"`
	UsageCount         int      `json:"usage_count,omitempty"`
	FootgunAnnotation  string   `json:"footgun_annotation,omitempty"`
	ImportProposalRef  string   `json:"import_proposal_ref,omitempty"`
	Tags               []string `json:"tags,omitempty"`

	Version int `json:"schema_version"`
}

var validSkillProvenances = map[string]bool{
	"shipped_with_stoke": true, "manufactured": true,
	"imported_external": true, "inherited_stoke": true,
}

var validSkillConfidences = map[string]bool{
	"proven": true, "tentative": true, "candidate": true,
}

func (s *Skill) NodeType() string     { return "skill" }
func (s *Skill) SchemaVersion() int   { return s.Version }

func (s *Skill) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("skill: name is required")
	}
	if s.Description == "" {
		return fmt.Errorf("skill: description is required")
	}
	if len(s.Applicability) == 0 {
		return fmt.Errorf("skill: applicability is required")
	}
	if s.Content == "" {
		return fmt.Errorf("skill: content is required")
	}
	if s.Provenance == "" {
		return fmt.Errorf("skill: provenance is required")
	}
	if !validSkillProvenances[s.Provenance] {
		return fmt.Errorf("skill: invalid provenance %q", s.Provenance)
	}
	if s.Confidence == "" {
		return fmt.Errorf("skill: confidence is required")
	}
	if !validSkillConfidences[s.Confidence] {
		return fmt.Errorf("skill: invalid confidence %q", s.Confidence)
	}
	if s.Category == "" {
		return fmt.Errorf("skill: category is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("skill: created_at is required")
	}
	if s.CreatedBy == "" {
		return fmt.Errorf("skill: created_by is required")
	}
	return nil
}

func init() {
	Register("skill", func() NodeTyper { return &Skill{Version: 1} })
}

// SkillLoaded records that a skill was loaded into a stance's concern field.
// ID prefix: sk-load-
type SkillLoaded struct {
	SkillRef              string    `json:"skill_ref"`
	LoadingStanceID       string    `json:"loading_stance_id"`
	LoadingStanceRole     string    `json:"loading_stance_role"`
	ConcernFieldTemplate  string    `json:"concern_field_template"`
	MatchingApplicability string    `json:"matching_applicability"`
	TaskDAGScope          string    `json:"task_dag_scope"`
	LoopRef               string    `json:"loop_ref"`
	CreatedAt             time.Time `json:"created_at"`

	Version int `json:"schema_version"`
}

func (s *SkillLoaded) NodeType() string     { return "skill_loaded" }
func (s *SkillLoaded) SchemaVersion() int   { return s.Version }

func (s *SkillLoaded) Validate() error {
	if s.SkillRef == "" {
		return fmt.Errorf("skill_loaded: skill_ref is required")
	}
	if s.LoadingStanceID == "" {
		return fmt.Errorf("skill_loaded: loading_stance_id is required")
	}
	if s.LoadingStanceRole == "" {
		return fmt.Errorf("skill_loaded: loading_stance_role is required")
	}
	if s.ConcernFieldTemplate == "" {
		return fmt.Errorf("skill_loaded: concern_field_template is required")
	}
	if s.MatchingApplicability == "" {
		return fmt.Errorf("skill_loaded: matching_applicability is required")
	}
	if s.TaskDAGScope == "" {
		return fmt.Errorf("skill_loaded: task_dag_scope is required")
	}
	if s.LoopRef == "" {
		return fmt.Errorf("skill_loaded: loop_ref is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("skill_loaded: created_at is required")
	}
	return nil
}

func init() {
	Register("skill_loaded", func() NodeTyper { return &SkillLoaded{Version: 1} })
}

// SkillApplied records that a stance actually used a loaded skill in producing output.
// ID prefix: sk-apply-
type SkillApplied struct {
	SkillRef          string    `json:"skill_ref"`
	ApplyingStanceID  string    `json:"applying_stance_id"`
	ApplyingStanceRole string   `json:"applying_stance_role"`
	ArtifactRef       string    `json:"artifact_ref"`
	HowApplied        string    `json:"how_applied"`
	LoadRef           string    `json:"load_ref"`
	CreatedAt         time.Time `json:"created_at"`

	// Optional fields.
	ContradictionsFound string `json:"contradictions_found,omitempty"`

	Version int `json:"schema_version"`
}

func (s *SkillApplied) NodeType() string     { return "skill_applied" }
func (s *SkillApplied) SchemaVersion() int   { return s.Version }

func (s *SkillApplied) Validate() error {
	if s.SkillRef == "" {
		return fmt.Errorf("skill_applied: skill_ref is required")
	}
	if s.ApplyingStanceID == "" {
		return fmt.Errorf("skill_applied: applying_stance_id is required")
	}
	if s.ApplyingStanceRole == "" {
		return fmt.Errorf("skill_applied: applying_stance_role is required")
	}
	if s.ArtifactRef == "" {
		return fmt.Errorf("skill_applied: artifact_ref is required")
	}
	if s.HowApplied == "" {
		return fmt.Errorf("skill_applied: how_applied is required")
	}
	if s.LoadRef == "" {
		return fmt.Errorf("skill_applied: load_ref is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("skill_applied: created_at is required")
	}
	return nil
}

func init() {
	Register("skill_applied", func() NodeTyper { return &SkillApplied{Version: 1} })
}

// SkillImportProposal is a research stance's proposal to import an external skill.
// ID prefix: sk-imp-
type SkillImportProposal struct {
	ProposingStanceID  string    `json:"proposing_stance_id"`
	CandidateContent   string    `json:"candidate_content"`
	SourceMetadata     string    `json:"source_metadata"`
	ReputationSummary  string    `json:"reputation_summary"`
	SecurityReview     string    `json:"security_review"`
	ConsistencyReview  string    `json:"consistency_review"`
	RiskAssessment     string    `json:"risk_assessment"` // low, medium, high
	TaskContext        string    `json:"task_context"`
	CreatedAt          time.Time `json:"created_at"`

	// Optional fields.
	AlternativeCandidatesConsidered string `json:"alternative_candidates_considered,omitempty"`

	Version int `json:"schema_version"`
}

var validRiskAssessments = map[string]bool{
	"low": true, "medium": true, "high": true,
}

func (s *SkillImportProposal) NodeType() string     { return "skill_import_proposal" }
func (s *SkillImportProposal) SchemaVersion() int   { return s.Version }

func (s *SkillImportProposal) Validate() error {
	if s.ProposingStanceID == "" {
		return fmt.Errorf("skill_import_proposal: proposing_stance_id is required")
	}
	if s.CandidateContent == "" {
		return fmt.Errorf("skill_import_proposal: candidate_content is required")
	}
	if s.SourceMetadata == "" {
		return fmt.Errorf("skill_import_proposal: source_metadata is required")
	}
	if s.ReputationSummary == "" {
		return fmt.Errorf("skill_import_proposal: reputation_summary is required")
	}
	if s.SecurityReview == "" {
		return fmt.Errorf("skill_import_proposal: security_review is required")
	}
	if s.ConsistencyReview == "" {
		return fmt.Errorf("skill_import_proposal: consistency_review is required")
	}
	if s.RiskAssessment == "" {
		return fmt.Errorf("skill_import_proposal: risk_assessment is required")
	}
	if !validRiskAssessments[s.RiskAssessment] {
		return fmt.Errorf("skill_import_proposal: invalid risk_assessment %q", s.RiskAssessment)
	}
	if s.TaskContext == "" {
		return fmt.Errorf("skill_import_proposal: task_context is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("skill_import_proposal: created_at is required")
	}
	return nil
}

func init() {
	Register("skill_import_proposal", func() NodeTyper { return &SkillImportProposal{Version: 1} })
}
