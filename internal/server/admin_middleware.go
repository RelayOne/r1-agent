// Package server — A5 admin panel middleware.
//
// admin_middleware.go layers admin-role enforcement on top of the A4
// RelayOne SSO JWT verification (internal/auth). The contract is:
//
//   - Missing/invalid bearer:              302 → /auth/sso/start?next=<encoded>
//   - Valid SSO JWT but no role=admin:     403 (JSON or HTML, per Accept)
//   - Valid SSO JWT with role=admin:       handler runs, context carries
//                                          AdminPrincipal{User,Tenant,Roles}.
//
// Spec: specs/admin-panel.md §6 and §10 task 2.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/RelayOne/r1/internal/auth"
)

// AdminPrincipal is the verified caller identity that downstream
// handlers read from context. Populated by RequireAdmin after the JWT
// passes the role check; tests can build one directly via
// WithAdminPrincipal.
type AdminPrincipal struct {
	User   string   // sub claim (email)
	Tenant string   // tenant_id claim
	Roles  []string // roles claim (string array)
}

// HasRole reports whether the principal carries the named role.
// Constant-time compare per role keeps timing differences from leaking
// which role string matched.
func (p *AdminPrincipal) HasRole(name string) bool {
	if p == nil {
		return false
	}
	for _, r := range p.Roles {
		if len(r) == len(name) && subtle.ConstantTimeCompare([]byte(r), []byte(name)) == 1 {
			return true
		}
	}
	return false
}

type adminCtxKey struct{}

// WithAdminPrincipal attaches an AdminPrincipal to ctx. Exported so
// integration tests can inject a principal without spinning up a real
// JWT pipeline.
func WithAdminPrincipal(ctx context.Context, p *AdminPrincipal) context.Context {
	return context.WithValue(ctx, adminCtxKey{}, p)
}

// AdminPrincipalFromContext retrieves the principal set by RequireAdmin.
// Returns (nil, false) when no principal is attached — handlers should
// treat this as a programming error (RequireAdmin must run first).
func AdminPrincipalFromContext(ctx context.Context) (*AdminPrincipal, bool) {
	v, ok := ctx.Value(adminCtxKey{}).(*AdminPrincipal)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// AdminMiddlewareConfig tunes RequireAdmin for tests and dev bypass.
type AdminMiddlewareConfig struct {
	// SSOStartPath is the redirect target for unauthenticated callers.
	// Defaults to "/auth/sso/start" when empty.
	SSOStartPath string

	// RequiredRole is the role claim string that grants admin access.
	// Defaults to "admin" when empty.
	RequiredRole string

	// DevBypass, when true, skips JWT verification and synthesizes a
	// default admin principal. ONLY set in local dev — the wiring in
	// main.go reads R1_ADMIN_DEV_BYPASS=1 and refuses to enable bypass
	// when R1_ENV=production.
	DevBypass bool

	// DevBypassUser is the synthetic principal's user/tenant when
	// DevBypass is true. Defaults to "dev@local" / "dev".
	DevBypassUser   string
	DevBypassTenant string
}

// JWTVerifier is the narrow interface RequireAdmin needs from the A4
// auth package. The concrete *auth.JwtService satisfies it; tests pass
// a fake that returns canned VerifiedToken / error pairs.
type JWTVerifier interface {
	Verify(token string) (auth.VerifiedToken, error)
}

// RequireAdmin returns an http middleware that enforces the §6 matrix.
// Pass a JWTVerifier (typically *auth.JwtService) and an
// AdminMiddlewareConfig. The returned middleware is safe to wrap around
// any handler; it does not allocate per-request beyond the context key.
func RequireAdmin(verifier JWTVerifier, cfg AdminMiddlewareConfig) func(http.Handler) http.Handler {
	ssoStart := cfg.SSOStartPath
	if ssoStart == "" {
		ssoStart = "/auth/sso/start"
	}
	role := cfg.RequiredRole
	if role == "" {
		role = "admin"
	}
	devUser := cfg.DevBypassUser
	if devUser == "" {
		devUser = "dev@local"
	}
	devTenant := cfg.DevBypassTenant
	if devTenant == "" {
		devTenant = "dev"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Dev bypass for local-only iteration. Production deploys
			// MUST leave R1_ADMIN_DEV_BYPASS unset.
			if cfg.DevBypass {
				p := &AdminPrincipal{
					User:   devUser,
					Tenant: devTenant,
					Roles:  []string{role},
				}
				next.ServeHTTP(w, r.WithContext(WithAdminPrincipal(r.Context(), p)))
				return
			}

			if verifier == nil {
				// No verifier wired — fail closed.
				http.Error(w, "admin auth not configured", http.StatusServiceUnavailable)
				return
			}

			tok := extractAdminBearer(r)
			if tok == "" {
				redirectToSSO(w, r, ssoStart)
				return
			}
			verified, err := verifier.Verify(tok)
			if err != nil {
				redirectToSSO(w, r, ssoStart)
				return
			}

			user, _ := verified.Payload["sub"].(string)
			tenant, _ := verified.Payload["tenant_id"].(string)
			roles := readRoles(verified.Payload)

			p := &AdminPrincipal{User: user, Tenant: tenant, Roles: roles}
			if !p.HasRole(role) {
				writeForbidden(w, r, role)
				return
			}

			ctx := WithAdminPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractAdminBearer pulls a token from Authorization: Bearer or the
// __Host-r1_at cookie (A4's session-bound access cookie). Returns ""
// when neither carries a token.
func extractAdminBearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	if c, err := r.Cookie("__Host-r1_at"); err == nil && c.Value != "" {
		return c.Value
	}
	// Fall back to a plain cookie name so non-TLS dev environments
	// still work — the production deploy uses the __Host- prefixed
	// cookie above and never hits this branch.
	if c, err := r.Cookie("r1_session"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// readRoles parses the roles claim. Tokens vary: some carry a JSON
// array ([]any), some carry a comma-separated string. Both shapes are
// accepted so the middleware works against the staging IdP and the
// production one without a coordinated migration.
func readRoles(payload map[string]any) []string {
	v, ok := payload["roles"]
	if !ok {
		// Some IdPs nest roles under "realm_access" or similar; the
		// A4 wire contract pins the top-level key, so we only look
		// there. Future schemas can add a fallback here.
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		// Tolerate "admin,viewer" or "admin viewer".
		parts := strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' })
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// redirectToSSO sends a 302 to /auth/sso/start with the original path
// URL-encoded into the `next` parameter so the user lands back on the
// admin page they wanted after sign-in.
func redirectToSSO(w http.ResponseWriter, r *http.Request, ssoStart string) {
	next := r.URL.RequestURI()
	target := ssoStart + "?next=" + url.QueryEscape(next)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// writeForbidden responds 403. For JSON callers (Accept: application/json
// or path under /api/admin) the body is a structured error; for HTML
// callers it's a short text body so a tester can eyeball the failure
// reason in the browser.
func writeForbidden(w http.ResponseWriter, r *http.Request, role string) {
	w.Header().Set("Cache-Control", "no-store")
	if adminWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":  "forbidden",
			"reason": role + "_role_required",
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("403 forbidden: " + role + " role required\n"))
}

// adminWantsJSON returns true when the caller indicated a JSON response
// preference via Accept header or by routing through /api/admin.
// (Separate from the package-level wantsJSON in rules.go which has its
// own content-negotiation rules for the rules endpoints.)
func adminWantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/admin") {
		return true
	}
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false
	}
	// Cheap substring check is sufficient — content-negotiation here
	// is operator-facing, not a Spec-grade negotiator.
	return strings.Contains(accept, "application/json")
}

// redactRemoteAddr truncates the client IP to a /24 (IPv4) or /48
// (IPv6) so the AdminViewed ledger node can record geography without
// pinpointing the exact host. Returns "" when the address can't be
// parsed.
func redactRemoteAddr(addr string) string {
	// http.Request.RemoteAddr is "host:port" or "[ipv6]:port".
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// AdminBypassFromEnv reads R1_ADMIN_DEV_BYPASS. Returns true only when
// the value is exactly "1" — any other value, including "true"/"yes",
// is rejected to keep production deploys honest about opting in.
func AdminBypassFromEnv() bool {
	return os.Getenv("R1_ADMIN_DEV_BYPASS") == "1"
}
