// Package promptguard — fingerprint.go
//
// System-prompt cryptographic fingerprinting. Implements §T3 of
// specs/promptguard-hardening.md.
//
// Threat model bound: this guards against accidental modification of
// the assembled system prompt (packaging mistakes, supply-chain
// compromise on disk). It does NOT defend against an adversary with
// the signing key. See docs/security/prompt-injection.md for the full
// threat-model writeup.
//
// Three states are surfaced by VerifySystemPrompt:
//
//   - Verified : SHA-256 of the supplied prompt matches the stored
//                fingerprint AND the ed25519 signature verifies.
//   - Modified : SHA-256 differs but the signature still verifies.
//                Means a legitimate signed release changed the bytes.
//                Operator inspects the diff and `--accept-modified`.
//   - Tampered : signature does NOT verify. Either the fingerprint
//                blob was edited without re-signing, or the key
//                rotated unexpectedly, or the signature is malformed.
//
// Keys are loaded from env vars via SignerFromEnv/VerifierFromEnv with
// the first-wins / second-fallback shape the spec mandates. Absent any
// key, signing is a no-op WARN — daemons in dev mode must keep working
// without a configured key.
package promptguard

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// VerifyStatus is the tri-state result returned by VerifySystemPrompt.
type VerifyStatus int

const (
	// VerifyUnknown is the zero value, never returned by VerifySystemPrompt
	// in normal operation. Reserved so callers that range over the enum
	// have a stable default.
	VerifyUnknown VerifyStatus = iota
	// Verified means SHA matches and signature is valid.
	Verified
	// Modified means SHA differs but signature is valid.
	Modified
	// Tampered means signature does not verify.
	Tampered
)

// String renders the status for log lines and operator output.
func (v VerifyStatus) String() string {
	switch v {
	case Verified:
		return "Verified"
	case Modified:
		return "Modified"
	case Tampered:
		return "Tampered"
	default:
		return "Unknown"
	}
}

// SignedFingerprint is the persisted shape produced by SignSystemPrompt
// and consumed by VerifySystemPrompt. The Signature field signs the
// canonical-JSON of {sha256, signed_at, key_fp} (sorted keys) — the
// Signature field is excluded from the signed input by construction.
type SignedFingerprint struct {
	// PromptSHA256 is the hex-encoded SHA-256 of the assembled prompt
	// at the time the signature was produced.
	PromptSHA256 string `json:"prompt_sha256"`
	// SignedAt is the wall-clock time the signature was produced.
	SignedAt time.Time `json:"signed_at"`
	// KeyFingerprint is the first 16 hex chars of SHA-256(pubkey). It
	// disambiguates rotations so a stored fingerprint produced by an
	// old key surfaces as Tampered rather than silently re-verifying.
	KeyFingerprint string `json:"key_fingerprint"`
	// Signature is the hex-encoded ed25519 signature over the
	// canonical-JSON of {sha256, signed_at, key_fp} with sorted keys.
	Signature string `json:"signature"`
}

// Env var names used for key loading. The first wins, the second is a
// fallback so operators who already configured the ledger signing key
// do not need to add a second env var to enable system-prompt
// fingerprinting in dev environments.
const (
	envPromptguardSigningKey = "R1_PROMPTGUARD_SIGNING_KEY"
	envLedgerSigningKey      = "R1_LEDGER_SIGNING_KEY"
	envPromptguardVerifyKey  = "R1_PROMPTGUARD_VERIFY_KEY"
	envLedgerVerifyKey       = "R1_LEDGER_VERIFY_KEY"
)

// ErrNoSigningKey is returned by SignSystemPrompt when no signing key
// is configured. Callers in daemon-startup paths SHOULD log a WARN and
// continue (dev environments routinely run without a key). Tests use
// errors.Is to detect this path.
var ErrNoSigningKey = errors.New("promptguard: no signing key configured")

// ErrNoVerifyKey is returned by VerifySystemPrompt when no verify key
// is reachable. The verify-system-prompt CLI surfaces this as a
// distinct exit code so operators do not confuse "no key" with
// "signature mismatch".
var ErrNoVerifyKey = errors.New("promptguard: no verify key configured")

// SignerFromEnv loads an ed25519 private key from one of the named env
// vars (first-wins). The value is hex-encoded 64-byte ed25519 private
// key material. Returns ErrNoSigningKey if no env var is set so callers
// can branch on the no-key path without parsing strings.
func SignerFromEnv(names ...string) (ed25519.PrivateKey, error) {
	for _, n := range names {
		v := os.Getenv(n)
		if v == "" {
			continue
		}
		raw, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("promptguard: env %s: hex decode: %w", n, err)
		}
		if len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("promptguard: env %s: want %d bytes, got %d",
				n, ed25519.PrivateKeySize, len(raw))
		}
		return ed25519.PrivateKey(raw), nil
	}
	return nil, ErrNoSigningKey
}

// VerifierFromEnv loads an ed25519 public key from one of the named
// env vars (first-wins). Falls back to deriving the public key from
// the signing-key env vars if no dedicated verify key is set. The
// derive path is the common case in dev: operators set a single env
// var and the verifier reuses it.
func VerifierFromEnv(verifyNames []string, signNames []string) (ed25519.PublicKey, error) {
	for _, n := range verifyNames {
		v := os.Getenv(n)
		if v == "" {
			continue
		}
		raw, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("promptguard: env %s: hex decode: %w", n, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("promptguard: env %s: want %d bytes, got %d",
				n, ed25519.PublicKeySize, len(raw))
		}
		return ed25519.PublicKey(raw), nil
	}
	// Fallback: derive public from configured signing key.
	priv, err := SignerFromEnv(signNames...)
	if err != nil {
		if errors.Is(err, ErrNoSigningKey) {
			return nil, ErrNoVerifyKey
		}
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}

// DefaultSigner is the convenience accessor used by SignSystemPrompt.
// It walks the promptguard-specific env var first, then the ledger env
// var as fallback so operators with an existing ledger key do not have
// to copy it across.
func DefaultSigner() (ed25519.PrivateKey, error) {
	return SignerFromEnv(envPromptguardSigningKey, envLedgerSigningKey)
}

// DefaultVerifier mirrors DefaultSigner for the verification path.
func DefaultVerifier() (ed25519.PublicKey, error) {
	return VerifierFromEnv(
		[]string{envPromptguardVerifyKey, envLedgerVerifyKey},
		[]string{envPromptguardSigningKey, envLedgerSigningKey},
	)
}

// shortKeyID returns the first 16 hex chars of SHA-256(pubkey). Used as
// the KeyFingerprint stamp on a SignedFingerprint. Stable across
// goroutines; no shared state.
func shortKeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// canonicalSignedJSON produces the JSON bytes that get signed. Keys are
// emitted in alphabetic order so the signature is deterministic
// regardless of struct field declaration order. The Signature field
// itself is NOT part of the canonical input by construction.
func canonicalSignedJSON(sha256Hex, signedAtRFC3339, keyFP string) ([]byte, error) {
	m := map[string]string{
		"key_fp":    keyFP,
		"sha256":    sha256Hex,
		"signed_at": signedAtRFC3339,
	}
	// Sort keys explicitly via a sorted slice so the marshaller order
	// is stable across Go versions. json.Marshal on a map already emits
	// alphabetically since Go 1.12, but we double up so a regression in
	// the stdlib does not silently break signature compatibility.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kv struct {
		K, V string
	}
	pairs := make([]kv, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, kv{K: k, V: m[k]})
	}
	// Build the JSON manually to guarantee byte-stable ordering.
	out := []byte{'{'}
	for i, p := range pairs {
		if i > 0 {
			out = append(out, ',')
		}
		kb, err := json.Marshal(p.K)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(p.V)
		if err != nil {
			return nil, err
		}
		out = append(out, kb...)
		out = append(out, ':')
		out = append(out, vb...)
	}
	out = append(out, '}')
	return out, nil
}

// SignSystemPrompt computes a SHA-256 of promptText and signs the
// canonical {sha256, signed_at, key_fp} blob with the env-configured
// private key. Returns ErrNoSigningKey if no key is configured so
// callers in daemon-startup paths can WARN-and-continue.
func SignSystemPrompt(promptText string) (SignedFingerprint, error) {
	priv, err := DefaultSigner()
	if err != nil {
		return SignedFingerprint{}, err
	}
	return SignSystemPromptWithKey(promptText, priv, time.Now().UTC())
}

// SignSystemPromptWithKey is the deterministic-time variant used by
// tests. Daemon code paths use SignSystemPrompt which fills time.Now().
func SignSystemPromptWithKey(promptText string, priv ed25519.PrivateKey, signedAt time.Time) (SignedFingerprint, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return SignedFingerprint{}, fmt.Errorf("promptguard: invalid signing key size %d", len(priv))
	}
	sum := sha256.Sum256([]byte(promptText))
	sha := hex.EncodeToString(sum[:])
	pub := priv.Public().(ed25519.PublicKey)
	kfp := shortKeyID(pub)
	signedAtStr := signedAt.UTC().Format(time.RFC3339Nano)
	body, err := canonicalSignedJSON(sha, signedAtStr, kfp)
	if err != nil {
		return SignedFingerprint{}, fmt.Errorf("promptguard: canonicalize: %w", err)
	}
	sig := ed25519.Sign(priv, body)
	return SignedFingerprint{
		PromptSHA256:   sha,
		SignedAt:       signedAt.UTC(),
		KeyFingerprint: kfp,
		Signature:      hex.EncodeToString(sig),
	}, nil
}

// VerifySystemPrompt compares promptText's SHA-256 against fp's stored
// SHA-256 AND verifies the signature against the env-configured public
// key. Returns the tri-state status. The error return is non-nil for
// out-of-band failure modes (missing key, malformed signature hex);
// signature-mismatch returns (Tampered, nil) so callers can branch on
// status without unwrapping errors.
func VerifySystemPrompt(promptText string, fp SignedFingerprint) (VerifyStatus, error) {
	pub, err := DefaultVerifier()
	if err != nil {
		return VerifyUnknown, err
	}
	return VerifySystemPromptWithKey(promptText, fp, pub)
}

// VerifySystemPromptWithKey is the test-seam variant.
func VerifySystemPromptWithKey(promptText string, fp SignedFingerprint, pub ed25519.PublicKey) (VerifyStatus, error) {
	if len(pub) != ed25519.PublicKeySize {
		return VerifyUnknown, fmt.Errorf("promptguard: invalid verify key size %d", len(pub))
	}
	if fp.Signature == "" {
		return Tampered, nil
	}
	sig, err := hex.DecodeString(fp.Signature)
	if err != nil {
		// Malformed signature hex → Tampered. Operators inspect the
		// audit trail to discover whether someone hand-edited the
		// fingerprint blob.
		return Tampered, nil
	}
	signedAtStr := fp.SignedAt.UTC().Format(time.RFC3339Nano)
	body, err := canonicalSignedJSON(fp.PromptSHA256, signedAtStr, fp.KeyFingerprint)
	if err != nil {
		return VerifyUnknown, fmt.Errorf("promptguard: canonicalize: %w", err)
	}
	if !ed25519.Verify(pub, body, sig) {
		return Tampered, nil
	}
	// Signature valid. Compare the live SHA-256 to the fingerprint's.
	sum := sha256.Sum256([]byte(promptText))
	sha := hex.EncodeToString(sum[:])
	if sha == fp.PromptSHA256 {
		return Verified, nil
	}
	return Modified, nil
}

// FingerprintAssembledPromptForLedger is a thin helper that produces a
// JSON-marshalable wire shape suitable for embedding in a ledger node's
// Content field. Used by the daemon-startup boot path to write a
// SystemPromptFingerprint node without re-implementing the marshal
// every time.
func FingerprintAssembledPromptForLedger(fp SignedFingerprint) ([]byte, error) {
	return json.Marshal(fp)
}
