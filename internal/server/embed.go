package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// RegisterDashboardUI serves the embedded web dashboard at /.
// Call this after all API routes are registered.
//
// Path resolution order, per spec web-chat-ui §51:
//  1. "/" or "/dashboard" → write static/dist/index.html (the SPA shell).
//     Falls back to static/index.html if dist/ is absent (legacy build).
//  2. Anything else → file-serve from the embedded FS rooted at static/dist
//     (or static/, again as a fallback).
//
// We write index.html bytes directly instead of letting http.FileServer
// route "/index.html" — FileServer canonicalizes "/index.html" requests
// to "/", which collided with the path rewrite and caused a 301 loop.
func RegisterDashboardUI(s *Server) {
	dist, distErr := fs.Sub(staticFS, "static/dist")
	legacy, _ := fs.Sub(staticFS, "static")

	var indexBytes []byte
	if distErr == nil {
		if b, err := fs.ReadFile(dist, "index.html"); err == nil {
			indexBytes = b
		}
	}
	if indexBytes == nil {
		if b, err := fs.ReadFile(legacy, "index.html"); err == nil {
			indexBytes = b
		}
	}

	root := dist
	if root == nil {
		root = legacy
	}
	fileServer := http.FileServer(http.FS(root))

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path == "/" || r.URL.Path == "/dashboard" {
			if indexBytes == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexBytes)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
