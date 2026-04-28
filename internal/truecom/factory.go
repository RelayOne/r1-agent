package truecom

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/RelayOne/r1/internal/secrets"
)

// Mode selects which Client implementation NewFromEnv returns.
//
//   - ModeStub (default): in-memory StubClient. No network. Safe for
//     local dev, CI unit tests, and the zero-configuration stoke-mcp
//     startup path.
//   - ModeReal: RealClient against a live TrustPlane gateway. Requires
//     STOKE_TRUSTPLANE_URL + an Ed25519 private key.
//
// Any other value is an error at construction time — better to fail
// fast than silently fall through to Stub in prod.
type Mode string

const (
	ModeStub Mode = "stub"
	ModeReal Mode = "real"

	// EnvMode gates which implementation is built. Empty → ModeStub.
	EnvMode = "STOKE_TRUSTPLANE_MODE"

	// EnvURL is the gateway base URL for ModeReal.
	EnvURL = "STOKE_TRUSTPLANE_URL"

	// EnvURLCanonical is the cross-portfolio canonical name per
	// Truecom Task 23. envOrFallback accepts either EnvURL (legacy)
	// or EnvURLCanonical for a 90d dual-accept window closing
	// 2026-07-21. Setting both → canonical wins, no WARN log.
	EnvURLCanonical = "TRUECOM_API_URL"

	// EnvPrivKey / EnvPrivKey_FILE: Ed25519 private key, PEM-encoded
	// PKCS#8, resolved via internal/secrets (inline → env → file).
	// The helper reads EnvPrivKey inline and falls through to the
	// _FILE suffix per its contract.
	EnvPrivKey = "STOKE_TRUSTPLANE_PRIVKEY"

	// EnvPrivKeyCanonical mirrors EnvURLCanonical for the API key.
	EnvPrivKeyCanonical = "TRUECOM_API_KEY"
)

// NewFromEnv returns a trustplane.Client configured from environment
// variables. Resolution order:
//
//  1. STOKE_TRUSTPLANE_MODE=stub (or unset) → StubClient.
//  2. STOKE_TRUSTPLANE_MODE=real → RealClient wired to
//     STOKE_TRUSTPLANE_URL + decoded Ed25519 key from
//     STOKE_TRUSTPLANE_PRIVKEY / _FILE.
//
// Returns (stub, nil) for the stub path so local-dev callers never
// have to branch on err themselves. Returns (nil, err) only when the
// user asked for real mode and something is wrong with the
// configuration (missing URL, unreadable key file, malformed PEM).
//
// This is the single entry point stoke-mcp / CLI uses. Tests construct
// StubClient / RealClient directly.
func NewFromEnv() (Client, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(os.Getenv(EnvMode))))
	if mode == "" {
		mode = ModeStub
	}
	switch mode {
	case ModeStub:
		return NewStubClient(), nil
	case ModeReal:
		return newRealFromEnv()
	default:
		return nil, fmt.Errorf("trustplane: unknown %s=%q (want \"stub\" or \"real\")", EnvMode, string(mode))
	}
}

func newRealFromEnv() (*RealClient, error) {
	// Accept canonical TRUECOM_* env names OR legacy STOKE_TRUSTPLANE_*
	// during the 90d dual-accept window closing 2026-07-21. A WARN log
	// fires whenever the legacy name supplied the value.
	base := envOrFallback(EnvURLCanonical, EnvURL)
	if base == "" {
		return nil, fmt.Errorf("trustplane: %s=real requires %s (or legacy %s)", EnvMode, EnvURLCanonical, EnvURL)
	}
	// Prefer canonical for inline key; fall back to legacy. The
	// secrets resolver still handles the _FILE suffix below.
	keyInline := envOrFallback(EnvPrivKeyCanonical, EnvPrivKey)
	keyPEM, err := secrets.ResolveRequired(keyInline, EnvPrivKey)
	if err != nil {
		return nil, fmt.Errorf("trustplane: resolve %s: %w", EnvPrivKey, err)
	}
	priv, err := parseEd25519PEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("trustplane: parse %s: %w", EnvPrivKey, err)
	}
	return NewRealClient(RealClientOptions{
		BaseURL:    base,
		PrivateKey: priv,
	})
}

// parseEd25519PEM decodes a PEM block containing a PKCS#8-wrapped
// Ed25519 private key (what `openssl genpkey -algorithm ed25519`
// emits). Returns a clean, specific error on any of:
//
//   - empty / whitespace input
//   - non-PEM input
//   - PEM parses but not PKCS#8 / not Ed25519
//
// Operators running RealClient in prod will see exactly which step
// failed, which saves a lot of "why is auth broken" time.
func parseEd25519PEM(pemStr string) (ed25519.PrivateKey, error) {
	trimmed := strings.TrimSpace(pemStr)
	if trimmed == "" {
		return nil, fmt.Errorf("empty PEM input")
	}
	block, _ := pem.Decode([]byte(trimmed))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PEM key is not Ed25519 (got %T)", key)
	}
	return priv, nil
}
