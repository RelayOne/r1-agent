// Outbound /v1/hire + settlement + capability registration.
//
// Adds the hire-flow surface to RealClient. These calls layer identity
// headers (X-TrustPlane-*) on top of the DPoP proof that every call in
// real.go already carries. Identity headers are cheap enough
// (ed25519.Sign is sub-microsecond) that we attach them to every
// hire-flow call rather than piggybacking on DPoP — TrustPlane uses
// them to correlate request, contract, and instance without reparsing
// the JWT for every log line.
//
// Endpoints, matching TrustPlane's vendored OpenAPI:
//
//   POST /v1/hire              — hire a remote capability, returns contract.
//   POST /v1/capabilities      — register what this instance serves.
//   POST /v1/settlement/settle — release escrow on a completed contract.
//   POST /v1/settlement/dispute — withhold + file structured evidence.
//
// Callers: the hire engine (internal/hire) wires Settle/Dispute through
// the SettlementClient adapter further down this file, and agent-serve
// uses RegisterCapabilities on startup when --trustplane-register is
// set.

package trustplane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// base64RawURL is RFC 7517's base64url-without-padding encoding, used
// for JWK "x" coordinate serialization.
var base64RawURL = base64.RawURLEncoding

// bytesReader returns an io.Reader over b, or nil when b is
// nil/empty. Mirrors real.go's do() body-handling so both code paths
// emit the same "no Content-Length, no body" shape when the caller
// passes nil in.
func bytesReader(b []byte) io.Reader {
	if len(b) == 0 {
		return nil
	}
	return bytes.NewReader(b)
}

// HireRequest is the POST /v1/hire body.
type HireRequest struct {
	Capability string         `json:"capability"`
	Spec       string         `json:"spec,omitempty"`
	BudgetUSD  float64        `json:"budget_usd,omitempty"`
	PolicyRef  string         `json:"policy_ref,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// HireResult is the POST /v1/hire response.
type HireResult struct {
	ContractID string    `json:"contract_id"`
	AgentDID   string    `json:"agent_did"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// CapabilityRegistration is the POST /v1/capabilities body.
type CapabilityRegistration struct {
	DID          string          `json:"did"`
	AgentID      string          `json:"agent_id"`
	Version      string          `json:"version"`
	TaskTypes    []string        `json:"task_types"`
	Endpoint     string          `json:"endpoint,omitempty"`
	PublicKeyJWK json.RawMessage `json:"public_key_jwk"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// CapabilityRegistrationReceipt is the server's acknowledgement.
type CapabilityRegistrationReceipt struct {
	RegistrationID string    `json:"registration_id"`
	RegisteredAt   time.Time `json:"registered_at"`
}

// SettleRequestBody is the POST /v1/settlement/settle body.
type SettleRequestBody struct {
	ContractID string  `json:"contract_id"`
	AgentDID   string  `json:"agent_did,omitempty"`
	AmountUSD  float64 `json:"amount_usd"`
	Note       string  `json:"note,omitempty"`
}

// SettleResponseBody is the POST /v1/settlement/settle response.
type SettleResponseBody struct {
	SettlementID string `json:"settlement_id"`
	Note         string `json:"note,omitempty"`
}

// DisputeRequestBody is the POST /v1/settlement/dispute body. Shape
// mirrors hire.DisputeEvidence but lives here so the trustplane
// package doesn't import the hire package (avoids a cycle).
type DisputeRequestBody struct {
	ContractID        string            `json:"contract_id"`
	AgentDID          string            `json:"agent_did,omitempty"`
	Spec              string            `json:"spec,omitempty"`
	DeliverySample    []byte            `json:"delivery_sample,omitempty"`
	FailedCriterionID string            `json:"failed_criterion_id,omitempty"`
	FailedReason      string            `json:"failed_reason,omitempty"`
	Verdicts          []json.RawMessage `json:"verdicts,omitempty"`
}

// DisputeResponseBody is the POST /v1/settlement/dispute response.
type DisputeResponseBody struct {
	DisputeID string `json:"dispute_id"`
	Note      string `json:"note,omitempty"`
}

// WithIdentity attaches an IdentitySigner to an already-constructed
// RealClient. Returns the same pointer for chaining. The signer is
// optional: nil disables identity headers on outbound hire-flow calls
// (DPoP still runs — the gateway can fall back to JWT-only auth).
func (c *RealClient) WithIdentity(signer *IdentitySigner) *RealClient {
	c.identity = signer
	return c
}

// doHire is like do() but also emits the four X-TrustPlane-* identity
// headers when the client has an IdentitySigner attached. contractID
// may be empty (e.g. on POST /v1/hire itself, before a contract
// exists).
func (c *RealClient) doHire(ctx context.Context, method, path string, in any, contractID string) ([]byte, int, error) {
	fullURL := c.baseURL + path
	var bodyBytes []byte
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return nil, 0, fmt.Errorf("trustplane: marshal request: %w", err)
		}
		bodyBytes = buf
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytesReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("trustplane: build request: %w", err)
	}
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	proof, err := c.signer.Sign(method, fullURL)
	if err != nil {
		return nil, 0, fmt.Errorf("trustplane: DPoP sign: %w", err)
	}
	req.Header.Set("DPoP", proof)

	if c.identity != nil {
		if err := c.identity.ApplyIdentityHeaders(req, bodyBytes, contractID); err != nil {
			return nil, 0, fmt.Errorf("trustplane: identity headers: %w", err)
		}
	}

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

// Hire POST /v1/hire. Returns a HireResult on 2xx; httpError on
// anything else. Identity headers are emitted with an empty Contract-ID
// because the contract is what this call creates.
func (c *RealClient) Hire(ctx context.Context, req HireRequest) (HireResult, error) {
	if strings.TrimSpace(req.Capability) == "" {
		return HireResult{}, errors.New("trustplane: Hire requires capability")
	}
	body, status, err := c.doHire(ctx, http.MethodPost, "/v1/hire", req, "")
	if err != nil {
		return HireResult{}, err
	}
	if status/100 != 2 {
		return HireResult{}, wrapHTTPErr("POST", "/v1/hire", status, body, nil)
	}
	var out HireResult
	if err := json.Unmarshal(body, &out); err != nil {
		return HireResult{}, fmt.Errorf("trustplane: decode hire response: %w", err)
	}
	return out, nil
}

// RegisterCapabilities POST /v1/capabilities. Called on agent-serve
// startup when --trustplane-register is set, so the gateway learns
// what this instance can do and how to reach it.
func (c *RealClient) RegisterCapabilities(ctx context.Context, req CapabilityRegistration) (CapabilityRegistrationReceipt, error) {
	if strings.TrimSpace(req.DID) == "" {
		return CapabilityRegistrationReceipt{}, errors.New("trustplane: RegisterCapabilities requires DID")
	}
	if len(req.PublicKeyJWK) == 0 {
		return CapabilityRegistrationReceipt{}, errors.New("trustplane: RegisterCapabilities requires PublicKeyJWK")
	}
	body, status, err := c.doHire(ctx, http.MethodPost, "/v1/capabilities", req, "")
	if err != nil {
		return CapabilityRegistrationReceipt{}, err
	}
	if status/100 != 2 {
		return CapabilityRegistrationReceipt{}, wrapHTTPErr("POST", "/v1/capabilities", status, body, nil)
	}
	var out CapabilityRegistrationReceipt
	if err := json.Unmarshal(body, &out); err != nil {
		return CapabilityRegistrationReceipt{}, fmt.Errorf("trustplane: decode capabilities response: %w", err)
	}
	return out, nil
}

// Settle POST /v1/settlement/settle. Called by agent-serve on task
// completion (and by hire.Engine.VerifyAndSettle on acceptance).
func (c *RealClient) Settle(ctx context.Context, req SettleRequestBody) (SettleResponseBody, error) {
	if strings.TrimSpace(req.ContractID) == "" {
		return SettleResponseBody{}, errors.New("trustplane: Settle requires contract_id")
	}
	body, status, err := c.doHire(ctx, http.MethodPost, "/v1/settlement/settle", req, req.ContractID)
	if err != nil {
		return SettleResponseBody{}, err
	}
	if status/100 != 2 {
		return SettleResponseBody{}, wrapHTTPErr("POST", "/v1/settlement/settle", status, body, nil)
	}
	var out SettleResponseBody
	if err := json.Unmarshal(body, &out); err != nil {
		return SettleResponseBody{}, fmt.Errorf("trustplane: decode settle response: %w", err)
	}
	return out, nil
}

// Dispute POST /v1/settlement/dispute. Called by agent-serve on task
// failure and by hire.Engine.VerifyAndSettle on AC rejection.
func (c *RealClient) Dispute(ctx context.Context, req DisputeRequestBody) (DisputeResponseBody, error) {
	if strings.TrimSpace(req.ContractID) == "" {
		return DisputeResponseBody{}, errors.New("trustplane: Dispute requires contract_id")
	}
	body, status, err := c.doHire(ctx, http.MethodPost, "/v1/settlement/dispute", req, req.ContractID)
	if err != nil {
		return DisputeResponseBody{}, err
	}
	if status/100 != 2 {
		return DisputeResponseBody{}, wrapHTTPErr("POST", "/v1/settlement/dispute", status, body, nil)
	}
	var out DisputeResponseBody
	if err := json.Unmarshal(body, &out); err != nil {
		return DisputeResponseBody{}, fmt.Errorf("trustplane: decode dispute response: %w", err)
	}
	return out, nil
}

// BuildPublicKeyJWK returns the RFC 7517 JSON Web Key for an Ed25519
// public key. Used by agent-serve to embed its registered pubkey in the
// capabilities record so the gateway can verify identity headers
// without a separate lookup.
func BuildPublicKeyJWK(pub ed25519.PublicKey) (json.RawMessage, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("trustplane: JWK: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	jwk := map[string]string{
		"kty": "OKP",
		"crv": "Ed25519",
		"x":   base64RawURL.EncodeToString(pub),
	}
	buf, err := json.Marshal(jwk)
	if err != nil {
		return nil, fmt.Errorf("trustplane: marshal JWK: %w", err)
	}
	return buf, nil
}
