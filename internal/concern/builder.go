// Package concern implements the concern field — a structured projection of
// ledger state into a stance's prompt context. When a stance is spawned, the
// harness calls BuildConcernField to get a one-shot snapshot rendered into the
// stance's system prompt.
package concern

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/concern/sections"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

// StanceRole identifies which stance role is being spawned.
type StanceRole string

const (
	RoleDev          StanceRole = "dev"
	RoleReviewer     StanceRole = "reviewer"
	RoleJudge        StanceRole = "judge"
	RoleCTO          StanceRole = "cto"
	RoleLeadEngineer StanceRole = "lead_engineer"
	RolePO           StanceRole = "po"
	RoleResearcher   StanceRole = "researcher"
	RoleQALead       StanceRole = "qa_lead"
	RoleStakeholder  StanceRole = "stakeholder"
	RoleLeadDesigner StanceRole = "lead_designer"
	RoleSDM          StanceRole = "sdm"
	RoleVPEng        StanceRole = "vp_eng"
)

// Face distinguishes whether the stance is creating or reviewing.
type Face string

const (
	FaceProposing Face = "proposing"
	FaceReviewing Face = "reviewing"
)

// Scope specifies where in the task DAG this concern field is scoped.
type Scope struct {
	MissionID string
	TaskID    string
	LoopID    string
	BranchID  string
	StanceID  string // stance being built for (used for skill load audit)
}

// ConcernField is a one-shot projection of ledger state into prompt context.
// It has no setter methods — build a new one instead of updating.
type ConcernField struct {
	Role     StanceRole
	Face     Face
	Scope    Scope
	Sections []Section
}

// Section is a named block of rendered context.
type Section struct {
	Name    string
	Content string
	Cap     int // max entries, 0 = unlimited
}

// Template defines which sections to include and how.
type Template struct {
	Role     StanceRole
	Face     Face
	Sections []SectionSpec
}

// SectionSpec configures one section of the concern field.
type SectionSpec struct {
	Name     string
	QueryFn  sections.QueryFunc
	Cap      int
	Required bool
}

// Builder constructs concern fields from ledger state.
type Builder struct {
	ledger    *ledger.Ledger
	bus       *bus.Bus
	templates map[string]Template
}

// NewBuilder creates a concern field builder backed by the given ledger and bus.
func NewBuilder(l *ledger.Ledger, b *bus.Bus) *Builder {
	return &Builder{
		ledger:    l,
		bus:       b,
		templates: make(map[string]Template),
	}
}

// RegisterTemplate adds a template to the builder. The name is typically
// a descriptive key like "dev_implementing_ticket".
func (b *Builder) RegisterTemplate(name string, tmpl Template) {
	b.templates[name] = tmpl
}

// BuildConcernField constructs a concern field for the given role, face, and scope.
// It finds a matching template, queries the ledger for each section, and returns
// the assembled ConcernField. For skills sections, it writes skill_loaded ledger
// nodes and emits skill.loaded bus events before returning.
func (b *Builder) BuildConcernField(ctx context.Context, role StanceRole, face Face, scope Scope) (*ConcernField, error) {
	tmpl, tmplName, err := b.findTemplate(role, face)
	if err != nil {
		return nil, err
	}

	sScope := sections.Scope{
		MissionID: scope.MissionID,
		TaskID:    scope.TaskID,
		LoopID:    scope.LoopID,
		BranchID:  scope.BranchID,
	}

	cf := &ConcernField{
		Role:  role,
		Face:  face,
		Scope: scope,
	}

	hasSkillsSection := false
	for _, spec := range tmpl.Sections {
		content, err := spec.QueryFn(ctx, sScope, b.ledger)
		if err != nil {
			if spec.Required {
				return nil, fmt.Errorf("required section %q failed: %w", spec.Name, err)
			}
			continue
		}

		if spec.Name == "applicable_skills" && content != "" {
			hasSkillsSection = true
		}

		cf.Sections = append(cf.Sections, Section{
			Name:    spec.Name,
			Content: content,
			Cap:     spec.Cap,
		})
	}

	// Log skill loads: write skill_loaded nodes and emit skill.loaded events.
	if hasSkillsSection {
		if err := b.logSkillLoads(ctx, scope, role, tmplName); err != nil {
			return nil, err
		}
	}

	return cf, nil
}

// logSkillLoads queries the skills that were included in the concern field
// and writes a skill_loaded node + skill.loaded event for each.
func (b *Builder) logSkillLoads(ctx context.Context, scope Scope, role StanceRole, tmplName string) error {
	skills, err := b.ledger.Query(ctx, ledger.QueryFilter{
		Type:      "skill",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return fmt.Errorf("concern: query skills for audit: %w", err)
	}

	for _, sk := range skills {
		nodeContent, _ := json.Marshal(map[string]any{
			"schema_version":         1,
			"skill_ref":              sk.ID,
			"loading_stance_id":      scope.StanceID,
			"loading_stance_role":    string(role),
			"concern_field_template": tmplName,
			"matching_applicability": "mission_scope",
			"task_dag_scope":         scope.TaskID,
			"loop_ref":               scope.LoopID,
			"created_at":             time.Now(),
		})

		nodeID, err := b.ledger.AddNode(ctx, ledger.Node{
			Type:          "skill_loaded",
			SchemaVersion: 1,
			CreatedBy:     "concern_field_builder",
			MissionID:     scope.MissionID,
			Content:       nodeContent,
		})
		if err != nil {
			return fmt.Errorf("concern: log skill load: %w", err)
		}

		evtPayload, _ := json.Marshal(map[string]any{
			"skill_loaded_id": nodeID,
			"skill_id":        sk.ID,
			"stance_id":       scope.StanceID,
			"stance_role":     string(role),
		})
		if err := b.bus.Publish(bus.Event{
			Type:      bus.EvtSkillLoaded,
			EmitterID: "concern_field_builder",
			Scope: bus.Scope{
				MissionID: scope.MissionID,
			},
			Payload: evtPayload,
		}); err != nil {
			return fmt.Errorf("concern: emit skill loaded: %w", err)
		}
	}
	return nil
}

// findTemplate returns the first template matching the given role and face,
// along with its registered name.
func (b *Builder) findTemplate(role StanceRole, face Face) (*Template, string, error) {
	for name, tmpl := range b.templates {
		if tmpl.Role == role && tmpl.Face == face {
			return &tmpl, name, nil
		}
	}
	return nil, "", fmt.Errorf("concern: no template for role=%s face=%s", role, face)
}
