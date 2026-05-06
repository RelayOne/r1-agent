// Package main — ui_v2_flag.go
//
// Spec 4 §4 (TASK-1): centralised feature-flag config + SRI table for
// the v2 dashboard. Originally backed the R1_SERVER_UI_V2 toggle that
// gated the v2 surface during the two-release-cycle parallel-deploy
// window.
//
// Spec D (D-UI2-7) cleanup: the parallel-deploy window has elapsed
// (v2 shipped through PRs #154/#155/#156/#160/#162/#167) and the
// legacy v1 SPA has been removed. R1_SERVER_UI_V2 is therefore gone
// — Enabled is hardcoded true. The V2Config struct stays so the
// existing 30+ callsites of v2Enabled() / traceV2Enabled() /
// LoadV2Config() compile unchanged.
//
// R1_SERVER_SHARE_ENABLED stays — it is a separate /share/* gate.
package main

import "os"

// V2Config carries every v2-surface tuning knob. The SRI table is
// baked-in (a vendor blob change requires a recompile + a
// sri_test.go bump in the same commit). Enabled is hardcoded true
// post-Spec-D — the legacy SPA fallback no longer exists, so v2 IS
// the only surface.
type V2Config struct {
	Enabled      bool   // hardcoded true post-Spec-D (legacy SPA removed)
	ShareEnabled bool   // R1_SERVER_SHARE_ENABLED=1 — second gate on /share/*
	HtmxSRI      string // SRI for /ui/vendor/htmx.min.js
	HtmxSseSRI   string // SRI for /ui/vendor/htmx-ext-sse.js
}

// vendoredSRI is the compile-time SRI manifest for vendor blobs that
// page templates inline as <script integrity=...>. The values mirror
// the SRI[] table in scripts/vendor-ui.sh exactly; sri_test.go
// asserts this every CI run, so a vendor bump that forgets to
// update this map will fail before a deploy can ship.
var vendoredSRI = map[string]string{
	"htmx.min.js":     "sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+",
	"htmx-ext-sse.js": "sha384-QA9wXqexhwzXTuTvuF5QP82pddm3R2hy81UzXi7ioNTqNF2b75hlkkSGjafohhL3",
}

// LoadV2Config bakes in the compile-time SRI table + reads the
// remaining env-driven knobs (R1_SERVER_SHARE_ENABLED). Enabled is
// hardcoded true — Spec D removed the R1_SERVER_UI_V2 toggle once
// the legacy SPA was deleted (no v1 fallback exists to flip back
// to). The strict-"1" semantics for the share gate are preserved:
// ops scripts thinking "true" is on shouldn't accidentally flip a
// customer-visible surface.
func LoadV2Config() V2Config {
	return V2Config{
		Enabled:      true,
		ShareEnabled: os.Getenv("R1_SERVER_SHARE_ENABLED") == "1",
		HtmxSRI:      vendoredSRI["htmx.min.js"],
		HtmxSseSRI:   vendoredSRI["htmx-ext-sse.js"],
	}
}

// Renderable reports whether v2 templates should serve content.
// Always true post-Spec-D (the legacy SPA fallback is gone, so v2
// is the only surface). Kept as a method so the 30+ existing
// callsites compile without a refactor.
func (c V2Config) Renderable() bool { return true }

// CanServeShare reports whether /share/{hash} should respond with
// content. Both flags must be on; either off → 404 with the
// appropriate body per Spec 4 §10 T13.
func (c V2Config) CanServeShare() bool { return c.Enabled && c.ShareEnabled }
