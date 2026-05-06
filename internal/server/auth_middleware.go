package server

// HTTP middlewares for the `r1 serve` loopback plane.
// Implements specs/r1d-server.md Phase C, items 18–20:
//
//   - requireBearer        — Authorization: Bearer <token> match.
//   - requireLoopbackHost  — r.Host pinned to 127.0.0.1:<port> or localhost:<port>.
//   - requireLoopbackOrigin — Origin pinned to loopback for state-changing
//                              methods + WS upgrade. Allow null/missing for
//                              CLI HTTP clients.
//
// Each middleware returns a `func(http.Handler) http.Handler` so they
// compose with standard Go patterns:
//
//	handler := requireLoopbackOrigin(
//	          requireLoopbackHost(port,
//	          requireBearer(token, mux)))
//
// They are intentionally separate (rather than one super-middleware)
// so a future SSE bridge that allows token-via-query (TASK-33) can
// skip requireBearer on its specific path while still enforcing the
// Origin and Host gates.

import (
	"crypto/subtle"
	"net/http"
)

// requireBearer returns a middleware that rejects requests whose
// `Authorization` header does not match `Bearer <token>`. On
// rejection it sends 401 and a `WWW-Authenticate: Bearer realm="r1"`
// header per RFC 6750 §3.
//
// Token comparison uses subtle.ConstantTimeCompare to defend against
// timing-side-channel attacks on the token (the bearer is short and
// the comparison happens on every request, so a naive == leaks
// length and content over a few thousand probes).
//
// An empty `token` argument is treated as a programming error: the
// middleware would accept any request, which is never what callers
// want. We refuse to construct it in that case — `requireBearer("")`
// returns a middleware that rejects EVERY request with 500. Tests
// for this branch live in TestRequireBearer_RejectsEmptyToken.
func requireBearer(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				http.Error(w, "server: bearer token not configured", http.StatusInternalServerError)
				return
			}
			got := r.Header.Get("Authorization")
			want := "Bearer " + token
			// Reject before constant-time compare if lengths
			// differ (constant-time only protects equal-length
			// inputs; differing lengths leak nothing more than
			// the request did already).
			if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="r1"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireBearer is the exported alias for requireBearer used by
// callers outside this package. Kept lowercase-internal in the
// codebase but exported here so cmd/r1/serve_cmd.go (Phase H) can
// wire it without an awkward re-export.
func RequireBearer(token string) func(http.Handler) http.Handler {
	return requireBearer(token)
}
