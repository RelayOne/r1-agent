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
func RegisterDashboardUI(s *Server) {
	sub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(sub))

	// Serve index.html at root and /dashboard, static assets at their paths.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path == "/" || r.URL.Path == "/dashboard" {
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})
}
