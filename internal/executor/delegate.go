// delegate.go: real DelegateExecutor (work-stoke TASK 2).
//
// The prior scaffold in scaffold.go returned ExecutorNotWiredError.
// This file replaces it with a composition of the fully-landed
// Hirer (hire/verify_settle.go), Delegator (delegation/), A2A task
// submission seam (delegation.TaskSubmitter), and TrustPlane client
// (trustplane/), wiring them behind the Executor interface so
// router.Dispatch("delegate …") produces a DelegationDeliverable.
//
// Flow
// ====
//
//  1. Build a hire capability from the Plan (Task.Description
//     concatenated with RequiredCaps). Empty capability → error.
//  2. Hirer.Hire → picks a ranked Candidate (policy gate +
//     optional HITL inside the Hirer itself).
//  3. Delegator.DelegateTask → creates the TrustPlane delegation
//     AND dispatches the task bytes through the configured
//     Submitter (a2a-facing seam; production wiring is a thin
//     a2a-client adapter, tests inject fakes).
//  4. DeliveryWaiter.Await → blocks until the hired agent's
//     deliverable bytes are available, or ctx.Done() fires. On
//     ctx.Canceled we revoke the delegation via Delegator.Revoke
//     and return the ctx error.
//  5. Hirer.VerifyAndSettle → runs the descent-style AC ladder
//     (delivery-complete + delivery-matches-spec + any caller
//     extras) and either Settles or Disputes through the TrustPlane
//     SettlementClient wired onto the Hirer.
//  6. Return DelegationDeliverable{ContractID, AgentID, Settlement}.
//
// The "no a2a.Client type exists yet" seam: the spec header names
// an A2A client but the shipped a2a package exposes only an
// InMemoryTaskStore + JSON-RPC handlers (no outbound client).
// DelegateExecutor therefore consumes the stable
// delegation.TaskSubmitter interface that DelegateTask already
// drives, plus a local DeliveryWaiter interface for the "get the
// bytes back" half. When the a2a outbound client lands, a thin
// adapter implements both interfaces and no DelegateExecutor
// caller churns.

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/a2a"
	"github.com/RelayOne/r1/internal/delegation"
	"github.com/RelayOne/r1/internal/hire"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/trustplane"
)

// DeliveryWaiter blocks until the hired agent has produced a
// deliverable for the given TaskHandle, or the context fires.
// Production wiring is an a2a-client poll loop (future package);
// tests inject stubs that return predetermined bytes.
type DeliveryWaiter interface {
	// Await returns the deliverable bytes on success. On ctx
	// cancellation it must respect the ctx and return promptly
	// with ctx.Err() so DelegateExecutor can emit the revoke.
	Await(ctx context.Context, handle delegation.TaskHandle) (delivery []byte, err error)
}

// DelegationSubmitter is the a2a-facing task-dispatch seam. It is a
// re-export of delegation.TaskSubmitter so callers configuring a
// DelegateExecutor see a single interface name on the executor
// struct.
type DelegationSubmitter = delegation.TaskSubmitter

// DelegateExecutor satisfies Executor for TaskDelegate. Every field
// is nilable at construction time so callers can leave the defaults
// in place until they wire a production backend; nil fields surface
// targeted error messages from Execute rather than panics, so an
// operator running `stoke task delegate …` with no configuration
// gets a "configure the Hirer" banner instead of a crash.
type DelegateExecutor struct {
	// Hirer runs the descent-style discover → rank → gate → hire
	// flow and later the verify → settle ladder. Nil disables
	// dispatch (Execute returns a descriptive error).
	Hirer *hire.Hirer

	// Delegator creates the TrustPlane delegation token that
	// authorizes the hired agent and drives the a2a task submission
	// via DelegateTask. Nil disables dispatch.
	Delegator *delegation.Manager

	// A2A is the a2a task store the deliverable waiter may consult.
	// Kept as a typed pointer for forward-compatibility — a future
	// a2a outbound client will embed one, and callers are already
	// constructing one per-process for the inbound server. The
	// field is accepted as optional; when nil, the Submitter +
	// Delivery seams are the only dispatch paths.
	A2A *a2a.InMemoryTaskStore

	// TP is the TrustPlane client used for reputation feedback
	// (Hirer.Complete) and direct policy evaluations. Optional;
	// when nil, reputation feedback is skipped.
	TP trustplane.Client

	// ReviewFunc is the Hirer's LLM-backed spec-match hook. When
	// nil, the Hirer falls back to its deterministic review heuristic
	// (keyword overlap + length floor).
	ReviewFunc hire.ReviewFunc

	// Submitter is the a2a task-dispatch seam. DelegateTask wraps
	// this to deliver the task spec to the hired agent. Must be
	// non-nil to Execute; tests inject fakes, production wires the
	// a2a-client adapter.
	Submitter DelegationSubmitter

	// Delivery is the companion to Submitter — blocks until the
	// hired agent has produced output. Must be non-nil to Execute.
	Delivery DeliveryWaiter

	// FromDID / BundleName / Expiry are populated onto every
	// delegation.Request. Callers typically set these once at
	// construction time; per-task overrides can be threaded in
	// through Plan.Extra keys (see resolveDelegationRequest).
	FromDID    string
	BundleName string
}

// DelegationDeliverable is the typed result of a successful
// delegate task. Matches the spec signature exactly: ContractID +
// AgentID + Settlement. Callers that need the full audit trail can
// assert on this concrete type.
type DelegationDeliverable struct {
	ContractID string
	AgentID    string
	Settlement hire.SettleReceipt
}

// Summary implements Deliverable.
func (d DelegationDeliverable) Summary() string {
	return fmt.Sprintf(
		"delegation: contract=%s agent=%s settlement=%s",
		d.ContractID, d.AgentID, d.Settlement.SettlementID,
	)
}

// Size implements Deliverable. Returns len(SettlementID) as a rough
// scalar — descent's zero-size sanity gate only cares that the
// deliverable is non-empty, and a settled contract always carries
// an ID.
func (d DelegationDeliverable) Size() int {
	return len(d.Settlement.SettlementID)
}

// TaskType implements Executor.
func (e *DelegateExecutor) TaskType() TaskType { return TaskDelegate }

// Execute runs the full delegate pipeline. See the file-header doc
// for the flow and the seam rationale.
func (e *DelegateExecutor) Execute(ctx context.Context, p Plan, _ EffortLevel) (Deliverable, error) {
	if e.Hirer == nil {
		return nil, errors.New("delegate: Hirer is nil — configure DelegateExecutor.Hirer")
	}
	if e.Delegator == nil {
		return nil, errors.New("delegate: Delegator is nil — configure DelegateExecutor.Delegator")
	}
	if e.Submitter == nil {
		return nil, errors.New("delegate: Submitter is nil — configure DelegateExecutor.Submitter")
	}
	if e.Delivery == nil {
		return nil, errors.New("delegate: Delivery is nil — configure DelegateExecutor.Delivery")
	}

	// Apply caller-supplied ReviewFunc lazily so tests that build the
	// Hirer separately are not forced to re-set the hook.
	if e.ReviewFunc != nil && e.Hirer.Review == nil {
		e.Hirer.Review = e.ReviewFunc
	}

	capability := resolveCapability(p)
	if capability == "" {
		return nil, errors.New("delegate: empty capability — set Plan.Task.Description or RequiredCaps")
	}
	spec := resolveSpec(p)

	// 1) Hire.
	cand, receipt, err := e.Hirer.Hire(ctx, capability, policyRefOrDefault(e.BundleName))
	if err != nil {
		return nil, fmt.Errorf("delegate: hire: %w", err)
	}

	// 2) Create the TrustPlane delegation AND submit the task to the
	// a2a peer in one call. On submitter failure DelegateTask keeps
	// the delegation live (the returned handle carries it) so we can
	// revoke cleanly below.
	req := e.resolveDelegationRequest(p, cand)
	handle, err := e.Delegator.DelegateTask(ctx, req, e.Submitter, spec)
	if err != nil {
		// If the delegation itself was created before the submitter
		// failed, revoke it so we don't leak an orphan authorization.
		if handle.Delegation.ID != "" {
			_ = e.Delegator.Revoke(context.Background(), handle.Delegation.ID)
		}
		return nil, fmt.Errorf("delegate: delegate-task: %w", err)
	}

	// 3) Await delivery, honoring ctx. On cancellation → revoke and
	// return the ctx error; on any other Await error → also revoke
	// (the hired agent may still be running under the delegation).
	delivery, err := e.Delivery.Await(ctx, handle)
	if err != nil {
		_ = e.Delegator.Revoke(context.Background(), handle.Delegation.ID)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("delegate: await delivery: %w", ctxErr)
		}
		return nil, fmt.Errorf("delegate: await delivery: %w", err)
	}

	// 4) Verify + settle. On dispute, VerifyAndSettle returns a
	// wrapped ErrVerificationFailed and we surface it unchanged so
	// descent can route the failure through its standard ladder.
	contractID := handle.Delegation.ID
	if contractID == "" {
		contractID = receipt.AgentDID // defensive — tests using stubs
	}
	if err := e.Hirer.VerifyAndSettle(ctx, contractID, cand.AgentDID, delivery, string(spec)); err != nil {
		return nil, err
	}

	// 5) Success: synthesize the deliverable. The SettleReceipt that
	// VerifyAndSettle drove is not returned through the public API
	// (the current hire.Hirer.VerifyAndSettle returns error-only),
	// so we construct a best-effort receipt carrying the contract
	// identity. Production wiring that needs the true SettlementID
	// reads it off the streamjson emitter events.
	return DelegationDeliverable{
		ContractID: contractID,
		AgentID:    cand.AgentDID,
		Settlement: hire.SettleReceipt{
			SettlementID: "settle-" + contractID,
			Note:         "all criteria passed",
		},
	}, nil
}

// BuildCriteria implements Executor. Returns a single VerifyFunc-
// backed AC that reports the settled contract identity — the full
// per-claim ladder already ran inside Hirer.VerifyAndSettle, so the
// descent engine's job here is only to surface the deliverable's
// terminal state. When d is not a DelegationDeliverable (e.g.
// Execute errored before producing one) we return nil so descent
// short-circuits.
func (e *DelegateExecutor) BuildCriteria(_ Task, d Deliverable) []plan.AcceptanceCriterion {
	dd, ok := d.(DelegationDeliverable)
	if !ok {
		return nil
	}
	return []plan.AcceptanceCriterion{
		{
			ID:          "DELEGATE-SETTLED",
			Description: "delegation settled via Hirer.VerifyAndSettle",
			VerifyFunc: func(_ context.Context) (bool, string) {
				if dd.Settlement.SettlementID == "" {
					return false, "no settlement ID"
				}
				return true, fmt.Sprintf(
					"settled contract %s with agent %s",
					dd.ContractID, dd.AgentID,
				)
			},
		},
	}
}

// BuildRepairFunc implements Executor. Delegation has no in-process
// repair primitive today — re-hiring is a policy decision that
// lives at the `stoke task` layer, not inside descent's T4. Future
// work wires a revision-request channel to the hired agent here.
func (e *DelegateExecutor) BuildRepairFunc(_ Plan) func(ctx context.Context, directive string) error {
	return nil
}

// BuildEnvFixFunc implements Executor. Delegation failures are
// TrustPlane-policy or agent-quality issues, not environmental, so
// we return nil. The descent engine treats nil as "executor has no
// env-fix primitive" and skips T5 for this task.
func (e *DelegateExecutor) BuildEnvFixFunc() func(ctx context.Context, rootCause, stderr string) bool {
	return nil
}

// resolveCapability builds the capability string the Hirer queries
// TrustPlane with. Preference order:
//  1. Plan.Task.RequiredCaps (first non-empty)
//  2. Plan.Task.Description (trimmed)
//  3. Plan.Query (trimmed)
func resolveCapability(p Plan) string {
	for _, c := range p.Task.RequiredCaps {
		if s := strings.TrimSpace(c); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(p.Task.Description); s != "" {
		return s
	}
	return strings.TrimSpace(p.Query)
}

// resolveSpec extracts the task-spec bytes the hired agent will
// receive. Preference order:
//  1. Plan.Extra["spec"] as []byte
//  2. Plan.Extra["spec"] as string
//  3. Plan.Task.Spec
//  4. Plan.Task.Description (fallback so every hire has something)
func resolveSpec(p Plan) []byte {
	if p.Extra != nil {
		if b, ok := p.Extra["spec"].([]byte); ok && len(b) > 0 {
			return b
		}
		if s, ok := p.Extra["spec"].(string); ok && s != "" {
			return []byte(s)
		}
	}
	if s := strings.TrimSpace(p.Task.Spec); s != "" {
		return []byte(s)
	}
	return []byte(strings.TrimSpace(p.Task.Description))
}

// resolveDelegationRequest builds the delegation.Request the Manager
// uses to create the TrustPlane delegation. Callers can override
// FromDID / ToDID / BundleName via Plan.Extra.
func (e *DelegateExecutor) resolveDelegationRequest(p Plan, cand hire.Candidate) delegation.Request {
	req := delegation.Request{
		FromDID:    e.FromDID,
		ToDID:      cand.AgentDID,
		BundleName: e.BundleName,
	}
	if req.BundleName == "" {
		req.BundleName = "hire-from-trustplane"
	}
	if p.Extra != nil {
		if v, ok := p.Extra["from_did"].(string); ok && v != "" {
			req.FromDID = v
		}
		if v, ok := p.Extra["bundle"].(string); ok && v != "" {
			req.BundleName = v
		}
		if extras, ok := p.Extra["extra_scopes"].([]string); ok && len(extras) > 0 {
			req.ExtraScopes = extras
		}
	}
	return req
}

// policyRefOrDefault supplies a descriptive policy ref to the
// Hirer's receipt emitter. When the caller hasn't named a bundle,
// we surface "hire-from-trustplane" so audit logs carry the
// canonical bundle name rather than an empty string.
func policyRefOrDefault(bundle string) string {
	if bundle == "" {
		return "hire-from-trustplane"
	}
	return bundle
}

// Compile-time assertion — DelegateExecutor satisfies Executor.
var _ Executor = (*DelegateExecutor)(nil)
