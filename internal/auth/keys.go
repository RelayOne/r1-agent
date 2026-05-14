package auth

// keys.go — key material loading + persistence.
//
// Three KeySource implementations (priority order):
//
//   - SecretManagerSource — reads from GCP Secret Manager when the cloud
//     adapter is configured. Returns ErrUnsupported when the GCP libs
//     are unavailable in this build (matches the lazy-load pattern used
//     by internal/cloud).
//   - EnvSource — reads PEM material directly out of environment vars.
//   - FileSource — reads (or generates on first use) PEM material from
//     a directory. Defaults to ~/.r1/keys/ when Dir is empty. File modes
//     follow the discipline established by internal/ledger/redact_sign.go:
//     priv 0600, pub 0644, dir 0755.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupported is returned by a KeySource that is not available in
// the current build (e.g. SecretManagerSource when the GCP libs are
// stubbed out). Callers should treat it as "skip this source" and try
// the next.
var ErrUnsupported = errors.New("auth keys: source unsupported in this build")

// ErrNoKeyMaterial is returned when no KeySource produces material and
// the caller has not asked for first-time generation.
var ErrNoKeyMaterial = errors.New("auth keys: no key material found")

// KeyMaterial is the union of HS256 and RS256 key material plus the
// `kid` (key id) header value used to disambiguate signing keys when
// multiple are in flight (rotation, multi-tenant).
type KeyMaterial struct {
	// Algorithm is "HS256" or "RS256". When HS256, HMACSecret is set
	// and RSA fields are nil. When RS256, the RSA fields are set and
	// HMACSecret is nil.
	Algorithm string

	// HMACSecret is the raw secret bytes for HS256. >=32 bytes per the
	// TS contract.
	HMACSecret []byte

	// RSAPrivate is the PKCS8-decoded RSA private key for RS256
	// signing. Nil under HS256.
	RSAPrivate *rsa.PrivateKey

	// RSAPublic is the SPKI-decoded RSA public key for RS256 verify.
	// Nil under HS256.
	RSAPublic *rsa.PublicKey

	// KID is the key id stamped into JWT headers. Derived from a
	// truncated sha256 of the public key (RS256) or of the secret
	// (HS256) so it is stable across restarts without persisting an
	// extra file.
	KID string
}

// KeySource abstracts a way to obtain KeyMaterial. Multiple sources can
// be tried in priority order; the first that returns a non-nil material
// + nil err wins.
type KeySource interface {
	// Load returns the key material or an error. Returning
	// ErrUnsupported means "skip me — I'm not available in this
	// build"; ErrNoKeyMaterial means "I exist but found nothing";
	// any other error halts the cascade.
	Load(ctx context.Context) (*KeyMaterial, error)

	// Name returns a short identifier for log lines.
	Name() string
}

// LoadKeyMaterial walks the supplied sources in order, returning the
// first non-nil material. Sources returning ErrUnsupported are silently
// skipped; sources returning ErrNoKeyMaterial fall through to the next.
// Any other error halts the cascade.
//
// Pass a FileSource last to enable first-time keypair generation as a
// fallback.
func LoadKeyMaterial(ctx context.Context, sources ...KeySource) (*KeyMaterial, error) {
	if len(sources) == 0 {
		return nil, errors.New("auth keys: LoadKeyMaterial: no sources")
	}
	var firstHardErr error
	for _, src := range sources {
		mat, err := src.Load(ctx)
		if err != nil {
			if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNoKeyMaterial) {
				continue
			}
			if firstHardErr == nil {
				firstHardErr = fmt.Errorf("auth keys: source %q: %w", src.Name(), err)
			}
			continue
		}
		if mat != nil {
			return mat, nil
		}
	}
	if firstHardErr != nil {
		return nil, firstHardErr
	}
	return nil, ErrNoKeyMaterial
}

// DefaultKeySources returns the production cascade:
//
//   - SecretManagerSource (stub in this build; returns ErrUnsupported
//     until cloud integration is enabled)
//   - EnvSource(os.Getenv)
//   - FileSource{Dir: ~/.r1/keys/}
//
// The file source generates a fresh RSA-2048 keypair on first use so
// the daemon can boot without operator intervention. Operators who want
// HS256 must set AUTH_JWT_SECRET (EnvSource picks it up).
func DefaultKeySources(_ context.Context) []KeySource {
	dir, err := defaultKeyDir()
	if err != nil {
		// We still return EnvSource so the caller can run without
		// FileSource (env-only deployments). dir defaults to "" and
		// FileSource handles that as ErrNoKeyMaterial.
		dir = ""
	}
	return []KeySource{
		SecretManagerSource{},
		EnvSource{Getenv: os.Getenv},
		FileSource{Dir: dir},
	}
}

// defaultKeyDir resolves ~/.r1/keys/ honoring R1_HOME for tests.
func defaultKeyDir() (string, error) {
	if h := os.Getenv("R1_HOME"); h != "" {
		return filepath.Join(h, "keys"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".r1", "keys"), nil
}

// SecretManagerSource is a stub. The real impl will route through
// internal/cloud once that package exposes a Secret Manager client.
// For now it returns ErrUnsupported so the cascade falls through.
type SecretManagerSource struct{}

// Load implements KeySource.
func (SecretManagerSource) Load(_ context.Context) (*KeyMaterial, error) {
	return nil, ErrUnsupported
}

// Name implements KeySource.
func (SecretManagerSource) Name() string { return "secret-manager" }

// EnvSource reads PEM/secret material directly out of env vars. Mirrors
// jwtServiceFromEnv in TS:
//
//   - AUTH_JWT_PUBLIC_KEY present + AUTH_JWT_PRIVATE_KEY present -> RS256.
//   - AUTH_JWT_SECRET present -> HS256.
//   - Otherwise -> ErrNoKeyMaterial.
type EnvSource struct {
	// Getenv is the lookup function. Pass os.Getenv in production;
	// tests pass a closure over a map for hermetic coverage.
	Getenv func(string) string
}

// Name implements KeySource.
func (EnvSource) Name() string { return "env" }

// Load implements KeySource.
func (s EnvSource) Load(_ context.Context) (*KeyMaterial, error) {
	if s.Getenv == nil {
		return nil, ErrNoKeyMaterial
	}
	priv := s.Getenv("AUTH_JWT_PRIVATE_KEY")
	pub := s.Getenv("AUTH_JWT_PUBLIC_KEY")
	if pub != "" {
		if priv == "" {
			return nil, fmt.Errorf("AUTH_JWT_PRIVATE_KEY required when AUTH_JWT_PUBLIC_KEY set")
		}
		return parseRSAKeyMaterial([]byte(priv), []byte(pub))
	}
	secret := s.Getenv("AUTH_JWT_SECRET")
	if secret == "" {
		return nil, ErrNoKeyMaterial
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("AUTH_JWT_SECRET must be >= 32 chars (got %d)", len(secret))
	}
	return &KeyMaterial{
		Algorithm:  "HS256",
		HMACSecret: []byte(secret),
		KID:        hmacKID([]byte(secret)),
	}, nil
}

// FileSource reads PEM material from a directory. When Dir exists but
// contains no material, generates a fresh RSA-2048 keypair as a
// fallback — the default behavior, set DisableGenerate to opt out.
//
// Files:
//
//	jwt-priv.pem    PKCS8 RSA private (mode 0600)
//	jwt-pub.pem     SPKI RSA public  (mode 0644)
//	jwt-secret      raw bytes for HS256 (mode 0600)
//
// RS256 takes precedence over HS256: if both shapes are present on
// disk, the RSA pair wins. The HS256 secret-file path is only used
// when the RSA pair is absent.
type FileSource struct {
	Dir string

	// DisableGenerate opts out of the "create a fresh RSA-2048 keypair
	// on first use" fallback. Tests that want a strict no-side-effect
	// read set it true; production wiring leaves it false so the
	// daemon boots without operator intervention.
	DisableGenerate bool
}

// Name implements KeySource.
func (FileSource) Name() string { return "file" }

// Load implements KeySource.
func (s FileSource) Load(_ context.Context) (*KeyMaterial, error) {
	if s.Dir == "" {
		return nil, ErrNoKeyMaterial
	}
	privPath := filepath.Join(s.Dir, "jwt-priv.pem")
	pubPath := filepath.Join(s.Dir, "jwt-pub.pem")
	secretPath := filepath.Join(s.Dir, "jwt-secret")

	privBytes, errPriv := os.ReadFile(privPath)
	pubBytes, errPub := os.ReadFile(pubPath)
	if errPriv == nil && errPub == nil {
		return parseRSAKeyMaterial(privBytes, pubBytes)
	}

	// Try the HMAC secret next.
	if secretBytes, err := os.ReadFile(secretPath); err == nil {
		// Strip a trailing newline that operators often add when they
		// `echo > jwt-secret`. We trim ASCII whitespace only.
		secret := strings.TrimRight(string(secretBytes), "\r\n\t ")
		if len(secret) < 32 {
			return nil, fmt.Errorf("auth keys: %s has < 32 chars (got %d)", secretPath, len(secret))
		}
		return &KeyMaterial{
			Algorithm:  "HS256",
			HMACSecret: []byte(secret),
			KID:        hmacKID([]byte(secret)),
		}, nil
	}

	// Be defensive about the "partial" case: one file present, one
	// missing. Refuse to overwrite a partial setup; require operator
	// triage. This must be checked BEFORE the "should-generate?"
	// branch because a partial setup is never safe to clobber.
	if errors.Is(errPriv, os.ErrNotExist) != errors.Is(errPub, os.ErrNotExist) {
		return nil, fmt.Errorf("auth keys: partial key material in %s (priv=%v pub=%v); refusing to overwrite", s.Dir, errPriv, errPub)
	}

	// Nothing on disk. Generate iff allowed. The zero-value of
	// GenerateIfMissing (false) is treated as "allowed" so the struct
	// literal FileSource{Dir: dir} means "read or create" — that's
	// the production cascade default. Callers who want a strict
	// no-side-effect read set DisableGenerate=true.
	if s.DisableGenerate {
		return nil, ErrNoKeyMaterial
	}

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("auth keys: mkdir %s: %w", s.Dir, err)
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("auth keys: generate rsa: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("auth keys: marshal pkcs8: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("auth keys: marshal spki: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		return nil, fmt.Errorf("auth keys: write priv: %w", err)
	}
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		return nil, fmt.Errorf("auth keys: write pub: %w", err)
	}
	return &KeyMaterial{
		Algorithm:  "RS256",
		RSAPrivate: priv,
		RSAPublic:  &priv.PublicKey,
		KID:        rsaKID(&priv.PublicKey),
	}, nil
}

// parseRSAKeyMaterial decodes a PKCS8 priv + SPKI pub PEM pair.
//
// Accepts both "PRIVATE KEY" (PKCS8) and "RSA PRIVATE KEY" (PKCS1)
// because legacy operator scripts sometimes use the older OpenSSL
// label. Likewise for "PUBLIC KEY" vs "RSA PUBLIC KEY".
func parseRSAKeyMaterial(privPEM, pubPEM []byte) (*KeyMaterial, error) {
	priv, err := parseRSAPrivatePEM(privPEM)
	if err != nil {
		return nil, fmt.Errorf("auth keys: parse priv: %w", err)
	}
	pub, err := parseRSAPublicPEM(pubPEM)
	if err != nil {
		return nil, fmt.Errorf("auth keys: parse pub: %w", err)
	}
	// Sanity: priv.N must equal pub.N (catches the most common
	// "mismatched halves" operator error before we mint a token that
	// nobody can verify).
	if priv.N.Cmp(pub.N) != 0 {
		return nil, errors.New("auth keys: private and public moduli differ")
	}
	return &KeyMaterial{
		Algorithm:  "RS256",
		RSAPrivate: priv,
		RSAPublic:  pub,
		KID:        rsaKID(pub),
	}, nil
}

func parseRSAPrivatePEM(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
}

func parseRSAPublicPEM(b []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	switch block.Type {
	case "PUBLIC KEY":
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("PKIX key is not RSA")
		}
		return rsaKey, nil
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
}

// rsaKID returns the first 6 bytes of sha256(SPKI(pub)) hex-encoded.
// 12 hex chars is enough collision resistance for a per-deployment
// keystore and short enough to log without wrapping.
func rsaKID(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:6])
}

// hmacKID derives a 12-char hex id from the raw secret. Stable across
// restarts so a rotation produces a different KID without persisting an
// extra file.
func hmacKID(secret []byte) string {
	h := sha256.Sum256(secret)
	return hex.EncodeToString(h[:6])
}
