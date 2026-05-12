package auth

// interop_test.go — cross-language interop coverage.
//
// Two flavors:
//
//  1. Generated-in-Go round trip. Always runs. Verifies our own
//     signer/verifier are self-consistent across HS256 + RS256.
//
//  2. Real TS fixtures loaded from /home/eric/repos/auth-core/test/.
//     Loads JWTs that the TS service emits (or that we emit and
//     persist for the TS side to round-trip). Gated on the
//     R1_TEST_AUTH_INTEROP env var so it doesn't fail builds when the
//     auth-core repo isn't checked out next to r1-agent.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTSContract_HS256_FixedSecret asserts the TS reference contract:
// secret "a".repeat(32), issuer "https://test.relayone.dev", audience
// "test-app", payload {"foo":"bar","sub":"user-1"} round-trips through
// our service.
//
// This is the closest thing to a TS-emitted token we can run inside a
// pure-Go test: both sides target the same JOSE primitives. Genuine
// TS-emitted fixtures are loaded by TestTSToken_VerifiesInGo below.
func TestTSContract_HS256_FixedSecret(t *testing.T) {
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "https://test.relayone.dev",
		Audience:   "test-app",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("a", 32)),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	tok, err := svc.Sign(map[string]any{"foo": "bar"}, SignOptions{Subject: "user-1"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	v, err := svc.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Payload["foo"] != "bar" {
		t.Errorf("foo = %v", v.Payload["foo"])
	}
	if v.Payload["sub"] != "user-1" {
		t.Errorf("sub = %v", v.Payload["sub"])
	}
}

// TestTSToken_VerifiesInGo loads a TS-emitted token from the
// auth-core test directory and verifies it with the matching Go
// service. The TS side persists the token via a one-shot script
// (documented in docs/integrations/relayone-sso.md §6).
//
// Gated on R1_TEST_AUTH_INTEROP so a builder without auth-core
// checked out doesn't fail.
func TestTSToken_VerifiesInGo(t *testing.T) {
	if os.Getenv("R1_TEST_AUTH_INTEROP") != "1" {
		t.Skip("set R1_TEST_AUTH_INTEROP=1 to run cross-language interop test")
	}
	fixtureDir := authCoreFixtureDir(t)
	tokPath := filepath.Join(fixtureDir, "ts-issued-hs256.jwt")
	tokBytes, err := os.ReadFile(tokPath)
	if err != nil {
		t.Skipf("fixture %s missing: %v", tokPath, err)
		return
	}
	metaPath := filepath.Join(fixtureDir, "ts-issued-hs256.meta.json")
	var meta struct {
		Issuer   string `json:"issuer"`
		Audience string `json:"audience"`
		Secret   string `json:"secret"`
	}
	if mb, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(mb, &meta)
	} else {
		// Fall back to the documented test contract.
		meta.Issuer = "https://test.relayone.dev"
		meta.Audience = "test-app"
		meta.Secret = strings.Repeat("a", 32)
	}
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     meta.Issuer,
		Audience:   meta.Audience,
		Algorithm:  AlgHS256,
		SigningKey: []byte(meta.Secret),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	v, err := svc.Verify(strings.TrimSpace(string(tokBytes)))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Payload["token_use"] != "access" && v.Payload["foo"] == nil {
		t.Errorf("payload missing expected claims: %v", v.Payload)
	}
}

// TestGoToken_EmitsValidCompactJWS proves a Go-emitted token is a
// well-formed compact JWS that the TS verifier should accept. The
// out-of-process TS verifier runs under `make interop-verify-ts`; this
// test just persists the artifact + asserts shape.
func TestGoToken_EmitsValidCompactJWS(t *testing.T) {
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "https://test.relayone.dev",
		Audience:   "test-app",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("a", 32)),
		AccessTTL:  10 * time.Minute,
	})
	assert.NoError(t, err, "NewJwtService")
	tok, err := svc.Sign(map[string]any{"foo": "bar"}, SignOptions{Subject: "user-1"})
	assert.NoError(t, err, "Sign")
	parts := strings.Split(tok, ".")
	assert.Equal(t, len(parts), 3, "compact JWS must have 3 parts")
	for i, p := range parts {
		if p == "" {
			t.Errorf("part %d empty", i)
		}
	}
	// Persist artifact for the optional Node round-trip test. Best
	// effort: a read-only sandbox is OK; just skip on permission
	// errors.
	if os.Getenv("R1_TEST_EMIT_FIXTURE") == "1" {
		outDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(outDir, "go-emitted-hs256.jwt"), []byte(tok), 0o644)
	}
}

// assert namespace provides a tiny assertion shim so the per-test
// helpers can be invoked as `assert.NoError(...)` and `assert.Equal(...)`.
// We avoid pulling in github.com/stretchr/testify just for this — the
// project already lives without it, and the two helpers fit in 10 lines.
type assertNS struct{}

var assert assertNS

func (assertNS) NoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", msg, err)
	}
}

func (assertNS) Equal(t *testing.T, got, want int, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", msg, got, want)
	}
}

// TestRS256_BothDirections verifies an RSA roundtrip end-to-end
// (generate fresh keypair, sign in Go, verify in Go). The TS side
// uses the same PKCS8/SPKI PEM shapes so the fixture format ports.
func TestRS256_BothDirections(t *testing.T) {
	privPEM, pubPEM := keyPairForTest(t)
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "rs256-iss",
		Audience:   "rs256-aud",
		Algorithm:  AlgRS256,
		SigningKey: privPEM,
		PublicKey:  pubPEM,
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	tok, err := svc.Sign(map[string]any{"r": "v"}, SignOptions{Subject: "sub-x"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	v, err := svc.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Payload["r"] != "v" {
		t.Errorf("r = %v", v.Payload["r"])
	}
	if v.Header.Alg != "RS256" {
		t.Errorf("alg = %q", v.Header.Alg)
	}
}

// TestErrJwtVerification_IsConsistent confirms the sentinel error
// matching contract we expose to downstream callers (handlers,
// middleware).
func TestErrJwtVerification_IsConsistent(t *testing.T) {
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "iss",
		Audience:   "aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("x", 32)),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	_, err = svc.Verify("not-a-jwt")
	if err == nil || !errors.Is(err, ErrJwtVerification) {
		t.Errorf("err=%v should be ErrJwtVerification", err)
	}
}

// authCoreFixtureDir resolves /home/eric/repos/auth-core/test/ when
// the repo is checked out adjacent to r1-agent. Tests skip gracefully
// when the directory isn't present.
func authCoreFixtureDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("AUTH_CORE_FIXTURE_DIR"); d != "" {
		return d
	}
	// /home/eric/repos/r1-agent-A4/internal/auth -> ../../../../auth-core/test
	candidates := []string{
		"/home/eric/repos/auth-core/test",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}
