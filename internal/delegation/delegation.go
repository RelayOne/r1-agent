// Package delegation implements STOKE-014: a thin Stoke-side
// wrapper over truecom.Client. In production the wrapped
// client is truecom.RealClient, which speaks to the TrustPlane
// gateway over HTTP against a vendored OpenAPI spec — there is no
// Go SDK dependency anywhere in this call path. All cryptographic
// primitives (Ed25519 signing, ActClaim chain walking, attenuation
// enforcement, offline verification, revocation registry) live
// TrustPlane-side; this package exposes the Stoke-flavored API
// (Delegate / Verify / Revoke) and attaches Stoke-specific
// metadata (default policy bundles from configs/policies/,
// session context, audit anchoring).
//
// Scope of this file:
//
//   - Delegate(from, to, scope, expiry, parent?) calls
//     truecom.Client.CreateDelegation
//   - Verify / Revoke call the matching truecom.Client methods
//   - Policy template registry: maps bundle names → scope sets
//     so callers can say "I want read-only-calendar scope"
//     without enumerating every Cedar action name
//   - Subscribes to NATS trustplane.delegation.revoked (wiring
//     slot; saga orchestrator will consume in STOKE-016)
//   - A2A-core (spec-5 build-order 5): DelegateTask ties the
//     TrustPlane delegation to an A2A peer task submission via
//     a pluggable TaskSubmitter so the rest of spec-5 (HMAC
//     verifier, budget reserve, a2a-go client) can layer on top
//     without disturbing this seam.
package delegation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/truecom"
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
// truecom.Client so the rest of Stoke consumes one import
// (delegation.Manager) rather than juggling TrustPlane types +
// Stoke-side metadata separately.
type Manager struct {
	tp      truecom.Client
	bundles map[string][]string
}

// NewManager returns a Manager wired to the supplied TrustPlane
// client. The initial bundle set is DefaultPolicyBundles;
// callers can add more via RegisterBundle.
func NewManager(tp truecom.Client) *Manager {
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
// Distinct from truecom.DelegationRequest so operators can
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
func (m *Manager) Delegate(ctx context.Context, req Request) (truecom.Delegation, error) {
	scopes, err := m.BundleScopes(req.BundleName)
	if err != nil {
		return truecom.Delegation{}, err
	}
	scopes = append(scopes, req.ExtraScopes...)
	tpReq := truecom.DelegationRequest{
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

// --- A2A-core: DelegateTask (spec-5 build-order 5) ---
//
// DelegateTask is the core A2A-delegation primitive the spec-5
// DelegateExecutor (Part 6) composes on top of. It binds two
// concerns the rest of the spec layers atop: (a) create the
// TrustPlane-side delegation that authorizes the hire, and
// (b) submit the task to an A2A-compatible peer via a pluggable
// TaskSubmitter. Callers who later wire the a2a-go client + HMAC
// token verifier just swap the submitter; the orchestration
// seam is stable.
//
// This deliberately avoids pulling a2a-go / bbolt / SQLite /
// Ed25519 signing into this commit — the spec's Part 2 (HMAC
// verifier), Part 3 (budget reservation), and Part 5 (A2A
// client) each land as follow-on steps. What ships here is the
// minimum primitive that lets an operator say "hire agent B to
// do X under delegation D" and have the delegation created +
// task dispatched in one call, with typed errors for the two
// failure modes that matter at this layer.

// ErrTaskDispatchFailed is returned by DelegateTask when the
// delegation was successfully created but the A2A submitter
// rejected the task. The caller holds a live delegation
// (visible via Manager.Revoke + TrustPlane audit) and can
// decide whether to retry against a different peer or revoke
// and report.
var ErrTaskDispatchFailed = errors.New("delegation: task dispatch failed")

// ErrEmptyTaskSpec is returned when DelegateTask is called with
// an empty TaskSpec. Zero-length specs are a caller bug —
// surface it eagerly rather than issuing a delegation for an
// unspecified task.
var ErrEmptyTaskSpec = errors.New("delegation: empty task spec")

// TaskSubmitter is the plug point for the A2A transport. The
// real implementation (spec Part 5) wraps a2a-go's client; tests
// wire a fake. Only the minimum needed for "hire and dispatch"
// lives here: fuller stream / cancel / subscribe methods arrive
// with the full DelegateExecutor in spec Part 6.
type TaskSubmitter interface {
	// SubmitTask dispatches a task to the A2A peer identified
	// by the delegation + opaque spec bytes. Returns the
	// peer-assigned task ID on success.
	SubmitTask(ctx context.Context, d truecom.Delegation, spec []byte) (taskID string, err error)
}

// TaskHandle is what DelegateTask returns on success: the
// delegation record (so callers can audit + revoke) plus the
// peer-assigned task ID (so callers can poll + cancel).
type TaskHandle struct {
	Delegation truecom.Delegation
	TaskID     string
}

// DelegateTask is the end-to-end primitive: create the
// delegation via TrustPlane, then submit the task bytes to the
// A2A peer via submitter. On submitter failure the already-
// issued delegation is NOT auto-revoked — callers decide
// whether revocation is appropriate for their retry story, and
// blindly revoking would double-charge the audit log for
// transient network errors.
//
// The returned handle on success carries both the delegation
// (for downstream Verify / Revoke / saga registration) and the
// peer's task ID (for status polling).
func (m *Manager) DelegateTask(ctx context.Context, req Request, submitter TaskSubmitter, spec []byte) (TaskHandle, error) {
	if len(spec) == 0 {
		return TaskHandle{}, ErrEmptyTaskSpec
	}
	if submitter == nil {
		return TaskHandle{}, fmt.Errorf("%w: nil submitter", ErrTaskDispatchFailed)
	}
	d, err := m.Delegate(ctx, req)
	if err != nil {
		return TaskHandle{}, err
	}
	taskID, err := submitter.SubmitTask(ctx, d, spec)
	if err != nil {
		return TaskHandle{Delegation: d}, fmt.Errorf("%w: %w", ErrTaskDispatchFailed, err)
	}
	return TaskHandle{Delegation: d, TaskID: taskID}, nil
}
