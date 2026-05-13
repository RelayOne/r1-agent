package auth

// wire_test.go — coverage for the daemon mount helper.
//
// MountAuth has three branches keyed on R1_AUTH_MODE:
//   - ModeAnonymous returns the identity middleware; no routes mounted.
//   - ModeSSO mounts the four routes and gates the wrapper.
//   - ModeBoth mounts the routes and allows anonymous fall-through.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"", ModeAnonymous},
		{"anonymous", ModeAnonymous},
		{"ANON", ModeAnonymous},
		{"sso", ModeSSO},
		{"SSO", ModeSSO},
		{"both", ModeBoth},
		{"BOTH", ModeBoth},
		{"unknown-value", ModeAnonymous}, // forward-compatible default
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseMode(tc.in)
			if got != tc.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMountAuth_AnonymousIsPassthrough(t *testing.T) {
	wrap, jwt, err := MountAuth(context.Background(), WireOptions{
		Mode: ModeAnonymous,
	})
	if err != nil {
		t.Fatalf("MountAuth: %v", err)
	}
	if jwt != nil {
		t.Errorf("anonymous mode should return nil JwtService, got %v", jwt)
	}
	// Wrapper must be a passthrough.
	called := false
	handler := wrap(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called {
		t.Error("passthrough did not invoke handler")
	}
}

func TestMountAuth_SSOMountsRoutes(t *testing.T) {
	env := map[string]string{
		"AUTH_JWT_ISSUER":            "test-iss",
		"AUTH_JWT_AUDIENCE":          "test-aud",
		"AUTH_JWT_SECRET":            strings.Repeat("k", 32),
		"RELAYONE_SSO_CLIENT_ID":     "cid",
		"RELAYONE_SSO_CLIENT_SECRET": "cs",
		"RELAYONE_SSO_ISSUER":        "https://idp.example",
		"RELAYONE_SSO_REDIRECT_URI":  "https://app/cb",
	}
	mux := http.NewServeMux()
	wrap, jwt, err := MountAuth(context.Background(), WireOptions{
		Mode:   ModeSSO,
		Mux:    mux,
		Getenv: func(k string) string { return env[k] },
		Config: SsoConfig{CookieInsecureForTests: true},
	})
	if err != nil {
		t.Fatalf("MountAuth: %v", err)
	}
	if jwt == nil {
		t.Fatal("JwtService not constructed")
	}
	if wrap == nil {
		t.Fatal("wrapper nil")
	}
	// Routes should be reachable on the mux (StartHandler returns
	// 503 without a configured SSO client; we wired one above, so
	// it should attempt the redirect and yield either 302 or 500).
	// Either way, the route is NOT a 404.
	probe := httptest.NewRequest("GET", "/auth/sso/start?next=/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, probe)
	if rec.Code == http.StatusNotFound {
		t.Errorf("route /auth/sso/start not mounted (got 404)")
	}
}

func TestMountAuth_SSOWithoutClient_StillMountsJWT(t *testing.T) {
	// SSO client env vars absent — SsoClientFromEnv returns (nil, nil).
	// JwtService env vars present. SSO routes should not mount; the
	// wrapper still enforces JWT bearer auth.
	env := map[string]string{
		"AUTH_JWT_ISSUER":   "test-iss",
		"AUTH_JWT_AUDIENCE": "test-aud",
		"AUTH_JWT_SECRET":   strings.Repeat("k", 32),
	}
	mux := http.NewServeMux()
	wrap, jwt, err := MountAuth(context.Background(), WireOptions{
		Mode:   ModeSSO,
		Mux:    mux,
		Getenv: func(k string) string { return env[k] },
	})
	if err != nil {
		t.Fatalf("MountAuth: %v", err)
	}
	if jwt == nil {
		t.Fatal("JwtService should be built even without SSO client")
	}
	// Wrapper rejects without a token.
	handler := wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401 (no token)", rec.Code)
	}
}

func TestMountAuth_BothAllowsAnonymous(t *testing.T) {
	env := map[string]string{
		"AUTH_JWT_ISSUER":   "test-iss",
		"AUTH_JWT_AUDIENCE": "test-aud",
		"AUTH_JWT_SECRET":   strings.Repeat("k", 32),
	}
	mux := http.NewServeMux()
	wrap, _, err := MountAuth(context.Background(), WireOptions{
		Mode:   ModeBoth,
		Mux:    mux,
		Getenv: func(k string) string { return env[k] },
	})
	if err != nil {
		t.Fatalf("MountAuth: %v", err)
	}
	called := false
	handler := wrap(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Error("ModeBoth should allow anonymous fall-through")
	}
}

func TestMountAuth_SSORequiresMux(t *testing.T) {
	env := map[string]string{
		"AUTH_JWT_ISSUER":   "i",
		"AUTH_JWT_AUDIENCE": "a",
		"AUTH_JWT_SECRET":   strings.Repeat("k", 32),
	}
	_, _, err := MountAuth(context.Background(), WireOptions{
		Mode:   ModeSSO,
		Getenv: func(k string) string { return env[k] },
	})
	if err == nil {
		t.Error("want error from missing Mux")
	}
}

func TestMountAuth_JWTConstructFailurePropagates(t *testing.T) {
	// JWT env vars missing -> JwtServiceFromEnv returns an error;
	// MountAuth wraps it.
	mux := http.NewServeMux()
	_, _, err := MountAuth(context.Background(), WireOptions{
		Mode:   ModeSSO,
		Mux:    mux,
		Getenv: func(string) string { return "" },
	})
	if err == nil {
		t.Error("want error from missing JWT config")
	}
}

func TestMountAuth_DefaultsModeToAnonymous(t *testing.T) {
	wrap, jwt, err := MountAuth(context.Background(), WireOptions{})
	if err != nil {
		t.Fatalf("MountAuth: %v", err)
	}
	if jwt != nil {
		t.Errorf("empty Mode should default to anonymous (nil JwtService)")
	}
	called := false
	wrap(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Error("default mode should be a passthrough")
	}
}

func TestTenantFromContext_NoToken(t *testing.T) {
	got := TenantFromContext(context.Background())
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestTenantFromContext_WithToken(t *testing.T) {
	ctx := WithVerified(context.Background(), &VerifiedToken{
		Payload: map[string]any{"tenant_id": "ten-A"},
	})
	got := TenantFromContext(ctx)
	if got != "ten-A" {
		t.Errorf("got %q, want ten-A", got)
	}
}

func TestSubjectFromContext(t *testing.T) {
	if SubjectFromContext(context.Background()) != "" {
		t.Error("want empty subject without token")
	}
	ctx := WithVerified(context.Background(), &VerifiedToken{
		Payload: map[string]any{"sub": "user-99"},
	})
	if got := SubjectFromContext(ctx); got != "user-99" {
		t.Errorf("got %q, want user-99", got)
	}
}
