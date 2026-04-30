package wizard

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SkillAuthoringDecisions struct {
	SessionID         string     `json:"session_id"`
	SkillID           string     `json:"skill_id"`
	SkillVersion      int        `json:"skill_version"`
	StartedAt         time.Time  `json:"started_at"`
	CompletedAt       time.Time  `json:"completed_at"`
	Mode              string     `json:"mode"`
	OperatorID        string     `json:"operator_id,omitempty"`
	QuestionPackID    string     `json:"question_pack_id"`
	SourceArtifactRef string     `json:"source_artifact_ref,omitempty"`
	Decisions         []Decision `json:"decisions"`
	ProducedIRRef     string     `json:"produced_ir_ref,omitempty"`
	AnalyzerProofRef  string     `json:"analyzer_proof_ref,omitempty"`
	FinalStatus       string     `json:"final_status"`
	Version           int        `json:"schema_version"`
}

type Decision struct {
	Step              int             `json:"step"`
	Stage             string          `json:"stage"`
	QuestionID        string          `json:"question_id"`
	QuestionText      string          `json:"question_text"`
	Mode              string          `json:"mode"`
	OperatorAnswer    string          `json:"operator_answer,omitempty"`
	LLMReasoning      string          `json:"llm_reasoning,omitempty"`
	LLMConfidence     float64         `json:"llm_confidence,omitempty"`
	InterpretedValue  json.RawMessage `json:"interpreted_value"`
	IRPath            string          `json:"ir_path"`
	IRValue           json.RawMessage `json:"ir_value"`
	OperatorConfirmed bool            `json:"operator_confirmed"`
	ConfirmedAt       time.Time       `json:"confirmed_at"`
	RevisionOfStep    int             `json:"revision_of_step,omitempty"`
	RelatedDecisions  []int           `json:"related_decisions,omitempty"`
}

func (s *SkillAuthoringDecisions) Validate() error {
	if s.SessionID == "" {
		return errors.New("wizard: session_id required")
	}
	if s.SkillID == "" {
		return errors.New("wizard: skill_id required")
	}
	if s.Mode == "" {
		return errors.New("wizard: mode required")
	}
	if !validSessionModes[s.Mode] {
		return fmt.Errorf("wizard: invalid mode %q", s.Mode)
	}
	if s.QuestionPackID == "" {
		return errors.New("wizard: question_pack_id required")
	}
	if s.FinalStatus == "" {
		return errors.New("wizard: final_status required")
	}
	if !validStatuses[s.FinalStatus] {
		return fmt.Errorf("wizard: invalid final_status %q", s.FinalStatus)
	}
	if s.StartedAt.IsZero() {
		return errors.New("wizard: started_at required")
	}
	if s.Version < 1 {
		return errors.New("wizard: schema_version must be >= 1")
	}
	for i := range s.Decisions {
		if err := s.Decisions[i].Validate(); err != nil {
			return fmt.Errorf("wizard: decision %d: %w", i, err)
		}
	}
	return nil
}

func (d *Decision) Validate() error {
	if d.Step < 0 {
		return errors.New("step must be non-negative")
	}
	if d.Stage == "" {
		return errors.New("stage required")
	}
	if d.QuestionID == "" {
		return errors.New("question_id required")
	}
	if d.Mode != "operator" && d.Mode != "llm-best-judgment" {
		return fmt.Errorf("invalid mode %q", d.Mode)
	}
	if d.Mode == "llm-best-judgment" && (d.LLMConfidence < 0 || d.LLMConfidence > 1) {
		return fmt.Errorf("llm_confidence %f outside [0,1]", d.LLMConfidence)
	}
	if d.IRPath == "" {
		return errors.New("ir_path required")
	}
	if len(d.IRValue) == 0 {
		return errors.New("ir_value required")
	}
	return nil
}

type Query struct {
	QuestionID        string
	QuestionIDPrefix  string
	Stage             string
	Mode              string
	OperatorConfirmed *bool
	ConfidenceLT      *float64
	ConfidenceGTE     *float64
	IRPathPrefix      string
}

func (s *SkillAuthoringDecisions) Filter(q Query) []Decision {
	out := make([]Decision, 0, len(s.Decisions))
	for _, d := range s.Decisions {
		if q.QuestionID != "" && d.QuestionID != q.QuestionID {
			continue
		}
		if q.QuestionIDPrefix != "" && !strings.HasPrefix(d.QuestionID, q.QuestionIDPrefix) {
			continue
		}
		if q.Stage != "" && d.Stage != q.Stage {
			continue
		}
		if q.Mode != "" && d.Mode != q.Mode {
			continue
		}
		if q.OperatorConfirmed != nil && d.OperatorConfirmed != *q.OperatorConfirmed {
			continue
		}
		if q.ConfidenceLT != nil && d.LLMConfidence >= *q.ConfidenceLT {
			continue
		}
		if q.ConfidenceGTE != nil && d.LLMConfidence < *q.ConfidenceGTE {
			continue
		}
		if q.IRPathPrefix != "" && !strings.HasPrefix(d.IRPath, q.IRPathPrefix) {
			continue
		}
		out = append(out, d)
	}
	return out
}

var validSessionModes = map[string]bool{
	"interactive": true,
	"headless":    true,
	"hybrid":      true,
}

var validStatuses = map[string]bool{
	"registered":           true,
	"rejected_by_analyzer": true,
	"rejected_by_tests":    true,
	"abandoned":            true,
	"in_progress":          true,
}
