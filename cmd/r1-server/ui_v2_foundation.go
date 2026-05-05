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
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
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
	v2Tmpls    map[string]*template.Template
	v2TmplErr  error
)

// parseV2Templates parses each top-level page template separately,
// pairing it with base.html + every partial. Returning a per-page
// map (rather than a single shared *template.Template) avoids
// `{{ define "scripts" }}` style block-name collisions between page
// templates — html/template registers blocks globally inside a tree,
// so two pages each defining a "scripts" block in one shared tree
// step on each other's render contexts.
//
// Result keys are the basename without ".html" (e.g. "session-graph").
// parseV2Template(name) is a convenience that returns one template.
//
// Cached for the process lifetime via sync.Once.
func parseV2Templates() (map[string]*template.Template, error) {
	v2TmplOnce.Do(func() {
		out, err := buildV2Tmpls()
		if err != nil {
			v2TmplErr = fmt.Errorf("parse v2 templates: %w", err)
			return
		}
		v2Tmpls = out
	})
	return v2Tmpls, v2TmplErr
}

// parseV2Template returns a single page template by name (basename
// without extension). Returns nil + a not-found error if the page
// template doesn't exist.
func parseV2Template(name string) (*template.Template, error) {
	all, err := parseV2Templates()
	if err != nil {
		return nil, err
	}
	t, ok := all[name]
	if !ok {
		return nil, fmt.Errorf("v2 template not found: %s", name)
	}
	return t, nil
}

func buildV2Tmpls() (map[string]*template.Template, error) {
	pages, err := webFS.ReadDir("ui/web")
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for _, p := range pages {
		if p.IsDir() || !strings.HasSuffix(p.Name(), ".html") {
			continue
		}
		// base.html holds the {{ define "base" }} block; every page
		// is parsed alongside it + every partial.
		t := template.New(p.Name()).Option("missingkey=error")
		t, err := t.ParseFS(webFS, "ui/web/base.html", "ui/web/"+p.Name(), "ui/web/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", p.Name(), err)
		}
		key := strings.TrimSuffix(p.Name(), ".html")
		out[key] = t
	}
	if _, ok := out["base"]; !ok {
		// "base" is referenced as a stand-alone template by tests
		// that exercise the shell only.
		t := template.New("base.html").Option("missingkey=error")
		t, err := t.ParseFS(webFS, "ui/web/base.html", "ui/web/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("parse base.html: %w", err)
		}
		out["base"] = t
	}
	return out, nil
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

// V2BaseContext is the minimum context every base.html-extending
// template needs. Specs 2-4 embed it via composition.
type V2BaseContext struct {
	Title      string
	HtmxSRI    string
	HtmxSseSRI string
	SessionID  string
}

// SRIs computed from the on-disk vendor blobs landed by Spec 1's
// scripts/vendor-ui.sh. Hard-coded copies here so the Go handler
// doesn't have to re-read scripts/vendor-ui.sh at every request.
// Spec 5's vendor_freshness_test asserts these match the blobs at
// startup time so a vendor bump that forgets to update this struct
// is caught in CI.
const (
	htmxSRI    = "sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+"
	htmxSseSRI = "sha384-QA9wXqexhwzXTuTvuF5QP82pddm3R2hy81UzXi7ioNTqNF2b75hlkkSGjafohhL3"
)

// newV2BaseContext seeds the SRI and session id from a request. Title
// can be overridden by the caller.
func newV2BaseContext(sessionID, title string) V2BaseContext {
	return V2BaseContext{
		Title:      title,
		HtmxSRI:    htmxSRI,
		HtmxSseSRI: htmxSseSRI,
		SessionID:  sessionID,
	}
}

// SessionGraphContext is the template context for session-graph.html.
// GraphData is the marshaled {nodes, edges} JSON the page hydrates
// from before graph.js runs.
type SessionGraphContext struct {
	V2BaseContext
	GraphData template.JS
}

// serveSessionGraph renders the v2 3D graph view at
// /session/{id}/graph when R1_SERVER_UI_V2=1. Sets COOP/COEP so the
// worker can use SharedArrayBuffer where supported.
func serveSessionGraph(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		serveGraphIndex(w, r)
		return
	}
	tmpl, err := parseV2Template("session-graph")
	if err != nil {
		http.Error(w, "v2 templates: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		// Fall back to last segment if PathValue isn't set (older mux).
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 2 {
			sid = parts[1]
		}
	}
	// Spec 4 will replace this stub with a real lookup against the
	// SQLite ledger. For now, emit an empty graph payload — graph.js
	// + the SSE stream will populate the scene as events arrive.
	graphJSON, _ := json.Marshal(struct {
		Nodes []struct{} `json:"nodes"`
		Edges []struct{} `json:"edges"`
	}{})
	ctx := SessionGraphContext{
		V2BaseContext: newV2BaseContext(sid, "Session "+sid+" — Graph"),
		GraphData:     template.JS(graphJSON),
	}
	setV2CSP(w.Header())
	setV2CrossOriginIsolation(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := tmpl.ExecuteTemplate(w, "session-graph", ctx); err != nil {
		http.Error(w, "render session-graph: "+err.Error(), http.StatusInternalServerError)
		return
	}
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
