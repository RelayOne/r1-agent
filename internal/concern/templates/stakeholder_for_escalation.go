package templates

import (
	"github.com/RelayOne/r1-agent/internal/concern"
	"github.com/RelayOne/r1-agent/internal/concern/sections"
)

// StakeholderForEscalation returns the template for a stakeholder handling
// an escalation.
func StakeholderForEscalation() concern.Template {
	return concern.Template{
		Role: concern.RoleStakeholder,
		Face: concern.FaceReviewing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, false, 0),
			spec("active_loops", sections.ActiveLoops, false, 0),
			spec("dissent_history", sections.DissentHistory, false, 0),
			spec("sdm_advisories", sections.SDMAdvisories, false, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("recent_activity", sections.RecentActivity, false, 20),
		},
	}
}
