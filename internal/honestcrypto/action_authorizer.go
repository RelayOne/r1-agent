// action_authorizer.go — the pre-execution authorization seam.
//
// Authorize(req) -> (Allow | Deny | RequireApproval, reason); SessionSpecHash().
// The default backend is a signed action allowlist, reject-before-execute: a
// session runs under a signed permitted-action spec; an action the spec does not
// permit is rejected before it executes. A stronger backend may be registered
// later that enforces the same policy more deeply — it drops in by config with
// zero caller changes.
package honestcrypto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// Decision is the outcome of an authorization check.
type Decision string

const (
	// DecisionAllow permits the action.
	DecisionAllow Decision = "Allow"
	// DecisionDeny rejects the action.
	DecisionDeny Decision = "Deny"
	// DecisionRequireApproval routes the action to human approval.
	DecisionRequireApproval Decision = "RequireApproval"
)

// AuthorizationResult is a decision plus a human-readable reason.
type AuthorizationResult struct {
	Decision Decision
	Reason   string
}

// ActionRequest is a request to perform an action against an optional resource.
type ActionRequest struct {
	// Action is the action type, e.g. "model.call", "tool.exec", "pay".
	Action string
	// Resource is the optional resource/target the action addresses.
	Resource string
	// Params are arbitrary parameters the spec may constrain.
	Params map[string]any
}

// SignedActionSpec is the envelope a principal signs before a session: the
// allowlisted actions, optional approval-gated actions, an expiry, and an
// Ed25519 signature over the canonical (principal, allow, requireApproval,
// expiresAt).
type SignedActionSpec struct {
	// Principal is the portfolio-uniform principal (RelayOne OIDC subject) who
	// authorized the session.
	Principal string `json:"principal"`
	// Allow lists allowlisted action types. "*" permits any type (still subject
	// to RequireApproval).
	Allow []string `json:"allow"`
	// RequireApproval lists action types that are permitted but must route to
	// human approval.
	RequireApproval []string `json:"requireApproval,omitempty"`
	// ExpiresAt is an RFC-3339 expiry; a spec past this is rejected.
	ExpiresAt string `json:"expiresAt"`
	// Signature is the base64 Ed25519 signature over the canonical spec preimage.
	Signature string `json:"signature"`
}

// ActionAuthorizer authorizes actions against a session's active spec.
type ActionAuthorizer interface {
	Authorize(req ActionRequest) AuthorizationResult
	// SessionSpecHash returns a stable hash of the session's active spec —
	// recorded in the audit chain.
	SessionSpecHash() string
	// Backend is the stable backend name.
	Backend() string
}

// BackendSignedAllowlist is the default ActionAuthorizer backend name.
const BackendSignedAllowlist = "signed-allowlist"

func specPreimage(principal string, allow, requireApproval []string, expiresAt string) ([]byte, error) {
	allowSorted := append([]string(nil), allow...)
	sort.Strings(allowSorted)
	approvalSorted := append([]string(nil), requireApproval...)
	sort.Strings(approvalSorted)
	// []string -> []any so the canonicalizer's array path handles it.
	allowAny := make([]any, len(allowSorted))
	for i, s := range allowSorted {
		allowAny[i] = s
	}
	approvalAny := make([]any, len(approvalSorted))
	for i, s := range approvalSorted {
		approvalAny[i] = s
	}
	return Canonicalize(map[string]any{
		"principal":       principal,
		"allow":           allowAny,
		"requireApproval": approvalAny,
		"expiresAt":       expiresAt,
	})
}

// SignActionSpec signs an action spec (test/demo helper; prod signs with the
// principal's key).
func SignActionSpec(principal string, allow, requireApproval []string, expiresAt, privateKeyPEM string) (SignedActionSpec, error) {
	pre, err := specPreimage(principal, allow, requireApproval, expiresAt)
	if err != nil {
		return SignedActionSpec{}, err
	}
	sig, err := SignBytes(pre, privateKeyPEM)
	if err != nil {
		return SignedActionSpec{}, err
	}
	return SignedActionSpec{
		Principal:       principal,
		Allow:           allow,
		RequireApproval: requireApproval,
		ExpiresAt:       expiresAt,
		Signature:       sig,
	}, nil
}

// SignedAllowlistAuthorizer is the unencumbered default. Reject-before-execute: a
// tampered or wrongly-signed spec refuses to construct (fail-closed); an action
// outside the allowlist is denied before it runs; a RequireApproval action
// returns RequireApproval so the host routes it to its human queue.
type SignedAllowlistAuthorizer struct {
	spec     SignedActionSpec
	specHash string
	now      func() time.Time
}

// NewSignedAllowlistAuthorizer constructs the authorizer, verifying the spec
// signature against the principal's public key. A spec whose signature fails
// makes construction fail (fail-closed). Pass nil for now to use the wall clock.
func NewSignedAllowlistAuthorizer(spec SignedActionSpec, principalPublicKeyPEM string, now func() time.Time) (*SignedAllowlistAuthorizer, error) {
	if now == nil {
		now = time.Now
	}
	pre, err := specPreimage(spec.Principal, spec.Allow, spec.RequireApproval, spec.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if !VerifyBytes(pre, spec.Signature, principalPublicKeyPEM) {
		return nil, errors.New("honestcrypto: action spec signature invalid — refusing to start")
	}
	sum := sha256.Sum256(pre)
	return &SignedAllowlistAuthorizer{spec: spec, specHash: hex.EncodeToString(sum[:]), now: now}, nil
}

// Backend returns the stable backend name.
func (a *SignedAllowlistAuthorizer) Backend() string { return BackendSignedAllowlist }

// Authorize evaluates a request against the signed spec: expiry denies, an
// approval-gated action returns RequireApproval, an allowlisted (or "*") action
// is allowed, else denied.
func (a *SignedAllowlistAuthorizer) Authorize(req ActionRequest) AuthorizationResult {
	exp, err := time.Parse(time.RFC3339, a.spec.ExpiresAt)
	if err != nil || !a.now().Before(exp) {
		return AuthorizationResult{Decision: DecisionDeny, Reason: "action spec expired"}
	}
	if contains(a.spec.RequireApproval, req.Action) {
		return AuthorizationResult{Decision: DecisionRequireApproval, Reason: "action '" + req.Action + "' requires human approval"}
	}
	if contains(a.spec.Allow, "*") || contains(a.spec.Allow, req.Action) {
		return AuthorizationResult{Decision: DecisionAllow, Reason: "permitted by signed action spec"}
	}
	return AuthorizationResult{Decision: DecisionDeny, Reason: "action '" + req.Action + "' not in signed allowlist"}
}

// SessionSpecHash returns the stable spec hash.
func (a *SignedAllowlistAuthorizer) SessionSpecHash() string { return a.specHash }

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---- Registry: swap a stronger authorizer in by config, zero caller changes ----

var (
	authorizerMu sync.RWMutex
	authorizer   ActionAuthorizer
)

// SetActionAuthorizer sets the active authorizer for a session.
func SetActionAuthorizer(next ActionAuthorizer) {
	authorizerMu.Lock()
	defer authorizerMu.Unlock()
	authorizer = next
}

// GetActionAuthorizer returns the active authorizer; it fails closed if a
// session has not installed one.
func GetActionAuthorizer() (ActionAuthorizer, error) {
	authorizerMu.RLock()
	defer authorizerMu.RUnlock()
	if authorizer == nil {
		return nil, errors.New("honestcrypto: no active authorizer (fail-closed)")
	}
	return authorizer, nil
}

// ResetActionAuthorizer clears the active authorizer (test hygiene).
func ResetActionAuthorizer() {
	authorizerMu.Lock()
	defer authorizerMu.Unlock()
	authorizer = nil
}
