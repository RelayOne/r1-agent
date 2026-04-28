// Identity headers for outbound Truecom requests (work-stoke T20).
//
// Every outbound /v1/hire (and, by extension, any other authenticated
// Truecom call that the caller tags as a hire-flow request) emits
// four identity headers that let the far side verify the caller is
// who their DID says they are without round-tripping through a DPoP
// JWT for every replay check:
//
//   X-Truecom-DID:         did:plc:<instance DID>
//   X-Truecom-Signature:   base64(Ed25519 sig over canonical input)
//   X-Truecom-Timestamp:   <ms epoch>
//   X-Truecom-Contract-ID: <contract UUID, when known>
//
// The canonical input signed by the Ed25519 key is:
//
//   "${method}.${path}.${ts}.${sha256hex(body)}"
//
// Upper-cased method, path as sent on the wire (leading "/"), timestamp
// matching the X-Truecom-Timestamp header, and the lowercase hex
// sha256 of the request body (empty-string sha256 when body is nil).
// Deterministic — tests can recompute and verify without seeing the
// signer's state.
//
// During the 30-day TrustPlane → Truecom rename window, inbound
// handlers accept either X-Truecom-* or X-TrustPlane-* (see
// ReadIdentityHeaders). Outbound handlers always emit the new name so
// we aren't migrating twice.
package truecom

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header canonical names (outbound). Inbound readers accept either
// these or their X-TrustPlane-* pre-rename counterparts.
const (
	HeaderDID        = "X-Truecom-DID"
	HeaderSignature  = "X-Truecom-Signature"
	HeaderTimestamp  = "X-Truecom-Timestamp"
	HeaderContractID = "X-Truecom-Contract-ID"

	// Pre-rename aliases kept for the 30-day TrustPlane transition.
	// Remove after 2026-05-22.
	legacyHeaderDID        = "X-TrustPlane-DID"
	legacyHeaderSignature  = "X-TrustPlane-Signature"
	legacyHeaderTimestamp  = "X-TrustPlane-Timestamp"
	legacyHeaderContractID = "X-TrustPlane-Contract-ID"
)

// IdentitySigner builds identity headers for outbound requests. It
// owns the instance DID + the Ed25519 private key; callers hand it the
// method/path/body and get back a ready-to-merge http.Header.
//
// Safe for concurrent use — only mutable state is time.Now. Tests can
// inject Now for deterministic signatures.
type IdentitySigner struct {
	// DID is the instance's Truecom DID, e.g. "did:plc:stoke-abc".
	// Emitted as X-Truecom-DID verbatim.
	DID string
	// Priv is the Ed25519 private key corresponding to the public key
	// registered with Truecom. Must be ed25519.PrivateKeySize bytes.
	Priv ed25519.PrivateKey
	// Now, when non-nil, overrides time.Now for tests.
	Now func() time.Time
}

// NewIdentitySigner validates inputs and returns an IdentitySigner.
// Empty DID or wrong-length key fail fast at construction so request
// paths never have to branch on "is my signer ready?".
func NewIdentitySigner(did string, priv ed25519.PrivateKey) (*IdentitySigner, error) {
	if strings.TrimSpace(did) == "" {
		return nil, errors.New("truecom: IdentitySigner requires non-empty DID")
	}
	if l := len(priv); l != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("truecom: IdentitySigner private key must be %d bytes, got %d", ed25519.PrivateKeySize, l)
	}
	return &IdentitySigner{DID: did, Priv: priv}, nil
}

// BuildIdentityHeaders produces the four X-Truecom-* headers for a
// request. contractID may be empty; when empty the Contract-ID header
// is omitted (not set to "") so the receiver can distinguish "no
// contract" from "empty string contract".
//
// method is upper-cased for the canonical input; path is used as-is
// (caller is responsible for the leading "/"); body may be nil.
func (s *IdentitySigner) BuildIdentityHeaders(method, path string, body []byte, contractID string) (http.Header, error) {
	if s == nil {
		return nil, errors.New("trustplane: nil IdentitySigner")
	}
	ts := s.now().UnixMilli()
	tsStr := strconv.FormatInt(ts, 10)
	sum := sha256.Sum256(body)
	input := strings.ToUpper(method) + "." + path + "." + tsStr + "." + hex.EncodeToString(sum[:])
	sig := ed25519.Sign(s.Priv, []byte(input))

	h := http.Header{}
	h.Set(HeaderDID, s.DID)
	h.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	h.Set(HeaderTimestamp, tsStr)
	if c := strings.TrimSpace(contractID); c != "" {
		h.Set(HeaderContractID, c)
	}
	return h, nil
}

// ApplyIdentityHeaders is a convenience wrapper: build + copy onto an
// existing http.Request. Overwrites any pre-existing identity headers
// on req.
func (s *IdentitySigner) ApplyIdentityHeaders(req *http.Request, body []byte, contractID string) error {
	h, err := s.BuildIdentityHeaders(req.Method, req.URL.Path, body, contractID)
	if err != nil {
		return err
	}
	for k, v := range h {
		if len(v) > 0 {
			req.Header.Set(k, v[0])
		}
	}
	return nil
}

func (s *IdentitySigner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// IdentityHeaderValues is the parsed view of the four identity headers
// on an inbound request. Empty fields mean the header was absent under
// both the X-Truecom-* and legacy X-TrustPlane-* names.
type IdentityHeaderValues struct {
	DID        string
	Signature  string
	Timestamp  string
	ContractID string
	// UsedLegacy reports true when at least one field came from the
	// X-Truecom-* namespace — useful for metrics during the transition.
	UsedLegacy bool
}

// ReadIdentityHeaders extracts the four identity headers from an
// inbound request, falling back to the X-TrustPlane-* names when the
// X-Truecom-* variant is absent. During the 30-day transition both
// namespaces are equally valid. Returns the zero value when no
// identity headers are present.
func ReadIdentityHeaders(h http.Header) IdentityHeaderValues {
	var v IdentityHeaderValues
	pick := func(primary, legacy string) (string, bool) {
		if got := h.Get(primary); got != "" {
			return got, false
		}
		if got := h.Get(legacy); got != "" {
			return got, true
		}
		return "", false
	}
	var anyLegacy bool
	if s, legacy := pick(HeaderDID, legacyHeaderDID); s != "" {
		v.DID = s
		anyLegacy = anyLegacy || legacy
	}
	if s, legacy := pick(HeaderSignature, legacyHeaderSignature); s != "" {
		v.Signature = s
		anyLegacy = anyLegacy || legacy
	}
	if s, legacy := pick(HeaderTimestamp, legacyHeaderTimestamp); s != "" {
		v.Timestamp = s
		anyLegacy = anyLegacy || legacy
	}
	if s, legacy := pick(HeaderContractID, legacyHeaderContractID); s != "" {
		v.ContractID = s
		anyLegacy = anyLegacy || legacy
	}
	v.UsedLegacy = anyLegacy
	return v
}

// VerifyIdentitySignature recomputes the canonical input from
// (method, path, body, values) and verifies the X-Truecom-Signature
// header against pub. Returns nil on match, an error otherwise.
//
// Callers are responsible for checking Timestamp freshness separately;
// this helper only adjudicates the signature bytes.
func VerifyIdentitySignature(pub ed25519.PublicKey, method, path string, body []byte, v IdentityHeaderValues) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("truecom: verify: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	if v.Signature == "" || v.Timestamp == "" {
		return errors.New("truecom: verify: missing signature or timestamp header")
	}
	sig, err := base64.StdEncoding.DecodeString(v.Signature)
	if err != nil {
		return fmt.Errorf("truecom: verify: decode signature: %w", err)
	}
	sum := sha256.Sum256(body)
	input := strings.ToUpper(method) + "." + path + "." + v.Timestamp + "." + hex.EncodeToString(sum[:])
	if !ed25519.Verify(pub, []byte(input), sig) {
		return errors.New("truecom: verify: signature does not match canonical input")
	}
	return nil
}
