// Package hire implements STOKE-012: external agent hire via
// TrustPlane discovery. On a local skill/tool cache-miss,
// Stoke queries the TrustPlane marketplace for candidate
// agents offering the requested capability, ranks them by
// (reputation × cost-efficiency × latency), and either
// auto-hires the top candidate (if local policy allows) or
// escalates via HITL for high-value / low-reputation cases.
//
// From the LLM's perspective the difference between calling a
// local tool and invoking a hired remote agent is invisible —
// the gateway wraps the hire path as a tool-result-shaped
// return so the agent loop doesn't need branching logic per
// hire vs. local.
//
// Scope of this file:
//
//   - Candidate struct + scoring
//   - Policy gate config (auto-hire vs HITL thresholds)
//   - HireDecision flow (discovery → rank → gate → hire)
//   - Hire-receipt + work-receipt emission (to the ledger
//     through an injected writer interface — package-local
//     types keep us free of direct internal/ledger/ imports)
//   - Reputation feedback on completion
//
// The LLM-facing gateway shim lives in internal/mcp/ (or
// wherever Stoke surfaces tool results); this package is
// backend-only so it's independently testable.
package hire

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ericmacdougall/stoke/internal/trustplane"
)

// ErrNoCandidates is returned when TrustPlane discovery returns
// zero candidates for the requested capability.
var ErrNoCandidates = errors.New("hire: no candidates from TrustPlane discovery")

// ErrPolicyBlocked is returned when the policy gate rejects
// every candidate (e.g. all have reputation below the floor,
// or estimated cost exceeds the budget).
var ErrPolicyBlocked = errors.New("hire: all candidates blocked by policy")

// ErrHITLRejected is returned when HITL escalation results in
// a rejection.
var ErrHITLRejected = errors.New("hire: HITL rejected the hire")

// Candidate is one TrustPlane-sourced agent offering the
// requested capability. Produced by the discovery layer and
// consumed by the ranker.
type Candidate struct {
	AgentDID    string
	Capability  string
	Reputation  float64 // [0,1]
	EstimatedCostUSD float64
	EstimatedLatencyMs int
	// Annotations is opaque metadata the TrustPlane
	// marketplace attaches (badges, certifications, etc.).
	Annotations map[string]string
}

// Score returns the composite ranking score. Higher is better.
//
//	score = reputation × (1 / (1 + cost_usd)) × (1 / (1 + latency_ms / 1000))
//
// Each factor is normalized so a single dimension can't dominate
// — a cheap-but-unreliable agent doesn't outrank a reliable
// one unless its reputation ratio compensates.
func (c Candidate) Score() float64 {
	costFactor := 1.0 / (1.0 + c.EstimatedCostUSD)
	latencyFactor := 1.0 / (1.0 + float64(c.EstimatedLatencyMs)/1000.0)
	return c.Reputation * costFactor * latencyFactor
}

// Policy configures the gate that decides auto-hire vs HITL
// vs deny.
type Policy struct {
	// MinReputation: candidates below this floor are filtered
	// out entirely. Default 0.5.
	MinReputation float64

	// MaxEstimatedCostUSD: candidates above this ceiling are
	// filtered out. 0 means "unbounded".
	MaxEstimatedCostUSD float64

	// AutoHireReputationFloor: auto-hire the top candidate
	// only when its reputation >= this value. Below, escalate
	// to HITL. Default 0.75.
	AutoHireReputationFloor float64

	// AutoHireCostCeilingUSD: auto-hire only when the top
	// candidate's estimated cost is <= this. Above, escalate.
	// 0 means "no auto-hire based on cost".
	AutoHireCostCeilingUSD float64
}

// DefaultPolicy is the conservative defaults: filter below
// rep 0.5 + below $unlimited; auto-hire only when rep >= 0.75
// and cost <= $1.
var DefaultPolicy = Policy{
	MinReputation:           0.5,
	MaxEstimatedCostUSD:     0, // unbounded
	AutoHireReputationFloor: 0.75,
	AutoHireCostCeilingUSD:  1.0,
}

// ReceiptWriter writes hire + work receipts to the ledger.
// Provided as an interface so this package doesn't import
// internal/ledger/ directly.
type ReceiptWriter interface {
	WriteHireReceipt(ctx context.Context, r HireReceipt) error
	WriteWorkReceipt(ctx context.Context, r WorkReceipt) error
}

// HireReceipt records a successful hire event.
type HireReceipt struct {
	AgentDID   string
	Capability string
	PolicyRef  string // e.g. "hire-from-trustplane"
	// HIttedHITL=true if the hire required HITL approval
	// before commiting.
	HittedHITL bool
	EstimatedCostUSD float64
}

// WorkReceipt records the outcome of a hired agent's work.
// Emitted on completion; feeds back into TrustPlane reputation
// via RecordReputation.
type WorkReceipt struct {
	AgentDID string
	Outcome  string // "success" | "failure" | "partial"
	Note     string
	Tokens   int
	DurationMs int
}

// Discoverer queries TrustPlane for candidates. Separated as
// an interface so tests can inject mock candidates without
// going through the trustplane.Client surface.
type Discoverer interface {
	Discover(ctx context.Context, capability string) ([]Candidate, error)
}

// trustplaneDiscoverer uses the TrustPlane Client's discovery
// call. This is the production implementation; test scaffolds
// use fakeDiscoverer.
type trustplaneDiscoverer struct {
	tp trustplane.Client
}

// NewDiscoverer wraps a trustplane.Client as a Discoverer.
// Currently this is a thin adapter; when the TrustPlane
// gateway exposes a richer discovery shape (filters by
// locality, SLA tier, etc.) this is where that translation
// lives. The RealClient implementation speaks to the gateway
// over HTTP against the vendored OpenAPI spec — no Go SDK.
func NewDiscoverer(tp trustplane.Client) Discoverer {
	return &trustplaneDiscoverer{tp: tp}
}

// Discover calls LookupReputation for a synthetic set of
// well-known marketplace agents and shapes the results as
// Candidates. Real implementation will swap in a TrustPlane
// discovery endpoint once the gateway's OpenAPI spec exposes
// one (hand-written HTTP call, not a Go SDK).
func (d *trustplaneDiscoverer) Discover(ctx context.Context, capability string) ([]Candidate, error) {
	// The current TrustPlane stub doesn't expose a discovery
	// list, so this implementation returns zero candidates —
	// production wiring replaces this with a real Discovery
	// call. Tests use fakeDiscoverer directly.
	_ = ctx
	_ = capability
	return nil, nil
}

// HITLBroker escalates low-reputation or high-value hires to
// the human operator. Returns true on approved. Implementations
// typically proxy through trustplane.Client.RequestHITL.
type HITLBroker interface {
	ApproveHire(ctx context.Context, candidate Candidate) (bool, error)
}

// tpHITL is the default HITLBroker that routes approvals
// through the TrustPlane HITL service.
type tpHITL struct {
	tp trustplane.Client
}

// NewHITLBroker wraps a trustplane.Client.
func NewHITLBroker(tp trustplane.Client) HITLBroker {
	return &tpHITL{tp: tp}
}

func (h *tpHITL) ApproveHire(ctx context.Context, c Candidate) (bool, error) {
	resp, err := h.tp.RequestHITL(ctx, trustplane.HITLRequest{
		AgentDID: c.AgentDID,
		Question: fmt.Sprintf("Hire %s for %s at $%.2f, rep %.2f?", c.AgentDID, c.Capability, c.EstimatedCostUSD, c.Reputation),
	})
	if err != nil {
		return false, err
	}
	return resp.Decision == "approved", nil
}

// Engine orchestrates discovery → rank → gate → hire →
// receipt. Constructed once per Stoke process; method calls
// are safe from any goroutine (no mutable state on the
// engine itself).
type Engine struct {
	Discoverer Discoverer
	HITL       HITLBroker
	TP         trustplane.Client
	Receipts   ReceiptWriter
	Policy     Policy
}

// Hire runs the full hire flow for a capability. Returns the
// Candidate that was hired and the emitted HireReceipt.
func (e *Engine) Hire(ctx context.Context, capability string, policyRef string) (Candidate, HireReceipt, error) {
	cands, err := e.Discoverer.Discover(ctx, capability)
	if err != nil {
		return Candidate{}, HireReceipt{}, fmt.Errorf("hire: discover: %w", err)
	}
	if len(cands) == 0 {
		return Candidate{}, HireReceipt{}, ErrNoCandidates
	}
	cands = e.filter(cands)
	if len(cands) == 0 {
		return Candidate{}, HireReceipt{}, ErrPolicyBlocked
	}
	e.rank(cands)
	top := cands[0]

	receipt := HireReceipt{
		AgentDID:         top.AgentDID,
		Capability:       top.Capability,
		PolicyRef:        policyRef,
		EstimatedCostUSD: top.EstimatedCostUSD,
	}

	if e.requiresHITL(top) {
		if e.HITL == nil {
			return Candidate{}, HireReceipt{}, fmt.Errorf("hire: HITL broker not configured but candidate requires escalation")
		}
		ok, err := e.HITL.ApproveHire(ctx, top)
		if err != nil {
			return Candidate{}, HireReceipt{}, fmt.Errorf("hire: HITL: %w", err)
		}
		if !ok {
			return Candidate{}, HireReceipt{}, ErrHITLRejected
		}
		receipt.HittedHITL = true
	}

	if e.Receipts != nil {
		if err := e.Receipts.WriteHireReceipt(ctx, receipt); err != nil {
			return Candidate{}, HireReceipt{}, fmt.Errorf("hire: receipt write: %w", err)
		}
	}
	return top, receipt, nil
}

// Complete is called after a hired agent finishes its work.
// Emits a WorkReceipt to the ledger and posts reputation
// feedback to TrustPlane.
func (e *Engine) Complete(ctx context.Context, agentDID string, outcome string, note string, tokens, durationMs int) error {
	wr := WorkReceipt{
		AgentDID:   agentDID,
		Outcome:    outcome,
		Note:       note,
		Tokens:     tokens,
		DurationMs: durationMs,
	}
	if e.Receipts != nil {
		if err := e.Receipts.WriteWorkReceipt(ctx, wr); err != nil {
			return fmt.Errorf("hire: work receipt: %w", err)
		}
	}
	if e.TP != nil {
		delta := 0.0
		switch outcome {
		case "success":
			delta = 0.05
		case "failure":
			delta = -0.1
		case "partial":
			delta = 0.0
		}
		if err := e.TP.RecordReputation(ctx, trustplane.ReputationEntry{
			AgentDID:    agentDID,
			Outcome:     outcome,
			RatingDelta: delta,
			Note:        note,
		}); err != nil {
			return fmt.Errorf("hire: record reputation: %w", err)
		}
	}
	return nil
}

// filter drops candidates below the Policy floor.
func (e *Engine) filter(cands []Candidate) []Candidate {
	p := e.Policy
	if p.MinReputation == 0 && p.MaxEstimatedCostUSD == 0 {
		p = DefaultPolicy
	}
	out := cands[:0]
	for _, c := range cands {
		if c.Reputation < p.MinReputation {
			continue
		}
		if p.MaxEstimatedCostUSD > 0 && c.EstimatedCostUSD > p.MaxEstimatedCostUSD {
			continue
		}
		out = append(out, c)
	}
	return out
}

// rank sorts candidates by Score descending (best first).
func (e *Engine) rank(cands []Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].Score() > cands[j].Score()
	})
}

// requiresHITL reports whether the top candidate is below the
// auto-hire bar, triggering an HITL escalation.
func (e *Engine) requiresHITL(c Candidate) bool {
	p := e.Policy
	if p.AutoHireReputationFloor == 0 {
		p.AutoHireReputationFloor = DefaultPolicy.AutoHireReputationFloor
	}
	if p.AutoHireCostCeilingUSD == 0 {
		p.AutoHireCostCeilingUSD = DefaultPolicy.AutoHireCostCeilingUSD
	}
	if c.Reputation < p.AutoHireReputationFloor {
		return true
	}
	if c.EstimatedCostUSD > p.AutoHireCostCeilingUSD {
		return true
	}
	return false
}
