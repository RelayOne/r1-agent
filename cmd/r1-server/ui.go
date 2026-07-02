// Package main — ui.go
//
// Embeds the v2 web dashboard into the r1-server binary. Served at
// /ui/* for static assets (templates, CSS, vendored JS, partials)
// and at / + /session/{id} via the v2 htmx + Go-template handlers
// in ui_v2_foundation.go / index.go / trace.go.
//
// (Spec D — D-UI2-7 — removed the legacy vanilla-JS SPA + the
// R1_SERVER_UI_V2 envelope gate. The serveIndex / serveGraphIndex
// shims below survive only as dead-fallback hooks for the handful
// of flag-off branches that haven't been deleted yet — they
// 404 since the underlying files no longer exist.)
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
	// Thread the DB to the v2 session-graph payload builder (audit
	// A050): /session/{id}/graph hydrates its data island from the
	// per-session ledger projection when a DB is present.
	graphPayloadDB = db

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
	// When R1_SERVER_UI_V2=1, /session/{id}/graph serves the
	// InstancedMesh + Web Worker view from the v2 foundation
	// (Spec 2). When the flag is off, serveSessionGraph delegates
	// back to the legacy graph.html shell so pre-opt-in clients
	// keep working.
	mux.HandleFunc("GET /session/{id}/graph", serveSessionGraph)

	// Spec 4 §5.3: raw event stream view. v2-only — falls through
	// to 404 when the flag is off.
	mux.HandleFunc("GET /session/{id}/stream", serveStreamView)

	// Spec 4 §6.2 + §10 T11: memory-scoped graph view. Reuses the
	// session-graph render path with memory_id pre-filled. v2-only.
	mux.HandleFunc("GET /memories/{id}/graph", serveMemoryGraph)

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

		// Spec r1-server-ui-v2 §"Run diff view (minimum viable)".
		// Compares two sessions' event streams and reports
		// added/removed/changed-status rows. Content-diff is out of
		// scope here — see the footer + issue #144.
		mux.HandleFunc("GET /diff/{a}/{b}", db.serveDiff)

		// Spec 4 §7 + §10 T15-T17: streamed tar.gz export of the
		// session's ledger contents. v2-only and DB-backed.
		mux.HandleFunc("GET /api/session/{id}/export.tracebundle", db.serveTracebundleAdapter)

		// Spec C1 specs/cross-machine-session-migration.md §6.1 + §6.2
		// — `.r1session` bundle migrate-out / migrate-in handlers. The
		// migrate-out path is keyed by session id (one route per
		// session); migrate-in is a sink (any bundle, any source).
		// Both go through the same bearer middleware as the rest of
		// the /api/* surface — wired in main.go.
		mux.HandleFunc("POST /api/session/{id}/migrate-out", db.serveMigrateOut)
		mux.HandleFunc("POST /api/session/migrate-in", db.serveMigrateIn)
	}

	// Spec 27 §10 read-only settings viewer. Reads ~/.r1/config.yaml
	// if present, otherwise surfaces built-in defaults. No DB
	// dependency — config is file-system-sourced so the handler
	// stays responsive even when SQLite is locked.
	mux.HandleFunc("GET /settings", serveSettings)
}

// serveIndex was the legacy v1 SPA fallback handler. Spec D removed
// the underlying index.html when the legacy SPA was deleted, and
// dropping the R1_SERVER_UI_V2 toggle made every flag-off branch
// dead code (v2Enabled / traceV2Enabled / Renderable now return true
// unconditionally). The function survives because mountUI still
// registers it on `GET /session/` for malformed URLs; it 404s
// because there's nothing legacy to fall back to.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	// Guard: the only paths we own here are "/" and "/session/...".
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/session/") {
		http.NotFound(w, r)
		return
	}
	http.NotFound(w, r)
}

// serveGraphIndex was the legacy 3D-graph fallback. Same story as
// serveIndex — the graph.html file is gone and v2 is the only
// surface; the function survives only as a compile-time shim for
// flag-off branches that no longer execute. Returns 404.
func serveGraphIndex(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
