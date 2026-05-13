package auth

// middleware.go — JWT verification middleware for the r1 HTTP plane.
//
// The middleware extracts a token from either the Authorization: Bearer
// header (preferred) or the __Host-r1_at cookie (fallback for browser
// callers). On success it attaches a *VerifiedToken to the request
// context; downstream handlers retrieve it via VerifiedFromContext.

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// ctxKey is an unexported type to avoid context-key collisions.
type ctxKey struct{}

var verifiedCtxKey = ctxKey{}

// MiddlewareOptions configures RequireBearer.
type MiddlewareOptions struct {
	// AllowAnonymous, when true, lets requests without a token proceed.
	// Bad tokens still reject. Used for routes that accept BOTH SSO
	// JWTs and the loopback bearer token (the loopback gate runs in
	// front).
	AllowAnonymous bool

	// CookieName overrides the default access-token cookie name.
	// Defaults to "__Host-r1_at" — keep the __Host- prefix unless
	// you understand the security implications.
	CookieName string
}

// RequireBearer returns a middleware that gates the wrapped handler
// behind a verified JWT. On failure, responds 401 with a standard
// WWW-Authenticate header (RFC 6750 §3).
func (s *JwtService) RequireBearer(opts MiddlewareOptions) func(http.Handler) http.Handler {
	cookieName := opts.CookieName
	if cookieName == "" {
		cookieName = "__Host-r1_at"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, source := extractBearer(r, cookieName)
			if token == "" {
				if opts.AllowAnonymous {
					next.ServeHTTP(w, r)
					return
				}
				writeAuthChallenge(w, "invalid_request", "missing access token")
				return
			}
			verified, err := s.Verify(token)
			if err != nil {
				writeAuthChallenge(w, "invalid_token", "verification failed")
				_ = source // reserved for future logging
				return
			}
			ctx := WithVerified(r.Context(), &verified)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithVerified attaches a *VerifiedToken to ctx.
func WithVerified(ctx context.Context, v *VerifiedToken) context.Context {
	return context.WithValue(ctx, verifiedCtxKey, v)
}

// VerifiedFromContext retrieves the *VerifiedToken attached by
// RequireBearer. Returns (nil, false) when the context has no token —
// the typical case under AllowAnonymous.
func VerifiedFromContext(ctx context.Context) (*VerifiedToken, bool) {
	v, ok := ctx.Value(verifiedCtxKey).(*VerifiedToken)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// TenantFromContext returns the tenant_id claim from the verified
// token, or "" when no token is attached / the claim is missing.
// Convenience for downstream handlers that scope queries by tenant.
func TenantFromContext(ctx context.Context) string {
	v, ok := VerifiedFromContext(ctx)
	if !ok {
		return ""
	}
	t, _ := v.Payload["tenant_id"].(string)
	return t
}

// SubjectFromContext returns the sub claim from the verified token.
func SubjectFromContext(ctx context.Context) string {
	v, ok := VerifiedFromContext(ctx)
	if !ok {
		return ""
	}
	t, _ := v.Payload["sub"].(string)
	return t
}

// extractBearer pulls a token from the request. Returns the token plus
// a short source tag ("header" or "cookie") for diagnostic logging.
func extractBearer(r *http.Request, cookieName string) (string, string) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimSpace(auth[len("Bearer "):]), "header"
		}
	}
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return c.Value, "cookie"
	}
	return "", ""
}

// writeAuthChallenge writes a 401 with a WWW-Authenticate header.
// Body is a plain text string so callers without JSON decoders still
// see a useful message; the response has Cache-Control: no-store so
// proxies don't cache the 401.
func writeAuthChallenge(w http.ResponseWriter, errCode, msg string) {
	// Escape stray quotes in errCode to keep the header well-formed.
	clean := strings.ReplaceAll(errCode, `"`, "")
	w.Header().Set("WWW-Authenticate", `Bearer realm="r1", error="`+clean+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, http.StatusUnauthorized)
}

// ErrNoToken is returned by helpers like RequireVerified when the
// caller forgot to chain RequireBearer in front. Treat it as a 500;
// production code should not hit this path.
var ErrNoToken = errors.New("auth middleware: no verified token in context")
