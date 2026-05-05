package server

// agent_mount.go — TASK-34: mount internal/agentserve.Server.Handler()
// under /v1/agent/ on the `r1 serve` daemon's loopback mux, with the
// daemon's `Authorization: Bearer <tok>` flowing through.
//
// Two routes are registered:
//
//   /v1/agent/...   canonical, current. requireBearer enforces auth.
//   /api/...        alias for one minor version. Same auth gate; adds
//                   `Deprecation: true` (RFC 8594) on every response so
//                   clients pinning the old path see the deprecation
//                   signal and migrate before the alias is removed.
//
// Auth flow-through. agentserve has its own `X-Stoke-Bearer` gate
// (Server.withAuth in agentserve/server.go). We do NOT want two
// independent auth checks — the daemon's outer requireBearer is the
// single source of truth. To avoid a double-check we construct the
// agentserve.Server with `Bearer: nil` (no internal gate) and rely on
// the outer middleware. Callers who already construct an agentserve.Server
// elsewhere with bearers configured can still pass it; the inner gate
// will accept any token the outer gate already validated, since the
// outer gate filters the request before agentserve sees it. The
// duplicate check is a wash on the success path and a redundant 401
// on the failure path (which the outer already rejected anyway).
//
// Why two registrations rather than a single route with rewriting:
// http.ServeMux's longest-prefix match means /v1/agent/ and /api/ are
// independent prefixes. StripPrefix is the cleanest way to forward to
// the agentserve handler unchanged (it expects /api/... paths
// internally). The alias mount uses the agentserve handler directly
// (no strip) because the handler already serves /api/... paths.

import (
	"net/http"
	"strings"

	"github.com/RelayOne/r1/internal/agentserve"
)

// MountAgentServe registers the agentserve handler under /v1/agent/
// (canonical) and /api/ (deprecated alias) on the supplied mux. The
// bearer token is enforced by the outer requireBearer middleware on
// each route; pass an empty token to disable auth (testing only).
//
// agentSrv must be non-nil. mux must be non-nil. Returns the
// registered prefixes for tests/diagnostics.
func MountAgentServe(mux *http.ServeMux, agentSrv *agentserve.Server, bearer string) (canonical, alias string) {
	if mux == nil || agentSrv == nil {
		return "", ""
	}
	inner := agentSrv.Handler()

	// Canonical: /v1/agent/<rest> → strip prefix → forward as /<rest>.
	// The agentserve handler routes /api/capabilities etc., so we
	// rewrite the incoming path so a request to
	// /v1/agent/api/capabilities reaches the handler as /api/capabilities.
	// We deliberately keep the /api/ inside the canonical URL so the
	// alias is a literal substring drop. Operators flipping a client
	// from /api/capabilities → /v1/agent/api/capabilities is a one-line
	// change.
	stripped := http.StripPrefix("/v1/agent", inner)
	canonicalAuth := requireBearerOrPassthrough(bearer)(stripped)
	mux.Handle("/v1/agent/", canonicalAuth)

	// Deprecated alias: /api/<rest> → forward to inner unchanged. We
	// guard the alias with the same requireBearer and stamp every
	// response with `Deprecation: true` per RFC 8594. We narrow the
	// alias to paths the agentserve handler owns to avoid swallowing
	// /api/health or /api/status from other parts of the daemon. The
	// intent is "tasks + capabilities + chat-completions" only.
	aliasMux := http.NewServeMux()
	for _, p := range []string{
		"/api/capabilities",
		"/api/task",
		"/api/task/",
		"/v1/chat/completions",
	} {
		aliasMux.Handle(p, inner)
	}
	aliasAuth := requireBearerOrPassthrough(bearer)(deprecationHeader(aliasMux))
	mux.Handle("/api/capabilities", aliasAuth)
	mux.Handle("/api/task", aliasAuth)
	mux.Handle("/api/task/", aliasAuth)
	// Note: /v1/chat/completions is canonical-equivalent in agentserve
	// (the handler already serves it); we deliberately do NOT alias it
	// from /api/chat/completions because no historical client used that
	// path. Listed here for documentation only.
	return "/v1/agent/", "/api/"
}

// requireBearerOrPassthrough returns the bearer middleware when the
// token is non-empty, or a no-op pass-through when it is empty. Tests
// that don't care about auth use the empty form. Production wiring
// from cmd/r1/serve_cmd.go always passes a real token because the
// loopback daemon mints one on startup (TASK-13).
func requireBearerOrPassthrough(token string) func(http.Handler) http.Handler {
	if strings.TrimSpace(token) == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return requireBearer(token)
}

// deprecationHeader stamps `Deprecation: true` on every response. RFC
// 8594 lets the value be a date or a boolean; we use the boolean form
// because the deprecation window is "until the next minor release"
// rather than a fixed cutoff date.
func deprecationHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", "next-minor-release")
		next.ServeHTTP(w, r)
	})
}
