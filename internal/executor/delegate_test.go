package executor

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/delegation"
	"github.com/RelayOne/r1/internal/hire"
	"github.com/RelayOne/r1/internal/trustplane"
)

// ---- test doubles ----------------------------------------------------------

// stubDiscoverer implements hire.Discoverer with a fixed list.
type stubDiscoverer struct {
	cands []hire.Candidate
	err   error
}

func (s *stubDiscoverer) Discover(_ context.Context, _ string) ([]hire.Candidate, error) {
	return s.cands, s.err
}

// stubSettlement implements hire.SettlementClient. It records which
// terminal was called so tests can assert settle vs dispute.
type stubSettlement struct {
	mu           sync.Mutex
	settleCalls  []hire.SettleRequest
	disputeCalls []hire.DisputeEvidence
}

func (s *stubSettlement) Settle(_ context.Context, req hire.SettleRequest) (hire.SettleReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settleCalls = append(s.settleCalls, req)
	return hire.SettleReceipt{SettlementID: "settle-" + req.ContractID, Note: req.Note}, nil
}

func (s *stubSettlement) Dispute(_ context.Context, ev hire.DisputeEvidence) (hire.DisputeReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disputeCalls = append(s.disputeCalls, ev)
	return hire.DisputeReceipt{DisputeID: "dispute-" + ev.ContractID, Note: "filed"}, nil
}

// stubSubmitter records task submissions.
type stubSubmitter struct {
	mu     sync.Mutex
	calls  []stubSubmitterCall
	taskID string
	err    error
}

type stubSubmitterCall struct {
	Delegation trustplane.Delegation
	Spec       []byte
}

func (s *stubSubmitter) SubmitTask(_ context.Context, d trustplane.Delegation, spec []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubSubmitterCall{Delegation: d, Spec: append([]byte(nil), spec...)})
	if s.err != nil {
		return "", s.err
	}
	id := s.taskID
	if id == "" {
		id = "task-stub"
	}
	return id, nil
}

// stubDelivery implements DeliveryWaiter. Calling Await blocks on
// ctx.Done() when block==true; otherwise returns bytes.
type stubDelivery struct {
	bytes []byte
	err   error
	block bool
}

func (s *stubDelivery) Await(ctx context.Context, _ delegation.TaskHandle) ([]byte, error) {
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.bytes, nil
}

// revokeRecordingTP wraps a trustplane.StubClient so tests can
// observe RevokeDelegation calls (the stub itself tracks revocations
// internally but keeps the counter private).
type revokeRecordingTP struct {
	trustplane.Client
	mu       sync.Mutex
	revoked  []string
}

func (r *revokeRecordingTP) RevokeDelegation(ctx context.Context, id string) error {
	r.mu.Lock()
	r.revoked = append(r.revoked, id)
	r.mu.Unlock()
	return r.Client.RevokeDelegation(ctx, id)
}

func (r *revokeRecordingTP) revokeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.revoked)
}

// ---- builder ---------------------------------------------------------------

// buildDelegateExecutor wires a DelegateExecutor with all seams
// populated by stubs. Individual tests override the slices they
// care about before Execute.
func buildDelegateExecutor(
	t *testing.T,
	cands []hire.Candidate,
	settlement hire.SettlementClient,
	submitter DelegationSubmitter,
	delivery DeliveryWaiter,
) (*DelegateExecutor, *revokeRecordingTP) {
	t.Helper()
	tp := &revokeRecordingTP{Client: trustplane.NewStubClient()}
	hirer := &hire.Engine{
		Discoverer: &stubDiscoverer{cands: cands},
		TP:         tp,
		Policy:     hire.DefaultPolicy,
		Settlement: settlement,
	}
	manager := delegation.NewManager(tp)
	return &DelegateExecutor{
		Hirer:      hirer,
		Delegator:  manager,
		TP:         tp,
		Submitter:  submitter,
		Delivery:   delivery,
		FromDID:    "did:tp:operator",
		BundleName: "hire-from-trustplane",
	}, tp
}

// basePlan returns a Plan usable in every test; individual tests
// override fields as needed.
func basePlan() Plan {
	return Plan{
		Task: Task{
			ID:          "t1",
			Description: "translate README",
			Spec:        "please translate the README to Japanese and preserve formatting",
			TaskType:    TaskDelegate,
		},
	}
}

// ---- tests -----------------------------------------------------------------

func TestDelegateExecutor_Success(t *testing.T) {
	cands := []hire.Candidate{
		{AgentDID: "did:tp:agent-a", Capability: "translate README", Reputation: 0.9, EstimatedCostUSD: 0.3, EstimatedLatencyMs: 80},
	}
	set := &stubSettlement{}
	sub := &stubSubmitter{taskID: "task-abc"}
	deliv := &stubDelivery{bytes: []byte("translated README preserving formatting")}

	e, tp := buildDelegateExecutor(t, cands, set, sub, deliv)

	d, err := e.Execute(context.Background(), basePlan(), EffortStandard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	dd, ok := d.(DelegationDeliverable)
	if !ok {
		t.Fatalf("Execute returned %T, want DelegationDeliverable", d)
	}
	if dd.AgentID != "did:tp:agent-a" {
		t.Errorf("AgentID = %q, want did:tp:agent-a", dd.AgentID)
	}
	if dd.ContractID == "" {
		t.Error("ContractID should be non-empty")
	}
	if dd.Settlement.SettlementID == "" {
		t.Error("Settlement.SettlementID should be non-empty")
	}
	if len(sub.calls) != 1 {
		t.Fatalf("Submitter.SubmitTask calls = %d, want 1", len(sub.calls))
	}
	if len(set.settleCalls) != 1 {
		t.Fatalf("Settle calls = %d, want 1", len(set.settleCalls))
	}
	if len(set.disputeCalls) != 0 {
		t.Errorf("Dispute calls = %d, want 0", len(set.disputeCalls))
	}
	if tp.revokeCount() != 0 {
		t.Errorf("unexpected revoke on success path: %d", tp.revokeCount())
	}
	// BuildCriteria returns a single settled AC.
	crit := e.BuildCriteria(Task{}, dd)
	if len(crit) != 1 {
		t.Fatalf("BuildCriteria returned %d ACs, want 1", len(crit))
	}
	if crit[0].ID != "DELEGATE-SETTLED" {
		t.Errorf("AC ID = %q, want DELEGATE-SETTLED", crit[0].ID)
	}
	pass, reason := crit[0].VerifyFunc(context.Background())
	if !pass {
		t.Errorf("DELEGATE-SETTLED should pass: %s", reason)
	}
	// BuildRepairFunc / BuildEnvFixFunc return nil.
	if e.BuildRepairFunc(basePlan()) != nil {
		t.Error("BuildRepairFunc should be nil for delegate executor")
	}
	if e.BuildEnvFixFunc() != nil {
		t.Error("BuildEnvFixFunc should be nil for delegate executor")
	}
}

func TestDelegateExecutor_RevocationMidTask(t *testing.T) {
	cands := []hire.Candidate{
		{AgentDID: "did:tp:agent-b", Capability: "translate README", Reputation: 0.9, EstimatedCostUSD: 0.3, EstimatedLatencyMs: 80},
	}
	set := &stubSettlement{}
	sub := &stubSubmitter{taskID: "task-b"}
	// Delivery blocks on ctx.Done().
	deliv := &stubDelivery{block: true}

	e, tp := buildDelegateExecutor(t, cands, set, sub, deliv)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after Execute starts Await.
	done := make(chan struct{})
	go func() {
		cancel()
		close(done)
	}()

	_, err := e.Execute(ctx, basePlan(), EffortStandard)
	<-done
	if err == nil {
		t.Fatal("Execute should error on ctx cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err %v should wrap context.Canceled", err)
	}
	if got := tp.revokeCount(); got != 1 {
		t.Errorf("Delegator.Revoke calls = %d, want 1", got)
	}
	if len(set.settleCalls) != 0 {
		t.Errorf("Settle calls = %d, want 0 on cancel", len(set.settleCalls))
	}
}

func TestDelegateExecutor_HireFailure(t *testing.T) {
	// No candidates — Hirer.Hire returns ErrNoCandidates.
	set := &stubSettlement{}
	sub := &stubSubmitter{}
	deliv := &stubDelivery{}

	e, tp := buildDelegateExecutor(t, nil, set, sub, deliv)

	_, err := e.Execute(context.Background(), basePlan(), EffortStandard)
	if err == nil {
		t.Fatal("Execute should fail when no candidates")
	}
	if !errors.Is(err, hire.ErrNoCandidates) {
		t.Errorf("err %v should wrap hire.ErrNoCandidates", err)
	}
	if len(sub.calls) != 0 {
		t.Errorf("Submitter should not be called on hire failure: %d calls", len(sub.calls))
	}
	if tp.revokeCount() != 0 {
		t.Errorf("Revoke should not be called on hire failure: %d", tp.revokeCount())
	}
}

func TestDelegateExecutor_SettleDispute(t *testing.T) {
	cands := []hire.Candidate{
		{AgentDID: "did:tp:agent-c", Capability: "translate README", Reputation: 0.9, EstimatedCostUSD: 0.3, EstimatedLatencyMs: 80},
	}
	set := &stubSettlement{}
	sub := &stubSubmitter{taskID: "task-c"}
	// Empty delivery bytes → delivery-complete AC fails → dispute.
	deliv := &stubDelivery{bytes: []byte("")}

	e, _ := buildDelegateExecutor(t, cands, set, sub, deliv)

	_, err := e.Execute(context.Background(), basePlan(), EffortStandard)
	if err == nil {
		t.Fatal("Execute should error when delivery fails verification")
	}
	if !errors.Is(err, hire.ErrVerificationFailed) {
		t.Errorf("err %v should wrap hire.ErrVerificationFailed", err)
	}
	if len(set.disputeCalls) != 1 {
		t.Fatalf("Dispute calls = %d, want 1", len(set.disputeCalls))
	}
	if len(set.settleCalls) != 0 {
		t.Errorf("Settle calls = %d, want 0 on dispute", len(set.settleCalls))
	}
	ev := set.disputeCalls[0]
	if ev.AgentDID != "did:tp:agent-c" {
		t.Errorf("dispute AgentDID = %q, want did:tp:agent-c", ev.AgentDID)
	}
	if ev.FailedCriterionID == "" {
		t.Error("dispute should name a failed criterion ID")
	}
}

func TestDelegateExecutor_NilSeams(t *testing.T) {
	// Each nil field should surface a descriptive error rather than
	// crash. Exercises the defensive checks at Execute entry.
	e := &DelegateExecutor{}
	_, err := e.Execute(context.Background(), basePlan(), EffortStandard)
	if err == nil {
		t.Fatal("Execute should error when Hirer is nil")
	}
}

func TestDelegateExecutor_TaskType(t *testing.T) {
	e := &DelegateExecutor{}
	if e.TaskType() != TaskDelegate {
		t.Errorf("TaskType = %v, want TaskDelegate", e.TaskType())
	}
}

func TestDelegationDeliverable_SummaryAndSize(t *testing.T) {
	dd := DelegationDeliverable{
		ContractID: "del-1",
		AgentID:    "did:tp:x",
		Settlement: hire.SettleReceipt{SettlementID: "settle-del-1"},
	}
	if dd.Summary() == "" {
		t.Error("Summary should not be empty")
	}
	if dd.Size() != len("settle-del-1") {
		t.Errorf("Size = %d, want %d", dd.Size(), len("settle-del-1"))
	}
}
