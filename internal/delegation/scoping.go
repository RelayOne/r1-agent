// Package delegation — scoping.go
//
// STOKE-015: per-relationship capability scoping. Every tool
// invocation in a delegated session carries the delegation
// context (delegator, delegatee, scope set) which gets fed
// into TrustPlane's Cedar evaluator before the tool runs.
// Default-deny posture: an action without a matching scope in
// the current delegation is rejected.
//
// This file provides:
//
//   - DelegationContext: the struct passed through every tool
//     invocation in a delegated session
//   - Authorize: the gate that consults the TrustPlane Cedar
//     evaluator before a tool runs
//   - `stoke policy apply <bundle-name>` helper that turns a
//     named policy bundle into a DelegationContext seed
//
// Scope: Stoke doesn't re-implement Cedar. All evaluation
// lives in tp-policy-cedar via trustplane.Client.EvaluatePolicy.
// This file is the caller-side plumbing that makes each tool
// invocation pass the right request shape.
package delegation

import (
	"context"
	"errors"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/trustplane"
)

// DelegationContext carries the authority state of a delegated
// session: who delegated to whom, which scopes the delegatee
// holds, and which policy bundle governs the session.
type DelegationContext struct {
	// DelegatorDID is the delegator agent.
	DelegatorDID string

	// DelegateeDID is the acting agent (the one running the
	// tool).
	DelegateeDID string

	// DelegationID is the TrustPlane delegation token's ID.
	// Cedar uses it to look up revocation state and chain
	// parents.
	DelegationID string

	// Scopes is the set of capability strings the delegation
	// grants (e.g. "calendar_list_events", "scope_send_message").
	// These must have been in the delegation's declared scopes
	// list at creation time; Cedar re-verifies against the
	// authoritative TrustPlane-side record, but we carry them
	// here so the caller has something to log.
	Scopes []string

	// PolicyBundle names the Cedar bundle governing this
	// session (e.g. "personal-assistant", "coding-team").
	// Resolved at session start via stoke policy apply.
	PolicyBundle string
}

// HasScope reports whether the context carries a given scope
// string. Used by callers that want a local-only check before
// making the round-trip to TrustPlane (e.g. short-circuit
// rejection on obviously-missing scope).
func (d DelegationContext) HasScope(scope string) bool {
	for _, s := range d.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ErrActionDenied is returned by Authorize when Cedar rejects
// the action. Wraps trustplane.ErrPolicyDenied.
var ErrActionDenied = errors.New("delegation: action denied by policy")

// Authorize is the gate every tool invocation must pass
// through in a delegated session. It builds a
// trustplane.PolicyRequest from the DelegationContext +
// proposed action, then calls the TrustPlane Cedar evaluator.
// Returns nil on allow; wrapped ErrActionDenied on deny.
//
// Default-deny: an empty PolicyBundle is an error, not an
// implicit allow. Sessions that want "no policy check" must
// explicitly set a permissive bundle rather than leaving the
// field blank — otherwise a forgotten bundle would open the
// session wide.
func (m *Manager) Authorize(ctx context.Context, dctx DelegationContext, action string, resource map[string]any) error {
	if dctx.PolicyBundle == "" {
		return fmt.Errorf("%w: empty policy bundle (default-deny)", ErrActionDenied)
	}
	req := trustplane.PolicyRequest{
		PolicyBundle: dctx.PolicyBundle,
		Delegation:   dctx.DelegationID,
		Principal:    dctx.DelegateeDID,
		Action:       action,
		Resource:     resource,
	}
	if err := m.tp.EvaluatePolicy(ctx, req); err != nil {
		if errors.Is(err, trustplane.ErrPolicyDenied) {
			return fmt.Errorf("%w: action=%q", ErrActionDenied, action)
		}
		return err
	}
	return nil
}

// ApplyPolicyBundle seeds a DelegationContext with the scopes
// and bundle name from a named template. Equivalent to
// `stoke policy apply <name>` from the CLI: the caller gets a
// ready-to-use DelegationContext they can attach to a session.
func (m *Manager) ApplyPolicyBundle(name, delegatorDID, delegateeDID, delegationID string) (DelegationContext, error) {
	scopes, err := m.BundleScopes(name)
	if err != nil {
		return DelegationContext{}, err
	}
	return DelegationContext{
		DelegatorDID: delegatorDID,
		DelegateeDID: delegateeDID,
		DelegationID: delegationID,
		Scopes:       scopes,
		PolicyBundle: name,
	}, nil
}
