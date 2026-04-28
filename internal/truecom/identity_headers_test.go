package truecom

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestSigner(t *testing.T) (*IdentitySigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := NewIdentitySigner("did:plc:stoke-test", priv)
	if err != nil {
		t.Fatalf("NewIdentitySigner: %v", err)
	}
	return s, pub
}

func TestNewIdentitySigner_RejectsBadInputs(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := NewIdentitySigner("", priv); err == nil {
		t.Error("expected error for empty DID")
	}
	if _, err := NewIdentitySigner("did:plc:x", priv[:8]); err == nil {
		t.Error("expected error for truncated private key")
	}
}

func TestIdentityHeaders_AllFourPresent(t *testing.T) {
	s, _ := newTestSigner(t)
	body := []byte(`{"capability":"translate"}`)
	h, err := s.BuildIdentityHeaders(http.MethodPost, "/v1/hire", body, "c-123")
	if err != nil {
		t.Fatalf("BuildIdentityHeaders: %v", err)
	}
	want := []string{HeaderDID, HeaderSignature, HeaderTimestamp, HeaderContractID}
	for _, k := range want {
		if h.Get(k) == "" {
			t.Errorf("missing required header %s (got headers: %v)", k, h)
		}
	}
	if got := h.Get(HeaderDID); got != "did:plc:stoke-test" {
		t.Errorf("DID header = %q, want did:plc:stoke-test", got)
	}
	if got := h.Get(HeaderContractID); got != "c-123" {
		t.Errorf("Contract-ID header = %q, want c-123", got)
	}
	// Timestamp should parse as a millisecond epoch within ±5s of now.
	ts, err := strconv.ParseInt(h.Get(HeaderTimestamp), 10, 64)
	if err != nil {
		t.Fatalf("parse timestamp: %v", err)
	}
	delta := time.Since(time.UnixMilli(ts))
	if delta < -5*time.Second || delta > 5*time.Second {
		t.Errorf("timestamp skew too large: %v", delta)
	}
}

func TestIdentityHeaders_EmptyContractIDOmitted(t *testing.T) {
	s, _ := newTestSigner(t)
	h, err := s.BuildIdentityHeaders(http.MethodPost, "/v1/hire", nil, "")
	if err != nil {
		t.Fatalf("BuildIdentityHeaders: %v", err)
	}
	if got := h.Get(HeaderContractID); got != "" {
		t.Errorf("Contract-ID should be omitted when blank, got %q", got)
	}
	// The other three remain.
	for _, k := range []string{HeaderDID, HeaderSignature, HeaderTimestamp} {
		if h.Get(k) == "" {
			t.Errorf("missing %s on blank-contract call", k)
		}
	}
}

func TestIdentityHeaders_SignatureVerifies(t *testing.T) {
	s, pub := newTestSigner(t)
	body := []byte(`{"capability":"translate","budget":0.25}`)
	method := http.MethodPost
	path := "/v1/hire"
	h, err := s.BuildIdentityHeaders(method, path, body, "contract-7")
	if err != nil {
		t.Fatalf("BuildIdentityHeaders: %v", err)
	}

	v := ReadIdentityHeaders(h)
	if v.DID == "" || v.Signature == "" || v.Timestamp == "" {
		t.Fatalf("ReadIdentityHeaders missing fields: %+v", v)
	}
	if err := VerifyIdentitySignature(pub, method, path, body, v); err != nil {
		t.Fatalf("VerifyIdentitySignature rejected our own signature: %v", err)
	}

	// Body tampering must invalidate the sig.
	tampered := append([]byte{}, body...)
	tampered[0] = 'X'
	if err := VerifyIdentitySignature(pub, method, path, tampered, v); err == nil {
		t.Error("expected signature verification to fail when body is tampered")
	}
	// Path tampering must invalidate the sig.
	if err := VerifyIdentitySignature(pub, method, "/v1/hire/other", body, v); err == nil {
		t.Error("expected signature verification to fail when path differs")
	}
	// Wrong public key must fail.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyIdentitySignature(otherPub, method, path, body, v); err == nil {
		t.Error("expected verification to fail with an unrelated public key")
	}
}

func TestIdentityHeaders_VerifyRejectsMalformedSig(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v := IdentityHeaderValues{
		DID:       "did:plc:x",
		Signature: "!!not base64!!",
		Timestamp: "1700000000000",
	}
	if err := VerifyIdentitySignature(pub, http.MethodGet, "/v1/x", nil, v); err == nil {
		t.Error("expected error for non-base64 signature")
	}
	// Missing signature.
	v.Signature = ""
	if err := VerifyIdentitySignature(pub, http.MethodGet, "/v1/x", nil, v); err == nil {
		t.Error("expected error for missing signature field")
	}
	// Wrong-length public key.
	v.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
	if err := VerifyIdentitySignature(pub[:8], http.MethodGet, "/v1/x", nil, v); err == nil {
		t.Error("expected error for malformed public key")
	}
}

func TestReadIdentityHeaders_AcceptsBothNamespaces(t *testing.T) {
	// Server should accept a request whose client wrote the legacy
	// X-TrustPlane-* names during the 30-day transition window.
	h := http.Header{}
	h.Set(legacyHeaderDID, "did:plc:legacy")
	h.Set(legacyHeaderSignature, "sig==")
	h.Set(legacyHeaderTimestamp, "1700000000000")
	h.Set(legacyHeaderContractID, "c-legacy")

	v := ReadIdentityHeaders(h)
	if v.DID != "did:plc:legacy" || v.Signature != "sig==" || v.Timestamp != "1700000000000" || v.ContractID != "c-legacy" {
		t.Fatalf("legacy-namespace parse wrong: %+v", v)
	}
	if !v.UsedLegacy {
		t.Error("UsedLegacy should be true when all fields came from X-TrustPlane-*")
	}

	// Canonical X-Truecom-* wins when both are present.
	h.Set(HeaderDID, "did:plc:canonical")
	v = ReadIdentityHeaders(h)
	if v.DID != "did:plc:canonical" {
		t.Errorf("canonical X-Truecom-DID should win: got %q", v.DID)
	}
	// UsedLegacy still reports true because three of four came from the
	// legacy namespace — useful for migration metrics.
	if !v.UsedLegacy {
		t.Error("UsedLegacy should remain true when mixed")
	}

	// Mixed-case header access (http.Header canonicalizes keys).
	h2 := http.Header{}
	h2.Set("x-truecom-did", "did:plc:canonical")
	v2 := ReadIdentityHeaders(h2)
	if v2.DID != "did:plc:canonical" {
		t.Errorf("http.Header canonicalization failed: %q", v2.DID)
	}
}

func TestApplyIdentityHeaders_OverwritesExisting(t *testing.T) {
	s, _ := newTestSigner(t)
	req, _ := http.NewRequest(http.MethodPost, "http://example/v1/hire", strings.NewReader("body"))
	req.Header.Set(HeaderDID, "stale")
	if err := s.ApplyIdentityHeaders(req, []byte("body"), "c-9"); err != nil {
		t.Fatalf("ApplyIdentityHeaders: %v", err)
	}
	if got := req.Header.Get(HeaderDID); got != "did:plc:stoke-test" {
		t.Errorf("DID not overwritten: %q", got)
	}
	if got := req.Header.Get(HeaderContractID); got != "c-9" {
		t.Errorf("Contract-ID = %q, want c-9", got)
	}
}
