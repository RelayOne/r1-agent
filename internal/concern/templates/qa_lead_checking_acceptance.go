package templates

import (
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/concern/sections"
)

// QALeadCheckingAcceptance returns the template for a QA lead checking
// acceptance criteria.
func QALeadCheckingAcceptance() concern.Template {
	return concern.Template{
		Role: concern.RoleQALead,
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
