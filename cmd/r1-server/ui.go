// Package main — ui.go
//
// Embeds the web dashboard into the r1-server binary. Served at /ui/*
// for the static assets and / plus /session/{id} for the SPA shell —
// the SPA's path-based routing is handled client-side in app.js, so
// every unknown GET under those prefixes is handed the same
// index.html.
//
// The 3D ledger visualizer (RS-4 item 20) lives in a sibling
// graph.html page. Because Three.js + three-forcegraph are ~300KB of
// JS, we deliberately serve a dedicated HTML shell instead of
// inflating the instance-list SPA. graph.js loads those libraries
// from a pinned-version CDN; when the browser has no WebGL, the page
// swaps itself for a fallback banner linking back to the 2D stream
// view.
package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed ui/*
var embeddedUI embed.FS

// uiFS is a stripped view of embeddedUI that exposes files at their
// path-without-the-"ui/"-prefix so http.FileServer can serve them
// under /ui/. Built once at package init; any error panics because a
// missing embedded directory is a build-time bug, not runtime state.
var uiFS fs.FS = mustSubFS(embeddedUI, "ui")

func mustSubFS(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("embed sub " + dir + ": " + err.Error())
	}
	return sub
}

// mountUI adds the SPA + static-asset routes to mux. Kept separate
// from buildMux so the API handlers stay independently testable.
//
// The db pointer is threaded through for handlers that need SQLite
// access (currently just /memories; /settings is file-backed). A nil
// db disables the DB-backed routes — useful for unit tests that want
// to exercise the static UI surface without spinning up a SQLite
// file.
func mountUI(mux *http.ServeMux, db *DB) {
	// Static assets under /ui/ (app.js, style.css, any future
	// chunks). http.StripPrefix peels the /ui/ so the fs lookup
	// matches the embed's internal paths.
	assetHandler := http.StripPrefix("/ui/", http.FileServer(http.FS(uiFS)))
	mux.Handle("GET /ui/", assetHandler)

	// SPA shell. / + /session/{id} serve the vanilla-JS index.html
	// for pre-v2 clients; client-side router in app.js picks the
	// right view. Explicitly list the top-level route (GET /) so
	// ServeMux doesn't match /api/* paths here.
	//
	// work-stoke TASK 12: when R1_SERVER_UI_V2=1 and a DB handle is
	// available, GET / renders the htmx + Go templates dashboard
	// from templates/index.tmpl via (*DB).serveHTMLIndex. The
	// handler delegates back to serveIndex when the flag is off so
	// pre-opt-in clients still see the vanilla-JS SPA. DB-less
	// wiring (tests that only exercise the static UI) keeps the
	// original serveIndex registration.
	if db != nil {
		mux.HandleFunc("GET /{$}", db.serveHTMLIndex)
	} else {
		mux.HandleFunc("GET /{$}", serveIndex)
	}
	mux.HandleFunc("GET /session/", serveIndex)

	// Dedicated 3D ledger visualizer (RS-4 item 20). Registered
	// explicitly so ServeMux prefers it over the /session/ SPA
	// fallback for this one sub-path — Go 1.22's pattern precedence
	// ranks concrete paths above prefix matches.
	mux.HandleFunc("GET /session/{id}/graph", serveGraphIndex)

	// work-stoke TASK 13: waterfall + tree default trace views. The
	// concrete /session/{id} + /session/{id}/tree patterns are more
	// specific than /session/ so Go 1.22's mux prefers them. When the
	// v2 flag is off the handlers delegate back to serveIndex, so the
	// SPA shell still serves pre-opt-in clients. DB-bound: the routes
	// only mount when a DB is present (tests without DB still get the
	// SPA fallback).
	if db != nil {
		mux.HandleFunc("GET /session/{id}", db.serveTraceWaterfall)
		mux.HandleFunc("GET /session/{id}/tree", db.serveTraceTree)
	}

	// Spec 27 §5.3 read-only content-addressed share view.
	// Implementation in share.go; the handler is dual-gated by
	// R1_SERVER_UI_V2 + R1_SERVER_SHARE_ENABLED and 404s when
	// either gate is off, satisfying the v2 acceptance criterion
	// that share/* routes 404 in MVP mode.
	mux.HandleFunc("GET /share/{hash}", serveShare)

	// Spec 27 §6.1 read-only memory-bus explorer (grouped list).
	// Gated by R1_SERVER_UI_V2 only — the memory explorer ships in
	// the default v2 surface (no second toggle). DB-bound: the
	// handler reads from stoke_memory_bus. When db is nil (test
	// harness only), the route 404s so a mux without DB backing
	// doesn't panic on request.
	if db != nil {
		mux.HandleFunc("GET /memories", db.serveMemories)

		// work-stoke TASK 14: memory CRUD endpoints. POST creates a new
		// row, PUT / DELETE operate by autoincrement id. Writes whose
		// scope is "always" require the R1_MEMORIES_PASSPHRASE (legacy
		// STOKE_MEMORIES_PASSPHRASE) passphrase supplied via the JSON
		// body — see memories.go requirePassphraseIfAlways. Routes are
		// also R1_SERVER_UI_V2-gated inside the handlers themselves so
		// they 404 until the flag is set — matches the GET /memories
		// precedent.
		mux.HandleFunc("POST /api/memories", db.serveMemoryCreate)
		mux.HandleFunc("PUT /api/memories/{id}", db.serveMemoryUpdate)
		mux.HandleFunc("DELETE /api/memories/{id}", db.serveMemoryDelete)
	}

	// Spec 27 §10 read-only settings viewer. Reads ~/.r1/config.yaml
	// if present, otherwise surfaces built-in defaults. No DB
	// dependency — config is file-system-sourced so the handler
	// stays responsive even when SQLite is locked.
	mux.HandleFunc("GET /settings", serveSettings)
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	// Guard: the only paths we own here are "/" and "/session/...".
	// Anything else falls through to 404. /api/* is routed first by
	// the mux thanks to method+path specificity, so this is mostly
	// defense-in-depth.
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/session/") {
		http.NotFound(w, r)
		return
	}
	raw, err := fs.ReadFile(uiFS, "index.html")
	if err != nil {
		http.Error(w, "ui missing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(raw)
}

// serveGraphIndex returns the dedicated 3D visualizer HTML shell.
// graph.html references /ui/graph.js + /ui/graph.css + CDN-hosted
// Three.js + forcegraph bundles; the shell is tiny but the scripts
// pull in a significant amount of code, which is why this page is
// only loaded on explicit navigation to /session/{id}/graph rather
// than lazy-loaded inside the main SPA.
func serveGraphIndex(w http.ResponseWriter, _ *http.Request) {
	raw, err := fs.ReadFile(uiFS, "graph.html")
	if err != nil {
		http.Error(w, "graph ui missing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(raw)
}
