package hire

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/RelayOne/r1-agent/internal/trustplane"
)

type fakeDiscoverer struct {
	cands []Candidate
	err   error
}

func (f *fakeDiscoverer) Discover(_ context.Context, _ string) ([]Candidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cands, nil
}

type fakeHITL struct {
	approve bool
	called  int
	err     error
}

func (f *fakeHITL) ApproveHire(_ context.Context, _ Candidate) (bool, error) {
	f.called++
	if f.err != nil {
		return false, f.err
	}
	return f.approve, nil
}

type fakeReceipts struct {
	mu     sync.Mutex
	hires  []HireReceipt
	works  []WorkReceipt
}

func (f *fakeReceipts) WriteHireReceipt(_ context.Context, r HireReceipt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hires = append(f.hires, r)
	return nil
}

func (f *fakeReceipts) WriteWorkReceipt(_ context.Context, r WorkReceipt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.works = append(f.works, r)
	return nil
}

func TestCandidate_Score_ReputationDominatesWhenEqual(t *testing.T) {
	high := Candidate{Reputation: 0.9, EstimatedCostUSD: 1.0, EstimatedLatencyMs: 200}
	low := Candidate{Reputation: 0.3, EstimatedCostUSD: 1.0, EstimatedLatencyMs: 200}
	if high.Score() <= low.Score() {
		t.Errorf("higher reputation should score higher when cost+latency equal")
	}
}

func TestCandidate_Score_CostMatters(t *testing.T) {
	cheap := Candidate{Reputation: 0.7, EstimatedCostUSD: 0.1, EstimatedLatencyMs: 200}
	expensive := Candidate{Reputation: 0.7, EstimatedCostUSD: 10.0, EstimatedLatencyMs: 200}
	if cheap.Score() <= expensive.Score() {
		t.Error("cheaper candidate should score higher at equal reputation")
	}
}

func TestHire_NoCandidates(t *testing.T) {
	e := &Engine{Discoverer: &fakeDiscoverer{}}
	_, _, err := e.Hire(context.Background(), "translate", "bundle")
	if !errors.Is(err, ErrNoCandidates) {
		t.Errorf("want ErrNoCandidates, got %v", err)
	}
}

func TestHire_DiscoveryError(t *testing.T) {
	e := &Engine{Discoverer: &fakeDiscoverer{err: errors.New("marketplace down")}}
	_, _, err := e.Hire(context.Background(), "translate", "bundle")
	if err == nil {
		t.Error("expected error on discovery failure")
	}
}

func TestHire_AutoHireTopCandidate(t *testing.T) {
	e := &Engine{
		Discoverer: &fakeDiscoverer{cands: []Candidate{
			{AgentDID: "a", Capability: "translate", Reputation: 0.8, EstimatedCostUSD: 0.5, EstimatedLatencyMs: 100},
			{AgentDID: "b", Capability: "translate", Reputation: 0.9, EstimatedCostUSD: 0.3, EstimatedLatencyMs: 80},
		}},
		Policy:   DefaultPolicy,
		Receipts: &fakeReceipts{},
	}
	top, receipt, err := e.Hire(context.Background(), "translate", "bundle")
	if err != nil {
		t.Fatalf("Hire: %v", err)
	}
	if top.AgentDID != "b" {
		t.Errorf("got %q want b (higher score)", top.AgentDID)
	}
	if receipt.HittedHITL {
		t.Error("high-rep + low-cost candidate should not escalate")
	}
}

func TestHire_LowReputationEscalatesHITL(t *testing.T) {
	hitl := &fakeHITL{approve: true}
	e := &Engine{
		Discoverer: &fakeDiscoverer{cands: []Candidate{
			{AgentDID: "rookie", Capability: "translate", Reputation: 0.6, EstimatedCostUSD: 0.5, EstimatedLatencyMs: 100},
		}},
		HITL:     hitl,
		Policy:   DefaultPolicy,
		Receipts: &fakeReceipts{},
	}
	_, receipt, err := e.Hire(context.Background(), "translate", "bundle")
	if err != nil {
		t.Fatalf("Hire: %v", err)
	}
	if !receipt.HittedHITL {
		t.Error("low-rep candidate should escalate")
	}
	if hitl.called != 1 {
		t.Errorf("HITL called %d times, want 1", hitl.called)
	}
}

func TestHire_HITLRejected(t *testing.T) {
	hitl := &fakeHITL{approve: false}
	e := &Engine{
		Discoverer: &fakeDiscoverer{cands: []Candidate{
			{AgentDID: "x", Capability: "c", Reputation: 0.6, EstimatedCostUSD: 0.5},
		}},
		HITL:   hitl,
		Policy: DefaultPolicy,
	}
	_, _, err := e.Hire(context.Background(), "c", "bundle")
	if !errors.Is(err, ErrHITLRejected) {
		t.Errorf("want ErrHITLRejected, got %v", err)
	}
}

func TestHire_PolicyBlocksAll(t *testing.T) {
	e := &Engine{
		Discoverer: &fakeDiscoverer{cands: []Candidate{
			{AgentDID: "lowrep", Reputation: 0.1, EstimatedCostUSD: 0.1},
		}},
		Policy: Policy{MinReputation: 0.5},
	}
	_, _, err := e.Hire(context.Background(), "c", "bundle")
	if !errors.Is(err, ErrPolicyBlocked) {
		t.Errorf("want ErrPolicyBlocked, got %v", err)
	}
}

func TestHire_CostCeilingFilters(t *testing.T) {
	e := &Engine{
		Discoverer: &fakeDiscoverer{cands: []Candidate{
			{AgentDID: "expensive", Reputation: 0.9, EstimatedCostUSD: 100},
			{AgentDID: "cheap", Reputation: 0.9, EstimatedCostUSD: 0.5},
		}},
		Policy: Policy{
			MinReputation:       0.5,
			MaxEstimatedCostUSD: 10, // expensive filtered
			AutoHireReputationFloor: 0.75,
			AutoHireCostCeilingUSD:  1.0,
		},
	}
	top, _, err := e.Hire(context.Background(), "c", "bundle")
	if err != nil {
		t.Fatalf("Hire: %v", err)
	}
	if top.AgentDID != "cheap" {
		t.Errorf("got %q want cheap (expensive filtered by MaxCost)", top.AgentDID)
	}
}

func TestComplete_WritesReceiptAndRecordsReputation(t *testing.T) {
	tp := trustplane.NewStubClient()
	receipts := &fakeReceipts{}
	e := &Engine{
		Receipts: receipts,
		TP:       tp,
	}
	if err := e.Complete(context.Background(), "did:tp:x", "success", "done", 1000, 2500); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	receipts.mu.Lock()
	defer receipts.mu.Unlock()
	if len(receipts.works) != 1 {
		t.Errorf("work receipts=%d want 1", len(receipts.works))
	}
	if receipts.works[0].Outcome != "success" {
		t.Errorf("outcome=%q", receipts.works[0].Outcome)
	}
	// Reputation should have been recorded.
	rep, _ := tp.LookupReputation(context.Background(), "did:tp:x")
	if rep.SuccessfulHires != 1 {
		t.Errorf("successful hires=%d want 1", rep.SuccessfulHires)
	}
}
