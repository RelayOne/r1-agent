// Package main — ui_v2_foundation.go
//
// Spec 1 of the r1-server-ui-v2 retrofit: the foundation that Specs 2-5
// layer on. This file owns:
//
//  1. webFS — a dedicated embed.FS rooted at the ui/ template tree.
//     Kept separate from `uiFS` so the static-asset handler stays
//     simple while the v2 handlers iterate templates independently.
//  2. parseV2Templates — per-page map keyed by basename without
//     extension. html/template registers blocks globally inside a
//     tree, so two pages each defining a "main" or "scripts" block
//     in one shared tree step on each other; the per-page parse keeps
//     them isolated.
//  3. setV2CSP — Content-Security-Policy headers for v2 responses.
//     Strict by default: default-src 'self'; no inline scripts.
//  4. setV2CrossOriginIsolation — COOP + COEP headers required for
//     `crossOriginIsolated` to enable SharedArrayBuffer (Spec 2).
//  5. V2BaseContext + newV2BaseContext — shared shell context every
//     v2 page template embeds via composition.
//  6. serveSessionGraph — Spec 2's 3D graph entry point.
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

// webFS embeds the v2 template tree. Vendor blobs live under
// cmd/r1-server/ui/vendor/ but are served by the existing /ui/
// static file handler via uiFS in ui.go — keeping them out of webFS
// avoids loading 200 KB of JS into the html/template parser.
//
//go:embed ui/*.html ui/partials/*.html
var webFS embed.FS

var (
	v2TmplOnce sync.Once
	v2Tmpls    map[string]*template.Template
	v2TmplErr  error
)

// parseV2Templates parses each top-level page template separately,
// pairing it with base.html + every partial. Returning a per-page
// map (rather than a single shared *template.Template) avoids
// `{{ define "main" }}` and `{{ define "scripts" }}` style block-name
// collisions between page templates — html/template registers blocks
// globally inside a tree, so two pages each defining the same block
// in one shared tree step on each other's render contexts.
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
	pages, err := webFS.ReadDir("ui")
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
		t, err := t.ParseFS(webFS, "ui/base.html", "ui/"+p.Name(), "ui/partials/*.html")
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
		t, err := t.ParseFS(webFS, "ui/base.html", "ui/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("parse base.html: %w", err)
		}
		out["base"] = t
	}
	return out, nil
}

// V2BaseContext is the minimum context every base.html-extending
// template needs. Page-specific contexts embed it via composition.
type V2BaseContext struct {
	Title      string
	HtmxSRI    string
	HtmxSseSRI string
	SessionID  string
}

// newV2BaseContext seeds V2BaseContext with the SRI baked into
// LoadV2Config + the session id and title.
func newV2BaseContext(sessionID, title string) V2BaseContext {
	cfg := LoadV2Config()
	return V2BaseContext{
		Title:      title,
		HtmxSRI:    cfg.HtmxSRI,
		HtmxSseSRI: cfg.HtmxSseSRI,
		SessionID:  sessionID,
	}
}

// setV2CSP attaches the Content-Security-Policy header used by every
// v2 response. Spec §4 explicitly forbids inline scripts so any
// drift toward `unsafe-inline` would break the page rather than
// silently degrade. img-src 'self' data: allows base64-encoded SVG
// glyphs (Spec 3's redaction lock) without external image loads.
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

// setV2CrossOriginIsolation attaches the COOP + COEP + CORP headers
// required for `crossOriginIsolated` to evaluate true in the browser,
// which is the gate the Spec 2 graph-worker uses to decide between
// SharedArrayBuffer and transferable-ArrayBuffer transports.
//
// Spec 2 §3.2; RT-D3-FORCE-WEBWORKER recommendation. The fallback
// transferable path keeps working when these headers are absent — so
// this is an opt-in perf flag, not a hard requirement.
func setV2CrossOriginIsolation(h http.Header) {
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Embedder-Policy", "require-corp")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
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
//
// Spec 4 §10 T11: when the request carries `?memory_id=<id>`, the
// payload is annotated with filter=memory + the memory id so graph.js
// can render only that memory's read/write subgraph.
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
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 2 {
			sid = parts[1]
		}
	}
	memoryID := r.URL.Query().Get("memory_id")
	title := "Session " + sid + " — Graph"
	if memoryID != "" {
		title = "Memory " + memoryID + " — Graph"
	}
	graphJSON := buildGraphPayload(sid, memoryID)
	ctx := SessionGraphContext{
		V2BaseContext: newV2BaseContext(sid, title),
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

// serveMemoryGraph renders /memories/{id}/graph by delegating to
// serveSessionGraph with `?memory_id=<id>` synthesised. Spec 4 §6.2.
// The PathValue("id") is the memory id; the renderer's session-id
// slot is set to the memory id so the existing breadcrumb/topbar
// still has a value.
func serveMemoryGraph(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	memID := r.PathValue("id")
	if memID == "" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	q.Set("memory_id", memID)
	r.URL.RawQuery = q.Encode()
	r.SetPathValue("id", memID)
	serveSessionGraph(w, r)
}

// buildGraphPayload returns the JSON payload graph.js hydrates from.
// Empty-payload fallback while the per-session ledger lookup isn't
// wired — graph.js still works, the scene just starts empty + the
// SSE stream populates it.
func buildGraphPayload(sessionID, memoryID string) []byte {
	type emptyGraph struct {
		Nodes     []struct{} `json:"nodes"`
		Edges     []struct{} `json:"edges"`
		Filter    string     `json:"filter,omitempty"`
		MemoryID  string     `json:"memory_id,omitempty"`
		SessionID string     `json:"session_id"`
	}
	g := emptyGraph{
		Nodes:     []struct{}{},
		Edges:     []struct{}{},
		SessionID: sessionID,
	}
	if memoryID != "" {
		g.Filter = "memory"
		g.MemoryID = memoryID
	}
	out, _ := json.Marshal(g)
	return out
}
