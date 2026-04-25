// Package verityrename provides header dual-accept helpers for the
// Veritize rename window (V1-2, 30-day window ending 2026-05-23).
//
// Canonical names are X-Veritize-*; legacy names are X-Verity-*. Both
// are accepted on ingress and emitted on egress through the window.
// At V6-1 (2026-05-23): drop legacy emit; keep legacy accept for one
// more grace cycle then drop entirely.
package verityrename

import "net/http"

// Header constants. Canonical is the post-rename preferred name;
// Legacy is the pre-rename name accepted through 2026-05-23.
const (
	LegacyClientHeader    = "X-Verity-Client"
	CanonicalClientHeader = "X-Veritize-Client"

	LegacyOrgHeader    = "X-Verity-Org"
	CanonicalOrgHeader = "X-Veritize-Org"

	LegacySignatureHeader    = "X-Verity-Signature"
	CanonicalSignatureHeader = "X-Veritize-Signature"
)

// HeaderPair is a (canonical, legacy) tuple for DualSend and DualAccept.
type HeaderPair struct {
	Canonical, Legacy string
}

// Pre-formed pairs so call sites don't construct literals.
var (
	ClientHeaderPair    = HeaderPair{Canonical: CanonicalClientHeader, Legacy: LegacyClientHeader}
	OrgHeaderPair       = HeaderPair{Canonical: CanonicalOrgHeader, Legacy: LegacyOrgHeader}
	SignatureHeaderPair = HeaderPair{Canonical: CanonicalSignatureHeader, Legacy: LegacySignatureHeader}
)

// DualSend stamps both the canonical and legacy header names on h with
// the same value. Empty value is a no-op on both names.
// Use at every egress site through 2026-05-23; then drop legacy assignment.
func DualSend(h http.Header, pair HeaderPair, value string) {
	if h == nil || value == "" {
		return
	}
	h.Set(pair.Canonical, value)
	h.Set(pair.Legacy, value)
}

// DualAccept reads an incoming header value from r, preferring the
// canonical name when present and falling back to the legacy name.
// Returns (value, true) when either header is present and non-empty,
// ("", false) when both are absent.
// Canonical wins when both are present — allows senders to upgrade
// without breaking receivers that still forward both.
func DualAccept(r *http.Request, pair HeaderPair) (string, bool) {
	if r == nil || r.Header == nil {
		return "", false
	}
	if v := r.Header.Get(pair.Canonical); v != "" {
		return v, true
	}
	if v := r.Header.Get(pair.Legacy); v != "" {
		return v, true
	}
	return "", false
}
