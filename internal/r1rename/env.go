// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"os"
)

// EnvDeprecationDate is the announced sunset for legacy STOKE_* env
// vars per work-r1-rename.md S6-3. Retained as a documentation
// constant so any historical references in logs / runbooks can still
// be cross-referenced; no longer consulted by LookupEnv post-S6-3.
const EnvDeprecationDate = "2026-07-23"

// LookupEnv resolves a canonical R1_* env var. The S1-1 90-day
// dual-accept window (with STOKE_* legacy fallback) elapsed
// 2026-07-23 per S6-3; legacy names are no longer consulted.
// Deployments that still set only the legacy name will read "" and
// the resulting "missing required env" failure at each call site
// surfaces the canonical name operators must migrate to.
//
// The `legacy` parameter is accepted but ignored so existing call
// sites at `r1rename.LookupEnv("R1_DATA_DIR", "STOKE_DATA_DIR")`
// continue to compile unchanged; a follow-up pass can sweep the
// call sites to drop the second argument.
func LookupEnv(canonical, _ string) string {
	return os.Getenv(canonical)
}
