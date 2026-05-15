package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzReturns200JSON(t *testing.T) {
	rr := httptest.NewRecorder()
	handleHealthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"service":"r1-admin"`) {
		t.Errorf("missing service marker: %s", rr.Body.String())
	}
}

func TestDashboardRendersWithNavLinks(t *testing.T) {
	rr := httptest.NewRecorder()
	handleDashboard(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"r1 admin", "Dashboard", "/sessions", "/lanes", "/users", "/license-keys", "/antitrunc"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}

func TestEachAdminPageRenders200(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"/sessions":     handleSessions,
		"/lanes":        handleLanes,
		"/users":        handleUsers,
		"/license-keys": handleLicenseKeys,
		"/usage":        handleUsage,
		"/revenue":      handleRevenue,
		"/antitrunc":    handleAntitrunc,
		"/settings":     handleSettings,
	}
	for path, h := range cases {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status=%d, want 200", path, rr.Code)
		}
		body := rr.Body.String()
		if !strings.Contains(body, "<!doctype html>") {
			t.Errorf("%s: missing doctype, got %q", path, body[:100])
		}
	}
}

func TestDashboardOnlyServesRoot(t *testing.T) {
	rr := httptest.NewRecorder()
	handleDashboard(rr, httptest.NewRequest(http.MethodGet, "/some/other/path", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 on non-root path, got %d", rr.Code)
	}
}

func TestSettingsShowsTrackingStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	handleSettings(rr, httptest.NewRequest(http.MethodGet, "/settings", nil))
	body := rr.Body.String()
	for _, want := range []string{"PostHog", "Customer.io", "CodeRadar", "JWT issuer"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %q", want)
		}
	}
}

func TestRequireOperatorBypassesPublicPaths(t *testing.T) {
	envName = "prod" // simulate prod gating
	defer func() { envName = "dev" }()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/", handleDashboard)
	h := requireOperator(mux)

	// Public path: 200 even without auth
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("/healthz prod: code=%d want 200", rr.Code)
	}
	// Non-public path in prod without auth: redirect to SSO
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusFound {
		t.Errorf("/ prod no-auth: code=%d want 302", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Location"), "/v1/auth/sso/start") {
		t.Errorf("redirect target: %q", rr.Header().Get("Location"))
	}
}

func TestRequireOperatorPassesInDev(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard)
	h := requireOperator(mux)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("dev unauth: code=%d want 200", rr.Code)
	}
}

func TestRequireOperator_VerifiesJWTAndOperatorRoleInProd(t *testing.T) {
	issuer := "r1-admin-test"
	audience := "r1-admin"
	secret := strings.Repeat("s", 32)
	t.Setenv("AUTH_JWT_ISSUER", issuer)
	t.Setenv("AUTH_JWT_AUDIENCE", audience)
	t.Setenv("AUTH_JWT_SECRET", secret)

	prevEnv := envName
	envName = "prod"
	defer func() { envName = prevEnv }()

	operatorToken := mustSignAdminToken(t, issuer, audience, secret, map[string]any{
		"roles":     []string{"operator"},
		"tenant_id": "tenant-1",
		"sub":       "operator-1",
	})
	memberToken := mustSignAdminToken(t, issuer, audience, secret, map[string]any{
		"roles": []string{"member"},
		"sub":   "member-1",
	})
	invalidBearerToken := mustSignAdminToken(t, issuer, audience, strings.Repeat("x", 32), map[string]any{
		"roles": []string{"operator"},
		"sub":   "operator-2",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard)
	h := requireOperator(mux)

	tests := []struct {
		name       string
		authHeader string
		wantCode   int
		wantBody   string
	}{
		{
			name:       "invalid bearer rejected",
			authHeader: "Bearer " + invalidBearerToken,
			wantCode:   http.StatusUnauthorized,
			wantBody:   "invalid token",
		},
		{
			name:       "malformed token rejected",
			authHeader: "Bearer not-a-jwt",
			wantCode:   http.StatusUnauthorized,
			wantBody:   "invalid token",
		},
		{
			name:       "prefix-only bearer rejected",
			authHeader: "Bearer ",
			wantCode:   http.StatusUnauthorized,
			wantBody:   "invalid token",
		},
		{
			name:       "valid operator allowed",
			authHeader: "Bearer " + operatorToken,
			wantCode:   http.StatusOK,
			wantBody:   "Dashboard",
		},
		{
			name:       "non-operator rejected",
			authHeader: "Bearer " + memberToken,
			wantCode:   http.StatusForbidden,
			wantBody:   "operator role required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.authHeader)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status=%d, want %d; body=%q", rr.Code, tc.wantCode, rr.Body.String())
			}
			if !strings.Contains(strings.ToLower(rr.Body.String()), strings.ToLower(tc.wantBody)) {
				t.Fatalf("body=%q, want substring %q", rr.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestRequireOperator_FailsClosedWhenJWTConfigMissingInProd(t *testing.T) {
	prevEnv := envName
	envName = "prod"
	defer func() { envName = prevEnv }()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard)
	h := requireOperator(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "admin auth unavailable") {
		t.Fatalf("body=%q, want admin auth unavailable", rr.Body.String())
	}
}

func TestEnvOrUnsetReflectsPresence(t *testing.T) {
	key := "R1_ADMIN_TEST_TRACKING"
	if got := envOrUnset(key); got != "unset" {
		t.Fatalf("envOrUnset(%q)=%q, want unset", key, got)
	}
	t.Setenv(key, "configured")
	if got := envOrUnset(key); got != "configured" {
		t.Fatalf("envOrUnset(%q)=%q, want configured", key, got)
	}
}

func TestHandleNotFoundIncludesPath(t *testing.T) {
	rr := httptest.NewRecorder()
	handleNotFound(rr, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/missing") {
		t.Fatalf("body=%q, want missing path", rr.Body.String())
	}
}

func TestHandleVersionShape(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"service": serviceName,
			"env":     envName,
			"version": versionStr,
		})
	})
	handler.ServeHTTP(rr, req)
	for _, want := range []string{`"service":"r1-admin"`, fmt.Sprintf(`"env":"%s"`, envName)} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("body=%q, want %q", rr.Body.String(), want)
		}
	}
}

func mustSignAdminToken(
	t *testing.T,
	issuer string,
	audience string,
	secret string,
	payload map[string]any,
) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]string{
		"alg": "HS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	body := map[string]any{
		"iss": issuer,
		"aud": audience,
	}
	for key, value := range payload {
		body[key] = value
	}
	payloadJSON, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	encoding := base64.RawURLEncoding
	unsigned := encoding.EncodeToString(headerJSON) + "." + encoding.EncodeToString(payloadJSON)
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(unsigned)); err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return unsigned + "." + encoding.EncodeToString(mac.Sum(nil))
}
