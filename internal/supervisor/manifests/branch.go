package manifests

import (
	"github.com/ericmacdougall/stoke/internal/supervisor"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/consensus"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/drift"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/hierarchy"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/research"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/skill"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/snapshot"
	"github.com/ericmacdougall/stoke/internal/supervisor/rules/trust"
)

// BranchRules returns all rules loaded by branch supervisors.
func BranchRules() []supervisor.Rule {
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

		// Hierarchy (branch-level: forward escalations upward)
		hierarchy.NewEscalationForwardsUpward(),

		// Drift
		drift.NewJudgeScheduled(),
		drift.NewIntentAlignmentCheck(),
		drift.NewBudgetThreshold(),

		// Research
		research.NewRequestDispatchesResearchers(),
		research.NewReportUnblocksRequester(),
		research.NewTimeout(),

		// Skill (audit + review, no extraction trigger)
		skill.NewLoadAudit(),
		skill.NewApplicationRequiresReview(),
		skill.NewContradictsOutcome(),
	}
}
