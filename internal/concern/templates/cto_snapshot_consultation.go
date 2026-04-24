package templates

import (
	"github.com/RelayOne/r1/internal/concern"
	"github.com/RelayOne/r1/internal/concern/sections"
)

// CTOSnapshotConsultation returns the template for a CTO snapshot consultation.
func CTOSnapshotConsultation() concern.Template {
	return concern.Template{
		Role: concern.RoleCTO,
		Face: concern.FaceReviewing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, false, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("active_loops", sections.ActiveLoops, false, 0),
			spec("sdm_advisories", sections.SDMAdvisories, false, 0),
			spec("research_reports", sections.ResearchReports, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 20),
		},
	}
}
