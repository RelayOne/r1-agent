package templates

import (
	"github.com/RelayOne/r1/internal/concern"
	"github.com/RelayOne/r1/internal/concern/sections"
)

// LeadEngineerDraftingSOW returns the template for a lead engineer drafting
// a statement of work.
func LeadEngineerDraftingSOW() concern.Template {
	return concern.Template{
		Role: concern.RoleLeadEngineer,
		Face: concern.FaceProposing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, false, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("research_reports", sections.ResearchReports, false, 0),
			spec("applicable_skills", sections.ApplicableSkills, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 10),
		},
	}
}
