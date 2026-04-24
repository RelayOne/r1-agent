// Package r1env provides the canonical R1_* env var helper. The S1-1
// 90-day dual-accept window (with STOKE_* legacy fallback) elapsed
// 2026-07-23 per work-r1-rename.md §S6-3; the legacy half has been
// removed. Get(canonical, legacy) now reads only the canonical name;
// the `legacy` parameter is retained for call-site compatibility but
// ignored. Deployments that still set only the legacy STOKE_* name
// will read "" and the resulting "missing required env" failure at
// each call site surfaces the canonical name operators must migrate to.
package r1env

import (
	"os"
)

// deprecationDate records the date the legacy STOKE_* fallback was
// retired. Kept as a documentation constant so any historical log
// references can still be cross-referenced; no longer consulted at
// runtime.
const deprecationDate = "2026-07-23"

// Get returns the canonical env var value if set; otherwise "". The
// `legacy` parameter is accepted for call-site compatibility but is
// never read -- the S6-3 drop (2026-07-23) removed the legacy fallback.
//
// Callers should pass the canonical var name, e.g.
// r1env.Get("R1_ADMIN_PASS", "STOKE_ADMIN_PASS"). A follow-up sweep
// can drop the second argument from call sites; retaining the param
// here keeps this drop's diff bounded to the r1env package.
func Get(canonical, _ string) string {
	return os.Getenv(canonical)
}

// ResetWarnOnceForTests is a no-op post-S6-3 (no warn-once state
// remains). Retained so existing test helpers that called it keep
// compiling unchanged.
func ResetWarnOnceForTests() {}
