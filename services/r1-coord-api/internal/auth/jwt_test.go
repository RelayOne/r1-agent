package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func newTestService() *JwtService {
	return NewJwtServiceHS256("r1-test", "r1-coord-api", []byte("super-secret-test-key-32-bytes-long"))
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s := newTestService()
	tok, err := s.Sign(Claims{Sub: "user-1", Email: "test@example.com", Roles: []string{"operator"}})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if strings.Count(tok, ".") != 2 {
		t.Fatalf("token does not have 3 parts: %q", tok)
	}
	got, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Sub != "user-1" || got.Email != "test@example.com" {
		t.Fatalf("unexpected claims: %+v", got)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "operator" {
		t.Fatalf("roles mismatch: %v", got.Roles)
	}
	if got.JTI == "" {
		t.Fatalf("expected JTI to be set")
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	signer := newTestService()
	tok, _ := signer.Sign(Claims{Sub: "user-1"})
	other := NewJwtServiceHS256("different-issuer", "r1-coord-api", signer.Secret)
	if _, err := other.Verify(tok); err == nil {
		t.Fatalf("expected verify to reject wrong issuer, got nil error")
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	signer := newTestService()
	tok, _ := signer.Sign(Claims{Sub: "user-1"})
	other := NewJwtServiceHS256("r1-test", "different-audience", signer.Secret)
	if _, err := other.Verify(tok); err == nil {
		t.Fatalf("expected verify to reject wrong audience, got nil error")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	signer := newTestService()
	tok, _ := signer.Sign(Claims{Sub: "user-1"})
	other := NewJwtServiceHS256("r1-test", "r1-coord-api", []byte("different-secret-32-bytes-aaaa"))
	if _, err := other.Verify(tok); err == nil {
		t.Fatalf("expected verify to reject wrong secret, got nil error")
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	s := newTestService()
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }
	tok, _ := s.Sign(Claims{Sub: "user-1"})

	// Advance the clock past expiry.
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0).Add(2 * time.Hour) }
	if _, err := s.Verify(tok); err == nil {
		t.Fatalf("expected expired token to fail verify, got nil error")
	}
}

func TestRefreshExtendsExpiry(t *testing.T) {
	s := newTestService()
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }
	tok, _ := s.Sign(Claims{Sub: "user-1"})

	// Advance 30 min — well within MaxAge.
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0).Add(30 * time.Minute) }
	refreshed, err := s.Refresh(tok)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	c, err := s.Verify(refreshed)
	if err != nil {
		t.Fatalf("Verify refreshed: %v", err)
	}
	if c.Iat != s.Now().Unix() {
		t.Fatalf("expected refreshed iat to be Now(), got %d", c.Iat)
	}
}

func TestRefreshRefusesPastMaxAge(t *testing.T) {
	s := newTestService()
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }
	tok, _ := s.Sign(Claims{Sub: "user-1"})

	// Advance past MaxAge (8h default).
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0).Add(9 * time.Hour) }
	if _, err := s.Refresh(tok); err == nil {
		t.Fatalf("expected refresh past max age to fail, got nil error")
	}
}

func TestRS256SignVerifyRoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	pubBytes, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	s, err := NewJwtServiceRS256("r1-test", "r1-coord-api", privPEM, pubPEM)
	if err != nil {
		t.Fatalf("NewJwtServiceRS256: %v", err)
	}
	tok, err := s.Sign(Claims{Sub: "user-rs", MSP: "msp-1"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.MSP != "msp-1" {
		t.Fatalf("MSP=%q want msp-1", c.MSP)
	}
}

func TestSignClampsExpiryToMaxAge(t *testing.T) {
	s := newTestService()
	s.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }
	// Try to sign with a 100-day expiry.
	tok, err := s.Sign(Claims{Sub: "user-1", Exp: time.Unix(1_000_000_000, 0).Add(100 * 24 * time.Hour).Unix()})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	c, _ := s.Verify(tok)
	expected := s.Now().Add(s.MaxAge).Unix()
	if c.Exp != expected {
		t.Errorf("Exp=%d, want clamp to %d", c.Exp, expected)
	}
}
