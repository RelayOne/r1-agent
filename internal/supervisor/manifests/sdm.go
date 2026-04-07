package manifests

import (
	"github.com/ericmacdougall/stoke/internal/supervisor"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/sdm"
)

// SDMRules returns all rules loaded by the SDM supervisor.
// All SDM rules are detection-only — they emit advisory events but never
// pause workers, spawn stances, or transition state.
func SDMRules() []supervisor.Rule {
	return []supervisor.Rule{
		sdm.NewCollisionFileModification(),
		sdm.NewDependencyCrossed(),
		sdm.NewDuplicateWorkDetected(),
		sdm.NewScheduleRiskCriticalPath(),
		sdm.NewDriftCrossBranch(),
	}
}
