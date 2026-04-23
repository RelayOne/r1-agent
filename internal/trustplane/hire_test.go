package trustplane

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newHireClient builds a RealClient wired to an httptest server and an
// IdentitySigner so outbound hire-flow calls carry identity headers.
// Returns both the client and the public key so tests can verify the
// signature the server saw.
func newHireClient(t *testing.T, handler http.Handler) (*RealClient, *httptest.Server, ed25519.PublicKey) {
	t.Helper()
	dpopPub, dpopPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("dpop key: %v", err)
	}
	_ = dpopPub
	identPub, identPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("identity key: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewRealClient(RealClientOptions{BaseURL: srv.URL, PrivateKey: dpopPriv})
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	signer, err := NewIdentitySigner("did:plc:stoke-test", identPriv)
	if err != nil {
		t.Fatalf("NewIdentitySigner: %v", err)
	}
	c.WithIdentity(signer)
	return c, srv, identPub
}

func TestRealClient_Hire_EmitsIdentityHeaders(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	c, _, pub := newHireClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hire" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"contract_id":"c-abc","agent_did":"did:plc:remote","accepted_at":"2026-04-22T00:00:00Z"}`))
	}))

	result, err := c.Hire(context.Background(), HireRequest{
		Capability: "translate",
		BudgetUSD:  0.25,
		PolicyRef:  "personal-assistant",
	})
	if err != nil {
		t.Fatalf("Hire: %v", err)
	}
	if result.ContractID != "c-abc" {
		t.Errorf("contract_id = %q", result.ContractID)
	}

	// All four required headers present (Contract-ID is allowed to be
	// empty on /v1/hire because the contract does not exist yet).
	for _, k := range []string{HeaderDID, HeaderSignature, HeaderTimestamp} {
		if gotHeaders.Get(k) == "" {
			t.Errorf("missing outbound header %s", k)
		}
	}
	if got := gotHeaders.Get(HeaderContractID); got != "" {
		t.Errorf("Contract-ID should be empty on /v1/hire, got %q", got)
	}

	// DPoP header still present.
	if gotHeaders.Get("DPoP") == "" {
		t.Error("DPoP header missing — identity headers must not displace DPoP")
	}

	// Signature verifies against the canonical input the client built.
	v := ReadIdentityHeaders(gotHeaders)
	if err := VerifyIdentitySignature(pub, http.MethodPost, "/v1/hire", gotBody, v); err != nil {
		t.Errorf("VerifyIdentitySignature: %v", err)
	}
}

func TestRealClient_Settle_IncludesContractID(t *testing.T) {
	var gotHeaders http.Header
	c, _, _ := newHireClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"settlement_id":"s-1","note":"ok"}`))
	}))
	receipt, err := c.Settle(context.Background(), SettleRequestBody{
		ContractID: "c-123",
		AmountUSD:  0.25,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if receipt.SettlementID != "s-1" {
		t.Errorf("settlement_id = %q", receipt.SettlementID)
	}
	if got := gotHeaders.Get(HeaderContractID); got != "c-123" {
		t.Errorf("Contract-ID header = %q, want c-123", got)
	}
}

func TestRealClient_Dispute_IncludesContractIDAndEvidence(t *testing.T) {
	var gotBody DisputeRequestBody
	var gotHeaders http.Header
	c, _, _ := newHireClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &gotBody)
		_, _ = w.Write([]byte(`{"dispute_id":"d-1","note":"filed"}`))
	}))
	_, err := c.Dispute(context.Background(), DisputeRequestBody{
		ContractID:        "c-fail",
		FailedCriterionID: "delivery-complete",
		FailedReason:      "empty",
	})
	if err != nil {
		t.Fatalf("Dispute: %v", err)
	}
	if gotBody.ContractID != "c-fail" || gotBody.FailedCriterionID != "delivery-complete" {
		t.Errorf("body echo wrong: %+v", gotBody)
	}
	if got := gotHeaders.Get(HeaderContractID); got != "c-fail" {
		t.Errorf("Contract-ID = %q", got)
	}
}

func TestRealClient_RegisterCapabilities_BodyShape(t *testing.T) {
	var gotBody CapabilityRegistration
	c, _, pub := newHireClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/capabilities" {
			t.Errorf("path = %s", r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(buf, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"registration_id":"reg-1","registered_at":"2026-04-22T00:00:00Z"}`))
	}))
	jwk, err := BuildPublicKeyJWK(pub)
	if err != nil {
		t.Fatalf("BuildPublicKeyJWK: %v", err)
	}
	receipt, err := c.RegisterCapabilities(context.Background(), CapabilityRegistration{
		DID:          "did:plc:stoke-test",
		AgentID:      "stoke-abc",
		Version:      "1.0.0",
		TaskTypes:    []string{"research", "deploy"},
		Endpoint:     "https://stoke.local:8440",
		PublicKeyJWK: jwk,
	})
	if err != nil {
		t.Fatalf("RegisterCapabilities: %v", err)
	}
	if receipt.RegistrationID != "reg-1" {
		t.Errorf("registration_id = %q", receipt.RegistrationID)
	}
	if gotBody.DID != "did:plc:stoke-test" {
		t.Errorf("DID field = %q", gotBody.DID)
	}
	if len(gotBody.TaskTypes) != 2 {
		t.Errorf("task_types len = %d", len(gotBody.TaskTypes))
	}
	// Public key JWK is present and well-formed.
	if len(gotBody.PublicKeyJWK) == 0 {
		t.Fatal("public_key_jwk missing from request body")
	}
	var jwkOut map[string]string
	if err := json.Unmarshal(gotBody.PublicKeyJWK, &jwkOut); err != nil {
		t.Fatalf("decode JWK: %v", err)
	}
	if jwkOut["kty"] != "OKP" || jwkOut["crv"] != "Ed25519" || jwkOut["x"] == "" {
		t.Errorf("JWK shape wrong: %+v", jwkOut)
	}
}

func TestBuildPublicKeyJWK_RejectsWrongSize(t *testing.T) {
	if _, err := BuildPublicKeyJWK([]byte{1, 2, 3}); err == nil {
		t.Error("expected error for short public key")
	}
}

func TestRealClient_Hire_RejectsEmptyCapability(t *testing.T) {
	c, _, _ := newHireClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))
	if _, err := c.Hire(context.Background(), HireRequest{}); err == nil {
		t.Error("expected error for empty capability")
	}
}

func TestRealClient_Settle_RejectsEmptyContractID(t *testing.T) {
	c, _, _ := newHireClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be reached")
	}))
	if _, err := c.Settle(context.Background(), SettleRequestBody{}); err == nil {
		t.Error("expected error for empty contract_id")
	}
	if _, err := c.Dispute(context.Background(), DisputeRequestBody{}); err == nil {
		t.Error("expected error for empty dispute contract_id")
	}
}

// Probe: time.Time comparison sanity so the import is used and the
// AcceptedAt decode round-trips.
func TestHireResult_TimeDecodes(t *testing.T) {
	want := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	buf := []byte(`{"contract_id":"c","agent_did":"d","accepted_at":"2026-04-22T00:00:00Z"}`)
	var r HireResult
	if err := json.Unmarshal(buf, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !r.AcceptedAt.Equal(want) {
		t.Errorf("AcceptedAt = %v, want %v", r.AcceptedAt, want)
	}
}
