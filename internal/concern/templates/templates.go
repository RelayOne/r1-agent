// Package templates provides concern field template definitions for each
// stance role. Each template specifies which sections to include and how.
package templates

import (
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/concern/sections"
)

// All returns every registered template keyed by name.
func All() map[string]concern.Template {
	return map[string]concern.Template{
		"dev_implementing_ticket": DevImplementingTicket(),
		"dev_fix_dissent":         DevFixDissent(),
		"reviewer_for_pr":         ReviewerForPR(),
		"reviewer_for_sow":        ReviewerForSOW(),
		"judge_iteration":         JudgeForIterationThreshold(),
		"judge_drift":             JudgeForDrift(),
		"cto_snapshot":            CTOSnapshotConsultation(),
		"lead_engineer_sow":       LeadEngineerDraftingSOW(),
		"po_drafting_prd":         PODraftingPRD(),
		"researcher_uncertainty":  ResearcherForUncertainty(),
		"qa_lead_acceptance":      QALeadCheckingAcceptance(),
		"stakeholder_escalation":  StakeholderForEscalation(),
	}
}

// RegisterAll registers every template on the given builder.
func RegisterAll(b *concern.Builder) {
	for name, tmpl := range All() {
		b.RegisterTemplate(name, tmpl)
	}
}

// spec is a shorthand constructor to reduce boilerplate.
func spec(name string, qf sections.QueryFunc, required bool, maxItems int) concern.SectionSpec {
	return concern.SectionSpec{
		Name:     name,
		QueryFn:  qf,
		Required: required,
		Cap:      maxItems,
	}
}
