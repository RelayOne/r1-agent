package server

import (
	"net/http"
	"net/url"
	"strings"
)

// requireLoopbackOrigin returns a middleware that rejects state-
// changing requests (POST/PUT/PATCH/DELETE) and WebSocket upgrade
// requests whose Origin header is not loopback.
//
// Allowed Origin values:
//
//   - http://localhost[:port]
//   - https://localhost[:port]
//   - http://127.0.0.1[:port]
//   - https://127.0.0.1[:port]
//   - "null"        — sandboxed iframes, file:// pages.
//   - missing       — non-browser CLI clients (curl, r1 ctl HTTP path).
//
// All other Origin values get 403.
//
// Read methods (GET, HEAD, OPTIONS) are NOT gated by Origin because:
//
//   - GET requests don't have CSRF semantics in the spec's threat
//     model — the daemon's GET endpoints (status, health) are
//     idempotent and safe to leak to a cross-origin probe.
//   - The Bearer token gate (TASK-18) still applies, so a cross-
//     origin GET still needs the token.
//
// State-changing methods (POST/PUT/PATCH/DELETE) and WS upgrades
// (which can introduce stateful subscriptions) are gated. WS
// upgrades are detected by `Connection: Upgrade` + `Upgrade: websocket`
// per RFC 6455 §4.2.1 — case-insensitive matches.
func requireLoopbackOrigin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldGateOrigin(r) {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if originIsLoopbackOrAbsent(origin) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
		})
	}
}

// RequireLoopbackOrigin is the exported alias.
func RequireLoopbackOrigin() func(http.Handler) http.Handler {
	return requireLoopbackOrigin()
}

// shouldGateOrigin reports whether the request needs the Origin
// check. State-changing HTTP methods AND WebSocket upgrade requests
// are gated; safe methods are not.
func shouldGateOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	// WS upgrade requests use GET but carry Connection: Upgrade and
	// Upgrade: websocket. Per RFC 6455 §4.2.1 those headers can be
	// case-insensitive comma-separated tokens.
	if r.Method == http.MethodGet && isWebSocketUpgrade(r) {
		return true
	}
	return false
}

// originIsLoopbackOrAbsent reports whether the Origin header is one
// of the allowed values: loopback URL, the literal "null", or
// missing/empty.
func originIsLoopbackOrAbsent(origin string) bool {
	if origin == "" {
		return true // missing — non-browser CLI client.
	}
	if origin == "null" {
		return true // sandboxed iframe / file://.
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// isWebSocketUpgrade reports whether r is a WS upgrade per RFC 6455.
// Both Connection: Upgrade and Upgrade: websocket must be present;
// header values may be comma-separated tokens.
func isWebSocketUpgrade(r *http.Request) bool {
	conn := r.Header.Get("Connection")
	upg := r.Header.Get("Upgrade")
	if !headerHasToken(conn, "upgrade") {
		return false
	}
	return headerHasToken(upg, "websocket")
}

// headerHasToken returns true if value contains target as a
// comma-separated case-insensitive token.
func headerHasToken(value, target string) bool {
	target = strings.ToLower(target)
	for _, tok := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(tok), target) {
			return true
		}
	}
	return false
}
