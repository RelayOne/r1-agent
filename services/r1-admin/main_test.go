package main

import (
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
