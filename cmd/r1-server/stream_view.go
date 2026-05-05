// Package main — stream_view.go
//
// Spec 4 §5.3 + §10 T6: serves /session/{id}/stream — the raw SSE
// view that mirrors the existing serveStreamingEvents handler but
// renders the v2 page shell + a single <pre> swap target. Power
// users debugging the chain tier get the unstyled stream they're
// used to, but framed by the v2 chrome so navigation works.
//
// The handler is a thin v2-only renderer: when the v2 flag is off
// it 404s (consistent with other v2 pages — pre-opt-in clients
// don't get a half-shaped stream view).
package main

import (
	"net/http"
	"strings"
)

// streamViewContext is the template context for session-stream.html.
type streamViewContext struct {
	V2BaseContext
	Session     streamSessionInfo
	LastEventID string
}

type streamSessionInfo struct {
	ID            string
	Name          string
	StartedAtUnix int64
	UpdatedAtUnix int64
}

// serveStreamView renders the raw event stream. Mounted by ui.go at
// GET /session/{id}/stream when DB is non-nil; falls through to 404
// when v2 is off.
func serveStreamView(w http.ResponseWriter, r *http.Request) {
	cfg := LoadV2Config()
	if !cfg.Renderable() {
		http.NotFound(w, r)
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 2 {
			sid = parts[1]
		}
	}
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	tmpl, err := parseV2Template("session-stream")
	if err != nil {
		http.Error(w, "v2 templates: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ctx := streamViewContext{
		V2BaseContext: V2BaseContext{
			Title:      "Session " + sid + " — stream",
			HtmxSRI:    cfg.HtmxSRI,
			HtmxSseSRI: cfg.HtmxSseSRI,
			SessionID:  sid,
		},
		Session: streamSessionInfo{
			ID:   sid,
			Name: "session " + sid,
		},
		LastEventID: r.Header.Get("Last-Event-ID"),
	}
	setV2CSP(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := tmpl.ExecuteTemplate(w, "session-stream", ctx); err != nil {
		http.Error(w, "render session-stream: "+err.Error(), http.StatusInternalServerError)
	}
}
