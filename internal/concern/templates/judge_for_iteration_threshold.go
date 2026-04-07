package templates

import (
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/concern/sections"
)

// JudgeForIterationThreshold returns the template for a judge evaluating
// whether an iteration loop should continue or terminate.
func JudgeForIterationThreshold() concern.Template {
	return concern.Template{
		Role: concern.RoleJudge,
		Face: concern.FaceReviewing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, true, 0),
			spec("active_loops", sections.ActiveLoops, true, 0),
			spec("dissent_history", sections.DissentHistory, false, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("recent_activity", sections.RecentActivity, false, 20),
		},
	}
}

// JudgeForDrift returns the template for a judge evaluating scope drift.
func JudgeForDrift() concern.Template {
	return concern.Template{
		Role: concern.RoleJudge,
		Face: concern.FaceProposing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("task_dag_scope", sections.TaskDAGScope, true, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("recent_activity", sections.RecentActivity, false, 20),
		},
	}
}
