// Package manifests provides rule sets for each supervisor type.
//
// Each manifest function returns the complete set of rules for its supervisor
// tier. Rules are constructed here and returned as a slice — the supervisor
// core handles registration, priority sorting, and wizard overrides.
package manifests

import (
	"github.com/ericmacdougall/stoke/internal/supervisor"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/consensus"
	crossteam "github.com/ericmacdougall/stoke/internal/supervisor/rules/cross_team"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/drift"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/hierarchy"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/research"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/skill"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/snapshot"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/trust"
)

// MissionRules returns all rules loaded by the mission supervisor.
func MissionRules() []supervisor.Rule {
	return []supervisor.Rule{
		// Trust (non-disableable)
		trust.NewCompletionRequiresSecondOpinion(),
		trust.NewFixRequiresSecondOpinion(),
		trust.NewProblemRequiresSecondOpinion(),

		// Consensus
		consensus.NewDraftRequiresReview(),
		consensus.NewDissentRequiresAddress(),
		consensus.NewConvergenceDetected(),
		consensus.NewIterationThreshold(),
		consensus.NewPartnerTimeout(),

		// Snapshot
		snapshot.NewModificationRequiresCTO(),
		snapshot.NewFormatterRequiresConsent(),

		// Cross-team
		crossteam.NewModificationRequiresCTO(),

		// Hierarchy (mission-level)
		hierarchy.NewCompletionRequiresParentAgreement(),
		hierarchy.NewUserEscalation(),

		// Drift
		drift.NewJudgeScheduled(),
		drift.NewIntentAlignmentCheck(),
		drift.NewBudgetThreshold(),

		// Research
		research.NewRequestDispatchesResearchers(),
		research.NewReportUnblocksRequester(),
		research.NewTimeout(),

		// Skill
		skill.NewExtractionTrigger(),
		skill.NewLoadAudit(),
		skill.NewApplicationRequiresReview(),
		skill.NewContradictsOutcome(),
		skill.NewImportConsensus(),
	}
}
