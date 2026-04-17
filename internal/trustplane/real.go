// RealClient is the production HTTP implementation of trustplane.Client.
// It talks to the TrustPlane gateway over hand-written HTTP calls that
// match the vendored OpenAPI spec at internal/trustplane/openapi/gateway.yaml.
//
// Design rules:
//
//   - No TrustPlane Go SDK import. The only TrustPlane artifact we
//     consume is the vendored OpenAPI YAML (as documentation, not for
//     codegen). If the gateway adds or changes an endpoint, update
//     openapi/gateway.yaml AND the matching method here.
//   - Every request carries a DPoP header produced by dpop.Signer.
//     The signer key is the same Ed25519 key registered at identity
//     creation; rotating the key means re-registering the identity.
//   - Timeouts: per-request context.Context wins; the http.Client
//     carries a conservative 30s default so a forgotten ctx never
//     leaks a goroutine on gateway hangs.
//   - Retries: none. TrustPlane's delegation / policy / audit surface
//     is non-idempotent for write paths; retry policy belongs at the
//     caller, after reading the error.
//
// Error mapping:
//
//   - 401 → fmt.Errorf("trustplane: unauthorized (DPoP): %s", body)
//     (callers surface this to user; usually means identity not
//     registered or DPoP key mismatch).
//   - 403 on /delegation/verify → ErrDelegationInvalid wrapped with body.
//   - 403 on /policy/evaluate  → ErrPolicyDenied wrapped with body.
//   - 4xx (other)              → typed httpError with status + body.
//   - 5xx                      → typed httpError; caller can retry.
//
// Nothing here panics; every path returns an error the caller can log.

package trustplane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/trustplane/dpop"
)

// RealClient calls a live TrustPlane gateway over HTTP with DPoP proofs.
type RealClient struct {
	baseURL string
	http    *http.Client
	signer  *dpop.Signer
}

// RealClientOptions configures a RealClient.
type RealClientOptions struct {
	// BaseURL is the gateway root, e.g. "https://gateway.trustplane.dev".
	// Trailing slashes are trimmed.
	BaseURL string
	// PrivateKey is the Ed25519 private key whose public half was
	// submitted at identity registration. Every request is DPoP-signed
	// with this key.
	PrivateKey ed25519.PrivateKey
	// HTTPClient is optional; when nil a default 30s-timeout client
	// is used. Provide your own when you need custom transport
	// (TLS pins, proxy, instrumentation).
	HTTPClient *http.Client
}

// NewRealClient builds a RealClient. Validates that BaseURL parses
// and PrivateKey is a valid Ed25519 key; returns an error if either
// is malformed so the problem surfaces at startup, not at first-RPC.
func NewRealClient(opts RealClientOptions) (*RealClient, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, errors.New("trustplane: RealClient requires BaseURL")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("trustplane: parse BaseURL: %w", err)
	}
	signer, err := dpop.NewSigner(opts.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("trustplane: build DPoP signer: %w", err)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &RealClient{
		baseURL: base,
		http:    client,
		signer:  signer,
	}, nil
}

// httpError is the unified error returned for any non-2xx response
// that isn't mapped to a sentinel (ErrPolicyDenied, ErrDelegationInvalid).
// Callers can errors.As into this type to read Status + Body for
// diagnostics.
type httpError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("trustplane: %s %s → %d: %s", e.Method, e.URL, e.Status, truncate(e.Body, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// do issues an HTTP request with a fresh DPoP proof and returns the
// response body (fully read) + status on any response. 2xx bodies are
// returned to caller; non-2xx bodies are folded into an httpError so
// the caller can introspect. ctx is respected on both the signature
// step (random bytes) and the HTTP call.
func (c *RealClient) do(ctx context.Context, method, path string, in any) ([]byte, int, error) {
	fullURL := c.baseURL + path
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return nil, 0, fmt.Errorf("trustplane: marshal request: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, 0, fmt.Errorf("trustplane: build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	proof, err := c.signer.Sign(method, fullURL)
	if err != nil {
		return nil, 0, fmt.Errorf("trustplane: DPoP sign: %w", err)
	}
	req.Header.Set("DPoP", proof)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("trustplane: HTTP do: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("trustplane: read response: %w", err)
	}
	return out, resp.StatusCode, nil
}

// wrapHTTPErr converts a non-2xx response into the appropriate error.
// Sentinels (ErrPolicyDenied, ErrDelegationInvalid) are returned for
// endpoint+status combos that the caller needs to branch on; the
// generic httpError covers everything else.
func wrapHTTPErr(method, path string, status int, body []byte, sentinel error) error {
	if sentinel != nil && status == http.StatusForbidden {
		return fmt.Errorf("%w: %s", sentinel, truncate(string(body), 200))
	}
	return &httpError{Method: method, URL: path, Status: status, Body: string(body)}
}

// ---- Client interface implementation ----

// RegisterIdentity POST /v1/identity.
func (c *RealClient) RegisterIdentity(ctx context.Context, req IdentityRequest) (Identity, error) {
	payload := map[string]any{
		"agent_id":    req.AgentID,
		"stance_role": req.StanceRole,
		"public_key":  req.PublicKey,
		"annotations": req.Annotations,
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/identity", payload)
	if err != nil {
		return Identity{}, err
	}
	if status/100 != 2 {
		return Identity{}, wrapHTTPErr("POST", "/v1/identity", status, body, nil)
	}
	var wire struct {
		DID          string    `json:"did"`
		SVIDBytes    []byte    `json:"svid_bytes"`
		RegisteredAt time.Time `json:"registered_at"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return Identity{}, fmt.Errorf("trustplane: decode identity response: %w", err)
	}
	return Identity{
		DID:          wire.DID,
		SVIDBytes:    wire.SVIDBytes,
		RegisteredAt: wire.RegisteredAt,
	}, nil
}

// AnchorAudit POST /v1/audit/anchor.
func (c *RealClient) AnchorAudit(ctx context.Context, root AuditRoot) (AuditAnchor, error) {
	payload := map[string]any{
		"ledger_id":  root.LedgerID,
		"root_hash":  root.RootHash,
		"emitted_at": root.EmittedAt,
		"meta":       root.Meta,
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/audit/anchor", payload)
	if err != nil {
		return AuditAnchor{}, err
	}
	if status/100 != 2 {
		return AuditAnchor{}, wrapHTTPErr("POST", "/v1/audit/anchor", status, body, nil)
	}
	var wire struct {
		AnchorID      string    `json:"anchor_id"`
		AnchoredAt    time.Time `json:"anchored_at"`
		TrustPlaneRef string    `json:"trustplane_ref"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return AuditAnchor{}, fmt.Errorf("trustplane: decode anchor response: %w", err)
	}
	return AuditAnchor{
		AnchorID:      wire.AnchorID,
		AnchoredAt:    wire.AnchoredAt,
		TrustPlaneRef: wire.TrustPlaneRef,
	}, nil
}

// RequestHITL POST /v1/hitl/request.
func (c *RealClient) RequestHITL(ctx context.Context, req HITLRequest) (HITLResponse, error) {
	payload := map[string]any{
		"agent_did":        req.AgentDID,
		"question":         req.Question,
		"context":          req.Context,
		"deadline_seconds": int(req.Deadline / time.Second),
		"annotations":      req.Annotations,
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/hitl/request", payload)
	if err != nil {
		return HITLResponse{}, err
	}
	if status/100 != 2 {
		return HITLResponse{}, wrapHTTPErr("POST", "/v1/hitl/request", status, body, nil)
	}
	var wire struct {
		Decision    string    `json:"decision"`
		Reasoning   string    `json:"reasoning"`
		ResponderID string    `json:"responder_id"`
		DecidedAt   time.Time `json:"decided_at"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return HITLResponse{}, fmt.Errorf("trustplane: decode HITL response: %w", err)
	}
	return HITLResponse{
		Decision:    wire.Decision,
		Reasoning:   wire.Reasoning,
		ResponderID: wire.ResponderID,
		DecidedAt:   wire.DecidedAt,
	}, nil
}

// LookupReputation GET /v1/reputation/{agent_did}.
func (c *RealClient) LookupReputation(ctx context.Context, agentDID string) (Reputation, error) {
	path := "/v1/reputation/" + url.PathEscape(agentDID)
	body, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Reputation{}, err
	}
	if status/100 != 2 {
		return Reputation{}, wrapHTTPErr("GET", path, status, body, nil)
	}
	var wire struct {
		AgentDID        string    `json:"agent_did"`
		Score           float64   `json:"score"`
		TotalHires      int       `json:"total_hires"`
		SuccessfulHires int       `json:"successful_hires"`
		LastRecordedAt  time.Time `json:"last_recorded_at"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return Reputation{}, fmt.Errorf("trustplane: decode reputation: %w", err)
	}
	return Reputation{
		AgentDID:        wire.AgentDID,
		Score:           wire.Score,
		TotalHires:      wire.TotalHires,
		SuccessfulHires: wire.SuccessfulHires,
		LastRecordedAt:  wire.LastRecordedAt,
	}, nil
}

// RecordReputation POST /v1/reputation. Returns 204 on success.
func (c *RealClient) RecordReputation(ctx context.Context, entry ReputationEntry) error {
	payload := map[string]any{
		"agent_did":    entry.AgentDID,
		"outcome":      entry.Outcome,
		"rating_delta": entry.RatingDelta,
		"note":         entry.Note,
		"recorded_at":  entry.RecordedAt,
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/reputation", payload)
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return wrapHTTPErr("POST", "/v1/reputation", status, body, nil)
	}
	return nil
}

// CreateDelegation POST /v1/delegation.
func (c *RealClient) CreateDelegation(ctx context.Context, req DelegationRequest) (Delegation, error) {
	payload := map[string]any{
		"from_did":       req.FromDID,
		"to_did":         req.ToDID,
		"scopes":         req.Scopes,
		"expiry_seconds": int(req.Expiry / time.Second),
		"parent_id":      req.ParentID,
		"annotations":    req.Annotations,
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/delegation", payload)
	if err != nil {
		return Delegation{}, err
	}
	if status/100 != 2 {
		return Delegation{}, wrapHTTPErr("POST", "/v1/delegation", status, body, nil)
	}
	var wire struct {
		DelegationID string    `json:"delegation_id"`
		Token        string    `json:"token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return Delegation{}, fmt.Errorf("trustplane: decode delegation: %w", err)
	}
	return Delegation{
		ID:        wire.DelegationID,
		Token:     wire.Token,
		ExpiresAt: wire.ExpiresAt,
	}, nil
}

// VerifyDelegation POST /v1/delegation/{id}/verify.
// Returns nil on 204; ErrDelegationInvalid on 403; generic httpError otherwise.
func (c *RealClient) VerifyDelegation(ctx context.Context, delegationID, delegateeID string) error {
	path := "/v1/delegation/" + url.PathEscape(delegationID) + "/verify"
	payload := map[string]any{"delegatee_did": delegateeID}
	body, status, err := c.do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	if status/100 == 2 {
		return nil
	}
	return wrapHTTPErr("POST", path, status, body, ErrDelegationInvalid)
}

// RevokeDelegation POST /v1/delegation/{id}/revoke. 204 on success.
func (c *RealClient) RevokeDelegation(ctx context.Context, delegationID string) error {
	path := "/v1/delegation/" + url.PathEscape(delegationID) + "/revoke"
	body, status, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return wrapHTTPErr("POST", path, status, body, nil)
	}
	return nil
}

// EvaluatePolicy POST /v1/policy/evaluate.
// 204 → nil; 403 → ErrPolicyDenied wrapped with body; other 4xx/5xx → httpError.
func (c *RealClient) EvaluatePolicy(ctx context.Context, req PolicyRequest) error {
	payload := map[string]any{
		"policy_bundle": req.PolicyBundle,
		"delegation_id": req.Delegation,
		"principal":     req.Principal,
		"action":        req.Action,
		"resource":      req.Resource,
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/policy/evaluate", payload)
	if err != nil {
		return err
	}
	if status/100 == 2 {
		return nil
	}
	return wrapHTTPErr("POST", "/v1/policy/evaluate", status, body, ErrPolicyDenied)
}

// Compile-time interface conformance. Keeps Client additions from
// silently leaving RealClient behind.
var _ Client = (*RealClient)(nil)
