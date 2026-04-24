// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import "net/http"

// HeaderDeprecationDate is the announced sunset for legacy X-Stoke-*
// headers per work-r1-rename.md S6-1.
const HeaderDeprecationDate = "2026-05-23"

// Header constants. The CanonicalX-prefixed entries are the
// post-rename names (the form RelayGate prefers when both are
// present); the LegacyX-prefixed entries continue to be emitted on
// egress and accepted on ingress through the 30-day window.
const (
	CanonicalSessionHeader = "X-R1-Session-ID"
	LegacySessionHeader    = "X-Stoke-Session-ID"

	CanonicalAgentHeader = "X-R1-Agent-ID"
	LegacyAgentHeader    = "X-Stoke-Agent-ID"

	CanonicalTaskHeader = "X-R1-Task-ID"
	LegacyTaskHeader    = "X-Stoke-Task-ID"

	CanonicalBearerHeader = "X-R1-Bearer"
	LegacyBearerHeader    = "X-Stoke-Bearer"
)

// HeaderPair is a (canonical, legacy) tuple consumed by DualHeader and
// AcceptHeader. The dedicated type (rather than a two-arg func)
// matches the verityrename precedent and keeps call sites readable.
type HeaderPair struct {
	Canonical, Legacy string
}

// All four S1-2 dual-accept header pairs as package vars so call sites
// don't have to re-form the pair literal each time.
var (
	SessionHeaderPair = HeaderPair{Canonical: CanonicalSessionHeader, Legacy: LegacySessionHeader}
	AgentHeaderPair   = HeaderPair{Canonical: CanonicalAgentHeader, Legacy: LegacyAgentHeader}
	TaskHeaderPair    = HeaderPair{Canonical: CanonicalTaskHeader, Legacy: LegacyTaskHeader}
	BearerHeaderPair  = HeaderPair{Canonical: CanonicalBearerHeader, Legacy: LegacyBearerHeader}
)

// DualHeader stamps both the canonical and legacy header names on h
// with the same value. Empty values are skipped on BOTH names rather
// than emitted as empty strings -- standalone R1 runs with no
// session/task/agent/bearer identity emit zero correlation headers.
//
// Call this at every egress site that used to set a single X-Stoke-*
// header. After 2026-05-23 (S6-1) the legacy assignment is dropped
// and the canonical .Set call inlines.
func DualHeader(h http.Header, pair HeaderPair, value string) {
	if h == nil || value == "" {
		return
	}
	h.Set(pair.Canonical, value)
	h.Set(pair.Legacy, value)
}

// AcceptHeader reads an incoming header value, preferring the
// canonical X-R1-* name when present and falling back to the legacy
// X-Stoke-* name. Returns the value and a bool indicating whether
// either header was present. Empty values count as "absent" so a
// caller that explicitly clears the legacy header doesn't accidentally
// suppress the canonical fallback path on the next request.
//
// Intentionally does NOT log on legacy use -- request-scoped logs
// would generate a flood under load and the window is only 30 days. A
// single-shot boot-time inventory (if needed) should be derived from
// downstream consumer dashboards (RelayGate audit-ingest already
// records both header families per S4-2).
func AcceptHeader(r *http.Request, pair HeaderPair) (string, bool) {
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
