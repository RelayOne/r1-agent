package manifests

import (
	"github.com/RelayOne/r1/internal/supervisor"
	"github.com/RelayOne/r1/internal/supervisor/rules/antitrunc"
	"github.com/RelayOne/r1/internal/supervisor/rules/consensus"
	"github.com/RelayOne/r1/internal/supervisor/rules/drift"
	"github.com/RelayOne/r1/internal/supervisor/rules/hierarchy"
	"github.com/RelayOne/r1/internal/supervisor/rules/research"
	"github.com/RelayOne/r1/internal/supervisor/rules/skill"
	"github.com/RelayOne/r1/internal/supervisor/rules/snapshot"
	"github.com/RelayOne/r1/internal/supervisor/rules/trust"
)

// BranchRules returns all rules loaded by branch supervisors.
func BranchRules() []supervisor.Rule {
	return []supervisor.Rule{
		// Anti-truncation (non-disableable; runs first so phrase
		// detection precedes second-opinion routing)
		antitrunc.NewTruncationPhraseDetected(),
		antitrunc.NewScopeUnderdelivery(),
		antitrunc.NewSubagentSummaryTruncation(),

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
