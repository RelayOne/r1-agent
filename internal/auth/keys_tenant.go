package auth

// keys_tenant.go — per-tenant key derivation.
//
// HS256 single-tenant deployments use the root secret directly.
// HS256 multi-tenant deployments derive a per-tenant secret via
// HKDF-SHA256 with a versioned info string. Versioning the info string
// lets us roll the derivation scheme without breaking older tokens —
// both sides cut over together via the spec's "Open question" §10.
//
// RS256 keeps a shared private key but derives a per-tenant KID so the
// token header carries enough info for the verifier to route to the
// right per-tenant lookup table without parsing claims first. The KID
// is purely a routing hint; signature verification still uses the
// shared key.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"

	"golang.org/x/crypto/hkdf"
)

// hkdfTenantInfoV1 is the HKDF info string for v1 per-tenant secret
// derivation. The literal value is the load-bearing piece of the
// cross-language contract (spec §10 open question): if the TS side
// uses a different literal, both sides must roll in lockstep.
const hkdfTenantInfoV1 = "r1-jwt-tenant-v1:"

// DeriveTenantHMACSecret returns a 32-byte HMAC secret for the given
// tenant, derived from a root secret via HKDF-SHA256.
//
//	info = "r1-jwt-tenant-v1:" + tenantID
//	salt = nil    (root secret is already random; HKDF tolerates nil salt)
//
// The output is always exactly 32 bytes — matches the TS contract's
// minimum-secret-length check.
func DeriveTenantHMACSecret(rootSecret []byte, tenantID string) []byte {
	info := []byte(hkdfTenantInfoV1 + tenantID)
	reader := hkdf.New(sha256.New, rootSecret, nil, info)
	out := make([]byte, 32)
	// hkdf.New always returns a Reader that can produce up to
	// 255*HashLen bytes; 32 < 255*32 so io.ReadFull cannot fail here
	// except via a panic in the SHA-256 impl. We still check.
	if _, err := io.ReadFull(reader, out); err != nil {
		// HKDF should never fail at this output size with SHA-256.
		// Panic is the right call — silent zero-key would be a
		// catastrophic security regression.
		panic("auth: HKDF derivation failed: " + err.Error())
	}
	return out
}

// DeriveTenantKID returns a per-tenant KID built from the root KID +
// a 6-byte tag derived from the tenant id. Format: "<rootKID>-t-<tag>".
//
// The tag is sha256(tenantID)[:6] hex-encoded (12 chars), giving 48 bits
// of collision resistance — plenty for the per-deployment tenant set.
func DeriveTenantKID(rootKID, tenantID string) string {
	h := sha256.Sum256([]byte(tenantID))
	tag := hex.EncodeToString(h[:6])
	return rootKID + "-t-" + tag
}
