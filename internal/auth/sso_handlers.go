package auth

// sso_handlers.go — the four HTTP handlers that wire SsoClient + JwtService
// into the r1 daemon's HTTP plane:
//
//	GET  /auth/sso/start
//	GET  /auth/sso/callback
//	POST /auth/refresh
//	POST /auth/logout
//
// Cookie naming uses the __Host- prefix per the OWASP cookie-prefix
// guidance: requires Secure, no Domain attribute, and Path=/. The
// refresh cookie additionally uses Path=/auth so it's never sent to
// non-auth endpoints (defense in depth against XSS access-token theft).

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SsoConfig is the per-handler config passed at construction.
type SsoConfig struct {
	// CookieDomain is empty for __Host- prefix compliance. Reserved
	// for future use if we drop the __Host- prefix.
	CookieDomain string

	// RedirectAfterLoginAllowlist enumerates the absolute path prefixes
	// (starting with "/") that StartHandler's `next` query may resolve
	// to. Defaults to {"/"} when empty.
	RedirectAfterLoginAllowlist []string

	// LogoutRedirectURL is the post_logout_redirect_uri passed to the
	// IdP's end_session_endpoint. Empty disables RP-Initiated Logout.
	LogoutRedirectURL string

	// CookieInsecureForTests, when true, drops the Secure flag so cookies
	// survive a plain-http httptest.Server. NEVER set in production —
	// cookies must be Secure to satisfy the __Host- prefix.
	CookieInsecureForTests bool

	// AccessCookieName / RefreshCookieName override the defaults.
	AccessCookieName  string
	RefreshCookieName string
	StateCookieName   string
}

// SessionManager is the contract sso_handlers depends on for session
// creation + invalidation. The sessionhub.SessionHub adapter (in
// internal/server/sessionhub) implements it; tests pass a fake.
type SessionManager interface {
	// CreateAuthenticated registers a new session bound to the
	// verified RelayOne profile. Returns the session id; the caller
	// embeds that id into the JWT as `session_id`.
	CreateAuthenticated(profile RelayOneProfile, tokens Tokens) (sessionID string, err error)

	// Invalidate marks the session as logged-out. The implementation
	// MUST tolerate an empty/unknown sessionID — logout is idempotent.
	Invalidate(sessionID string) error
}

// SsoHandlers bundles the four handlers behind a single struct.
type SsoHandlers struct {
	Client     *SsoClient
	JWT        *JwtService
	StateStore StateStore
	Session    SessionManager
	Config     SsoConfig

	// now is a clock injection point for tests. Production wiring sets
	// it to time.Now.
	now func() time.Time
}

// NewSsoHandlers validates dependencies and returns a handler bundle.
// SessionManager may be nil for test fixtures that only exercise the
// /auth/refresh + /auth/logout paths.
func NewSsoHandlers(client *SsoClient, jwt *JwtService, state StateStore, session SessionManager, cfg SsoConfig) (*SsoHandlers, error) {
	if jwt == nil {
		return nil, errors.New("auth handlers: JwtService required")
	}
	if state == nil {
		return nil, errors.New("auth handlers: StateStore required")
	}
	if cfg.AccessCookieName == "" {
		cfg.AccessCookieName = "__Host-r1_at"
	}
	if cfg.RefreshCookieName == "" {
		cfg.RefreshCookieName = "__Host-r1_rt"
	}
	if cfg.StateCookieName == "" {
		cfg.StateCookieName = "__Host-r1_sso_state"
	}
	if len(cfg.RedirectAfterLoginAllowlist) == 0 {
		cfg.RedirectAfterLoginAllowlist = []string{"/"}
	}
	return &SsoHandlers{
		Client:     client,
		JWT:        jwt,
		StateStore: state,
		Session:    session,
		Config:     cfg,
		now:        time.Now,
	}, nil
}

// SetNow lets tests inject a deterministic clock.
func (h *SsoHandlers) SetNow(fn func() time.Time) {
	if fn != nil {
		h.now = fn
	}
}

// StartHandler implements GET /auth/sso/start?next=<path>.
//
// Flow:
//  1. Validate next is a safe relative path against the allowlist.
//  2. Mint state/verifier/nonce.
//  3. Persist them in the StateStore bound to the client IP+UA hash.
//  4. Build the authorize URL.
//  5. Set the state cookie and 302.
func (h *SsoHandlers) StartHandler(w http.ResponseWriter, r *http.Request) {
	if h.Client == nil {
		http.Error(w, "sso not configured", http.StatusServiceUnavailable)
		return
	}
	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/"
	}
	if !h.isAllowedNext(next) {
		http.Error(w, "invalid next path", http.StatusBadRequest)
		return
	}

	state, err := RandHex(32)
	if err != nil {
		http.Error(w, "random state failed", http.StatusInternalServerError)
		return
	}
	verifier, _, err := GeneratePKCEPair()
	if err != nil {
		http.Error(w, "pkce failed", http.StatusInternalServerError)
		return
	}
	nonce, err := RandHex(16)
	if err != nil {
		http.Error(w, "random nonce failed", http.StatusInternalServerError)
		return
	}

	bindingHash := ComputeClientBindingHash(clientIP(r), r.UserAgent())
	if err := h.StateStore.Put(StateEntry{
		State:             state,
		CodeVerifier:      verifier,
		Nonce:             nonce,
		Next:              next,
		ClientBindingHash: bindingHash,
		CreatedAt:         h.now(),
	}); err != nil {
		http.Error(w, "state persist failed", http.StatusInternalServerError)
		return
	}

	authURL, err := h.Client.BuildAuthorizeURL(AuthorizeInput{
		State:        state,
		CodeVerifier: verifier,
		Nonce:        nonce,
	})
	if err != nil {
		http.Error(w, "authorize url failed", http.StatusInternalServerError)
		return
	}

	h.setStateCookie(w, state)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// CallbackHandler implements GET /auth/sso/callback.
func (h *SsoHandlers) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if h.Client == nil {
		http.Error(w, "sso not configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	cookie, err := r.Cookie(h.Config.StateCookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, "state_mismatch: missing state cookie", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "state_mismatch", http.StatusBadRequest)
		return
	}

	entry, err := h.StateStore.Take(state)
	if err != nil {
		http.Error(w, "state_consumed", http.StatusBadRequest)
		return
	}

	expectedBinding := ComputeClientBindingHash(clientIP(r), r.UserAgent())
	if entry.ClientBindingHash != "" && entry.ClientBindingHash != expectedBinding {
		http.Error(w, "csrf_binding_mismatch", http.StatusBadRequest)
		return
	}

	res, err := h.Client.ExchangeCodeForProfile(r.Context(), code, entry.CodeVerifier, entry.Nonce)
	if err != nil {
		// Map known auth errors to 401; everything else as 502 since
		// it's upstream-IdP related.
		if errors.Is(err, ErrJwtVerification) ||
			errors.Is(err, ErrMissingIDToken) ||
			errors.Is(err, ErrNonceMismatch) {
			http.Error(w, "id_token_invalid", http.StatusUnauthorized)
			return
		}
		http.Error(w, "exchange_failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	tenantID := TenantFromProfile(res.Profile)
	roles := extractRoles(res.Profile.Raw)
	payload := map[string]any{
		"tenant_id":         tenantID,
		"roles":             roles,
		"relayone_user_id":  res.Profile.RelayoneUserID,
		"msp_managed_orgs":  res.Profile.MspManagedOrgs,
	}
	if res.Profile.MspOrgID != nil {
		payload["msp_org_id"] = *res.Profile.MspOrgID
	}

	var sessionID string
	if h.Session != nil {
		sid, err := h.Session.CreateAuthenticated(res.Profile, res.Tokens)
		if err != nil {
			http.Error(w, "session create failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sessionID = sid
		payload["session_id"] = sid
	}

	pair, err := h.JWT.IssuePair(payload, res.Profile.Sub)
	if err != nil {
		http.Error(w, "issue jwt failed", http.StatusInternalServerError)
		return
	}

	h.setAccessCookie(w, pair.AccessToken, time.Until(pair.AccessExpiresAt))
	h.setRefreshCookie(w, pair.RefreshToken, time.Until(pair.RefreshExpiresAt))
	h.clearStateCookie(w)

	// Echo for non-cookie clients (e.g. cmd/r1-admin).
	w.Header().Set("Authorization", "Bearer "+pair.AccessToken)

	dest := entry.Next
	if !h.isAllowedNext(dest) {
		dest = "/"
	}
	// Stash sessionID into the response for tests / log lines that
	// want it (header is harmless to browsers; cookies remain the
	// canonical conveyance).
	if sessionID != "" {
		w.Header().Set("X-R1-Session-Id", sessionID)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// RefreshHandler implements POST /auth/refresh.
func (h *SsoHandlers) RefreshHandler(w http.ResponseWriter, r *http.Request) {
	rt, err := r.Cookie(h.Config.RefreshCookieName)
	if err != nil || rt.Value == "" {
		writeAuthChallenge(w, "invalid_request", "missing refresh cookie")
		return
	}
	res, err := h.JWT.RefreshAccess(rt.Value)
	if err != nil {
		writeAuthChallenge(w, "invalid_token", "refresh failed")
		return
	}
	h.setAccessCookie(w, res.AccessToken, time.Until(res.AccessExpiresAt))
	w.Header().Set("Authorization", "Bearer "+res.AccessToken)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": res.AccessToken,
		"expires_at":   res.AccessExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

// LogoutHandler implements POST /auth/logout.
//
// Always returns 200 — logout is idempotent. When the discovery doc
// exposes an end_session_endpoint AND the access cookie carries an
// id_token in payload["id_token"], the handler redirects to the IdP's
// RP-Initiated Logout URL; otherwise it clears cookies locally and
// returns 200 with {"logged_out": true}.
func (h *SsoHandlers) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	// Best-effort session invalidate. The access cookie is parsed for
	// session_id; missing or expired tokens still let logout succeed.
	if at, err := r.Cookie(h.Config.AccessCookieName); err == nil && at.Value != "" {
		if v, err := h.JWT.Verify(at.Value); err == nil {
			if sid, _ := v.Payload["session_id"].(string); sid != "" && h.Session != nil {
				_ = h.Session.Invalidate(sid)
			}
		}
	}

	h.clearAccessCookie(w)
	h.clearRefreshCookie(w)

	// RP-Initiated Logout if discovery supplied an end_session_endpoint.
	if h.Client != nil {
		if eps, err := h.Client.Endpoints(r.Context()); err == nil && eps.EndSessionEndpoint != "" && h.Config.LogoutRedirectURL != "" {
			u, _ := url.Parse(eps.EndSessionEndpoint)
			if u != nil {
				qq := u.Query()
				qq.Set("post_logout_redirect_uri", h.Config.LogoutRedirectURL)
				u.RawQuery = qq.Encode()
				http.Redirect(w, r, u.String(), http.StatusFound)
				return
			}
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"logged_out":true}`))
}

// --- cookie helpers ---

func (h *SsoHandlers) baseCookie(name, value string, maxAge int, path string) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   !h.Config.CookieInsecureForTests,
		MaxAge:   maxAge,
	}
	return c
}

func (h *SsoHandlers) setStateCookie(w http.ResponseWriter, state string) {
	c := h.baseCookie(h.Config.StateCookieName, state, 600, "/")
	http.SetCookie(w, c)
}

func (h *SsoHandlers) clearStateCookie(w http.ResponseWriter) {
	c := h.baseCookie(h.Config.StateCookieName, "", -1, "/")
	http.SetCookie(w, c)
}

func (h *SsoHandlers) setAccessCookie(w http.ResponseWriter, val string, ttl time.Duration) {
	if ttl < time.Second {
		ttl = time.Second
	}
	c := h.baseCookie(h.Config.AccessCookieName, val, int(ttl.Seconds()), "/")
	http.SetCookie(w, c)
}

func (h *SsoHandlers) clearAccessCookie(w http.ResponseWriter) {
	c := h.baseCookie(h.Config.AccessCookieName, "", -1, "/")
	http.SetCookie(w, c)
}

func (h *SsoHandlers) setRefreshCookie(w http.ResponseWriter, val string, ttl time.Duration) {
	if ttl < time.Second {
		ttl = time.Second
	}
	c := h.baseCookie(h.Config.RefreshCookieName, val, int(ttl.Seconds()), "/auth")
	http.SetCookie(w, c)
}

func (h *SsoHandlers) clearRefreshCookie(w http.ResponseWriter) {
	c := h.baseCookie(h.Config.RefreshCookieName, "", -1, "/auth")
	http.SetCookie(w, c)
}

// --- helpers ---

func (h *SsoHandlers) isAllowedNext(next string) bool {
	if next == "" {
		return false
	}
	// Reject protocol-relative URLs (//evil.com) and absolute URLs.
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return false
	}
	if strings.Contains(next, "://") {
		return false
	}
	for _, allowed := range h.Config.RedirectAfterLoginAllowlist {
		if allowed == "" {
			continue
		}
		if allowed == "/" {
			// Any path under "/" is allowed (effectively a wildcard).
			return true
		}
		if next == allowed || strings.HasPrefix(next, allowed+"/") || strings.HasPrefix(next, allowed+"?") {
			return true
		}
	}
	return false
}

// clientIP returns a best-effort remote IP. Honors X-Forwarded-For when
// present (the loopback gate prevents spoofing by external callers in
// the r1 deployment model).
func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.IndexByte(xf, ','); i >= 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// extractRoles pulls a roles list out of the raw claims. Accepts
// []any or []string; missing or wrong-type returns ["member"] as a
// safe default.
func extractRoles(raw map[string]any) []string {
	if raw == nil {
		return []string{"member"}
	}
	if v, ok := raw["roles"]; ok {
		switch typed := v.(type) {
		case []string:
			if len(typed) > 0 {
				return typed
			}
		case []any:
			out := make([]string, 0, len(typed))
			for _, x := range typed {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return []string{"member"}
}
