package auth

// jwt_test.go — unit tests for the JwtService Go port. Mirrors
// auth-core/test/jwt-service.test.ts case-for-case so a TS engineer
// can read both side-by-side.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

const testSecret32 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newHS256ServiceT(t *testing.T) *JwtService {
	t.Helper()
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "https://test.relayone.dev",
		Audience:   "test-app",
		Algorithm:  AlgHS256,
		SigningKey: []byte(testSecret32),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	return svc
}

func TestSignVerifyRoundTrip_HS256(t *testing.T) {
	svc := newHS256ServiceT(t)
	tok, err := svc.Sign(map[string]any{"foo": "bar"}, SignOptions{Subject: "user-1"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	v, err := svc.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Payload["foo"] != "bar" {
		t.Errorf("payload[foo] = %v, want bar", v.Payload["foo"])
	}
	if v.Payload["sub"] != "user-1" {
		t.Errorf("payload[sub] = %v, want user-1", v.Payload["sub"])
	}
	if v.Header.Alg != "HS256" {
		t.Errorf("header.alg = %q, want HS256", v.Header.Alg)
	}
}

func TestWrongIssuerRejected(t *testing.T) {
	svc := newHS256ServiceT(t)
	tok, err := svc.Sign(map[string]any{}, SignOptions{Subject: "s"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	other, err := NewJwtService(JwtServiceOptions{
		Issuer:     "https://different.dev",
		Audience:   "test-app",
		Algorithm:  AlgHS256,
		SigningKey: []byte(testSecret32),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	if _, err := other.Verify(tok); !errors.Is(err, ErrJwtVerification) {
		t.Errorf("want ErrJwtVerification, got %v", err)
	}
}

func TestIssuePairAndRefresh(t *testing.T) {
	svc := newHS256ServiceT(t)
	pair, err := svc.IssuePair(map[string]any{"role": "admin"}, "user-7")
	if err != nil {
		t.Fatalf("IssuePair: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("empty token in pair")
	}
	refreshed, err := svc.RefreshAccess(pair.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshAccess: %v", err)
	}
	v, err := svc.Verify(refreshed.AccessToken)
	if err != nil {
		t.Fatalf("Verify refreshed: %v", err)
	}
	if v.Payload["role"] != "admin" {
		t.Errorf("role = %v, want admin", v.Payload["role"])
	}
	if v.Payload["token_use"] != "access" {
		t.Errorf("token_use = %v, want access", v.Payload["token_use"])
	}
}

func TestRefuseRefreshFromAccessToken(t *testing.T) {
	svc := newHS256ServiceT(t)
	pair, err := svc.IssuePair(map[string]any{}, "u")
	if err != nil {
		t.Fatalf("IssuePair: %v", err)
	}
	_, err = svc.RefreshAccess(pair.AccessToken)
	if !errors.Is(err, ErrNotRefreshToken) {
		t.Errorf("want ErrNotRefreshToken, got %v", err)
	}
	// ErrNotRefreshToken wraps ErrJwtVerification so callers can
	// errors.Is against either.
	if !errors.Is(err, ErrJwtVerification) {
		t.Errorf("ErrNotRefreshToken should wrap ErrJwtVerification, got %v", err)
	}
}

func TestRS256Roundtrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "rs256-iss",
		Audience:   "rs256-aud",
		Algorithm:  AlgRS256,
		SigningKey: privPEM,
		PublicKey:  pubPEM,
	})
	if err != nil {
		t.Fatalf("NewJwtService RS256: %v", err)
	}
	tok, err := svc.Sign(map[string]any{"x": 1}, SignOptions{Subject: "u"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	v, err := svc.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// JSON numbers decode as float64.
	got, ok := v.Payload["x"]
	if !ok {
		t.Fatalf("payload missing x")
	}
	switch g := got.(type) {
	case float64:
		if g != 1 {
			t.Errorf("x = %v, want 1", g)
		}
	case int64:
		if g != 1 {
			t.Errorf("x = %v, want 1", g)
		}
	case int:
		if g != 1 {
			t.Errorf("x = %v, want 1", g)
		}
	default:
		t.Errorf("x = %v (type %T), want numeric 1", got, got)
	}
	if v.Header.Alg != "RS256" {
		t.Errorf("alg = %q, want RS256", v.Header.Alg)
	}
}

func TestJwtServiceFromEnv_RejectsMissingIssuer(t *testing.T) {
	_, err := JwtServiceFromEnv(func(string) string { return "" })
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "AUTH_JWT_ISSUER") {
		t.Errorf("error %q should mention AUTH_JWT_ISSUER", err)
	}
}

func TestJwtServiceFromEnv_BuildsHS256(t *testing.T) {
	env := map[string]string{
		"AUTH_JWT_ISSUER":   "iss",
		"AUTH_JWT_AUDIENCE": "aud",
		"AUTH_JWT_SECRET":   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	svc, err := JwtServiceFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("JwtServiceFromEnv: %v", err)
	}
	tok, err := svc.Sign(map[string]any{}, SignOptions{Subject: "u"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
}

func TestJwtServiceFromEnv_RS256RequiresPrivate(t *testing.T) {
	env := map[string]string{
		"AUTH_JWT_ISSUER":     "iss",
		"AUTH_JWT_AUDIENCE":   "aud",
		"AUTH_JWT_PUBLIC_KEY": "-----BEGIN PUBLIC KEY-----\n-----END PUBLIC KEY-----",
	}
	_, err := JwtServiceFromEnv(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "AUTH_JWT_PRIVATE_KEY") {
		t.Errorf("want AUTH_JWT_PRIVATE_KEY error, got %v", err)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "iss",
		Audience:   "aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(testSecret32),
		AccessTTL:  100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	tok, _ := svc.Sign(map[string]any{}, SignOptions{Subject: "u"})
	time.Sleep(200 * time.Millisecond)
	if _, err := svc.Verify(tok); !errors.Is(err, ErrJwtVerification) {
		t.Errorf("want ErrJwtVerification on expired, got %v", err)
	}
}

func TestPayloadCannotOverwriteStandardClaims(t *testing.T) {
	svc := newHS256ServiceT(t)
	tok, err := svc.Sign(map[string]any{"iss": "evil.example", "foo": "bar"}, SignOptions{Subject: "u"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	v, err := svc.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Payload["iss"] != "https://test.relayone.dev" {
		t.Errorf("iss = %v, want service-configured issuer (caller payload must NOT override)", v.Payload["iss"])
	}
	if v.Payload["foo"] != "bar" {
		t.Errorf("foo = %v, want bar", v.Payload["foo"])
	}
}

func TestDeriveTenantHMACSecret_Deterministic(t *testing.T) {
	rootSecret := []byte("root-root-root-root-root-root-root")
	a := DeriveTenantHMACSecret(rootSecret, "tenant-A")
	b := DeriveTenantHMACSecret(rootSecret, "tenant-A")
	c := DeriveTenantHMACSecret(rootSecret, "tenant-B")
	if len(a) != 32 {
		t.Errorf("len = %d, want 32", len(a))
	}
	if string(a) != string(b) {
		t.Error("derivation not deterministic for same tenant")
	}
	if string(a) == string(c) {
		t.Error("different tenants yielded same secret")
	}
}

func TestDeriveTenantKID(t *testing.T) {
	rootKID := "abc123"
	a := DeriveTenantKID(rootKID, "tenant-A")
	b := DeriveTenantKID(rootKID, "tenant-A")
	c := DeriveTenantKID(rootKID, "tenant-B")
	if a != b {
		t.Error("non-deterministic")
	}
	if a == c {
		t.Error("different tenants collided")
	}
	if !strings.HasPrefix(a, rootKID+"-t-") {
		t.Errorf("got %q, want prefix %q-t-", a, rootKID)
	}
}
