// middleware.go — JWT-bearer auth middleware for net/http handlers.
package auth

import (
	"context"
	"net/http"
	"strings"
)

// ContextKey is the type used to store auth claims in request context.
type ContextKey string

const (
	// ContextClaimsKey is the request-context key under which Verify-ed
	// Claims are stored after the middleware passes.
	ContextClaimsKey ContextKey = "r1.auth.claims"
)

// FromContext fetches claims from a request's context. Returns nil if
// the request was not authenticated (e.g. the middleware was bypassed
// for a public route).
func FromContext(ctx context.Context) *Claims {
	v, _ := ctx.Value(ContextClaimsKey).(*Claims)
	return v
}

// Middleware returns an http middleware that gates each request on a
// valid Authorization: Bearer <jwt> header. On failure it writes a
// 401 with a JSON body. On success it calls next with claims attached
// to the request context.
//
// Pass an `optional` set of paths (exact-match) that bypass auth — for
// example "/healthz", "/livez", "/readyz", "/v1/version".
func Middleware(j *JwtService, optional ...string) func(http.Handler) http.Handler {
	publicSet := make(map[string]bool, len(optional))
	for _, p := range optional {
		publicSet[p] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicSet[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeAuthError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
				return
			}
			tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			claims, err := j.Verify(tok)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid bearer ("+err.Error()+")")
				return
			}
			ctx := context.WithValue(r.Context(), ContextClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("WWW-Authenticate", `Bearer realm="r1"`)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"ok":false,"error":"` + msg + `"}` + "\n"))
}
