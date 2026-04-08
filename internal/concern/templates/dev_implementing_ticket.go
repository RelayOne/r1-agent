package templates

import (
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/concern/sections"
)

// DevImplementingTicket returns the template for a dev stance implementing a ticket.
func DevImplementingTicket() concern.Template {
	return concern.Template{
		Role: concern.RoleDev,
		Face: concern.FaceProposing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, true, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("applicable_skills", sections.ApplicableSkills, false, 0),
			spec("snapshot_annotations", sections.SnapshotAnnotations, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 10),
		},
	}
}

// DevFixDissent returns the template for a dev stance fixing dissent.
func DevFixDissent() concern.Template {
	return concern.Template{
		Role: concern.RoleDev,
		Face: concern.FaceReviewing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, true, 0),
			spec("dissent_history", sections.DissentHistory, true, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("snapshot_annotations", sections.SnapshotAnnotations, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 10),
		},
	}
}
