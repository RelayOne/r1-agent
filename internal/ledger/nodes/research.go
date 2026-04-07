package nodes

import (
	"fmt"
	"time"
)

// ResearchRequest is a stance's request for research.
// ID prefix: req-
type ResearchRequest struct {
	RequestingStanceID          string    `json:"requesting_stance_id"`
	RequestingStanceRole        string    `json:"requesting_stance_role"`
	Question                    string    `json:"question"`
	ContextForQuestion          string    `json:"context_for_question"`
	Audience                    string    `json:"audience"`
	Urgency                     string    `json:"urgency"` // high, medium, low
	ParallelResearchersRequested int      `json:"parallel_researchers_requested"`
	CreatedAt                   time.Time `json:"created_at"`

	// Optional fields.
	LoopRef      string `json:"loop_ref,omitempty"`
	TaskDAGScope string `json:"task_dag_scope,omitempty"`

	Version int `json:"schema_version"`
}

var validUrgencies = map[string]bool{
	"high": true, "medium": true, "low": true,
}

func (r *ResearchRequest) NodeType() string     { return "research_request" }
func (r *ResearchRequest) SchemaVersion() int   { return r.Version }

func (r *ResearchRequest) Validate() error {
	if r.RequestingStanceID == "" {
		return fmt.Errorf("research_request: requesting_stance_id is required")
	}
	if r.RequestingStanceRole == "" {
		return fmt.Errorf("research_request: requesting_stance_role is required")
	}
	if r.Question == "" {
		return fmt.Errorf("research_request: question is required")
	}
	if r.ContextForQuestion == "" {
		return fmt.Errorf("research_request: context_for_question is required")
	}
	if r.Audience == "" {
		return fmt.Errorf("research_request: audience is required")
	}
	if r.Urgency == "" {
		return fmt.Errorf("research_request: urgency is required")
	}
	if !validUrgencies[r.Urgency] {
		return fmt.Errorf("research_request: invalid urgency %q", r.Urgency)
	}
	if r.ParallelResearchersRequested < 1 {
		return fmt.Errorf("research_request: parallel_researchers_requested must be at least 1")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("research_request: created_at is required")
	}
	return nil
}

func init() {
	Register("research_request", func() NodeTyper { return &ResearchRequest{Version: 1} })
}

// ResearchReport is a researcher's completed report.
// ID prefix: rep-
type ResearchReport struct {
	RequestRef           string    `json:"request_ref"`
	ResearcherStanceID   string    `json:"researcher_stance_id"`
	QuestionBeingAnswered string   `json:"question_being_answered"`
	SourcesCited         []string  `json:"sources_cited"`
	Conclusion           string    `json:"conclusion"`
	ConfidenceLevel      string    `json:"confidence_level"` // high, medium, low, inconclusive
	Limitations          string    `json:"limitations"`
	CreatedAt            time.Time `json:"created_at"`

	// Optional fields.
	DissentingEvidence string `json:"dissenting_evidence,omitempty"`

	Version int `json:"schema_version"`
}

var validConfidenceLevels = map[string]bool{
	"high": true, "medium": true, "low": true, "inconclusive": true,
}

func (r *ResearchReport) NodeType() string     { return "research_report" }
func (r *ResearchReport) SchemaVersion() int   { return r.Version }

func (r *ResearchReport) Validate() error {
	if r.RequestRef == "" {
		return fmt.Errorf("research_report: request_ref is required")
	}
	if r.ResearcherStanceID == "" {
		return fmt.Errorf("research_report: researcher_stance_id is required")
	}
	if r.QuestionBeingAnswered == "" {
		return fmt.Errorf("research_report: question_being_answered is required")
	}
	if len(r.SourcesCited) == 0 {
		return fmt.Errorf("research_report: sources_cited is required")
	}
	if r.Conclusion == "" {
		return fmt.Errorf("research_report: conclusion is required")
	}
	if r.ConfidenceLevel == "" {
		return fmt.Errorf("research_report: confidence_level is required")
	}
	if !validConfidenceLevels[r.ConfidenceLevel] {
		return fmt.Errorf("research_report: invalid confidence_level %q", r.ConfidenceLevel)
	}
	if r.Limitations == "" {
		return fmt.Errorf("research_report: limitations is required")
	}
	if r.ConfidenceLevel == "inconclusive" && r.Limitations == "" {
		return fmt.Errorf("research_report: limitations is required when confidence_level is inconclusive")
	}
	return nil
}

func init() {
	Register("research_report", func() NodeTyper { return &ResearchReport{Version: 1} })
}
