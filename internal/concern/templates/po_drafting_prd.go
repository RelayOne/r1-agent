package templates

import (
	"github.com/RelayOne/r1/internal/concern"
	"github.com/RelayOne/r1/internal/concern/sections"
)

// PODraftingPRD returns the template for a product owner drafting a PRD.
func PODraftingPRD() concern.Template {
	return concern.Template{
		Role: concern.RolePO,
		Face: concern.FaceProposing,
		Sections: []concern.SectionSpec{
			spec("original_user_intent", sections.OriginalUserIntent, true, 0),
			spec("prior_decisions", sections.PriorDecisions, false, 20),
			spec("research_reports", sections.ResearchReports, false, 0),
			spec("recent_activity", sections.RecentActivity, false, 10),
		},
	}
}
