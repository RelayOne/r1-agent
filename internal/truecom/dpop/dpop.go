// Package dpop implements an RFC 9449 DPoP proof-of-possession signer
// over Ed25519. Every RealClient HTTP request to the TrustPlane
// gateway carries a DPoP header produced by Sign(method, url). The
// gateway validates the JWT, verifies the public JWK against the one
// registered at identity creation, rejects jti replays, and binds the
// request to the Ed25519 key pair.
//
// Why stdlib-only: Stoke's posture is zero Go-module coupling to
// TrustPlane. We already hand-write the HTTP layer against the
// vendored OpenAPI spec; the DPoP bit is 50 lines of JOSE with
// Ed25519 and a base64-url encoder. Pulling go-jose or square/go-jose
// would add ~60 kloc of transitive deps to resolve EdDSA signing that
// is trivially expressed with crypto/ed25519.
//
// The JWT this package emits:
//
//	header  = {"typ":"dpop+jwt","alg":"EdDSA","jwk":{<OKP+Ed25519 public>}}
//	payload = {"jti":<random>,"htm":<method>,"htu":<url>,"iat":<now>}
//	sig     = ed25519(key, base64url(header).base64url(payload))
//
// RFC 9449 compliance:
//
//   - typ=dpop+jwt (§4.2)
//   - htm + htu claims present (§4.2)
//   - iat claim present (§4.2)
//   - jti is 16 random bytes base64url-encoded — unique per-request;
//     gateway replay cache handles jti dedup (§11.1)
//   - jwk embedded (§4.2); receiver derives the thumbprint if needed
//
// What this package does NOT do:
//
//   - ath (access-token hash): Stoke doesn't use DPoP-bound access
//     tokens; it's DPoP-only. If TrustPlane adds bound-token flows,
//     extend Signer with WithAccessToken.
//   - nonce (§8): the gateway may require a DPoP-Nonce value echoed
//     back on retry. Currently unimplemented; gateway has not demanded
//     it on any request we've seen. Plumb via WithNonce when needed.
package dpop

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Signer produces DPoP proofs for a single Ed25519 key pair. Safe for
// concurrent use — the only mutable state is time.Now and
// crypto/rand.Reader, both concurrency-safe.
type Signer struct {
	priv ed25519.PrivateKey
	// Precomputed header bytes reused across every proof. The JWK
	// embed doesn't change once the key is fixed so we JSON-encode
	// once and base64url it once.
	headerB64 string
}

// NewSigner builds a Signer from an Ed25519 key pair. The private key
// must be a 64-byte ed25519.PrivateKey (seed + public). Returns an
// error when the key length is wrong — surfacing this at construction
// time, not signing time, keeps per-request error paths clean.
func NewSigner(priv ed25519.PrivateKey) (*Signer, error) {
	if l := len(priv); l != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("dpop: ed25519 private key must be %d bytes, got %d", ed25519.PrivateKeySize, l)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("dpop: private key has non-ed25519 public half")
	}
	jwk := map[string]string{
		"kty": "OKP",
		"crv": "Ed25519",
		"x":   b64url(pub),
	}
	header := map[string]any{
		"typ": "dpop+jwt",
		"alg": "EdDSA",
		"jwk": jwk,
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("dpop: marshal header: %w", err)
	}
	return &Signer{priv: priv, headerB64: b64url(hb)}, nil
}

// Sign produces a DPoP proof JWT bound to (method, url). method is
// normalized to upper-case (§4.2: "The value of the HTTP method of
// the request... in upper case"). url is used verbatim (including
// query string, excluding fragment — caller strips #fragments before
// passing).
//
// Each call generates a fresh jti; callers should not reuse a proof
// across requests even for idempotent GETs, since the gateway replay
// cache rejects duplicates.
func (s *Signer) Sign(method, url string) (string, error) {
	jtiBytes := make([]byte, 16)
	if _, err := rand.Read(jtiBytes); err != nil {
		return "", fmt.Errorf("dpop: read random jti: %w", err)
	}
	payload := map[string]any{
		"jti": b64url(jtiBytes),
		"htm": upperASCII(method),
		"htu": url,
		"iat": time.Now().Unix(),
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("dpop: marshal payload: %w", err)
	}
	signingInput := s.headerB64 + "." + b64url(pb)
	sig := ed25519.Sign(s.priv, []byte(signingInput))
	return signingInput + "." + b64url(sig), nil
}

// b64url is base64-url without padding, per RFC 7515 §2.
func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// upperASCII is strings.ToUpper restricted to a-z → A-Z so an exotic
// method string like "GÊT" (if anyone ever passes one) doesn't go
// through unicode folding and disagree with what the gateway sees on
// the wire. HTTP methods are ASCII-only per RFC 9110 §9.1; anything
// else is a bug the caller should catch, but we handle the common
// case deterministically.
func upperASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
