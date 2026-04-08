// Package skillmfr handles the full lifecycle of skills in Stoke.
//
// It is a long-running process subscribed to bus events that writes
// skill-related ledger nodes. Four workflows:
//
//  1. Shipped library import — reads embedded skill files, writes to ledger
//  2. Manufacturing from completed missions — extracts patterns from decision logs
//  3. External skill import — validates and writes imported skills
//  4. Skill lifecycle management — handles confidence promotions/demotions
package skillmfr

// Confidence describes the trust level of a skill.
type Confidence string

const (
	ConfidenceCandidate Confidence = "candidate"
	ConfidenceTentative Confidence = "tentative"
	ConfidenceProven    Confidence = "proven"
)

// confidenceRank returns an ordinal for promotion/demotion comparisons.
func confidenceRank(c Confidence) int {
	switch c {
	case ConfidenceCandidate:
		return 0
	case ConfidenceTentative:
		return 1
	case ConfidenceProven:
		return 2
	default:
		return -1
	}
}

// promoteConfidence returns the next higher confidence level, capping at proven.
func promoteConfidence(c Confidence) Confidence {
	switch c {
	case ConfidenceCandidate:
		return ConfidenceTentative
	case ConfidenceTentative:
		return ConfidenceProven
	default:
		return c
	}
}

// demoteConfidence returns the next lower confidence level, capping at candidate.
func demoteConfidence(c Confidence) Confidence {
	switch c {
	case ConfidenceProven:
		return ConfidenceTentative
	case ConfidenceTentative:
		return ConfidenceCandidate
	default:
		return c
	}
}

// Provenance records where a skill came from.
type Provenance string

const (
	ProvenanceShipped      Provenance = "shipped_with_stoke"
	ProvenanceManufactured Provenance = "manufactured"
	ProvenanceImported     Provenance = "imported_external"
)

// SkillFile is the canonical representation of a skill for ledger storage.
type SkillFile struct {
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Keywords      []string   `json:"keywords"`
	Confidence    Confidence `json:"confidence"`
	Provenance    Provenance `json:"provenance"`
	Applicability []string   `json:"applicability"` // file types, task types, roles
	Content       string     `json:"content"`
	Gotchas       string     `json:"gotchas,omitempty"`
	FootgunNote   string     `json:"footgun_note,omitempty"`
}

// LifecycleAction describes what a review concluded about a skill.
type LifecycleAction string

const (
	ActionPromote     LifecycleAction = "promote"
	ActionDemote      LifecycleAction = "demote"
	ActionMarkFootgun LifecycleAction = "mark_footgun"
	ActionSupersede   LifecycleAction = "supersede"
)

// ReviewResult is the payload for skill.review.completed events.
type ReviewResult struct {
	SkillID       string          `json:"skill_id"`
	Action        LifecycleAction `json:"action"`
	Reasoning     string          `json:"reasoning"`
	NewConfidence Confidence      `json:"new_confidence,omitempty"`
}
