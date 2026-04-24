// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"os"
	"strconv"
	"strings"

	"github.com/RelayOne/r1/internal/r1env"
)

// EnvDeprecationDate is the announced sunset for legacy STOKE_* env
// vars per work-r1-rename.md S6-3. Operators see this date in every
// single-shot WARN line emitted by LookupEnv.
const EnvDeprecationDate = "2026-07-23"

// EnvLegacyDropEnv is the feature-flag env var that disables legacy
// STOKE_* fallback when set to a truthy value. Default behaviour
// (unset / "false") leaves dual-accept active. At S6-3 the default
// flips to true and the legacy lookup branch becomes a hard failure
// rather than a silent fallback.
const EnvLegacyDropEnv = "R1_ENV_LEGACY_DROP"

// LookupEnv resolves a (canonical, legacy) env-var pair under the S1-1
// 90-day dual-accept window. It returns:
//
//   - the canonical R1_* value if set and non-empty,
//   - otherwise the legacy STOKE_* value (with a single-shot WARN per
//     pair via the underlying r1env package),
//   - "" if neither is set, or
//   - "" if R1_ENV_LEGACY_DROP is truthy AND only the legacy var was
//     set (post-window canonical-only mode).
//
// Callers pass full var names, e.g.
// r1rename.LookupEnv("R1_DATA_DIR", "STOKE_DATA_DIR"). Passing an
// empty legacy disables the fallback (equivalent to os.Getenv on
// canonical only).
//
// LookupEnv is the single entry point the work-order specifies; it
// delegates to internal/r1env for the rate-limited WARN bookkeeping so
// existing call sites that already migrated to r1env.Get keep their
// once-per-pair semantics. Direct r1env.Get calls remain valid and
// produce identical behaviour -- this wrapper exists so new call sites
// have one canonical helper to reach for, matching the
// internal/verityrename precedent.
func LookupEnv(canonical, legacy string) string {
	if v := os.Getenv(canonical); v != "" {
		return v
	}
	if legacy == "" {
		return ""
	}
	if EnvLegacyDropEnabled() {
		// Post-window canonical-only mode: ignore legacy entirely so an
		// operator who forgot to flip a deploy config sees an empty
		// value (and the resulting "missing required env" failure)
		// instead of silently inheriting the legacy value.
		return ""
	}
	// Delegate to r1env.Get so the single-shot WARN is rate-limited
	// across r1env.Get and r1rename.LookupEnv call sites identically.
	// r1env.Get re-checks the canonical (cheap; same os.Getenv call),
	// then reads + warns on legacy. The double-check is intentional:
	// it keeps the WARN bookkeeping co-located with the legacy read so
	// future maintenance touches one place.
	return r1env.Get(canonical, legacy)
}

// EnvLegacyDropEnabled reports whether R1_ENV_LEGACY_DROP is set to a
// truthy value. Resolved per-call (not cached) so deployment configs
// flipped between LookupEnv calls take effect on the next read --
// matches the rollout story where operators flip the flag and roll
// the service.
func EnvLegacyDropEnabled() bool {
	v, ok := os.LookupEnv(EnvLegacyDropEnv)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false
	}
	return b
}
