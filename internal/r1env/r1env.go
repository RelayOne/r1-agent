// Package r1env implements the STOKE_* → R1_* env var dual-accept window
// per work-r1-rename.md §S1-1. The package exposes a single helper,
// Get(canonical, legacy), that reads the canonical R1_* variable first
// and falls back to the legacy STOKE_* variable when the canonical one
// is unset or empty.
//
// When the caller ends up using a legacy variable, a single-shot WARN
// line is logged via the standard library log package:
//
//	WARN: legacy env STOKE_FOO used; rename to R1_FOO before 2026-07-23
//
// The warning is rate-limited: one log line per (canonical, legacy)
// pair per process lifetime, tracked via sync.Once keyed on the pair.
// Callers can therefore invoke Get in hot paths without spamming
// stderr; the warning still fires exactly once so operators notice
// the deprecation without missing it.
//
// The 90-day deprecation window ends 2026-07-23. After that, callers
// should stop passing the legacy argument and the helper will stop
// reading STOKE_* entirely per phase S6-3.
package r1env

import (
	"log"
	"os"
	"sync"
)

// deprecationDate is the announced sunset for legacy STOKE_* env vars.
// Baked into every WARN line so operators see the exact drop date.
const deprecationDate = "2026-07-23"

// warnOnce tracks which (canonical, legacy) pairs have already logged
// a deprecation warning this process. Access is via the package-wide
// mutex below because the once values are looked up, created lazily,
// and reused.
var (
	warnOnceMu sync.Mutex
	warnOnce   = map[string]*sync.Once{}
)

// Get returns the canonical env var value if set and non-empty;
// otherwise it returns the legacy value (and logs a single-shot WARN
// when the legacy var was the one that supplied the value).
//
// Semantics:
//   - canonical set, legacy unset  → return canonical, no warning.
//   - canonical set, legacy set    → return canonical, no warning (canonical wins).
//   - canonical unset, legacy set  → return legacy, log single WARN.
//   - canonical unset, legacy unset → return "".
//
// Callers should pass the full variable names, e.g.
// r1env.Get("R1_ADMIN_PASS", "STOKE_ADMIN_PASS"). Passing an empty
// legacy string disables the fallback (equivalent to os.Getenv(canonical)).
func Get(canonical, legacy string) string {
	if v := os.Getenv(canonical); v != "" {
		return v
	}
	if legacy == "" {
		return ""
	}
	v := os.Getenv(legacy)
	if v == "" {
		return ""
	}
	logLegacyUsedOnce(canonical, legacy)
	return v
}

// logLegacyUsedOnce emits the deprecation WARN line at most once per
// (canonical, legacy) pair for the lifetime of the process.
func logLegacyUsedOnce(canonical, legacy string) {
	key := legacy + "|" + canonical
	warnOnceMu.Lock()
	once, ok := warnOnce[key]
	if !ok {
		once = &sync.Once{}
		warnOnce[key] = once
	}
	warnOnceMu.Unlock()
	once.Do(func() {
		log.Printf("WARN: legacy env %s used; rename to %s before %s", legacy, canonical, deprecationDate)
	})
}

// ResetWarnOnceForTests clears the internal once-table so tests can
// exercise the warning path repeatedly without bleeding state between
// cases. Not exported to non-test callers via a build tag because the
// helper is tiny and test-only use is documented; tests that need
// isolation should call this in a subtest setup.
func ResetWarnOnceForTests() {
	warnOnceMu.Lock()
	warnOnce = map[string]*sync.Once{}
	warnOnceMu.Unlock()
}
