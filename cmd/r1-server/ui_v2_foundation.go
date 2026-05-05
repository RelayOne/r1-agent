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
// `{{ define "main" }}` style block-name collisions between page
// templates — html/template registers blocks globally inside a tree,
// so two pages each defining a "main" block in one shared tree
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
		t := template.New(p.Name()).Option("missingkey=error")
		t, err := t.ParseFS(webFS, "ui/web/base.html", "ui/web/"+p.Name(), "ui/web/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", p.Name(), err)
		}
		key := strings.TrimSuffix(p.Name(), ".html")
		out[key] = t
	}
	if _, ok := out["base"]; !ok {
		t := template.New("base.html").Option("missingkey=error")
		t, err := t.ParseFS(webFS, "ui/web/base.html", "ui/web/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("parse base.html: %w", err)
		}
		out["base"] = t
	}
	return out, nil
}

// V2BaseContext is the minimum context every base.html-extending
// template needs. Page-specific contexts (e.g. session-stream)
// embed it via composition.
type V2BaseContext struct {
	Title      string
	HtmxSRI    string
	HtmxSseSRI string
	SessionID  string
}

// newV2BaseContext seeds the V2BaseContext with the SRI values
// baked into LoadV2Config + the session id and title.
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
