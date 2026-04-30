package ledgerlink

import (
	"time"

	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/r1skill/wizard"
)

type SkillAuthoringDecisionsNode struct {
	wizard.SkillAuthoringDecisions
}

func (n *SkillAuthoringDecisionsNode) NodeType() string   { return "skill_authoring_decisions" }
func (n *SkillAuthoringDecisionsNode) SchemaVersion() int { return n.Version }
func (n *SkillAuthoringDecisionsNode) Validate() error    { return n.SkillAuthoringDecisions.Validate() }

func NewSession(skillID, mode, packID string) SkillAuthoringDecisionsNode {
	now := time.Now().UTC()
	return SkillAuthoringDecisionsNode{
		SkillAuthoringDecisions: wizard.SkillAuthoringDecisions{
			SessionID:      "wizard-session-" + now.Format("20060102T150405.000000000"),
			SkillID:        skillID,
			SkillVersion:   1,
			StartedAt:      now,
			Mode:           mode,
			QuestionPackID: packID,
			FinalStatus:    "in_progress",
			Version:        1,
		},
	}
}

func init() {
	nodes.Register("skill_authoring_decisions", func() nodes.NodeTyper {
		return &SkillAuthoringDecisionsNode{}
	})
}
