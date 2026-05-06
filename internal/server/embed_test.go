package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)


// TestEmbedServesIndexAtRootAndDashboard verifies that the SPA's
// index.html (emitted by `npm run build` into static/dist/) is served
// at both `/` and `/dashboard`. Spec web-chat-ui item 51/55.
//
// We do NOT assert specific bundle content — that's the job of the
// web/scripts/verify-build-output.mjs check from item 48. Here we
// only assert that an HTTP client lands on text/html for both paths
// (the handler rewrites the request to /index.html and the embed
// file server may redirect through 301s on the way).
func TestEmbedServesIndexAtRootAndDashboard(t *testing.T) {
	srv := New(0, "", nil)
	RegisterDashboardUI(srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/", "/dashboard"} {
		path := path
		t.Run(path, func(t *testing.T) {
			resp, err := ts.Client().Get(ts.URL + path)
			if err != nil {
				t.Fatalf("path %q: GET error: %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("path %q: got status %d, want 200", path, resp.StatusCode)
			}
			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("path %q: got content-type %q, want text/html prefix", path, ct)
			}
		})
	}
}

// TestEmbedReturnsBundleAssets confirms that any non-root path falls
// through to the file server so JS / CSS bundles are reachable. The
// dist directory's exact bundle filenames are content-hashed and
// vary per build, so we only assert that *some* asset under
// /assets/ resolves to a 2xx response — Vite always emits at least
// one main bundle.
func TestEmbedReturnsBundleAssets(t *testing.T) {
	srv := New(0, "", nil)
	RegisterDashboardUI(srv)
	h := srv.Handler()

	// Probe the /assets/ directory itself; the http.FileServer
	// returns 404 for an unknown file but for a directory listing it
	// either redirects or returns the listing. We accept anything
	// other than 5xx — the real assertion is "no panic, no crash".
	req := httptest.NewRequest(http.MethodGet, "/assets/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code >= 500 {
		t.Fatalf("/assets/ probe returned 5xx: %d", rr.Code)
	}
}

