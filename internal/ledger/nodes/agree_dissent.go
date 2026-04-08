package nodes

import (
	"fmt"
	"time"
)

// Agree represents a consensus partner's agreement on a draft.
// ID prefix: agree-
type Agree struct {
	DraftRef          string    `json:"draft_ref"`
	AgreeingStanceID  string    `json:"agreeing_stance_id"`
	AgreeingStanceRole string   `json:"agreeing_stance_role"`
	Reasoning         string    `json:"reasoning"`
	CreatedAt         time.Time `json:"created_at"`

	// Optional fields.
	Caveats string `json:"caveats,omitempty"`

	Version int `json:"schema_version"`
}

func (a *Agree) NodeType() string     { return "agree" }
func (a *Agree) SchemaVersion() int   { return a.Version }

func (a *Agree) Validate() error {
	if a.DraftRef == "" {
		return fmt.Errorf("agree: draft_ref is required")
	}
	if a.AgreeingStanceID == "" {
		return fmt.Errorf("agree: agreeing_stance_id is required")
	}
	if a.AgreeingStanceRole == "" {
		return fmt.Errorf("agree: agreeing_stance_role is required")
	}
	if a.Reasoning == "" {
		return fmt.Errorf("agree: reasoning is required")
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("agree: created_at is required")
	}
	return nil
}

func init() {
	Register("agree", func() NodeTyper { return &Agree{Version: 1} })
}

// Dissent represents a consensus partner's disagreement with a draft.
// ID prefix: dissent-
type Dissent struct {
	DraftRef            string    `json:"draft_ref"`
	DissentingStanceID  string    `json:"dissenting_stance_id"`
	DissentingStanceRole string   `json:"dissenting_stance_role"`
	Reasoning           string    `json:"reasoning"`
	RequestedChange     string    `json:"requested_change"`
	Severity            string    `json:"severity"` // blocking, advisory
	CreatedAt           time.Time `json:"created_at"`

	// Optional fields.
	ResearchRefs []string `json:"research_refs,omitempty"`

	Version int `json:"schema_version"`
}

var validDissentSeverities = map[string]bool{
	"blocking": true, "advisory": true,
}

func (d *Dissent) NodeType() string     { return "dissent" }
func (d *Dissent) SchemaVersion() int   { return d.Version }

func (d *Dissent) Validate() error {
	if d.DraftRef == "" {
		return fmt.Errorf("dissent: draft_ref is required")
	}
	if d.DissentingStanceID == "" {
		return fmt.Errorf("dissent: dissenting_stance_id is required")
	}
	if d.DissentingStanceRole == "" {
		return fmt.Errorf("dissent: dissenting_stance_role is required")
	}
	if d.Reasoning == "" {
		return fmt.Errorf("dissent: reasoning is required")
	}
	if d.RequestedChange == "" {
		return fmt.Errorf("dissent: requested_change is required")
	}
	if d.Severity == "" {
		return fmt.Errorf("dissent: severity is required")
	}
	if !validDissentSeverities[d.Severity] {
		return fmt.Errorf("dissent: invalid severity %q", d.Severity)
	}
	if d.CreatedAt.IsZero() {
		return fmt.Errorf("dissent: created_at is required")
	}
	return nil
}

func init() {
	Register("dissent", func() NodeTyper { return &Dissent{Version: 1} })
}
