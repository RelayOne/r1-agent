// Package main implements the r1 in-house headless-browser service
// for spec C6 (specs/browser-remote-sandbox.md §T3).
//
// Topology:
//
//	[r1-coord-api] ─Bearer ID token─▶ [r1-browser Cloud Run]
//	                                            │
//	                                            ├─launches Chromium with
//	                                            │ --headless --remote-debugging-port=9222
//	                                            │ --user-data-dir=/tmp/userdata-<uuid>
//	                                            │ --no-sandbox --disable-gpu
//	                                            │ --disable-dev-shm-usage
//	                                            │
//	                                            └─reverse-proxies the inbound CDP-over-WS
//	                                              to localhost:9222
//
// Tenant isolation is enforced by Cloud Run: concurrency=1 per
// instance, one Chromium per container, --user-data-dir randomised
// per request. There is no shared filesystem state between
// containers; horizontal scaling handles parallel sessions.
//
// Auth: every WS upgrade carries `Authorization: Bearer <ID-token>`.
// v1 performs a structural JWT shape check only (Bearer prefix,
// len>=16, three dot-delimited segments — see verifyBearer in
// supervisor.go); it does NOT verify signature, audience, or expiry
// in-process. Google-signed ID-token signature + audience
// verification is enforced by Cloud Run IAM (run.invoker, two-SA
// pattern per spec T10). In-app JWKS verification is deferred to v2.
// R1_BROWSER_DEV_ANY_BEARER is a dev/integration-test-only escape
// hatch that accepts arbitrary bearer strings and must never be set
// in production.
//
// Endpoints:
//
//   GET /livez    — liveness probe (always 200 OK with {"ok": true})
//   GET /readyz   — readiness probe (200 OK after Chromium starts)
//   GET /         — CDP-over-WS upgrade endpoint (Bearer-auth required)
//
// Spec §T13b: a local-Docker container running this binary is the
// target of the `inhouse_live` integration test in
// internal/browser/inhouse/integration_test.go.

package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
)

// versionStr is stamped at build time via -X main.versionStr=<sha>.
var versionStr = "dev"

// chromiumReady flips true once the Chromium subprocess has been
// launched successfully. Used by /readyz.
var chromiumReady atomic.Bool

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", livezHandler)
	mux.HandleFunc("/readyz", readyzHandler)
	mux.HandleFunc("/healthz", livezHandler) // alias

	// CDP-over-WS upgrade endpoint. In v1 we delegate to a
	// pre-launched Chromium at 127.0.0.1:9222 — the supervisor logic
	// (launchChromium / waitChromiumReady) lives in supervisor.go
	// and is exercised end-to-end by the `inhouse_live` integration
	// test (T13b) against a running container.
	mux.Handle("/", newCDPProxy())

	// In the production container, Chromium is launched by the entry-
	// point script before the Go binary runs (so the supervisor stays
	// stateless — see Dockerfile.r1-browser). For local-dev `go run`
	// the env override skips Chromium and serves /livez + /readyz only.
	if os.Getenv("R1_BROWSER_SKIP_CHROMIUM") == "" {
		go startChromium()
	} else {
		chromiumReady.Store(true)
	}

	addr := ":" + port
	log.Printf("r1-browser v=%s listening on %s", versionStr, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// livezHandler returns 200 unconditionally — the process is up.
func livezHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"ok":true,"service":"r1-browser","version":%q}`, versionStr)
}

// readyzHandler returns 200 once Chromium has been launched.
func readyzHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !chromiumReady.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"ok":false,"reason":"chromium_not_ready"}`)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"ok":true}`)
}
