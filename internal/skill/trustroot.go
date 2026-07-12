// Package skill — trustroot.go
//
// C7 cross-product-skill-exchange T6: federated trust root.
//
// A TrustRootDocument is a signed list of ed25519 public keys keyed
// by kid (matching `derivePackKeyID` from internal/skillmfr). Each
// entry carries authority + optional tenant + validity window +
// scope prefixes. The document itself is signed by a single root
// operator key so clients can detect tampering of the trust set.
//
// Default-empty trust root behavior: when the document is absent OR
// keys is empty, R1 falls back to v1 behavior (signature-only check)
// — the trust-root layer is additive and never breaks v1 packs.
//
// Forward-compat note: this file uses raw ed25519 keys. A future
// Sigstore variant adds a cert_chain field and teaches MatchKey to
// verify chains. The on-disk shape is forward compatible — older
// clients ignore unknown fields.
package skill

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// TrustRootFile is the on-disk path (relative to the repo's data
// dir) where the document is persisted. Use TrustRootPathFor(repo)
// to compute the canonical absolute path.
const TrustRootFile = "skills/trust-root.json"

// TrustRootEntry is one publisher key + scopes + validity window.
type TrustRootEntry struct {
	KeyID     string             `json:"key_id"`
	PublicKey string             `json:"public_key"`
	Authority SignatureAuthority `json:"authority"`
	TenantID  string             `json:"tenant_id,omitempty"`
	NotBefore string             `json:"not_before,omitempty"`
	NotAfter  string             `json:"not_after,omitempty"`
	Scopes    []string           `json:"scopes,omitempty"`
}

// TrustRootDocument is the signed registry trust set.
type TrustRootDocument struct {
	Version   string           `json:"version"`
	IssuedAt  string           `json:"issued_at"`
	Keys      []TrustRootEntry `json:"keys"`
	Signature string           `json:"signature,omitempty"`
}

// Errors returned by trust-root operations. These match the spec's
// Error Handling table so callers can dispatch on them.
var (
	ErrTrustRootKeyNotFound      = errors.New("trustroot: key_id not in trust root")
	ErrTrustRootKeyExpired       = errors.New("trustroot: key expired")
	ErrTrustRootKeyNotYetValid   = errors.New("trustroot: key not yet valid")
	ErrTrustRootScopeViolation   = errors.New("trustroot: pack name outside key scope")
	ErrTrustRootSignatureInvalid = errors.New("trustroot: document signature invalid")
)

// TrustRootPathFor returns the canonical absolute path for the
// trust-root document in the supplied repo. Symmetrical with
// r1dir.CanonicalPathFor; lives here to keep the dependency arrow
// pointing skill -> r1dir (not the reverse).
func TrustRootPathFor(repo, root string) string {
	if root == "" {
		root = ".r1"
	}
	return filepath.Join(repo, root, TrustRootFile)
}

// LoadTrustRoot reads the document from path. Missing file is NOT an
// error — callers receive (nil, nil) and fall back to v1 behavior
// per spec.
func LoadTrustRoot(path string) (*TrustRootDocument, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("trustroot: read %q: %w", path, err)
	}
	var doc TrustRootDocument
	if jerr := json.Unmarshal(payload, &doc); jerr != nil {
		return nil, fmt.Errorf("trustroot: parse %q: %v", path, jerr)
	}
	for i := range doc.Keys {
		if strings.TrimSpace(doc.Keys[i].KeyID) == "" {
			return nil, fmt.Errorf("trustroot: entry %d missing key_id", i)
		}
		if !isValidAuthority(doc.Keys[i].Authority) {
			return nil, fmt.Errorf("trustroot: entry %d authority %q not allowed", i, doc.Keys[i].Authority)
		}
		if doc.Keys[i].Authority == AuthorityTenant && strings.TrimSpace(doc.Keys[i].TenantID) == "" {
			return nil, fmt.Errorf("trustroot: entry %d authority=tenant requires tenant_id", i)
		}
	}
	return &doc, nil
}

// SaveTrustRoot writes the document to path with 0644 perms,
// creating parent directories at 0755 as needed. The document MUST
// be signed before calling — clients refuse unsigned documents.
func SaveTrustRoot(path string, doc *TrustRootDocument) error {
	if doc == nil {
		return fmt.Errorf("trustroot: nil document")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("trustroot: mkdir: %w", err)
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("trustroot: marshal: %w", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("trustroot: write %q: %w", path, err)
	}
	return nil
}

// canonicalTrustRootBody returns the JSON bytes covered by the
// document signature. The Signature field is excluded so the
// signature cannot self-reference.
func canonicalTrustRootBody(doc *TrustRootDocument) ([]byte, error) {
	copyDoc := *doc
	copyDoc.Signature = ""
	// Sort keys by key_id for canonical ordering — different write
	// orders MUST produce the same bytes.
	keys := append([]TrustRootEntry(nil), copyDoc.Keys...)
	sort.Slice(keys, func(i, j int) bool { return keys[i].KeyID < keys[j].KeyID })
	copyDoc.Keys = keys
	return json.Marshal(copyDoc)
}

// SignTrustRoot stamps doc.Signature using priv. The original doc is
// mutated.
func SignTrustRoot(doc *TrustRootDocument, priv ed25519.PrivateKey) error {
	if doc == nil {
		return fmt.Errorf("trustroot: nil document")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("trustroot: invalid private key length %d", len(priv))
	}
	doc.Signature = ""
	body, err := canonicalTrustRootBody(doc)
	if err != nil {
		return fmt.Errorf("trustroot: canonicalize: %w", err)
	}
	sig := ed25519.Sign(priv, body)
	doc.Signature = base64.StdEncoding.EncodeToString(sig)
	return nil
}

// VerifyTrustRoot checks doc.Signature against rootPub. Returns
// ErrTrustRootSignatureInvalid on mismatch.
func VerifyTrustRoot(doc *TrustRootDocument, rootPub ed25519.PublicKey) error {
	if doc == nil {
		return fmt.Errorf("trustroot: nil document")
	}
	if len(rootPub) != ed25519.PublicKeySize {
		return fmt.Errorf("trustroot: invalid root public key length %d", len(rootPub))
	}
	if strings.TrimSpace(doc.Signature) == "" {
		return ErrTrustRootSignatureInvalid
	}
	body, err := canonicalTrustRootBody(doc)
	if err != nil {
		return fmt.Errorf("trustroot: canonicalize: %w", err)
	}
	// Strict (canonical-only) decode on the verification path — PORTS.md §7.
	sig, derr := honestcrypto.DecodeBase64Strict(doc.Signature)
	if derr != nil {
		return fmt.Errorf("%w: decode signature: %v", ErrTrustRootSignatureInvalid, derr)
	}
	if !ed25519.Verify(rootPub, body, sig) {
		return ErrTrustRootSignatureInvalid
	}
	return nil
}

// MatchKey searches doc for an entry matching kid, then validates
// the entry's window + scopes against packName + now.
//
// Returns the matched entry on success. Distinguishes
// ErrTrustRootKeyNotFound, ErrTrustRootKeyNotYetValid,
// ErrTrustRootKeyExpired, ErrTrustRootScopeViolation per spec.
//
// now is parameterized for testability; callers in production pass
// time.Now().UTC().
func MatchKey(doc *TrustRootDocument, kid, packName string, now time.Time) (*TrustRootEntry, error) {
	if doc == nil {
		return nil, ErrTrustRootKeyNotFound
	}
	for i := range doc.Keys {
		entry := doc.Keys[i]
		if entry.KeyID != kid {
			continue
		}
		if err := entryWithinWindow(entry, now); err != nil {
			return nil, err
		}
		if err := entryScopeAllows(entry, packName); err != nil {
			return nil, err
		}
		return &entry, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrTrustRootKeyNotFound, kid)
}

func entryWithinWindow(entry TrustRootEntry, now time.Time) error {
	if entry.NotBefore != "" {
		t, err := time.Parse(time.RFC3339, entry.NotBefore)
		if err != nil {
			return fmt.Errorf("trustroot: entry %q not_before parse: %v", entry.KeyID, err)
		}
		if now.Before(t) {
			return fmt.Errorf("%w: %s not_before=%s", ErrTrustRootKeyNotYetValid, entry.KeyID, entry.NotBefore)
		}
	}
	if entry.NotAfter != "" {
		t, err := time.Parse(time.RFC3339, entry.NotAfter)
		if err != nil {
			return fmt.Errorf("trustroot: entry %q not_after parse: %v", entry.KeyID, err)
		}
		if now.After(t) {
			return fmt.Errorf("%w: %s not_after=%s", ErrTrustRootKeyExpired, entry.KeyID, entry.NotAfter)
		}
	}
	return nil
}

func entryScopeAllows(entry TrustRootEntry, packName string) error {
	if len(entry.Scopes) == 0 {
		return nil
	}
	for _, prefix := range entry.Scopes {
		if strings.HasPrefix(packName, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%w: pack %q not allowed by scopes %v", ErrTrustRootScopeViolation, packName, entry.Scopes)
}

// DeriveKeyID computes the same kid format used by skillmfr —
// "ed25519:" + hex(sha256(pub))[:16]. Exported so registry operators
// can compute kids when authoring a trust-root document by hand.
func DeriveKeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + base16(sum[:8])
}

// base16 is a tiny hex encoder used by DeriveKeyID. Avoids importing
// encoding/hex transitively for one consumer.
func base16(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0f]
	}
	return string(out)
}

// DecodePublicKey decodes the base64-encoded public_key field.
// Helper for callers that want to construct an ed25519.PublicKey
// from a TrustRootEntry.
func DecodePublicKey(entry TrustRootEntry) (ed25519.PublicKey, error) {
	// Strict (canonical-only) decode — this key gates signature verification
	// downstream, so its encoding must be canonical too (PORTS.md §7).
	raw, err := honestcrypto.DecodeBase64Strict(entry.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("trustroot: decode entry %q public_key: %w", entry.KeyID, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("trustroot: entry %q public_key length %d, want %d",
			entry.KeyID, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// LoadOrGenerateRootKey resolves a root operator signing key from
// the supplied path. If the file exists, it must contain a base64
// ed25519 private key (PrivateKeySize bytes). If missing, a fresh
// key is generated and persisted at 0600. The returned pub key is
// derived from the priv key. Empty path returns an error.
func LoadOrGenerateRootKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("trustroot: root key path required")
	}
	payload, err := os.ReadFile(path)
	if err == nil {
		// TrimSpace strips the trailing "\n" this function's own writer
		// appends; the trimmed content must be canonical base64 (PORTS.md §7).
		raw, derr := honestcrypto.DecodeBase64Strict(strings.TrimSpace(string(payload)))
		if derr != nil {
			return nil, nil, fmt.Errorf("trustroot: decode root key %q: %w", path, derr)
		}
		if len(raw) != ed25519.PrivateKeySize {
			return nil, nil, fmt.Errorf("trustroot: root key %q length %d, want %d",
				path, len(raw), ed25519.PrivateKeySize)
		}
		priv := ed25519.PrivateKey(raw)
		return priv, priv.Public().(ed25519.PublicKey), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("trustroot: read root key %q: %w", path, err)
	}
	pub, priv, gerr := ed25519.GenerateKey(nil)
	if gerr != nil {
		return nil, nil, fmt.Errorf("trustroot: generate root key: %w", gerr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("trustroot: mkdir root key dir: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(priv)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, nil, fmt.Errorf("trustroot: write root key %q: %w", path, err)
	}
	return priv, pub, nil
}
