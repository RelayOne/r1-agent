// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

// AuditDeprecationDate is the announced sunset for legacy
// stoke_session_id / stoke_* audit metadata keys per
// work-r1-rename.md S6-2.
const AuditDeprecationDate = "2026-06-22"

// Canonical audit-metadata key constants. The legacy half is also
// emitted via DualAuditMeta during the 60-day window. Other audit
// keys can be added as new dual-emit sites land.
const (
	CanonicalAuditSessionIDKey = "r1_session_id"
	LegacyAuditSessionIDKey    = "stoke_session_id"
)

// DualAuditMeta writes BOTH the canonical and legacy audit-metadata
// keys into meta with the same value. Used at every audit-event
// emission site so downstream consumers reading either key during the
// 60-day window see identical content.
//
// Safe to call with a nil meta map: the function no-ops. Empty values
// still get written (some callers rely on the key being PRESENT in the
// envelope even when the session-ID is intentionally blank, e.g.
// pre-session bootstrap events). If the legacy key happens to equal
// the canonical key (defensive against future config drift), only one
// write fires.
func DualAuditMeta(meta map[string]any, canonical, legacy string, value any) {
	if meta == nil {
		return
	}
	meta[canonical] = value
	if legacy != "" && legacy != canonical {
		meta[legacy] = value
	}
}
