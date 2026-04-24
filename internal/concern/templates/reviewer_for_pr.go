package templates

import (
	"github.com/RelayOne/r1/internal/concern"
	"github.com/RelayOne/r1/internal/concern/sections"
)

// ReviewerForPR returns the template for a reviewer evaluating a PR.
func ReviewerForPR() concern.Template {
	return concern.Template{
		Role: concern.RoleReviewer,
		Face: concern.FaceReviewing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, true, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("dissent_history", sections.DissentHistory, false, 0),
			spec("snapshot_annotations", sections.SnapshotAnnotations, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 15),
		},
	}
}

// ReviewerForSOW returns the template for a reviewer evaluating a statement of work.
func ReviewerForSOW() concern.Template {
	return concern.Template{
		Role: concern.RoleReviewer,
		Face: concern.FaceProposing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("research_reports", sections.ResearchReports, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 10),
		},
	}
}
