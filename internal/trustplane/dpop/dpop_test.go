package dpop

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func mustKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func decodeJWT(t *testing.T, tok string) (header, payload map[string]any, sig []byte) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt should have 3 parts, got %d", len(parts))
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if err := json.Unmarshal(pb, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return
}

func TestSign_JWTStructure(t *testing.T) {
	_, priv := mustKey(t)
	s, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	tok, err := s.Sign("POST", "https://gateway.trustplane.dev/v1/delegation")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	header, payload, _ := decodeJWT(t, tok)

	if header["typ"] != "dpop+jwt" {
		t.Errorf("typ = %v, want dpop+jwt", header["typ"])
	}
	if header["alg"] != "EdDSA" {
		t.Errorf("alg = %v, want EdDSA", header["alg"])
	}
	jwk, ok := header["jwk"].(map[string]any)
	if !ok {
		t.Fatalf("jwk header missing or wrong type: %v", header["jwk"])
	}
	if jwk["kty"] != "OKP" || jwk["crv"] != "Ed25519" {
		t.Errorf("jwk kty/crv = %v/%v, want OKP/Ed25519", jwk["kty"], jwk["crv"])
	}
	if _, ok := jwk["x"].(string); !ok {
		t.Errorf("jwk.x missing")
	}

	if payload["htm"] != "POST" {
		t.Errorf("htm = %v, want POST", payload["htm"])
	}
	if payload["htu"] != "https://gateway.trustplane.dev/v1/delegation" {
		t.Errorf("htu = %v, want gateway URL", payload["htu"])
	}
	if _, ok := payload["jti"].(string); !ok {
		t.Errorf("jti missing")
	}
	if _, ok := payload["iat"].(float64); !ok {
		t.Errorf("iat missing or not numeric")
	}
}

func TestSign_SignatureVerifies(t *testing.T) {
	pub, priv := mustKey(t)
	s, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	tok, err := s.Sign("GET", "https://gateway.trustplane.dev/v1/reputation/did:tp:x")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(tok, ".")
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		t.Fatalf("signature does not verify against embedded public key")
	}
}

func TestSign_JTIUniqueness(t *testing.T) {
	_, priv := mustKey(t)
	s, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	seen := make(map[string]bool, 50)
	for i := 0; i < 50; i++ {
		tok, err := s.Sign("POST", "https://x/v1/y")
		if err != nil {
			t.Fatalf("Sign iter %d: %v", i, err)
		}
		_, payload, _ := decodeJWT(t, tok)
		jti, ok := payload["jti"].(string)
		if !ok {
			t.Fatalf("jti: unexpected type: %T", payload["jti"])
		}
		if seen[jti] {
			t.Fatalf("jti collision at iter %d: %s", i, jti)
		}
		seen[jti] = true
	}
}

func TestSign_MethodUpcased(t *testing.T) {
	_, priv := mustKey(t)
	s, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	tok, err := s.Sign("post", "https://x/v1/y")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, payload, _ := decodeJWT(t, tok)
	if payload["htm"] != "POST" {
		t.Errorf("htm = %v, want POST (lower-case input should be upcased)", payload["htm"])
	}
}

func TestNewSigner_WrongKeyLength(t *testing.T) {
	if _, err := NewSigner(ed25519.PrivateKey{1, 2, 3}); err == nil {
		t.Fatalf("expected error for short key, got nil")
	}
}

func TestNewSigner_NilKey(t *testing.T) {
	if _, err := NewSigner(nil); err == nil {
		t.Fatalf("expected error for nil key, got nil")
	}
}

func TestSign_EmbeddedJWKMatchesPublicKey(t *testing.T) {
	pub, priv := mustKey(t)
	s, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	tok, err := s.Sign("POST", "https://x/v1/y")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	header, _, _ := decodeJWT(t, tok)
	jwk, ok := header["jwk"].(map[string]any)
	if !ok {
		t.Fatalf("jwk: unexpected type: %T", header["jwk"])
	}
	jwkX, ok := jwk["x"].(string)
	if !ok {
		t.Fatalf("jwk.x: unexpected type: %T", jwk["x"])
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(jwkX)
	if err != nil {
		t.Fatalf("decode jwk.x: %v", err)
	}
	if string(xBytes) != string(pub) {
		t.Errorf("embedded public key bytes do not match generated public key")
	}
}
