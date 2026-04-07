package nodes

import (
	"encoding/json"
	"fmt"
	"time"
)

// Draft represents a candidate artifact under review by a loop's convened partners.
// ID prefix: draft-
type Draft struct {
	DraftType         string          `json:"draft_type"`          // prd, sow, ticket_definition, pr, refactor_proposal, fix, judge_verdict_draft
	LoopRef           string          `json:"loop_ref"`            // which loop this draft belongs to
	ProposingStanceID string          `json:"proposing_stance_id"` // stance that produced it
	Content           json.RawMessage `json:"content"`             // the artifact being reviewed
	CreatedAt         time.Time       `json:"created_at"`

	// Optional fields.
	Supersedes      string   `json:"supersedes,omitempty"`        // previous draft ID
	ResearchRefs    []string `json:"research_refs,omitempty"`
	SkillRefs       []string `json:"skill_refs,omitempty"`
	SnapshotAnnoRefs []string `json:"snapshot_anno_refs,omitempty"`

	Version int `json:"schema_version"`
}

var validDraftTypes = map[string]bool{
	"prd": true, "sow": true, "ticket_definition": true, "pr": true,
	"refactor_proposal": true, "fix": true, "judge_verdict_draft": true,
}

func (d *Draft) NodeType() string     { return "draft" }
func (d *Draft) SchemaVersion() int   { return d.Version }

func (d *Draft) Validate() error {
	if d.DraftType == "" {
		return fmt.Errorf("draft: draft_type is required")
	}
	if !validDraftTypes[d.DraftType] {
		return fmt.Errorf("draft: invalid draft_type %q", d.DraftType)
	}
	if d.LoopRef == "" {
		return fmt.Errorf("draft: loop_ref is required")
	}
	if d.ProposingStanceID == "" {
		return fmt.Errorf("draft: proposing_stance_id is required")
	}
	if len(d.Content) == 0 {
		return fmt.Errorf("draft: content is required")
	}
	if d.CreatedAt.IsZero() {
		return fmt.Errorf("draft: created_at is required")
	}
	return nil
}

func init() {
	Register("draft", func() NodeTyper { return &Draft{Version: 1} })
}
