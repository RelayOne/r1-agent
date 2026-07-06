package skillmfr

// pack_trust.go — supply-chain trust policy for skill packs.
//
// VerifyPackSignature proves a pack was signed by whoever holds the private
// key EMBEDDED IN THE PACK (self-signed) — it does not, on its own, establish
// that the signer is trusted. An attacker can mint their own keypair, sign a
// malicious pack, and embed the matching public key; the crypto check passes.
// VerifyPackTrusted adds the missing hop: the signer must be in a configured
// trust anchor, and an unsigned pack is rejected fail-closed. Both relaxations
// are opt-in via environment so checked-in / dev flows stay ergonomic while
// externally-sourced packs are gated.

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// EnvAllowUnsignedPacks, when truthy, accepts unsigned AND validly
	// self-signed packs (the pre-hardening behavior). Explicit dev/local
	// opt-in. It does NOT bypass a present-but-cryptographically-invalid
	// signature — a broken signature is always a hard failure.
	EnvAllowUnsignedPacks = "R1_ALLOW_UNSIGNED_SKILL_PACKS"
	// EnvTrustedPackKeys is a comma/space/newline-separated list of trusted
	// signer identities. Each entry may be a signature key_id (e.g.
	// "ed25519:abcdef12") or a base64-encoded ed25519 public key. A signed
	// pack is trusted iff its key_id or public key matches an entry.
	EnvTrustedPackKeys = "R1_TRUSTED_SKILL_PACK_KEYS"
)

// ErrPackUntrusted reports a pack whose signature is cryptographically valid
// but whose signer is not in the configured trust anchor.
var ErrPackUntrusted = errors.New("skillmfr: pack signed by untrusted key")

// PackTrustPolicy is the fail-closed trust policy applied to a pack.
type PackTrustPolicy struct {
	// AllowUnsigned accepts unsigned and self-signed packs (dev opt-in).
	AllowUnsigned bool
	// TrustedKeys is the set of trusted signer identities (key_id or base64
	// public key). Empty means "no trust anchor": a self-signed pack is then
	// NOT trusted unless AllowUnsigned.
	TrustedKeys map[string]bool
}

// LoadPackTrustPolicyFromEnv builds a PackTrustPolicy from the environment.
func LoadPackTrustPolicyFromEnv() PackTrustPolicy {
	return PackTrustPolicy{
		AllowUnsigned: envTruthy(os.Getenv(EnvAllowUnsignedPacks)),
		TrustedKeys:   ParseTrustedPackKeys(os.Getenv(EnvTrustedPackKeys)),
	}
}

// ParseTrustedPackKeys parses a trusted-keys list. Returns nil when empty so
// callers can cheaply test "no trust anchor configured".
func ParseTrustedPackKeys(v string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out[f] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// VerifyPackTrusted enforces the supply-chain trust policy fail-closed:
//
//   - unsigned pack            -> ErrPackUnsigned            (unless AllowUnsigned)
//   - signed, crypto-invalid   -> ErrPackSignatureInvalid    (ALWAYS — even with
//     AllowUnsigned: a broken signature is never tolerated)
//   - signed, valid, untrusted -> ErrPackUntrusted           (unless AllowUnsigned)
//   - signed, valid, trusted   -> (signature, nil)
//   - AllowUnsigned            -> unsigned/self-signed accepted (signature or nil)
func VerifyPackTrusted(packRoot string, policy PackTrustPolicy) (*PackSignature, error) {
	sig, err := VerifyPackSignature(packRoot)
	if err != nil {
		if errors.Is(err, ErrPackUnsigned) {
			if policy.AllowUnsigned {
				return nil, nil
			}
			return nil, ErrPackUnsigned
		}
		// Malformed/tampered signature — hard fail regardless of opt-in.
		return nil, err
	}
	if policy.AllowUnsigned {
		return sig, nil
	}
	if policy.TrustedKeys[sig.KeyID] || policy.TrustedKeys[sig.PublicKey] {
		return sig, nil
	}
	return nil, fmt.Errorf("%w: key_id=%q", ErrPackUntrusted, sig.KeyID)
}
