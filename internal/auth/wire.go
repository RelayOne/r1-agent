package auth

// wire.go — wiring helpers for the daemon's startup glue. Bundles the
// four HTTP routes + the JWT middleware + the optional anonymous-mode
// fallthrough into one MountAuth call. cmd/r1-server/main.go and
// cmd/r1/agent_serve_cmd.go both call this — the wiring discipline
// stays in one place so the operator-ux contract is uniform.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Mode mirrors the `R1_AUTH_MODE` env var. Three values:
//
//	"anonymous" — SSO routes disabled; only the existing loopback
//	              bearer auth gates non-loopback traffic. Default.
//	"sso"       — SSO routes mounted. Non-loopback traffic MUST present
//	              a verified JWT cookie or Authorization: Bearer.
//	"both"      — SSO routes mounted; loopback bearer ALSO accepted on
//	              the same routes (try JWT first, fall through). Useful
//	              during operator migration.
type Mode string

const (
	ModeAnonymous Mode = "anonymous"
	ModeSSO       Mode = "sso"
	ModeBoth      Mode = "both"
)

// ParseMode reads R1_AUTH_MODE (or any other env-string) and returns
// a validated Mode. Unknown values fall back to ModeAnonymous with
// nil error and stay forward-compatible with future modes.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "anonymous", "anon":
		return ModeAnonymous
	case "sso":
		return ModeSSO
	case "both":
		return ModeBoth
	}
	return ModeAnonymous
}

// WireOptions bundles every dependency MountAuth needs.
type WireOptions struct {
	Mode    Mode
	Mux     *http.ServeMux
	Session SessionManager

	// JwtOptions, when nil, MountAuth tries JwtServiceFromEnv. The
	// caller can also pre-build a JwtService and pass it via JWT.
	JWT *JwtService

	// SsoClient, when nil, MountAuth tries SsoClientFromEnv. Degraded
	// mode (no SSO env vars) is permitted in ModeAnonymous and silently
	// skipped in ModeSSO / ModeBoth (no SSO routes mounted, JWT routes
	// still work for direct-bearer callers like cmd/r1-admin).
	SsoClient *SsoClient

	// Config tunes cookie names + the post-login allowlist. Zero
	// values populate sensible defaults inside NewSsoHandlers.
	Config SsoConfig

	// Getenv allows tests to inject env vars without touching the
	// process environment. Defaults to os.Getenv when nil.
	Getenv func(string) string
}

// MountAuth installs the four routes + returns the handler-wrapper
// that downstream callers should apply to their protected routes.
// The wrapper is a no-op in ModeAnonymous so existing routes don't
// change behavior.
//
// Returns the wrapper plus the JwtService (so callers can mint tokens
// programmatically — useful for cmd/r1-admin login tooling). When SSO
// is mounted, the returned wrapper enforces `RequireBearer`; in
// ModeBoth, AllowAnonymous=true makes the wrapper additive.
func MountAuth(ctx context.Context, opts WireOptions) (wrapper func(http.Handler) http.Handler, jwt *JwtService, err error) {
	if opts.Mode == "" {
		opts.Mode = ModeAnonymous
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Mode == ModeAnonymous {
		// Zero-cost passthrough — existing requireBearer keeps working.
		return identityMW, nil, nil
	}
	if opts.Mux == nil {
		return nil, nil, errors.New("auth wire: Mux required for ModeSSO/ModeBoth")
	}

	jwtSvc := opts.JWT
	if jwtSvc == nil {
		var jerr error
		jwtSvc, jerr = JwtServiceFromEnv(opts.Getenv)
		if jerr != nil {
			return nil, nil, fmt.Errorf("auth wire: JwtServiceFromEnv: %w", jerr)
		}
	}

	ssoClient := opts.SsoClient
	if ssoClient == nil {
		sc, serr := SsoClientFromEnv(opts.Getenv)
		if serr != nil {
			return nil, nil, fmt.Errorf("auth wire: SsoClientFromEnv: %w", serr)
		}
		ssoClient = sc
	}

	if ssoClient != nil {
		stateStore := NewInMemoryStateStore()
		handlers, herr := NewSsoHandlers(ssoClient, jwtSvc, stateStore, opts.Session, opts.Config)
		if herr != nil {
			return nil, nil, fmt.Errorf("auth wire: NewSsoHandlers: %w", herr)
		}
		// Best-effort warm discovery so the first /auth/sso/start
		// doesn't pay the discovery RTT inline. Skip errors — pinned
		// endpoints are usable without discovery.
		go func() {
			_ = ssoClient.Discovery(ctx)
		}()
		opts.Mux.HandleFunc("GET /auth/sso/start", handlers.StartHandler)
		opts.Mux.HandleFunc("GET /auth/sso/callback", handlers.CallbackHandler)
		opts.Mux.HandleFunc("POST /auth/refresh", handlers.RefreshHandler)
		opts.Mux.HandleFunc("POST /auth/logout", handlers.LogoutHandler)
	}

	mw := jwtSvc.RequireBearer(MiddlewareOptions{
		AllowAnonymous: opts.Mode == ModeBoth,
	})
	return mw, jwtSvc, nil
}

// identityMW is a no-op middleware used in ModeAnonymous.
func identityMW(next http.Handler) http.Handler { return next }
