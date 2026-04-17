package trustplane

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) (*RealClient, *httptest.Server) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewRealClient(RealClientOptions{
		BaseURL:    srv.URL,
		PrivateKey: priv,
	})
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	return c, srv
}

func TestNewRealClient_RejectsBadInputs(t *testing.T) {
	if _, err := NewRealClient(RealClientOptions{BaseURL: ""}); err == nil {
		t.Errorf("expected error for empty BaseURL")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := NewRealClient(RealClientOptions{BaseURL: "https://x", PrivateKey: priv[:5]}); err == nil {
		t.Errorf("expected error for malformed private key")
	}
}

func TestRealClient_DPoPHeaderPresent(t *testing.T) {
	var seenDPoP string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenDPoP = r.Header.Get("DPoP")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"did":"did:tp:test","registered_at":"2026-01-01T00:00:00Z"}`))
	}))
	_, err := c.RegisterIdentity(context.Background(), IdentityRequest{AgentID: "a", StanceRole: "dev", PublicKey: "-----PEM-----"})
	if err != nil {
		t.Fatalf("RegisterIdentity: %v", err)
	}
	if seenDPoP == "" {
		t.Fatalf("DPoP header missing from request")
	}
	if strings.Count(seenDPoP, ".") != 2 {
		t.Errorf("DPoP header should be a 3-part JWT: %s", seenDPoP)
	}
}

func TestRealClient_CreateDelegation_SendsCorrectPayload(t *testing.T) {
	var seenBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/delegation" {
			t.Errorf("path = %s, want /v1/delegation", r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &seenBody)
		_, _ = w.Write([]byte(`{"delegation_id":"del-1","token":"tok","expires_at":"2026-01-01T01:00:00Z"}`))
	}))
	d, err := c.CreateDelegation(context.Background(), DelegationRequest{
		FromDID: "did:tp:a", ToDID: "did:tp:b",
		Scopes: []string{"read"},
		Expiry: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}
	if d.ID != "del-1" || d.Token != "tok" {
		t.Errorf("decoded delegation mismatch: %+v", d)
	}
	if seenBody["from_did"] != "did:tp:a" || seenBody["to_did"] != "did:tp:b" {
		t.Errorf("payload DIDs wrong: %v", seenBody)
	}
	if v, _ := seenBody["expiry_seconds"].(float64); int(v) != 900 {
		t.Errorf("expiry_seconds = %v, want 900", seenBody["expiry_seconds"])
	}
}

func TestRealClient_VerifyDelegation_MapsForbiddenToSentinel(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"revoked","message":"delegation revoked"}`))
	}))
	err := c.VerifyDelegation(context.Background(), "del-1", "did:tp:b")
	if err == nil {
		t.Fatalf("expected error on 403")
	}
	if !errors.Is(err, ErrDelegationInvalid) {
		t.Errorf("expected ErrDelegationInvalid, got: %v", err)
	}
}

func TestRealClient_VerifyDelegation_204IsNil(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.VerifyDelegation(context.Background(), "del-1", "did:tp:b"); err != nil {
		t.Fatalf("expected nil on 204, got: %v", err)
	}
}

func TestRealClient_EvaluatePolicy_403MapsToPolicyDenied(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"deny","message":"not allowed"}`))
	}))
	err := c.EvaluatePolicy(context.Background(), PolicyRequest{PolicyBundle: "x", Principal: "did:tp:a", Action: "y"})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Errorf("expected ErrPolicyDenied, got: %v", err)
	}
}

func TestRealClient_EvaluatePolicy_204IsNil(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.EvaluatePolicy(context.Background(), PolicyRequest{PolicyBundle: "x", Principal: "did:tp:a", Action: "y"}); err != nil {
		t.Fatalf("expected nil on 204, got: %v", err)
	}
}

func TestRealClient_LookupReputation(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/reputation/") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"agent_did":"did:tp:a","score":0.8,"total_hires":3,"successful_hires":2,"last_recorded_at":"2026-01-01T00:00:00Z"}`))
	}))
	r, err := c.LookupReputation(context.Background(), "did:tp:a")
	if err != nil {
		t.Fatalf("LookupReputation: %v", err)
	}
	if r.Score != 0.8 || r.TotalHires != 3 || r.SuccessfulHires != 2 {
		t.Errorf("decoded reputation mismatch: %+v", r)
	}
}

func TestRealClient_RevokeDelegation_204(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/revoke") {
			t.Errorf("path = %s, want *revoke", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.RevokeDelegation(context.Background(), "del-1"); err != nil {
		t.Fatalf("RevokeDelegation: %v", err)
	}
}

func TestRealClient_AnchorAudit_EncodesFields(t *testing.T) {
	var seen map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &seen)
		_, _ = w.Write([]byte(`{"anchor_id":"anc-1","anchored_at":"2026-01-01T00:00:00Z","trustplane_ref":"tp://anc/1"}`))
	}))
	anc, err := c.AnchorAudit(context.Background(), AuditRoot{
		LedgerID: "l1", RootHash: "deadbeef", EmittedAt: time.Unix(1700000000, 0),
		Meta: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("AnchorAudit: %v", err)
	}
	if anc.AnchorID != "anc-1" {
		t.Errorf("anchor_id = %s", anc.AnchorID)
	}
	if seen["ledger_id"] != "l1" || seen["root_hash"] != "deadbeef" {
		t.Errorf("body: %v", seen)
	}
}

func TestRealClient_NonSentinel4xxReturnsHTTPError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"bad","message":"malformed"}`))
	}))
	_, err := c.CreateDelegation(context.Background(), DelegationRequest{
		FromDID: "x", ToDID: "y", Scopes: []string{"z"}, Expiry: time.Minute,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var he *httpError
	if !errors.As(err, &he) {
		t.Fatalf("expected *httpError, got: %T %v", err, err)
	}
	if he.Status != 400 {
		t.Errorf("status = %d", he.Status)
	}
}
