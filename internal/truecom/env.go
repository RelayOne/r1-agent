package truecom

import (
	"log"
	"os"
	"strings"
)

// Canonical "truecom" env names with their legacy "trustplane" aliases.
// Per Truecom Task 23, the cross-portfolio rename is on a 90-day
// dual-accept window that closes 2026-07-21 — after that, only the
// canonical names are honored.
//
// R1's code-level types remain stoke/trustplane (no rename planned —
// see work-stoke.md §7 DO NOT #2). This helper lets operators set
// either the canonical or legacy env name while the portfolio
// transitions.
const (
	// Legacy removal deadline. After this date, envOrFallback should
	// hard-fail on legacy-only settings instead of warning.
	LegacyEnvSunsetDate = "2026-07-21"
)

// envOrFallback returns the value of canonical env var, or the value
// of legacy (with a one-time WARN log) if canonical is unset. Returns
// "" if neither is set. Whitespace-only values are treated as unset so
// accidental `export X=` lines do not select an empty backend.
func envOrFallback(canonical, legacy string) string {
	if v := strings.TrimSpace(os.Getenv(canonical)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(legacy)); v != "" {
		log.Printf("WARN: %s is deprecated; use %s instead (legacy support removed %s)",
			legacy, canonical, LegacyEnvSunsetDate)
		return v
	}
	return ""
}
