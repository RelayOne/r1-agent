package server

import (
	"fmt"
	"net/http"
)

// requireLoopbackHost returns a middleware that rejects requests
// whose r.Host is not in {"127.0.0.1:<port>", "localhost:<port>"}.
// Returns 403 on rejection.
//
// Why pin the Host header?
//
// DNS-rebinding attacks: a malicious page on `evil.com` resolves
// `attacker.evil.com` to 127.0.0.1, then issues fetches at the
// attacker domain. Browsers send the attacker domain in the `Host`
// header. Without a Host pin, the daemon would happily serve the
// request, bypassing same-origin protections. Pinning the Host
// header to the literal loopback addresses we actually accept
// closes the rebinding hole.
//
// "::1" (IPv6 loopback) is intentionally NOT in the allow-list
// because ServeLoopback binds 127.0.0.1 only (the IPv6 sibling
// would expose a separate bind, doubling the attack surface). If
// IPv6 is added later, this allow-list is the place to extend.
//
// The middleware is constructed with the resolved port from
// ServeLoopback. Since the port is ephemeral (TASK-17), callers
// must build the middleware AFTER Listen, not at startup.
func requireLoopbackHost(port int) func(http.Handler) http.Handler {
	allow1 := fmt.Sprintf("127.0.0.1:%d", port)
	allow2 := fmt.Sprintf("localhost:%d", port)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host == allow1 || r.Host == allow2 {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "forbidden: bad Host header", http.StatusForbidden)
		})
	}
}

// RequireLoopbackHost is the exported alias.
func RequireLoopbackHost(port int) func(http.Handler) http.Handler {
	return requireLoopbackHost(port)
}
