// Package trustplane is Stoke's internal view of the TrustPlane
// gateway (STOKE-011). This interface does not match any TrustPlane
// Go type exactly. Translation to TrustPlane's wire format will
// happen inside RealClient (real.go), which is scheduled by the
// TrustPlane implementation SOW but not yet present in this
// package. We deliberately do NOT import any TrustPlane Go module —
// the only coupling will be a vendored OpenAPI spec added under
// internal/trustplane/openapi/ by SOW task B-2 (also not yet
// present), which is used for hand-writing HTTP calls, not code
// generation.
//
// Scope consumed:
//
//   - identity registration (per-stance SVID minting on spawn)
//   - audit anchoring (ledger graph roots → TrustPlane audit
//     ledger)
//   - HITL routing (approval flows → TrustPlane HITL service)
//   - reputation reads + writes (discovery + post-invoke)
//   - delegation create / verify / revoke (via DelegationToken)
//   - policy evaluation (Cedar-backed, evaluated TrustPlane-side)
//
// Implementations:
//
//   - StubClient (this file): always-pass, in-memory, no network.
//     Default for local-dev and the zero-configuration startup path.
//   - MockClient (test-only): assertion-capable, test fixtures.
//   - RealClient (real.go): production HTTP client against the
//     TrustPlane gateway. Hand-written against the vendored OpenAPI
//     spec. No Go SDK dependency.
//
// Every method in this file is an interface declaration; the
// Client is intentionally small so implementations stay readable.
package truecom

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Client is the narrow facade the rest of Stoke consumes.
// Implementations: StubClient (this file, always-pass for
// local dev), MockClient (test-only, assertions), and
// RealClient (real.go — hand-written HTTP against the vendored
// TrustPlane OpenAPI spec, no Go SDK dependency).
type Client interface {
	// RegisterIdentity mints a SPIFFE SVID (or equivalent) for
	// the given agent and returns the identity's DID. Stores
	// public key material on the TrustPlane side; private key
	// stays local to Stoke's stancesign.
	RegisterIdentity(ctx context.Context, req IdentityRequest) (Identity, error)

	// AnchorAudit submits a ledger graph root to TrustPlane's
	// audit pipeline. Returns the TrustPlane-side anchor ID so
	// the emitting ledger can cross-reference.
	AnchorAudit(ctx context.Context, root AuditRoot) (AuditAnchor, error)

	// RequestHITL opens an HITL approval flow. Blocks until
	// the human responds or the deadline elapses (implementation
	// may poll + return a pending response earlier — callers
	// handle Status=pending by re-requesting).
	RequestHITL(ctx context.Context, req HITLRequest) (HITLResponse, error)

	// LookupReputation fetches the reputation score for an
	// agent from the TrustPlane marketplace. Used in discovery
	// to rank candidates.
	LookupReputation(ctx context.Context, agentDID string) (Reputation, error)

	// RecordReputation writes a reputation entry after a
	// successful hire+completion. Called by Stoke's hire path
	// on work-receipt.
	RecordReputation(ctx context.Context, entry ReputationEntry) error

	// CreateDelegation issues a new DelegationToken via the
	// TrustPlane gateway (POST /v1/delegation). Attenuation +
	// Ed25519 signing + ActClaim chain are TrustPlane-side
	// concerns; Stoke only calls in over HTTP.
	CreateDelegation(ctx context.Context, req DelegationRequest) (Delegation, error)

	// VerifyDelegation checks the current validity of a
	// delegation for a given delegatee. Returns nil when valid,
	// an error when revoked/expired/over-scoped.
	VerifyDelegation(ctx context.Context, delegationID, delegateeID string) error

	// RevokeDelegation triggers cascade revocation via the
	// TrustPlane FanoutDelegator. Cascade walking + child
	// invalidation + TrustPlane-side audit anchoring live in
	// TrustPlane; Stoke adds the saga-settlement layer on top.
	RevokeDelegation(ctx context.Context, delegationID string) error

	// EvaluatePolicy calls the TrustPlane Cedar evaluator with
	// a delegation context + proposed action. Returns
	// ErrPolicyDenied when the action is rejected. Policy
	// bundles are identified by name (e.g. "personal-assistant")
	// per STOKE-015.
	EvaluatePolicy(ctx context.Context, req PolicyRequest) error
}

// IdentityRequest is the RegisterIdentity input.
type IdentityRequest struct {
	AgentID   string // Stoke-internal agent identifier
	StanceRole string // "reviewer", "dev", "po", etc.
	PublicKey string // PEM-encoded ed25519 public key
	// Annotations is free-form metadata the TrustPlane side
	// attaches to the identity record (Stoke-specific
	// provenance, build version, etc.).
	Annotations map[string]string
}

// Identity is the registered result.
type Identity struct {
	DID          string    // e.g. "did:tp:stoke-agent-abc"
	SVIDBytes    []byte    // optional — may be empty in dev
	RegisteredAt time.Time
}

// AuditRoot is a ledger graph root anchor submission.
type AuditRoot struct {
	LedgerID  string
	RootHash  string // hex or base64; implementation decides
	EmittedAt time.Time
	// Meta carries the emitting stance, mission ID, etc.
	Meta map[string]string
}

// AuditAnchor is the TrustPlane-side anchor receipt.
type AuditAnchor struct {
	AnchorID    string
	AnchoredAt  time.Time
	TrustPlaneRef string // opaque URI back to TrustPlane audit UI
}

// HITLRequest kicks off a human-approval flow.
type HITLRequest struct {
	AgentDID    string
	Question    string
	Context     []string // ledger node IDs the human should read
	Deadline    time.Duration
	Annotations map[string]string
}

// HITLResponse is the human's answer (or a timeout sentinel).
type HITLResponse struct {
	Decision    string // "approved" | "rejected" | "modified" | "timed_out"
	Reasoning   string
	ResponderID string
	DecidedAt   time.Time
}

// Reputation summarizes an agent's marketplace standing.
type Reputation struct {
	AgentDID      string
	Score         float64
	TotalHires    int
	SuccessfulHires int
	LastRecordedAt time.Time
}

// ReputationEntry is the post-hire feedback write.
type ReputationEntry struct {
	AgentDID    string
	Outcome     string  // "success" | "failure" | "partial"
	RatingDelta float64 // signed change to the agent's rating
	Note        string
	RecordedAt  time.Time
}

// DelegationRequest creates a new delegation.
type DelegationRequest struct {
	FromDID    string
	ToDID      string
	Scopes     []string
	Expiry     time.Duration
	ParentID   string // chain attenuation parent
	Annotations map[string]string
}

// Delegation is the resulting token + metadata.
type Delegation struct {
	ID        string
	Token     string // opaque; receiver passes back to VerifyDelegation
	ExpiresAt time.Time
}

// PolicyRequest is an EvaluatePolicy input.
type PolicyRequest struct {
	PolicyBundle string // "personal-assistant" | "coding-team" | ...
	Delegation   string // delegation ID in scope; empty for operator-issued
	Principal    string // acting agent DID
	Action       string // e.g. "calendar_create_event"
	Resource     map[string]any
}

// ErrPolicyDenied is returned by EvaluatePolicy when the action
// is rejected by Cedar.
var ErrPolicyDenied = errors.New("trustplane: policy denied")

// ErrDelegationInvalid is returned by VerifyDelegation on
// revoked / expired / over-scoped delegations.
var ErrDelegationInvalid = errors.New("trustplane: delegation invalid")

// StubClient is a local-dev implementation: always-pass, in-memory
// state, no network. Returns synthetic IDs / DIDs / anchors so
// downstream code paths can exercise the full surface without a
// running TrustPlane.
type StubClient struct {
	mu          sync.Mutex
	identities  map[string]Identity
	delegations map[string]Delegation
	revoked     map[string]bool
	reputation  map[string]Reputation
	nextSeq     int
}

// NewStubClient returns a fresh StubClient.
func NewStubClient() *StubClient {
	return &StubClient{
		identities:  map[string]Identity{},
		delegations: map[string]Delegation{},
		revoked:     map[string]bool{},
		reputation:  map[string]Reputation{},
	}
}

func (s *StubClient) seq() int {
	s.nextSeq++
	return s.nextSeq
}

func (s *StubClient) RegisterIdentity(_ context.Context, req IdentityRequest) (Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := Identity{
		DID:          fmt.Sprintf("did:tp:stub-%s-%d", req.AgentID, s.seq()),
		RegisteredAt: time.Now().UTC(),
	}
	s.identities[req.AgentID] = id
	return id, nil
}

func (s *StubClient) AnchorAudit(_ context.Context, root AuditRoot) (AuditAnchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return AuditAnchor{
		AnchorID:      fmt.Sprintf("anchor-stub-%d", s.seq()),
		AnchoredAt:    time.Now().UTC(),
		TrustPlaneRef: "stub://audit/" + root.RootHash,
	}, nil
}

func (s *StubClient) RequestHITL(_ context.Context, req HITLRequest) (HITLResponse, error) {
	// Stub auto-approves so local-dev flows exercise the
	// approval path end-to-end. Production callers MUST use a
	// real Client for any safety-critical decision.
	return HITLResponse{
		Decision:    "approved",
		Reasoning:   "stub auto-approval (local dev)",
		ResponderID: "stub-operator",
		DecidedAt:   time.Now().UTC(),
	}, nil
}

func (s *StubClient) LookupReputation(_ context.Context, agentDID string) (Reputation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.reputation[agentDID]; ok {
		return r, nil
	}
	return Reputation{AgentDID: agentDID, Score: 0.5, LastRecordedAt: time.Now().UTC()}, nil
}

func (s *StubClient) RecordReputation(_ context.Context, entry ReputationEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reputation[entry.AgentDID]
	r.AgentDID = entry.AgentDID
	r.Score += entry.RatingDelta
	if entry.Outcome == "success" {
		r.SuccessfulHires++
	}
	r.TotalHires++
	r.LastRecordedAt = entry.RecordedAt
	s.reputation[entry.AgentDID] = r
	return nil
}

func (s *StubClient) CreateDelegation(_ context.Context, req DelegationRequest) (Delegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := Delegation{
		ID:        fmt.Sprintf("del-stub-%d", s.seq()),
		Token:     fmt.Sprintf("stub-token-%s-%s", req.FromDID, req.ToDID),
		ExpiresAt: time.Now().Add(req.Expiry).UTC(),
	}
	s.delegations[d.ID] = d
	return d, nil
}

func (s *StubClient) VerifyDelegation(_ context.Context, delegationID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked[delegationID] {
		return fmt.Errorf("%w: revoked", ErrDelegationInvalid)
	}
	d, ok := s.delegations[delegationID]
	if !ok {
		return fmt.Errorf("%w: not found", ErrDelegationInvalid)
	}
	if !d.ExpiresAt.IsZero() && time.Now().After(d.ExpiresAt) {
		return fmt.Errorf("%w: expired", ErrDelegationInvalid)
	}
	return nil
}

func (s *StubClient) RevokeDelegation(_ context.Context, delegationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[delegationID] = true
	return nil
}

func (s *StubClient) EvaluatePolicy(_ context.Context, req PolicyRequest) error {
	// Stub evaluator is deliberately permissive for local dev.
	// Real evaluation happens TrustPlane-side (Cedar engine
	// behind POST /v1/authorize-settlement or /v1/policy/evaluate);
	// RealClient calls it over HTTP, no Go SDK involved. Policy
	// bundles that don't exist trigger a denial so callers test
	// the deny path.
	if req.PolicyBundle == "" {
		return fmt.Errorf("%w: empty bundle", ErrPolicyDenied)
	}
	return nil
}
