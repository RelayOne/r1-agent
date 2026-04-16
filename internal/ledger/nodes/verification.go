// Package nodes — verification.go
//
// VerificationEvidence ledger node (STOKE-005 scope). Emitted at
// every VALIDATING state transition so the chain of "who verified
// what, with which reviewer, against which evidence" is
// reconstructable from the ledger alone.
//
// Distinct from critic.Verdict (which is a per-commit in-memory
// review decision): VerificationEvidence is the ledger-persisted
// record that a verification ran — including for non-code outputs
// (plans, research summaries, AC re-writes, stance verdicts).
package nodes

import (
	"fmt"
	"time"
)

// VerificationEvidence records one verification pass at a
// VALIDATING state transition.
//
// ID prefix: verify-
type VerificationEvidence struct {
	// SubjectRef is the content ID of the node being verified
	// (a task output, a plan, a research summary, a stance
	// verdict — anything that reaches a VALIDATING transition).
	SubjectRef string `json:"subject_ref"`

	// SubjectKind is the NodeType() of the subject, recorded so
	// downstream consumers can filter verifications by what was
	// verified without dereferencing SubjectRef.
	SubjectKind string `json:"subject_kind"`

	// ProducerModel + VerifierModel are the concrete model IDs
	// (not family names) that did the production and the
	// verification. VerifierModel must differ from ProducerModel
	// — builder ≠ reviewer enforcement lives in the routing
	// layer but we record the constraint-verifying evidence here.
	ProducerModel string `json:"producer_model"`
	VerifierModel string `json:"verifier_model"`

	// Verdict is one of:
	//   "agree"       — verifier confirms the producer's output
	//   "disagree"    — verifier rejects; HITL escalation follows
	//   "partial"     — verifier partially agrees; see Notes
	//   "insufficient" — verifier couldn't decide (missing context)
	Verdict string `json:"verdict"`

	// EvidenceRefs carry content IDs of artifacts (logs, diffs,
	// command outputs, test results) the verifier examined. The
	// critic.Verdict type has a parallel EvidenceRefs field for
	// the in-memory decision; this is the ledger-persisted copy
	// so the chain of "what did the verifier read?" survives
	// beyond the process lifetime.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`

	// Notes is the verifier's free-form explanation. Kept so
	// human operators auditing a disagreement can read the
	// verifier's reasoning without re-running the check.
	Notes string `json:"notes,omitempty"`

	// CrossFamily is true when ProducerModel and VerifierModel
	// belong to different model families (Claude vs Codex,
	// Claude vs Gemini, etc.). The harness recommends cross-
	// family review but doesn't auto-enforce it; recording the
	// actual state here lets the reviewereval measurement
	// harness correlate cross-family vs same-family verification
	// accuracy over time.
	CrossFamily bool `json:"cross_family"`

	// When the verification completed.
	When time.Time `json:"when"`

	Version int `json:"schema_version"`
}

func (v *VerificationEvidence) NodeType() string   { return "verification_evidence" }
func (v *VerificationEvidence) SchemaVersion() int { return v.Version }

var validVerificationVerdicts = map[string]bool{
	"agree":        true,
	"disagree":     true,
	"partial":      true,
	"insufficient": true,
}

func (v *VerificationEvidence) Validate() error {
	if v.SubjectRef == "" {
		return fmt.Errorf("verification_evidence: subject_ref is required")
	}
	if v.SubjectKind == "" {
		return fmt.Errorf("verification_evidence: subject_kind is required")
	}
	if v.ProducerModel == "" {
		return fmt.Errorf("verification_evidence: producer_model is required")
	}
	if v.VerifierModel == "" {
		return fmt.Errorf("verification_evidence: verifier_model is required")
	}
	if v.ProducerModel == v.VerifierModel {
		return fmt.Errorf("verification_evidence: producer_model and verifier_model must differ")
	}
	if v.Verdict == "" {
		return fmt.Errorf("verification_evidence: verdict is required")
	}
	if !validVerificationVerdicts[v.Verdict] {
		return fmt.Errorf("verification_evidence: invalid verdict %q", v.Verdict)
	}
	if v.When.IsZero() {
		return fmt.Errorf("verification_evidence: when is required")
	}
	return nil
}

func init() {
	Register("verification_evidence", func() NodeTyper { return &VerificationEvidence{Version: 1} })
}
