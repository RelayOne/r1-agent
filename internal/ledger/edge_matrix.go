package ledger

import "fmt"

// edgeConstraint defines an allowed (from_type, to_type) pair for a given edge type.
type edgeConstraint struct {
	FromType string
	ToType   string
}

// anyType is a sentinel value meaning "any node type is allowed".
const anyType = "*"

// allowedEdgeMatrix maps each EdgeType to its set of allowed (from_type, to_type) constraints.
// If a from or to type is anyType ("*"), any node type is accepted in that position.
var allowedEdgeMatrix = map[EdgeType][]edgeConstraint{
	// distills: decision_internal -> decision_repo only.
	EdgeDistills: {
		{FromType: "decision_internal", ToType: "decision_repo"},
	},

	// supersedes: same-type nodes only (a node can only supersede another of the same type).
	// We use anyType for both and enforce same-type at validation time.
	// See validateEdgeMatrix for the special same-type rule.
	EdgeSupersedes: {
		{FromType: anyType, ToType: anyType},
	},

	// depends_on: task -> task.
	EdgeDependsOn: {
		{FromType: "task", ToType: "task"},
	},

	// contradicts: decision or draft nodes can contradict each other.
	EdgeContradicts: {
		{FromType: "decision_internal", ToType: "decision_internal"},
		{FromType: "decision_repo", ToType: "decision_repo"},
		{FromType: "decision_internal", ToType: "decision_repo"},
		{FromType: "decision_repo", ToType: "decision_internal"},
		{FromType: "draft", ToType: "draft"},
	},

	// extends: general-purpose extension, but scoped to same-category nodes.
	// draft extends draft, task extends task, skill extends skill, research extends research.
	EdgeExtends: {
		{FromType: "draft", ToType: "draft"},
		{FromType: "task", ToType: "task"},
		{FromType: "skill", ToType: "skill"},
		{FromType: "research_report", ToType: "research_report"},
		{FromType: "research_report", ToType: "research_request"},
		{FromType: "snapshot_annotation", ToType: "snapshot_annotation"},
		{FromType: "loop", ToType: "loop"},
	},

	// references: general purpose citation, any -> any.
	EdgeReferences: {
		{FromType: anyType, ToType: anyType},
	},

	// resolves: escalation resolution chain.
	EdgeResolves: {
		{FromType: "stakeholder_directive", ToType: "escalation"},
		{FromType: "judge_verdict", ToType: "escalation"},
		{FromType: "decision_internal", ToType: "escalation"},
		{FromType: "decision_repo", ToType: "escalation"},
		{FromType: "branch_completion_agreement", ToType: "branch_completion_proposal"},
		{FromType: "branch_completion_dissent", ToType: "branch_completion_proposal"},
		{FromType: "dissent", ToType: "draft"},
		{FromType: "agree", ToType: "draft"},
	},
}

// validateEdgeMatrix checks whether the given edge type is allowed between the
// specified from and to node types. Returns nil if the combination is valid.
func validateEdgeMatrix(edgeType EdgeType, fromNodeType, toNodeType string) error {
	constraints, ok := allowedEdgeMatrix[edgeType]
	if !ok {
		return fmt.Errorf("ledger: edge type %q has no matrix entry", edgeType)
	}

	// Special rule for supersedes: from and to must be the same node type.
	if edgeType == EdgeSupersedes {
		if fromNodeType != toNodeType {
			return fmt.Errorf("ledger: %s edges require same node types; got %s -> %s",
				edgeType, fromNodeType, toNodeType)
		}
		return nil
	}

	for _, c := range constraints {
		fromMatch := c.FromType == anyType || c.FromType == fromNodeType
		toMatch := c.ToType == anyType || c.ToType == toNodeType
		if fromMatch && toMatch {
			return nil
		}
	}

	return fmt.Errorf("ledger: edge type %s not allowed from %s to %s",
		edgeType, fromNodeType, toNodeType)
}
