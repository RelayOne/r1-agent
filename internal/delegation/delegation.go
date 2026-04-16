// Package delegation implements STOKE-014: a thin Stoke-side
// wrapper over the TrustPlane delegation SDK. All cryptographic
// primitives (Ed25519 signing, ActClaim chain walking,
// attenuation enforcement, offline verification, revocation
// registry) live in TrustPlane; this package exposes the
// Stoke-flavored API (Delegate / Verify / Revoke) and attaches
// Stoke-specific metadata (default policy bundles from
// configs/policies/, session context, audit anchoring).
//
// Scope of this file:
//
//   - Delegate(from, to, scope, expiry, parent?) wraps the
//     TrustPlane Client.CreateDelegation
//   - Verify / Revoke wrap the matching TrustPlane calls
//   - Policy template registry: maps bundle names → scope sets
//     so callers can say "I want read-only-calendar scope"
//     without enumerating every Cedar action name
//   - Subscribes to NATS trustplane.delegation.revoked (wiring
//     slot; saga orchestrator will consume in STOKE-016)
package delegation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ericmacdougall/stoke/internal/trustplane"
)

// DefaultPolicyBundles names the templates shipped under
// configs/policies/. Bundle name → set of scope strings to
// attach to the DelegationRequest's Scopes field.
var DefaultPolicyBundles = map[string][]string{
	"read-only-calendar": {
		"calendar_list_events", "calendar_get_event",
		"calendar_search", "calendar_list_calendars",
		"scope_calendar_read",
	},
	"read-only-email": {
		"email_list_messages", "email_get_message",
		"email_search", "email_list_threads",
		"email_get_thread", "email_list_labels",
		"scope_email_read",
	},
	"send-on-behalf-of": {
		"send_message", "email_reply", "email_forward",
		"email_create_draft",
		"scope_send_message",
	},
	"schedule-on-behalf-of": {
		"calendar_create_event", "calendar_update_event",
		"calendar_move_event",
		"scope_calendar_write",
	},
	"hire-from-trustplane": {
		"trustplane_discover", "trustplane_get_agent_card",
		"trustplane_get_reputation", "trustplane_hire",
		"trustplane_pay",
		"scope_trustplane_discover", "scope_trustplane_hire",
		"scope_trustplane_pay",
	},
}

// ErrUnknownBundle is returned when a caller asks for a bundle
// name that isn't in DefaultPolicyBundles or registered
// dynamically.
var ErrUnknownBundle = errors.New("delegation: unknown policy bundle")

// Manager is the Stoke-side delegation facade. Wraps a
// trustplane.Client so the rest of Stoke consumes one import
// (delegation.Manager) rather than juggling TrustPlane types +
// Stoke-side metadata separately.
type Manager struct {
	tp      trustplane.Client
	bundles map[string][]string
}

// NewManager returns a Manager wired to the supplied TrustPlane
// client. The initial bundle set is DefaultPolicyBundles;
// callers can add more via RegisterBundle.
func NewManager(tp trustplane.Client) *Manager {
	bundles := make(map[string][]string, len(DefaultPolicyBundles))
	for k, v := range DefaultPolicyBundles {
		// Defensive copy so mutation after NewManager doesn't
		// affect the shared DefaultPolicyBundles map.
		cp := make([]string, len(v))
		copy(cp, v)
		bundles[k] = cp
	}
	return &Manager{tp: tp, bundles: bundles}
}

// RegisterBundle adds or replaces a named bundle's scope set.
// Useful when operators ship custom policies beyond the
// shipped defaults.
func (m *Manager) RegisterBundle(name string, scopes []string) {
	cp := make([]string, len(scopes))
	copy(cp, scopes)
	m.bundles[name] = cp
}

// BundleScopes returns the scope set for a named bundle, or
// ErrUnknownBundle.
func (m *Manager) BundleScopes(name string) ([]string, error) {
	s, ok := m.bundles[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBundle, name)
	}
	out := make([]string, len(s))
	copy(out, s)
	return out, nil
}

// Request is the Stoke-flavored delegation creation request.
// Distinct from trustplane.DelegationRequest so operators can
// name a policy BUNDLE instead of enumerating scope strings,
// and so Stoke-specific metadata (session context, annotations)
// is carried alongside without bleeding into the TrustPlane
// shape.
type Request struct {
	FromDID     string
	ToDID       string
	BundleName  string   // one of DefaultPolicyBundles or RegisterBundle
	ExtraScopes []string // additional scopes beyond the bundle
	Expiry      time.Duration
	ParentID    string
	Annotations map[string]string
}

// Delegate creates a delegation via TrustPlane using the named
// bundle's scopes (plus any ExtraScopes). Returns the issued
// delegation handle.
func (m *Manager) Delegate(ctx context.Context, req Request) (trustplane.Delegation, error) {
	scopes, err := m.BundleScopes(req.BundleName)
	if err != nil {
		return trustplane.Delegation{}, err
	}
	scopes = append(scopes, req.ExtraScopes...)
	tpReq := trustplane.DelegationRequest{
		FromDID:     req.FromDID,
		ToDID:       req.ToDID,
		Scopes:      scopes,
		Expiry:      req.Expiry,
		ParentID:    req.ParentID,
		Annotations: req.Annotations,
	}
	return m.tp.CreateDelegation(ctx, tpReq)
}

// Verify forwards to the TrustPlane Verify call. Returns nil
// when the delegation is currently valid for the delegatee;
// ErrDelegationInvalid otherwise.
func (m *Manager) Verify(ctx context.Context, delegationID, delegateeID string) error {
	return m.tp.VerifyDelegation(ctx, delegationID, delegateeID)
}

// Revoke forwards to TrustPlane's cascade-revoke call. The
// saga-settlement layer (STOKE-016) subscribes to the NATS
// revocation topic separately and applies settlement policy
// to in-flight WorkUnits.
func (m *Manager) Revoke(ctx context.Context, delegationID string) error {
	return m.tp.RevokeDelegation(ctx, delegationID)
}

// Bundles returns the sorted list of registered bundle names.
// Used by `stoke policy list` + reports.
func (m *Manager) Bundles() []string {
	out := make([]string, 0, len(m.bundles))
	for k := range m.bundles {
		out = append(out, k)
	}
	// Simple in-place sort to avoid an import of sort for one call.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
