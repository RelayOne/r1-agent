package auth

// jwt.go — Go port of auth-core/src/jwt-service.ts.
//
// Sign / verify / refresh primitives. Stateless. One JwtService per
// (issuer, audience, algorithm) tuple. Cross-language interop with the
// TS JwtService is the load-bearing invariant — see specs/relayone-sso.md
// §4 for the field-by-field mirror table.

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// Default TTLs match auth-core/src/jwt-service.ts:52-53.
const (
	defaultAccessTTLSec  = 60 * 15      // 15 minutes
	defaultRefreshTTLSec = 60 * 60 * 24 * 30 // 30 days
)

// ErrJwtVerification wraps every verification failure. Matches TS
// JwtVerificationError("jwt_verification_failed"). Callers can
// errors.Is(err, ErrJwtVerification) to distinguish a token problem
// from an upstream I/O error.
var ErrJwtVerification = errors.New("jwt_verification_failed")

// ErrNotRefreshToken is returned by RefreshAccess when the caller
// passes an access token. Matches TS "not_a_refresh_token".
var ErrNotRefreshToken = fmt.Errorf("%w: not_a_refresh_token", ErrJwtVerification)

// JwtAlgorithm is the supported signature alg set. Mirrors TS.
type JwtAlgorithm string

const (
	AlgHS256 JwtAlgorithm = "HS256"
	AlgRS256 JwtAlgorithm = "RS256"
)

// JwtServiceOptions configures a JwtService. Mirrors TS
// JwtServiceOptions field-for-field; field names are Go-idiomatic but
// the underlying claim names + algorithm semantics are identical.
type JwtServiceOptions struct {
	// Issuer is the `iss` claim. Required.
	Issuer string
	// Audience is the `aud` claim. Required.
	Audience string
	// Algorithm defaults to HS256. Set RS256 when keys are supplied.
	Algorithm JwtAlgorithm
	// SigningKey is the HS256 secret (raw bytes) OR PKCS8 PEM (RS256).
	// For RS256 callers can also pass the *rsa.PrivateKey directly via
	// RSAPrivate to skip the PEM parse.
	SigningKey []byte
	// PublicKey is the RS256 verify key (SPKI PEM). Required when
	// Algorithm == RS256. Callers can also pass *rsa.PublicKey directly
	// via RSAPublic.
	PublicKey []byte
	// RSAPrivate / RSAPublic override the PEM inputs when non-nil.
	// Useful when KeyMaterial was loaded already.
	RSAPrivate *rsa.PrivateKey
	RSAPublic  *rsa.PublicKey
	// AccessTTL defaults to 15 minutes when zero.
	AccessTTL time.Duration
	// RefreshTTL defaults to 30 days when zero.
	RefreshTTL time.Duration
	// KID is stamped into the JWT header. Optional; tooling like
	// rotation benefits from a stable id.
	KID string
}

// SignedTokenPair mirrors TS SignedTokenPair.
type SignedTokenPair struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

// VerifiedToken is the parsed + validated payload + header summary.
type VerifiedToken struct {
	Payload map[string]any
	Header  TokenHeader
}

// TokenHeader is the subset of protected-header fields callers care
// about (alg / typ / kid).
type TokenHeader struct {
	Alg string
	Typ string
	KID string
}

// SignOptions are the per-call sign knobs. Mirrors TS sign()'s second
// argument.
type SignOptions struct {
	// TTL overrides the service-level access TTL for this token only.
	// Zero means "use the service default".
	TTL time.Duration
	// Subject is the `sub` claim. Empty means "no subject".
	Subject string
}

// RefreshResult is what RefreshAccess returns.
type RefreshResult struct {
	AccessToken     string    `json:"access_token"`
	AccessExpiresAt time.Time `json:"access_expires_at"`
}

// JwtService is the sign/verify/refresh primitive.
type JwtService struct {
	issuer     string
	audience   string
	algorithm  jwa.SignatureAlgorithm
	signKey    jwk.Key
	verifyKey  jwk.Key
	accessTTL  time.Duration
	refreshTTL time.Duration
	kid        string
}

// NewJwtService validates the options and constructs the service.
//
// HS256: SigningKey must be >= 32 bytes; PublicKey is ignored.
// RS256: RSAPrivate (or SigningKey as PKCS8 PEM) + RSAPublic (or PublicKey
// as SPKI PEM) must both be supplied.
func NewJwtService(opts JwtServiceOptions) (*JwtService, error) {
	if opts.Issuer == "" {
		return nil, errors.New("auth jwt: Issuer required")
	}
	if opts.Audience == "" {
		return nil, errors.New("auth jwt: Audience required")
	}
	alg := opts.Algorithm
	if alg == "" {
		alg = AlgHS256
	}
	s := &JwtService{
		issuer:     opts.Issuer,
		audience:   opts.Audience,
		accessTTL:  opts.AccessTTL,
		refreshTTL: opts.RefreshTTL,
		kid:        opts.KID,
	}
	if s.accessTTL <= 0 {
		s.accessTTL = time.Duration(defaultAccessTTLSec) * time.Second
	}
	if s.refreshTTL <= 0 {
		s.refreshTTL = time.Duration(defaultRefreshTTLSec) * time.Second
	}

	switch alg {
	case AlgHS256:
		s.algorithm = jwa.HS256
		if len(opts.SigningKey) < 32 {
			return nil, fmt.Errorf("auth jwt: HS256 requires SigningKey >= 32 bytes (got %d)", len(opts.SigningKey))
		}
		k, err := jwk.FromRaw(opts.SigningKey)
		if err != nil {
			return nil, fmt.Errorf("auth jwt: HS256 key import: %w", err)
		}
		if s.kid != "" {
			_ = k.Set(jwk.KeyIDKey, s.kid)
		}
		s.signKey = k
		s.verifyKey = k
	case AlgRS256:
		s.algorithm = jwa.RS256
		priv := opts.RSAPrivate
		pub := opts.RSAPublic
		if priv == nil && len(opts.SigningKey) > 0 {
			p, err := parseRSAPrivatePEM(opts.SigningKey)
			if err != nil {
				return nil, fmt.Errorf("auth jwt: parse RS256 private PEM: %w", err)
			}
			priv = p
		}
		if pub == nil && len(opts.PublicKey) > 0 {
			p, err := parseRSAPublicPEM(opts.PublicKey)
			if err != nil {
				return nil, fmt.Errorf("auth jwt: parse RS256 public PEM: %w", err)
			}
			pub = p
		}
		if priv == nil {
			return nil, errors.New("auth jwt: RS256 requires RSAPrivate or SigningKey (PKCS8 PEM)")
		}
		if pub == nil {
			return nil, errors.New("auth jwt: RS256 requires RSAPublic or PublicKey (SPKI PEM)")
		}
		sk, err := jwk.FromRaw(priv)
		if err != nil {
			return nil, fmt.Errorf("auth jwt: RS256 private import: %w", err)
		}
		vk, err := jwk.FromRaw(pub)
		if err != nil {
			return nil, fmt.Errorf("auth jwt: RS256 public import: %w", err)
		}
		if s.kid != "" {
			_ = sk.Set(jwk.KeyIDKey, s.kid)
			_ = vk.Set(jwk.KeyIDKey, s.kid)
		}
		s.signKey = sk
		s.verifyKey = vk
	default:
		return nil, fmt.Errorf("auth jwt: unsupported algorithm %q", alg)
	}
	return s, nil
}

// Issuer returns the configured issuer string.
func (s *JwtService) Issuer() string { return s.issuer }

// Audience returns the configured audience string.
func (s *JwtService) Audience() string { return s.audience }

// Algorithm returns "HS256" or "RS256".
func (s *JwtService) Algorithm() string { return string(s.algorithm) }

// KID returns the configured key id (empty when unset).
func (s *JwtService) KID() string { return s.kid }

// AccessTTL exposes the configured access-token lifetime.
func (s *JwtService) AccessTTL() time.Duration { return s.accessTTL }

// RefreshTTL exposes the configured refresh-token lifetime.
func (s *JwtService) RefreshTTL() time.Duration { return s.refreshTTL }

// Sign mints a JWT carrying the supplied payload + standard claims.
// Sets iat/exp/iss/aud + jti automatically. opts.Subject populates sub.
func (s *JwtService) Sign(payload map[string]any, opts SignOptions) (string, error) {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = s.accessTTL
	}
	now := time.Now()
	tok := jwt.New()
	if err := tok.Set(jwt.IssuerKey, s.issuer); err != nil {
		return "", err
	}
	if err := tok.Set(jwt.AudienceKey, []string{s.audience}); err != nil {
		return "", err
	}
	if err := tok.Set(jwt.IssuedAtKey, now); err != nil {
		return "", err
	}
	if err := tok.Set(jwt.ExpirationKey, now.Add(ttl)); err != nil {
		return "", err
	}
	if err := tok.Set(jwt.JwtIDKey, uuid.NewString()); err != nil {
		return "", err
	}
	if opts.Subject != "" {
		if err := tok.Set(jwt.SubjectKey, opts.Subject); err != nil {
			return "", err
		}
	}
	for k, v := range payload {
		// Don't let the payload overwrite the standard claims we set
		// above — that would be a security footgun (caller-controlled
		// iss/exp). Quietly skip; the test for this case lives in
		// jwt_test.go.
		if isReservedClaim(k) {
			continue
		}
		if err := tok.Set(k, v); err != nil {
			return "", err
		}
	}
	signOpts := []jwt.SignOption{jwt.WithKey(s.algorithm, s.signKey)}
	signed, err := jwt.Sign(tok, signOpts...)
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

// Verify parses + validates a JWT. Returns ErrJwtVerification on any
// failure (expired, wrong issuer/audience, bad signature). Wrapping
// uses fmt.Errorf("%w: ...") so callers can errors.Is against
// ErrJwtVerification.
func (s *JwtService) Verify(token string) (VerifiedToken, error) {
	parsed, err := jwt.ParseString(token,
		jwt.WithKey(s.algorithm, s.verifyKey),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithValidate(true),
	)
	if err != nil {
		return VerifiedToken{}, fmt.Errorf("%w: %v", ErrJwtVerification, err)
	}
	// Re-parse with insecure to pull header for the caller. We trust
	// the header values only for diagnostic display; the signature
	// check above is what matters for security.
	hdr, _ := parseHeaderOnly(token)
	payload := tokenToMap(parsed)
	return VerifiedToken{
		Payload: payload,
		Header:  hdr,
	}, nil
}

// IssuePair mints an access+refresh pair. The access carries
// token_use="access"; the refresh carries token_use="refresh".
func (s *JwtService) IssuePair(payload map[string]any, subject string) (SignedTokenPair, error) {
	now := time.Now()
	accessPayload := clonePayload(payload)
	accessPayload["token_use"] = "access"
	refreshPayload := clonePayload(payload)
	refreshPayload["token_use"] = "refresh"

	at, err := s.Sign(accessPayload, SignOptions{TTL: s.accessTTL, Subject: subject})
	if err != nil {
		return SignedTokenPair{}, err
	}
	rt, err := s.Sign(refreshPayload, SignOptions{TTL: s.refreshTTL, Subject: subject})
	if err != nil {
		return SignedTokenPair{}, err
	}
	return SignedTokenPair{
		AccessToken:      at,
		RefreshToken:     rt,
		AccessExpiresAt:  now.Add(s.accessTTL),
		RefreshExpiresAt: now.Add(s.refreshTTL),
	}, nil
}

// RefreshAccess verifies a refresh token, strips JWT-managed claims,
// and mints a fresh access token carrying the same custom claims.
// Refresh-token rotation is the caller's choice — this method does NOT
// emit a new refresh token (matches TS).
func (s *JwtService) RefreshAccess(refreshToken string) (RefreshResult, error) {
	verified, err := s.Verify(refreshToken)
	if err != nil {
		return RefreshResult{}, err
	}
	use, _ := verified.Payload["token_use"].(string)
	if use != "refresh" {
		return RefreshResult{}, ErrNotRefreshToken
	}
	subject, _ := verified.Payload["sub"].(string)
	rest := make(map[string]any, len(verified.Payload))
	for k, v := range verified.Payload {
		if isReservedClaim(k) || k == "token_use" {
			continue
		}
		rest[k] = v
	}
	rest["token_use"] = "access"
	at, err := s.Sign(rest, SignOptions{TTL: s.accessTTL, Subject: subject})
	if err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{
		AccessToken:     at,
		AccessExpiresAt: time.Now().Add(s.accessTTL),
	}, nil
}

// JwtServiceFromEnv mirrors TS jwtServiceFromEnv(env). Returns an error
// whose message contains the exact missing env var name, so the test
// assertion `expect(...).toThrow(/AUTH_JWT_ISSUER/)` ports cleanly.
func JwtServiceFromEnv(getenv func(string) string) (*JwtService, error) {
	if getenv == nil {
		return nil, errors.New("auth jwt: JwtServiceFromEnv: nil getenv")
	}
	issuer := getenv("AUTH_JWT_ISSUER")
	audience := getenv("AUTH_JWT_AUDIENCE")
	if issuer == "" {
		return nil, errors.New("AUTH_JWT_ISSUER required")
	}
	if audience == "" {
		return nil, errors.New("AUTH_JWT_AUDIENCE required")
	}

	accessTTL, err := parseTTLSec(getenv("AUTH_JWT_ACCESS_TTL_SEC"))
	if err != nil {
		return nil, fmt.Errorf("AUTH_JWT_ACCESS_TTL_SEC: %w", err)
	}
	refreshTTL, err := parseTTLSec(getenv("AUTH_JWT_REFRESH_TTL_SEC"))
	if err != nil {
		return nil, fmt.Errorf("AUTH_JWT_REFRESH_TTL_SEC: %w", err)
	}

	if pubPEM := getenv("AUTH_JWT_PUBLIC_KEY"); pubPEM != "" {
		privPEM := getenv("AUTH_JWT_PRIVATE_KEY")
		if privPEM == "" {
			return nil, errors.New("AUTH_JWT_PRIVATE_KEY required when AUTH_JWT_PUBLIC_KEY set")
		}
		return NewJwtService(JwtServiceOptions{
			Issuer:     issuer,
			Audience:   audience,
			Algorithm:  AlgRS256,
			SigningKey: []byte(privPEM),
			PublicKey:  []byte(pubPEM),
			AccessTTL:  accessTTL,
			RefreshTTL: refreshTTL,
		})
	}
	secret := getenv("AUTH_JWT_SECRET")
	if secret == "" {
		return nil, errors.New("AUTH_JWT_SECRET required")
	}
	return NewJwtService(JwtServiceOptions{
		Issuer:     issuer,
		Audience:   audience,
		Algorithm:  AlgHS256,
		SigningKey: []byte(secret),
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	})
}

// JwtServiceFromKeyMaterial constructs a service directly from a
// KeyMaterial (produced by LoadKeyMaterial). Convenience for the
// daemon's startup glue.
func JwtServiceFromKeyMaterial(issuer, audience string, mat *KeyMaterial, accessTTL, refreshTTL time.Duration) (*JwtService, error) {
	if mat == nil {
		return nil, errors.New("auth jwt: nil KeyMaterial")
	}
	opts := JwtServiceOptions{
		Issuer:     issuer,
		Audience:   audience,
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
		KID:        mat.KID,
	}
	switch mat.Algorithm {
	case "HS256":
		opts.Algorithm = AlgHS256
		opts.SigningKey = mat.HMACSecret
	case "RS256":
		opts.Algorithm = AlgRS256
		opts.RSAPrivate = mat.RSAPrivate
		opts.RSAPublic = mat.RSAPublic
	default:
		return nil, fmt.Errorf("auth jwt: KeyMaterial algorithm %q", mat.Algorithm)
	}
	return NewJwtService(opts)
}

// parseTTLSec parses a seconds-as-int env var. Empty input returns
// 0 + nil (meaning "use default"). Negative or non-numeric returns
// a descriptive error.
func parseTTLSec(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not an integer: %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("must be >= 0 (got %d)", n)
	}
	return time.Duration(n) * time.Second, nil
}

// isReservedClaim reports whether a claim name is managed by the
// service. Caller payload must NOT override these.
func isReservedClaim(name string) bool {
	switch name {
	case "iss", "aud", "iat", "exp", "nbf", "sub", "jti":
		return true
	}
	return false
}

// clonePayload returns a shallow copy of m (deep enough for the
// map[string]any use case — values are interface{} and we don't mutate
// them).
func clonePayload(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// tokenToMap dumps a jwt.Token's claims into a flat map[string]any.
// Standard claims become their canonical names (iss, aud, iat, exp,
// sub, jti, nbf); custom claims are copied verbatim.
//
// Time-valued standard claims (iat/exp/nbf) are returned as time.Time
// — callers that need numeric epoch seconds can call .Unix().
func tokenToMap(t jwt.Token) map[string]any {
	out := map[string]any{}
	if v := t.Issuer(); v != "" {
		out["iss"] = v
	}
	if v := t.Audience(); len(v) > 0 {
		// Match the TS shape: aud is a string when there's exactly one
		// audience, []string otherwise. The jose verifier on the TS
		// side treats both the same way.
		if len(v) == 1 {
			out["aud"] = v[0]
		} else {
			out["aud"] = v
		}
	}
	if v := t.Subject(); v != "" {
		out["sub"] = v
	}
	if v := t.JwtID(); v != "" {
		out["jti"] = v
	}
	if v := t.IssuedAt(); !v.IsZero() {
		out["iat"] = v.Unix()
	}
	if v := t.Expiration(); !v.IsZero() {
		out["exp"] = v.Unix()
	}
	if v := t.NotBefore(); !v.IsZero() {
		out["nbf"] = v.Unix()
	}
	// Custom claims via PrivateClaims().
	for k, v := range t.PrivateClaims() {
		out[k] = v
	}
	return out
}

// parseHeaderOnly extracts the protected header of a compact JWS
// without verifying. Used to populate VerifiedToken.Header for caller
// diagnostics — the security check already happened above.
func parseHeaderOnly(token string) (TokenHeader, error) {
	// jwx exposes jws.ParseString which gives us protected header; we
	// avoid a second crypto pass by using jws.Parse.
	msg, err := jwsParse(token)
	if err != nil {
		return TokenHeader{}, err
	}
	if len(msg.Signatures()) == 0 {
		return TokenHeader{}, errors.New("no signatures")
	}
	h := msg.Signatures()[0].ProtectedHeaders()
	return TokenHeader{
		Alg: h.Algorithm().String(),
		Typ: h.Type(),
		KID: h.KeyID(),
	}, nil
}
