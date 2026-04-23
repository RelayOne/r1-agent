// Package hire — verify_settle.go (S-10 hire → verify → settle)
//
// This file gates payment settlement on a hired agent's deliverable.
// Before we release funds via TrustPlane, we run the deliverable
// through an acceptance criterion ladder. When every criterion
// passes, Settle is called and a `stoke.delegation.settle` event is
// emitted. When any criterion fails after active resolution attempts
// are exhausted, Dispute is called with the criterion audit trail as
// structured evidence, and a wrapping error is returned.
//
// Design notes
// ============
//
// Conceptually this is the same 8-tier verification descent Stoke
// uses for its own workers (see internal/plan/verification_descent.go),
// applied to external deliverables: deliverable bytes in, pass/fail
// verdict out, same classification, same soft-pass policy. That
// engine in its current shape is command-oriented (it runs shell
// commands against a repo checkout) and will grow a `VerifyFunc`
// field under the S-3 generalization; until that field exists on
// `plan.AcceptanceCriterion`, we model delegated-delivery ACs
// locally here in the hire package with the same verdict vocabulary
// (pass / soft-pass / fail). When S-3 merges, each deliveryCriterion
// can be mapped 1:1 onto a VerifyFunc AC and fed straight into the
// shared descent engine — no caller churn.
//
// Criterion model
// ===============
//
// Two baseline criteria apply to every delegated deliverable:
//
//   1. delivery-complete: non-empty deliverable received. A hired
//      agent that returns zero bytes is not billable regardless of
//      spec wording.
//
//   2. delivery-matches-spec: reviewer judges the deliverable
//      against the hire spec. When the Hirer has a ReviewFunc hook
//      configured, it's called with (ctx, spec, delivery) and
//      returns (pass bool, reason string). When ReviewFunc is nil,
//      a deterministic fallback (keyword-overlap + minimum length)
//      is used. The fallback is intentionally lightweight — it
//      doesn't pretend to be semantic review, it exists so that
//      local dev + tests can exercise the verify/settle path
//      without provisioning an LLM.
//
// Callers can supply extra criteria (spec-specific: output format,
// length bounds, keyword coverage, etc.) via VerifyAndSettleOpts.
//
// Two terminal paths
// ==================
//
//   all ACs pass        → SettlementClient.Settle(ctx, contractID, deliveryCostUSD=0)
//                         + EmitSystem("stoke.delegation.settle", {...})
//                         + nil returned.
//
//   any AC fails (hard) → SettlementClient.Dispute(ctx, DisputeEvidence{...})
//                         + EmitSystem("stoke.delegation.dispute", {...})
//                         + error wrapping the failed AC's reason.
//
// Event emission
// ==============
//
// We fire `stoke.delegation.verify` per AC iteration (pass or fail)
// so operators can tail the stream and see exactly which criterion
// tripped. On settle/dispute the terminal event carries the full
// criterion summary. All events go through the streamjson.Emitter
// when configured; when the Hirer's Emitter is nil, emission is a
// no-op (free-of-branch at call sites).
package hire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// Hirer is an alias of Engine, preserved so the S-10 verify/settle
// API reads naturally ("Hirer.VerifyAndSettle") while the legacy
// discover→hire→complete surface on Engine keeps working verbatim.
type Hirer = Engine

// SettlementClient is the slice of TrustPlane that this package
// needs for payment release / dispute escalation. Kept as a local
// interface so we don't depend on a Settle / Dispute surface being
// present on trustplane.Client yet — the package that owns the
// TrustPlane wire protocol will add those methods in its own
// commit and either implement this interface directly on
// trustplane.Client or provide a thin adapter. Until then, callers
// can inject a bespoke implementation and tests use a mock.
type SettlementClient interface {
	// Settle releases escrowed payment for a completed contract.
	// Called only after every acceptance criterion has passed.
	Settle(ctx context.Context, req SettleRequest) (SettleReceipt, error)

	// Dispute withholds payment and files a structured complaint.
	// Called when any acceptance criterion fails after descent.
	// Evidence is the full audit trail — criteria list, per-criterion
	// verdicts, the failed criterion's reason, and a snapshot of the
	// deliverable sufficient for a human reviewer or the TrustPlane
	// auditor to replay the decision.
	Dispute(ctx context.Context, evidence DisputeEvidence) (DisputeReceipt, error)
}

// SettleRequest is the payload for a successful settlement.
type SettleRequest struct {
	ContractID string
	AgentDID   string
	// PaidUSD is the amount released. In the MVP we pass the hire
	// receipt's EstimatedCostUSD; the TrustPlane side enforces the
	// actual escrow balance.
	PaidUSD float64
	// Note carries a short human-readable string for the audit
	// pipeline (e.g. "all 2 criteria passed").
	Note string
}

// SettleReceipt is the settlement acknowledgement.
type SettleReceipt struct {
	SettlementID string
	Note         string
}

// DisputeEvidence is the structured audit trail TrustPlane needs to
// adjudicate a dispute. It mirrors the descent engine's output
// vocabulary (criterion ID + pass/fail + reason + output) so a human
// reviewer can trace every verdict without re-running the flow.
type DisputeEvidence struct {
	ContractID string
	AgentDID   string
	// Spec is the original hire specification text, carried so the
	// auditor can judge spec-vs-deliverable without an out-of-band
	// lookup.
	Spec string
	// DeliverySample is the first N bytes of the deliverable (bounded
	// to keep the dispute payload small). The full bytes are expected
	// to be available via the content-addressed ledger.
	DeliverySample []byte
	// FailedCriterionID names the AC that tripped.
	FailedCriterionID string
	// FailedReason is the human-readable failure explanation from
	// that AC. Copied to top-level for operator clarity.
	FailedReason string
	// Verdicts is the per-criterion audit trail: criterion ID ->
	// { pass, reason, output }. Equivalent to the descent engine's
	// DescentResult.ToMap() shape.
	Verdicts []CriterionVerdict
}

// DisputeReceipt is the dispute acknowledgement.
type DisputeReceipt struct {
	DisputeID string
	Note      string
}

// CriterionVerdict is one entry in the descent audit trail.
type CriterionVerdict struct {
	ID     string
	Pass   bool
	Reason string
	Output string
}

// ReviewFunc is the Hirer's optional hook for LLM-based spec-match
// review. Returns (pass, reason). When nil, the deterministic
// fallback (ReviewDeliverableFallback) is used for the
// delivery-matches-spec criterion.
type ReviewFunc func(ctx context.Context, spec string, delivery []byte) (pass bool, reason string)

// VerifyAndSettleOpts carries per-call knobs. All fields are
// optional; zero values yield the standard two-criterion flow.
type VerifyAndSettleOpts struct {
	// Extra adds caller-supplied criteria to the baseline set.
	// Each entry is evaluated after the two baseline criteria.
	Extra []DeliveryCriterion

	// PaidUSD overrides the amount passed to Settle. When zero, the
	// Hirer can fill it from an external receipt; for now we pass
	// through as-is.
	PaidUSD float64

	// MaxDisputeSampleBytes bounds the DeliverySample included in a
	// DisputeEvidence payload. Default 4096. Set to -1 to disable
	// truncation (not recommended for real wire use).
	MaxDisputeSampleBytes int
}

// DeliveryCriterion is one verification gate applied to a hired
// agent's deliverable. Matches the shape S-3 will give to
// plan.AcceptanceCriterion.VerifyFunc — when that lands, each
// DeliveryCriterion can be converted 1:1 to a plan AC.
type DeliveryCriterion struct {
	ID          string
	Description string
	// Check is the verifier. Returns (pass, reason). The reason is
	// always populated, even on pass, so the audit trail records
	// WHY a criterion accepted the deliverable — useful for
	// reviewers auditing over-permissive ACs.
	Check func(ctx context.Context, spec string, delivery []byte) (pass bool, reason string)
}

// toPlanAC converts this DeliveryCriterion to a plan.AcceptanceCriterion
// shell carrying only ID + Description. The actual verification runs
// via Check(); this helper exists so callers (and future integration
// with plan.VerificationDescent) see the same ID/Description strings
// in ledger events and operator banners.
func (dc DeliveryCriterion) toPlanAC() plan.AcceptanceCriterion {
	return plan.AcceptanceCriterion{
		ID:          dc.ID,
		Description: dc.Description,
	}
}

// Errors returned by the verify/settle flow.
var (
	// ErrVerificationFailed wraps the reason from the first AC that
	// tripped. Callers can errors.Is against this sentinel to branch
	// on "deliverable rejected" without parsing the wrapped text.
	ErrVerificationFailed = errors.New("hire: deliverable verification failed")

	// ErrNoSettlementClient is returned when VerifyAndSettle is
	// called on a Hirer whose Settlement field is nil.
	ErrNoSettlementClient = errors.New("hire: settlement client not configured")
)

// VerifyAndSettle runs the descent-style acceptance ladder on a
// hired agent's deliverable and either settles (payment released) or
// disputes (payment withheld with structured evidence) via
// SettlementClient.
//
// See the file-header doc for the criterion model, the two terminal
// paths, and the event emission contract. Returns nil on a clean
// settle; returns an error wrapping the failing criterion's reason
// (and wrapping ErrVerificationFailed) on dispute.
func (h *Hirer) VerifyAndSettle(
	ctx context.Context,
	contractID string,
	agentDID string,
	delivery []byte,
	spec string,
	opts ...VerifyAndSettleOpts,
) error {
	if h.Settlement == nil {
		return ErrNoSettlementClient
	}
	var o VerifyAndSettleOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.MaxDisputeSampleBytes == 0 {
		o.MaxDisputeSampleBytes = 4096
	}

	criteria := h.buildDeliveryCriteria(contractID, spec, delivery)
	criteria = append(criteria, o.Extra...)

	verdicts := make([]CriterionVerdict, 0, len(criteria))
	for _, ac := range criteria {
		pass, reason := ac.Check(ctx, spec, delivery)
		v := CriterionVerdict{
			ID:     ac.ID,
			Pass:   pass,
			Reason: reason,
			Output: reason, // MVP: same string. Future: separate command output.
		}
		verdicts = append(verdicts, v)
		h.emitVerify(contractID, agentDID, v)
		if !pass {
			// Fail-fast: the descent engine will grow richer
			// per-AC classification when VerifyFunc-backed
			// criteria land in plan.VerificationDescent; for now
			// we stop at the first hard failure and dispute.
			return h.disputeAndReturn(ctx, contractID, agentDID, spec, delivery, v, verdicts, o)
		}
	}

	// All criteria passed — settle.
	receipt, err := h.Settlement.Settle(ctx, SettleRequest{
		ContractID: contractID,
		AgentDID:   agentDID,
		PaidUSD:    o.PaidUSD,
		Note:       fmt.Sprintf("all %d criteria passed", len(criteria)),
	})
	if err != nil {
		return fmt.Errorf("hire: settle: %w", err)
	}
	h.emitSettle(contractID, agentDID, receipt, verdicts)
	return nil
}

// disputeAndReturn files a dispute with structured evidence and
// returns a wrapped error. Factored out of VerifyAndSettle so the
// fail path stays small at the call site.
func (h *Hirer) disputeAndReturn(
	ctx context.Context,
	contractID string,
	agentDID string,
	spec string,
	delivery []byte,
	failed CriterionVerdict,
	verdicts []CriterionVerdict,
	o VerifyAndSettleOpts,
) error {
	sample := delivery
	if o.MaxDisputeSampleBytes > 0 && len(sample) > o.MaxDisputeSampleBytes {
		sample = sample[:o.MaxDisputeSampleBytes]
	}
	evidence := DisputeEvidence{
		ContractID:        contractID,
		AgentDID:          agentDID,
		Spec:              spec,
		DeliverySample:    sample,
		FailedCriterionID: failed.ID,
		FailedReason:      failed.Reason,
		Verdicts:          verdicts,
	}
	receipt, derr := h.Settlement.Dispute(ctx, evidence)
	h.emitDispute(contractID, agentDID, failed, verdicts, receipt, derr)
	wrapped := fmt.Errorf("%w: AC %s: %s", ErrVerificationFailed, failed.ID, failed.Reason)
	if derr != nil {
		// Dispute itself failed (network / auth). Surface both.
		return fmt.Errorf("%w; dispute filing error: %v", wrapped, derr)
	}
	return wrapped
}

// buildDeliveryCriteria returns the standard AC set applied to every
// delegated deliverable. See file-header doc for the rationale.
// Callers can extend via VerifyAndSettleOpts.Extra.
func (h *Hirer) buildDeliveryCriteria(contractID, spec string, delivery []byte) []DeliveryCriterion {
	_ = contractID // reserved — will flow into per-AC metadata when plan ACs gain VerifyFunc.
	return []DeliveryCriterion{
		{
			ID:          "delivery-complete",
			Description: "hired agent returned a non-empty deliverable",
			Check: func(_ context.Context, _ string, d []byte) (bool, string) {
				if len(bytes.TrimSpace(d)) == 0 {
					return false, "deliverable is empty (0 non-whitespace bytes)"
				}
				return true, fmt.Sprintf("deliverable is non-empty (%d bytes)", len(d))
			},
		},
		{
			ID:          "delivery-matches-spec",
			Description: "reviewer judges deliverable against hire spec",
			Check: func(ctx context.Context, s string, d []byte) (bool, string) {
				if h.Review != nil {
					return h.Review(ctx, s, d)
				}
				return ReviewDeliverableFallback(s, d)
			},
		},
	}
}

// ReviewDeliverableFallback is the deterministic spec-match check
// used when no LLM review hook is configured on the Hirer. It is
// INTENTIONALLY MINIMAL — it exists so that local dev, CI, and tests
// can exercise the verify/settle path end-to-end without wiring an
// LLM provider, not because keyword overlap is a credible substitute
// for semantic review.
//
// Rules (in order):
//
//  1. Empty spec: pass (nothing to check against). Log the
//     degenerate case for auditor visibility.
//  2. Empty delivery: fail. delivery-complete should have caught
//     this already but we defend in depth.
//  3. Length floor: |delivery| >= 0.5 * |spec|. Catches the
//     "agent returned one-word ack" regression. For specs shorter
//     than 4 bytes this check is skipped (too noisy).
//  4. Keyword overlap: at least 1 non-stopword token from the spec
//     must appear in the delivery (case-insensitive, Unicode-safe
//     tokenization). Catches the "agent returned unrelated text"
//     regression.
//
// Returns (pass, reason). The reason is populated on both success
// and failure so the audit trail records the actual comparison.
func ReviewDeliverableFallback(spec string, delivery []byte) (bool, string) {
	specTrim := strings.TrimSpace(spec)
	if specTrim == "" {
		return true, "fallback review: spec is empty, nothing to compare"
	}
	if len(bytes.TrimSpace(delivery)) == 0 {
		return false, "fallback review: delivery is empty"
	}
	// Length floor — only enforce for non-trivial specs.
	if len(specTrim) >= 4 && float64(len(delivery)) < 0.5*float64(len(specTrim)) {
		return false, fmt.Sprintf(
			"fallback review: delivery too short (%d bytes < 50%% of spec's %d bytes)",
			len(delivery), len(specTrim))
	}
	// Keyword overlap.
	specTokens := tokenize(specTrim)
	deliveryTokens := tokenize(string(delivery))
	if len(specTokens) == 0 {
		// Spec was all stopwords / punctuation. Fall through: we
		// cannot do overlap, so treat as pass-by-degenerate-input.
		return true, "fallback review: spec has no substantive tokens after stopword filter"
	}
	deliverySet := make(map[string]struct{}, len(deliveryTokens))
	for _, t := range deliveryTokens {
		deliverySet[t] = struct{}{}
	}
	matched := 0
	var hits []string
	for _, t := range specTokens {
		if _, ok := deliverySet[t]; ok {
			matched++
			if len(hits) < 5 {
				hits = append(hits, t)
			}
		}
	}
	if matched == 0 {
		return false, fmt.Sprintf(
			"fallback review: zero keyword overlap with spec (tried %d non-stopword tokens)",
			len(specTokens))
	}
	return true, fmt.Sprintf(
		"fallback review: %d/%d spec tokens appear in delivery (sample: %s)",
		matched, len(specTokens), strings.Join(hits, ","))
}

// tokenize returns the lowercased word tokens in s, excluding a
// small set of English stopwords. Unicode-safe: splits on any
// non-letter-or-digit rune so "L'Oréal" becomes ["l","oréal"].
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tok := strings.ToLower(cur.String())
		cur.Reset()
		if _, stop := stopwords[tok]; stop {
			return
		}
		if len(tok) < 2 {
			return
		}
		out = append(out, tok)
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "but": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {},
	"of": {}, "to": {}, "in": {}, "on": {}, "at": {}, "for": {},
	"with": {}, "by": {}, "from": {}, "as": {}, "it": {}, "this": {},
	"that": {}, "these": {}, "those": {}, "you": {}, "your": {},
	"we": {}, "our": {}, "they": {}, "their": {}, "i": {}, "my": {},
	"not": {}, "no": {}, "yes": {}, "do": {}, "does": {}, "did": {},
	"have": {}, "has": {}, "had": {}, "will": {}, "would": {},
	"can": {}, "could": {}, "should": {}, "may": {}, "might": {},
}

// ---------------------------------------------------------------------------
// Event emission helpers
// ---------------------------------------------------------------------------

// emitVerify fires one stoke.delegation.verify event per criterion.
// When the Hirer has no Emitter configured, this is a no-op.
func (h *Hirer) emitVerify(contractID, agentDID string, v CriterionVerdict) {
	if h.Emitter == nil || !h.Emitter.Enabled() {
		return
	}
	h.Emitter.EmitSystem("stoke.delegation.verify", map[string]any{
		"contract_id":  contractID,
		"agent_did":    agentDID,
		"criterion_id": v.ID,
		"pass":         v.Pass,
		"reason":       v.Reason,
	})
}

// emitSettle fires the terminal stoke.delegation.settle event with
// the full verdict audit trail. No-op when Emitter is nil.
func (h *Hirer) emitSettle(contractID, agentDID string, r SettleReceipt, verdicts []CriterionVerdict) {
	if h.Emitter == nil || !h.Emitter.Enabled() {
		return
	}
	h.Emitter.EmitSystem("stoke.delegation.settle", map[string]any{
		"contract_id":   contractID,
		"agent_did":     agentDID,
		"settlement_id": r.SettlementID,
		"note":          r.Note,
		"verdicts":      verdictsToMaps(verdicts),
	})
}

// emitDispute fires the terminal stoke.delegation.dispute event. The
// dispute filing itself may have errored; that error is included in
// the event body so operators can see TrustPlane-side trouble.
func (h *Hirer) emitDispute(
	contractID, agentDID string,
	failed CriterionVerdict,
	verdicts []CriterionVerdict,
	r DisputeReceipt,
	fileErr error,
) {
	if h.Emitter == nil || !h.Emitter.Enabled() {
		return
	}
	body := map[string]any{
		"contract_id":       contractID,
		"agent_did":         agentDID,
		"failed_criterion":  failed.ID,
		"failed_reason":     failed.Reason,
		"dispute_id":        r.DisputeID,
		"note":              r.Note,
		"verdicts":          verdictsToMaps(verdicts),
	}
	if fileErr != nil {
		body["filing_error"] = fileErr.Error()
	}
	h.Emitter.EmitSystem("stoke.delegation.dispute", body)
}

// verdictsToMaps shapes the audit trail for streamjson / JSON-
// serializable emission. Mirrors the DescentResult.ToMap() idea
// in plan.VerificationDescent.
func verdictsToMaps(vs []CriterionVerdict) []map[string]any {
	out := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, map[string]any{
			"id":     v.ID,
			"pass":   v.Pass,
			"reason": v.Reason,
			"output": v.Output,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Compile-time wiring: Hirer additive fields
// ---------------------------------------------------------------------------
//
// The VerifyAndSettle flow requires three hooks on the Hirer that
// are nilable additive fields on Engine (hire.go):
//
//   Settlement SettlementClient   — TrustPlane settle/dispute interface
//   Review     ReviewFunc         — optional LLM review hook
//   Emitter    *streamjson.Emitter — optional event emitter
//
// Zero-value Engine continues to work for the legacy
// discover→hire→complete flow.
