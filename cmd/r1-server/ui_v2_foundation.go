// Package main — ui_v2_foundation.go
//
// Spec 1 of the r1-server-ui-v2 retrofit: the foundation that Specs 2-5
// layer on. This file owns:
//
//  1. webFS — a dedicated embed.FS rooted at the parallel ui/web/
//     template tree. Kept separate from the legacy `uiFS` so the
//     vanilla-JS SPA at /ui/* keeps serving its own bundle while the
//     v2 handlers iterate templates independently.
//  2. parseV2Templates — single-shot parse via template.ParseFS,
//     cached behind sync.Once. Panics on parse error (this is build-
//     time-broken HTML, not runtime state).
//  3. setV2CSP — Content-Security-Policy headers for v2 responses.
//     Strict by default: default-src 'self'; no inline scripts.
//
// The v2 handler wiring (calling parseV2Templates() from a real
// HTTP handler) is left for Spec 4. This task only sets up the
// helpers + asserts they compile + load successfully.
package main

import (
	"embed"
	"fmt"
	"net/http"
	"sync"
	"text/template"
)

// webFS embeds the v2 template tree. The vendor blobs live under
// cmd/r1-server/ui/web/vendor/ but are served by the existing /ui/
// static file handler via uiFS in ui.go — keeping them out of webFS
// avoids loading 200 KB of JS into the html/template parser by
// mistake when Spec 4 calls ParseFS(webFS, "*.html"). The embed
// pattern is therefore template-only.
//
//go:embed ui/web/*.html ui/web/partials/*.html
var webFS embed.FS

var (
	v2TmplOnce sync.Once
	v2Tmpl     *template.Template
	v2TmplErr  error
)

// parseV2Templates returns the parsed root template for the v2 UI
// surface. Parses are cached for the process lifetime via sync.Once;
// any parse error is surfaced on every subsequent call so handlers
// can fail fast rather than serve a partially-broken response.
//
// Templates parsed: ui/web/*.html (top-level page templates added by
// Specs 2-4) and ui/web/partials/*.html (the import-map block defined
// by TASK-6 plus future shared partials).
//
// Spec 4 will replace the existing serveHTMLIndex with one that calls
// parseV2Templates and executes "base" against a populated context;
// this task only ensures the parser survives a clean run.
func parseV2Templates() (*template.Template, error) {
	v2TmplOnce.Do(func() {
		t := template.New("v2").Option("missingkey=error")
		t, err := t.ParseFS(webFS, "ui/web/*.html", "ui/web/partials/*.html")
		if err != nil {
			v2TmplErr = fmt.Errorf("parse v2 templates: %w", err)
			return
		}
		v2Tmpl = t
	})
	return v2Tmpl, v2TmplErr
}

// setV2CSP attaches the Content-Security-Policy header used by every
// v2 response. Spec §4 explicitly forbids inline scripts so any
// drift toward `unsafe-inline` would break the page rather than
// silently degrade. img-src 'self' data: allows base64-encoded SVG
// glyphs (Spec 3's redaction lock) without external image loads.
//
// Handlers added by Specs 2-4 call this before WriteHeader. Kept as
// a tiny standalone helper so the policy stays in one place and any
// future relaxation is reviewed in isolation.
func setV2CSP(h http.Header) {
	h.Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self'; "+
			"style-src 'self'; "+
			"img-src 'self' data:; "+
			"connect-src 'self'; "+
			"font-src 'self'; "+
			"object-src 'none'; "+
			"base-uri 'self'; "+
			"frame-ancestors 'none'")
}

// setV2CrossOriginIsolation attaches the COOP + COEP headers required
// for `crossOriginIsolated` to evaluate true in the browser, which is
// the gate the Spec 2 graph-worker uses to decide between
// SharedArrayBuffer and transferable-ArrayBuffer transports.
//
// COOP=same-origin + COEP=require-corp is the minimum that turns SAB
// on. The handler ALSO sets CORP=same-origin on every response under
// /ui/web/ so subresources (vendored .js, .css) satisfy the embedder
// requirement; without that, any imported module under a different
// CORP value would block the page from being cross-origin-isolated
// even though same-origin.
//
// Spec 2 §3.2; RT-D3-FORCE-WEBWORKER recommendation. The fallback
// transferable path keeps working when these headers are absent — so
// this is an opt-in perf flag, not a hard requirement.
func setV2CrossOriginIsolation(h http.Header) {
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Embedder-Policy", "require-corp")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}
