// Package main — ui_v2_flag.go
//
// Spec 4 §4 (TASK-1): centralised feature-flag config + SRI table for
// the v2 dashboard. Replaces the four scattered
// `os.Getenv("R1_SERVER_UI_V2") == "1"` checks across ui.go, share.go,
// memories.go, trace.go.
//
// Reads at startup and on each call (env reads are cheap; this lets
// tests flip the flag with t.Setenv between subtests).
package main

import "os"

// V2Config carries every v2-surface tuning knob. Loaded at request
// time from environment variables; the SRI table is baked-in (a
// vendor blob change requires a recompile + a sri_test.go bump in
// the same commit).
type V2Config struct {
	Enabled      bool   // R1_SERVER_UI_V2=1
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

// LoadV2Config reads the current environment + bakes in the
// compile-time SRI table. Strict equality to "1" — anything else
// (truthy / "true" / "yes" / ...) reads as off, matching the
// behaviour of the previous os.Getenv checks. That strictness is
// deliberate: ops scripts thinking "true" is on shouldn't
// accidentally flip a customer-visible surface.
func LoadV2Config() V2Config {
	return V2Config{
		Enabled:      os.Getenv("R1_SERVER_UI_V2") == "1",
		ShareEnabled: os.Getenv("R1_SERVER_SHARE_ENABLED") == "1",
		HtmxSRI:      vendoredSRI["htmx.min.js"],
		HtmxSseSRI:   vendoredSRI["htmx-ext-sse.js"],
	}
}

// Renderable reports whether v2 templates should serve content. A
// thin pass-through over Enabled so callers can swap to a more
// granular predicate (e.g. progressive rollout) without touching the
// 30+ callsites.
func (c V2Config) Renderable() bool { return c.Enabled }

// CanServeShare reports whether /share/{hash} should respond with
// content. Both flags must be on; either off → 404 with the
// appropriate body per Spec 4 §10 T13.
func (c V2Config) CanServeShare() bool { return c.Enabled && c.ShareEnabled }
