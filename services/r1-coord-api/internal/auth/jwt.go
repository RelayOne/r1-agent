// Package auth implements the r1 auth contract — JWT issuance + RelayOne MSP
// SSO — as a Go port of the canonical @relayone/auth-core Node package.
// This is Path A from the operator's deployment scope: avoid running a
// separate Node service in front of the Go SaaS surfaces.
//
// Contract:
//
//	JwtService — HS256 by default; RS256 when AUTH_JWT_PUBLIC_KEY env is
//	  present. Sign / Verify / Refresh. Claims include `sub`, `iss`, `aud`,
//	  `iat`, `exp`, plus optional `msp`, `org`, `email`, `roles[]`.
//
//	RelayOneSsoClient — OIDC client against RelayOne's /oauth/authorize +
//	  /oauth/token + /oauth/userinfo endpoints. Lifts the MSP claims (msp,
//	  org, roles) into the issued r1 JWT.
//
// Drift contract: this package's behavior tracks @relayone/auth-core HEAD;
// the README in that repo documents the env-var contract verbatim. When
// the Node package adds a feature, file it here too. Test fixtures kept
// in sync via cross-checks in services/r1-coord-api/internal/auth/*_test.go.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims is the JWT payload r1 issues. Fields mirror @relayone/auth-core
// JwtService output.
type Claims struct {
	Sub    string   `json:"sub"`
	Iss    string   `json:"iss"`
	Aud    string   `json:"aud"`
	Iat    int64    `json:"iat"`
	Exp    int64    `json:"exp"`
	Email  string   `json:"email,omitempty"`
	MSP    string   `json:"msp,omitempty"`
	Org    string   `json:"org,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	JTI    string   `json:"jti,omitempty"`
}

// Algorithm is the JWT signing algorithm: HS256 (default) or RS256
// (when AUTH_JWT_PUBLIC_KEY is set on the verifier side).
type Algorithm string

const (
	HS256 Algorithm = "HS256"
	RS256 Algorithm = "RS256"
)

// JwtService signs and verifies JWTs. HS256 uses the shared secret;
// RS256 uses RSA private/public keys. Default expiry = 1 hour;
// max-age cap = 8 hours (refresh before that).
type JwtService struct {
	Issuer   string
	Audience string
	Alg      Algorithm

	// HS256
	Secret []byte

	// RS256
	PrivateKey *rsa.PrivateKey
	PublicKey  *rsa.PublicKey

	// Defaults
	DefaultExpiry time.Duration
	MaxAge        time.Duration

	// Test injection.
	Now func() time.Time
}

// NewJwtServiceHS256 creates a JwtService with HS256 signing.
func NewJwtServiceHS256(issuer, audience string, secret []byte) *JwtService {
	return &JwtService{
		Issuer:        issuer,
		Audience:      audience,
		Alg:           HS256,
		Secret:        secret,
		DefaultExpiry: 1 * time.Hour,
		MaxAge:        8 * time.Hour,
		Now:           time.Now,
	}
}

// NewJwtServiceRS256 creates a JwtService with RS256 signing.
// privatePEM is PEM-encoded PKCS#1 or PKCS#8; publicPEM is PEM-encoded
// SubjectPublicKeyInfo. Either may be nil — sign-only or verify-only.
func NewJwtServiceRS256(issuer, audience string, privatePEM, publicPEM []byte) (*JwtService, error) {
	s := &JwtService{
		Issuer:        issuer,
		Audience:      audience,
		Alg:           RS256,
		DefaultExpiry: 1 * time.Hour,
		MaxAge:        8 * time.Hour,
		Now:           time.Now,
	}
	if len(privatePEM) > 0 {
		priv, err := parseRSAPrivateKey(privatePEM)
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		s.PrivateKey = priv
		s.PublicKey = &priv.PublicKey
	}
	if len(publicPEM) > 0 {
		pub, err := parseRSAPublicKey(publicPEM)
		if err != nil {
			return nil, fmt.Errorf("public key: %w", err)
		}
		s.PublicKey = pub
	}
	if s.PrivateKey == nil && s.PublicKey == nil {
		return nil, errors.New("RS256 needs at least one of private/public key")
	}
	return s, nil
}

// Sign produces a compact JWS string. Adds iat/exp from Now() if claims
// haven't set them; clamps Exp to MaxAge from now.
func (s *JwtService) Sign(claims Claims) (string, error) {
	now := s.Now().UTC()
	if claims.Iss == "" {
		claims.Iss = s.Issuer
	}
	if claims.Aud == "" {
		claims.Aud = s.Audience
	}
	if claims.Iat == 0 {
		claims.Iat = now.Unix()
	}
	if claims.Exp == 0 {
		claims.Exp = now.Add(s.DefaultExpiry).Unix()
	}
	maxExp := now.Add(s.MaxAge).Unix()
	if claims.Exp > maxExp {
		claims.Exp = maxExp
	}
	if claims.JTI == "" {
		jti, err := generateJTI()
		if err != nil {
			return "", err
		}
		claims.JTI = jti
	}

	header := struct {
		Alg Algorithm `json:"alg"`
		Typ string    `json:"typ"`
	}{Alg: s.Alg, Typ: "JWT"}

	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("encode header: %w", err)
	}
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode claims: %w", err)
	}
	headerB64 := base64.RawURLEncoding.EncodeToString(headerBytes)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := headerB64 + "." + payloadB64

	sig, err := s.sign([]byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify parses + validates a JWT and returns the claims. Verifies
// signature, alg, iss, aud, exp.
func (s *JwtService) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid JWT: not 3 parts")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var header struct {
		Alg Algorithm `json:"alg"`
		Typ string    `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if header.Alg != s.Alg {
		return nil, fmt.Errorf("alg mismatch: got %s, want %s", header.Alg, s.Alg)
	}

	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if err := s.verify([]byte(signingInput), sig); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	now := s.Now().UTC().Unix()
	if claims.Exp != 0 && now >= claims.Exp {
		return nil, errors.New("token expired")
	}
	if claims.Iss != s.Issuer {
		return nil, fmt.Errorf("iss mismatch: got %q, want %q", claims.Iss, s.Issuer)
	}
	if claims.Aud != s.Audience {
		return nil, fmt.Errorf("aud mismatch: got %q, want %q", claims.Aud, s.Audience)
	}
	return &claims, nil
}

// Refresh produces a new JWT with a fresh exp. Refuses to refresh past
// MaxAge from the original iat (so a stolen refresh token has bounded
// damage).
func (s *JwtService) Refresh(token string) (string, error) {
	claims, err := s.Verify(token)
	if err != nil {
		return "", fmt.Errorf("verify before refresh: %w", err)
	}
	now := s.Now().UTC().Unix()
	maxExp := claims.Iat + int64(s.MaxAge.Seconds())
	if now >= maxExp {
		return "", errors.New("token past max age; full re-auth required")
	}
	claims.Iat = now
	claims.Exp = 0 // Sign() will fill in DefaultExpiry, capped at maxExp
	jti, err := generateJTI()
	if err != nil {
		return "", err
	}
	claims.JTI = jti
	return s.Sign(*claims)
}

// --- internal signing helpers -----------------------------------------

func (s *JwtService) sign(input []byte) ([]byte, error) {
	switch s.Alg {
	case HS256:
		mac := hmac.New(sha256.New, s.Secret)
		mac.Write(input)
		return mac.Sum(nil), nil
	case RS256:
		if s.PrivateKey == nil {
			return nil, errors.New("RS256 sign requires private key")
		}
		hashed := sha256.Sum256(input)
		return rsa.SignPKCS1v15(rand.Reader, s.PrivateKey, 0x00, hashed[:])
	}
	return nil, fmt.Errorf("unsupported alg: %s", s.Alg)
}

func (s *JwtService) verify(input, sig []byte) error {
	switch s.Alg {
	case HS256:
		mac := hmac.New(sha256.New, s.Secret)
		mac.Write(input)
		if !hmac.Equal(mac.Sum(nil), sig) {
			return errors.New("HMAC mismatch")
		}
		return nil
	case RS256:
		if s.PublicKey == nil {
			return errors.New("RS256 verify requires public key")
		}
		hashed := sha256.Sum256(input)
		return rsa.VerifyPKCS1v15(s.PublicKey, 0x00, hashed[:], sig)
	}
	return fmt.Errorf("unsupported alg: %s", s.Alg)
}

func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("PEM decode failed")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	}
	return nil, fmt.Errorf("unsupported PEM type: %q", block.Type)
}

func parseRSAPublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("PEM decode failed")
	}
	if block.Type != "PUBLIC KEY" && block.Type != "RSA PUBLIC KEY" {
		return nil, fmt.Errorf("unsupported PEM type: %q", block.Type)
	}
	if block.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(block.Bytes)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("PKIX key is not RSA")
	}
	return rsaKey, nil
}

func generateJTI() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
