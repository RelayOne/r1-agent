package auth

// sso_handlers_test.go — end-to-end coverage of the four HTTP routes
// against a hermetic IdP built on httptest. The IdP serves the OIDC
// discovery doc, a small JWKS, the authorize redirect, the token
// endpoint, and userinfo — enough to drive the full happy + sad paths.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// mockIdP is a minimal OIDC provider for tests. It hosts the
// discovery doc, JWKS, authorize redirect, token endpoint, and
// userinfo. The signing key is RSA-2048 generated once per fixture so
// id_token signatures actually verify.
type mockIdP struct {
	t       *testing.T
	server  *httptest.Server
	privKey *rsa.PrivateKey
	pubKey  *rsa.PublicKey
	kid     string
	issuer  string
	// userinfo claims returned for any valid access token.
	userinfo map[string]any
	// id_token nonce echoed back when the IdP sees one in the
	// authorize call. Lock-protected because tests fire goroutines.
	mu sync.Mutex
	// codeToNonce maps issued codes to the nonce the test wants
	// echoed in the id_token claim.
	codeToNonce map[string]string
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	m := &mockIdP{
		t:           t,
		privKey:     priv,
		pubKey:      &priv.PublicKey,
		kid:         "idp-test-1",
		userinfo:    defaultUserinfo(),
		codeToNonce: map[string]string{},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	m.issuer = m.server.URL
	return m
}

func (m *mockIdP) Close() { m.server.Close() }

func (m *mockIdP) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/.well-known/openid-configuration":
		m.serveDiscovery(w, r)
	case r.URL.Path == "/.well-known/jwks.json":
		m.serveJWKS(w, r)
	case strings.HasSuffix(r.URL.Path, "/oauth/authorize"):
		// In an end-to-end test the user-agent is the test code;
		// just redirect back with a fixed code + the echoed state.
		m.serveAuthorize(w, r)
	case strings.HasSuffix(r.URL.Path, "/oauth/token"):
		m.serveToken(w, r)
	case strings.HasSuffix(r.URL.Path, "/oauth/userinfo"):
		m.serveUserinfo(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *mockIdP) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                                m.issuer,
		"authorization_endpoint":                m.issuer + "/oauth/authorize",
		"token_endpoint":                        m.issuer + "/oauth/token",
		"userinfo_endpoint":                     m.issuer + "/oauth/userinfo",
		"jwks_uri":                              m.issuer + "/.well-known/jwks.json",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (m *mockIdP) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(m.pubKey.N.Bytes())
	eBytes := big.NewInt(int64(m.pubKey.E)).Bytes()
	e := base64.RawURLEncoding.EncodeToString(eBytes)
	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": m.kid,
				"n":   n,
				"e":   e,
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jwks)
}

func (m *mockIdP) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	nonce := q.Get("nonce")
	redirect := q.Get("redirect_uri")
	code := "code-" + state
	m.mu.Lock()
	m.codeToNonce[code] = nonce
	m.mu.Unlock()
	u, _ := url.Parse(redirect)
	qq := u.Query()
	qq.Set("code", code)
	qq.Set("state", state)
	u.RawQuery = qq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (m *mockIdP) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")
	m.mu.Lock()
	nonce := m.codeToNonce[code]
	m.mu.Unlock()
	idTok, err := m.mintIDToken(nonce)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := map[string]any{
		"access_token":  "at-" + code,
		"id_token":      idTok,
		"refresh_token": "rt-" + code,
		"token_type":    "Bearer",
		"expires_in":    900,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockIdP) serveUserinfo(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "missing bearer", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.userinfo)
}

func (m *mockIdP) mintIDToken(nonce string) (string, error) {
	// Use jwx directly so we hit the same signing path the production
	// code uses on the other side.
	key, err := jwk.FromRaw(m.privKey)
	if err != nil {
		return "", err
	}
	_ = key.Set(jwk.KeyIDKey, m.kid)
	_ = key.Set(jwk.AlgorithmKey, jwa.RS256)
	tok := jwt.New()
	_ = tok.Set(jwt.IssuerKey, m.issuer)
	_ = tok.Set(jwt.AudienceKey, "cid")
	_ = tok.Set(jwt.SubjectKey, m.userinfo["sub"])
	_ = tok.Set(jwt.IssuedAtKey, time.Now())
	_ = tok.Set(jwt.ExpirationKey, time.Now().Add(1*time.Hour))
	if nonce != "" {
		_ = tok.Set("nonce", nonce)
	}
	for k, v := range m.userinfo {
		if k == "sub" {
			continue
		}
		_ = tok.Set(k, v)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, key))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func defaultUserinfo() map[string]any {
	return map[string]any{
		"sub":              "ro_user_42",
		"email":            "user@msp.example",
		"email_verified":   true,
		"name":             "MSP User",
		"relayone_user_id": "ro_user_42",
		"relayone_org_id":  "ro_org_msp",
		"msp_org_id":       "ro_org_msp",
		"msp_managed_orgs": []any{"ro_org_a", "ro_org_b"},
		"roles":            []any{"admin"},
	}
}

// fakeSessionManager records calls for test assertions.
type fakeSessionManager struct {
	mu          sync.Mutex
	created     []RelayOneProfile
	invalidated []string
	nextID      int
}

func (f *fakeSessionManager) CreateAuthenticated(p RelayOneProfile, _ Tokens) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.created = append(f.created, p)
	return fmt.Sprintf("sess-%d", f.nextID), nil
}

func (f *fakeSessionManager) Invalidate(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, id)
	return nil
}

// hsTestHandlers builds an SsoHandlers backed by an HS256 JwtService
// (cheap, deterministic) and the supplied mock IdP. Returns the
// bundle + a fake session manager for assertions.
func hsTestHandlers(t *testing.T, idp *mockIdP) (*SsoHandlers, *fakeSessionManager) {
	t.Helper()
	jwt, err := NewJwtService(JwtServiceOptions{
		Issuer:     "r1-test",
		Audience:   "r1-test-aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("k", 32)),
		AccessTTL:  10 * time.Minute,
		RefreshTTL: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	client, err := NewSsoClient(SsoClientOptions{
		ClientID:     "cid",
		ClientSecret: "cs",
		Issuer:       idp.issuer,
		RedirectURI:  "http://localhost/auth/sso/callback",
	})
	if err != nil {
		t.Fatalf("NewSsoClient: %v", err)
	}
	state := NewInMemoryStateStore()
	t.Cleanup(state.Stop)
	sm := &fakeSessionManager{}
	h, err := NewSsoHandlers(client, jwt, state, sm, SsoConfig{
		RedirectAfterLoginAllowlist: []string{"/dashboard", "/"},
		CookieInsecureForTests:      true,
	})
	if err != nil {
		t.Fatalf("NewSsoHandlers: %v", err)
	}
	return h, sm
}

func TestSsoFullFlow(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()

	h, sm := hsTestHandlers(t, idp)

	// Phase 1: GET /auth/sso/start?next=/dashboard
	startReq := httptest.NewRequest("GET", "/auth/sso/start?next=/dashboard", nil)
	startRec := httptest.NewRecorder()
	h.StartHandler(startRec, startReq)

	if startRec.Code != http.StatusFound {
		t.Fatalf("start: got %d, want 302", startRec.Code)
	}
	loc, err := url.Parse(startRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("missing state")
	}
	var stateCookie *http.Cookie
	for _, c := range startRec.Result().Cookies() {
		if c.Name == h.Config.StateCookieName {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("state cookie not set")
	}
	if stateCookie.Value != state {
		t.Errorf("cookie state %q != redirect state %q", stateCookie.Value, state)
	}

	// Phase 2: simulate the IdP's authorize-redirect by hitting the
	// mock authorize endpoint, which redirects to our callback.
	idpURL := loc.String()
	idpClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := idpClient.Get(idpURL)
	if err != nil {
		t.Fatalf("authorize fetch: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: got %d, want 302", resp.StatusCode)
	}
	callbackLoc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse callback loc: %v", err)
	}

	// Phase 3: GET /auth/sso/callback?code=...&state=...
	cbReq := httptest.NewRequest("GET", callbackLoc.RequestURI(), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	h.CallbackHandler(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback: got %d, want 302; body=%s", cbRec.Code, cbRec.Body.String())
	}
	if loc := cbRec.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("redirect = %q, want /dashboard", loc)
	}
	if len(sm.created) != 1 {
		t.Fatalf("session not created: %v", sm.created)
	}
	p := sm.created[0]
	if p.RelayoneOrgID != "ro_org_msp" {
		t.Errorf("tenant claim wrong: %v", p)
	}

	// Verify access cookie issued.
	var atCookie, rtCookie *http.Cookie
	for _, c := range cbRec.Result().Cookies() {
		switch c.Name {
		case h.Config.AccessCookieName:
			atCookie = c
		case h.Config.RefreshCookieName:
			rtCookie = c
		}
	}
	if atCookie == nil || rtCookie == nil {
		t.Fatalf("missing access/refresh cookie: at=%v rt=%v", atCookie, rtCookie)
	}
	// Refresh cookie path must be /auth (defense in depth).
	if rtCookie.Path != "/auth" {
		t.Errorf("refresh cookie Path = %q, want /auth", rtCookie.Path)
	}
	// Verify the access token validates.
	v, err := h.JWT.Verify(atCookie.Value)
	if err != nil {
		t.Fatalf("verify minted access: %v", err)
	}
	if v.Payload["tenant_id"] != "ro_org_msp" {
		t.Errorf("tenant_id claim = %v", v.Payload["tenant_id"])
	}
	if v.Payload["session_id"] != "sess-1" {
		t.Errorf("session_id claim = %v", v.Payload["session_id"])
	}
}

func TestStateMismatchReturns400(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()
	h, _ := hsTestHandlers(t, idp)
	// Put a state into the store so the cookie/state mismatch is the
	// only thing that fails (we want a clean 400 from the mismatch).
	_ = h.StateStore.Put(StateEntry{State: "real-state", CreatedAt: time.Now()})
	req := httptest.NewRequest("GET", "/auth/sso/callback?code=c&state=different-state", nil)
	req.AddCookie(&http.Cookie{Name: h.Config.StateCookieName, Value: "real-state"})
	rec := httptest.NewRecorder()
	h.CallbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "state_mismatch") {
		t.Errorf("body %q missing state_mismatch", rec.Body.String())
	}
}

func TestStateReplayRejected(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()
	h, _ := hsTestHandlers(t, idp)

	// Phase 1: legitimate start to populate the state store.
	startRec := httptest.NewRecorder()
	h.StartHandler(startRec, httptest.NewRequest("GET", "/auth/sso/start?next=/dashboard", nil))
	stateCookie := startRec.Result().Cookies()[0]
	loc, _ := url.Parse(startRec.Header().Get("Location"))
	state := loc.Query().Get("state")

	// Fetch the IdP authorize redirect to mint a code.
	idpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := idpClient.Get(loc.String())
	resp.Body.Close()
	cbLoc, _ := url.Parse(resp.Header.Get("Location"))

	// Phase 2: first callback succeeds.
	cbReq1 := httptest.NewRequest("GET", cbLoc.RequestURI(), nil)
	cbReq1.AddCookie(stateCookie)
	cbRec1 := httptest.NewRecorder()
	h.CallbackHandler(cbRec1, cbReq1)
	if cbRec1.Code != http.StatusFound {
		t.Fatalf("first callback: %d body=%s", cbRec1.Code, cbRec1.Body.String())
	}

	// Phase 3: replay with same state must fail (Take is one-shot).
	cbReq2 := httptest.NewRequest("GET", cbLoc.RequestURI(), nil)
	cbReq2.AddCookie(stateCookie)
	cbRec2 := httptest.NewRecorder()
	h.CallbackHandler(cbRec2, cbReq2)
	if cbRec2.Code != http.StatusBadRequest {
		t.Errorf("replay got %d (want 400); body=%s state=%s", cbRec2.Code, cbRec2.Body.String(), state)
	}
}

func TestRefreshAfterExpiry_Returns401(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()
	h, _ := hsTestHandlers(t, idp)

	// Custom short-TTL JwtService.
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "r1-test",
		Audience:   "r1-test-aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("k", 32)),
		AccessTTL:  100 * time.Millisecond,
		RefreshTTL: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	h.JWT = svc
	pair, _ := svc.IssuePair(map[string]any{}, "u")
	time.Sleep(300 * time.Millisecond)
	req := httptest.NewRequest("POST", "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: h.Config.RefreshCookieName, Value: pair.RefreshToken})
	rec := httptest.NewRecorder()
	h.RefreshHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
	wa := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(wa, "invalid_token") {
		t.Errorf("WWW-Authenticate = %q, want contains invalid_token", wa)
	}
}

func TestRefreshWithoutCookie_Returns401_NoStackTrace(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()
	h, _ := hsTestHandlers(t, idp)
	req := httptest.NewRequest("POST", "/auth/refresh", nil)
	rec := httptest.NewRecorder()
	h.RefreshHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "panic") || strings.Contains(body, "goroutine ") || strings.Contains(body, "/r1-agent") {
		t.Errorf("body leaks stack: %q", body)
	}
}

func TestLogoutWithoutCookie_StillReturns200(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()
	h, _ := hsTestHandlers(t, idp)
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.LogoutHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"logged_out":true`) {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestCallbackRejectsNonAllowlistedNext(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.Close()
	h, _ := hsTestHandlers(t, idp)
	cases := []struct {
		next       string
		wantStatus int
	}{
		{"https://evil.com", http.StatusBadRequest},
		{"//evil.com", http.StatusBadRequest},
		{"/dashboard", http.StatusFound},
	}
	for _, tc := range cases {
		t.Run(tc.next, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/auth/sso/start?next="+url.QueryEscape(tc.next), nil)
			rec := httptest.NewRecorder()
			h.StartHandler(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("got %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestMiddleware_RequireBearer(t *testing.T) {
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "iss",
		Audience:   "aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	tok, _ := svc.Sign(map[string]any{"tenant_id": "t1"}, SignOptions{Subject: "u"})
	mw := svc.RequireBearer(MiddlewareOptions{})
	called := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if v, ok := VerifiedFromContext(r.Context()); !ok || v.Payload["tenant_id"] != "t1" {
			t.Errorf("missing verified token in ctx")
		}
		w.WriteHeader(http.StatusOK)
	}))

	// With valid bearer.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("handler not invoked with valid token")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("got %d", rec.Code)
	}

	// Without bearer.
	called = false
	req2 := httptest.NewRequest("GET", "/", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if called {
		t.Error("handler invoked without token")
	}
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec2.Code)
	}
}

func TestMiddleware_AllowAnonymous(t *testing.T) {
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "iss",
		Audience:   "aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatalf("NewJwtService: %v", err)
	}
	mw := svc.RequireBearer(MiddlewareOptions{AllowAnonymous: true})
	called := false
	handler := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("handler not invoked under AllowAnonymous")
	}
}

func TestStateStore_TakeOneShot(t *testing.T) {
	s := NewInMemoryStateStoreWithTTL(1 * time.Minute)
	defer s.Stop()
	if err := s.Put(StateEntry{State: "x", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.Take("x"); err != nil {
		t.Fatalf("Take: %v", err)
	}
	if _, err := s.Take("x"); !errors.Is(err, ErrStateNotFound) {
		t.Errorf("second Take returned %v, want ErrStateNotFound", err)
	}
}

func TestStateStore_TTLExpiry(t *testing.T) {
	s := NewInMemoryStateStoreWithTTL(50 * time.Millisecond)
	defer s.Stop()
	_ = s.Put(StateEntry{State: "x", CreatedAt: time.Now()})
	time.Sleep(150 * time.Millisecond)
	if _, err := s.Take("x"); !errors.Is(err, ErrStateNotFound) {
		t.Errorf("expected expiry, got %v", err)
	}
}

// keyPairForTest is a helper for the interop test family — generates a
// reusable RSA-2048 keypair PEM-encoded.
func keyPairForTest(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	privDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return
}

// silenceSha256ImportUnused is a no-op reference to keep crypto/sha256
// imported even if no other test path uses it directly (defensive — it
// is referenced via the helpers above).
func silenceSha256ImportUnused() {
	_ = sha256.Size
	_ = context.Background()
}
