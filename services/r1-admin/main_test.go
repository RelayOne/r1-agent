package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestDashboardExplainsUnavailableTopMetrics(t *testing.T) {
	rr := httptest.NewRecorder()
	handleDashboard(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	for _, want := range []string{
		"Dashboard truth",
		"Active sessions",
		"Requires <code>GET /v1/sessions</code> on the configured coord API.",
		"Live lanes",
		"No coord-api lane endpoint is wired in this repo.",
		"USD spent (24h)",
		"No central usage aggregation endpoint or table is wired.",
		"Anti-trunc fires (24h)",
		"No persisted anti-trunc result store is exposed to this page.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard body missing %q", want)
		}
	}
}

func TestBuildDashboardSnapshotUsesCoordHealthAndJWTConfig(t *testing.T) {
	prevEnv := envName
	prevVersion := versionStr
	prevCoordAPI := coordAPI
	prevStartedAt := startedAt
	t.Cleanup(func() {
		envName = prevEnv
		versionStr = prevVersion
		coordAPI = prevCoordAPI
		startedAt = prevStartedAt
	})

	envName = "prod"
	versionStr = "v2026.05.15"
	startedAt = time.Unix(1_700_000_000, 0).UTC()

	t.Setenv("AUTH_JWT_ISSUER", "issuer.example")
	t.Setenv("AUTH_JWT_AUDIENCE", "r1-admin")
	t.Setenv("AUTH_JWT_SECRET", strings.Repeat("s", 32))

	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"service":    "r1-coord-api",
			"env":        "prod",
			"version":    "coord-2026.05.15",
			"uptime_sec": 321,
		})
	}))
	defer coordSrv.Close()
	coordAPI = coordSrv.URL

	snapshot := buildDashboardSnapshot(context.Background(), startedAt.Add(95*time.Second), coordSrv.Client(), "")
	if snapshot.Admin.Version != "v2026.05.15" {
		t.Fatalf("admin version=%q want v2026.05.15", snapshot.Admin.Version)
	}
	if snapshot.Admin.Uptime != "1m35s" {
		t.Fatalf("admin uptime=%q want 1m35s", snapshot.Admin.Uptime)
	}
	if snapshot.Coord.Status != "healthy" {
		t.Fatalf("coord status=%q want healthy", snapshot.Coord.Status)
	}
	if snapshot.Coord.Service != "r1-coord-api" || snapshot.Coord.Version != "coord-2026.05.15" {
		t.Fatalf("coord snapshot=%+v", snapshot.Coord)
	}
	if snapshot.Auth.Mode != "jwt enforced" {
		t.Fatalf("auth mode=%q want jwt enforced", snapshot.Auth.Mode)
	}
	if snapshot.Auth.Issuer != "issuer.example" || snapshot.Auth.Audience != "r1-admin" {
		t.Fatalf("auth snapshot=%+v", snapshot.Auth)
	}
}

func TestBuildDashboardSnapshotDegradesWhenCoordUnavailable(t *testing.T) {
	prevEnv := envName
	prevCoordAPI := coordAPI
	prevStartedAt := startedAt
	t.Cleanup(func() {
		envName = prevEnv
		coordAPI = prevCoordAPI
		startedAt = prevStartedAt
	})

	envName = "dev"
	coordAPI = "http://127.0.0.1:1"
	startedAt = time.Unix(1_700_000_000, 0).UTC()

	snapshot := buildDashboardSnapshot(context.Background(), startedAt.Add(5*time.Second), &http.Client{Timeout: 50 * time.Millisecond}, "")
	if snapshot.Coord.Status != "unreachable" {
		t.Fatalf("coord status=%q want unreachable", snapshot.Coord.Status)
	}
	if snapshot.Auth.Mode != "dev bypass" {
		t.Fatalf("auth mode=%q want dev bypass", snapshot.Auth.Mode)
	}
	if snapshot.Coord.Error == "" {
		t.Fatalf("expected coord error to be populated")
	}
}

func TestDashboardRendersRuntimeSummary(t *testing.T) {
	prevEnv := envName
	prevVersion := versionStr
	prevCoordAPI := coordAPI
	prevStartedAt := startedAt
	t.Cleanup(func() {
		envName = prevEnv
		versionStr = prevVersion
		coordAPI = prevCoordAPI
		startedAt = prevStartedAt
	})

	envName = "prod"
	versionStr = "v2026.05.15"
	startedAt = time.Unix(1_700_000_000, 0).UTC()
	t.Setenv("AUTH_JWT_ISSUER", "issuer.example")
	t.Setenv("AUTH_JWT_AUDIENCE", "r1-admin")
	t.Setenv("AUTH_JWT_SECRET", strings.Repeat("s", 32))

	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"service":    "r1-coord-api",
			"env":        "prod",
			"version":    "coord-2026.05.15",
			"uptime_sec": 321,
		})
	}))
	defer coordSrv.Close()
	coordAPI = coordSrv.URL

	rr := httptest.NewRecorder()
	handleDashboard(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	for _, want := range []string{
		"Admin version",
		"v2026.05.15",
		"Coord API",
		"healthy",
		"Auth mode",
		"jwt enforced",
		"coord-2026.05.15",
		"issuer.example",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard body missing %q", want)
		}
	}
	if strings.Contains(body, "Numbers populated by widgets") {
		t.Fatalf("dashboard still contains placeholder widget copy: %q", body)
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

func TestUsersPageRendersVerifiedOperatorScope(t *testing.T) {
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
		"sub":       "operator-1",
		"email":     "ops@example.com",
		"tenant_id": "tenant-1",
		"org_id":    "org-7",
		"msp": map[string]any{
			"id":    "msp-9",
			"roles": []string{"operator"},
		},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/users", handleUsers)
	h := requireOperator(mux)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%q", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		"Current operator scope",
		"jwt enforced",
		"operator-1",
		"ops@example.com",
		"tenant-1",
		"msp-9",
		"org-7",
		"operator",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("users body missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "Wiring depends on the user store") {
		t.Fatalf("users page still contains placeholder copy: %q", body)
	}
}

func TestAntitruncShowsExplicitUnavailableState(t *testing.T) {
	rr := httptest.NewRecorder()
	handleAntitrunc(rr, httptest.NewRequest(http.MethodGet, "/antitrunc", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"Hosted anti-truncation status",
		"Unavailable in this admin build",
		"r1 antitrunc verify -n 20",
		"audit/antitrunc/",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("antitrunc body missing %q: %q", want, body)
		}
	}
	for _, unwanted := range []string{
		"Numbers populated by a Cloud Scheduler job",
		"last 100 commits",
		"<td>prod</td>",
		"<td>staging</td>",
		"<td>dev</td>",
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("antitrunc body still contains stale placeholder %q: %q", unwanted, body)
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
	// Must be ABSOLUTE to coord-api: admin serves no /v1/auth/sso/*
	// routes, so a relative Location looped forever (audit A026).
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, coordAPI) || !strings.HasSuffix(loc, "/v1/auth/sso/start") {
		t.Errorf("redirect target: %q, want %s/v1/auth/sso/start", loc, coordAPI)
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

// --- /sessions live-wiring tests -----------------------------------------

// TestSessionsPageNoBearerRendersExplicitState: with no forwarded bearer
// (the dev bypass path), the page renders an explicit not-available state
// and NO fabricated rows, and the stale "not implemented" copy is gone.
func TestSessionsPageNoBearerRendersExplicitState(t *testing.T) {
	rr := httptest.NewRecorder()
	handleSessions(rr, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "No operator bearer was forwarded") {
		t.Fatalf("missing no-bearer explanation: %q", body)
	}
	if strings.Contains(body, "not implemented in this repo's coord-api") {
		t.Fatalf("stale 'not implemented' notice still present")
	}
	if strings.Contains(body, "post-deploy iteration") {
		t.Fatalf("stale 'post-deploy iteration' scaffold copy still present")
	}
}

// TestSessionsPageRendersLiveRows: with a forwarded bearer and a coord-api
// that returns real rows, the page renders one escaped row per session plus
// the pagination/freshness footer.
func TestSessionsPageRendersLiveRows(t *testing.T) {
	prevCoordAPI := coordAPI
	t.Cleanup(func() { coordAPI = prevCoordAPI })

	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer op-token" {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "operator role required"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"sessions": []map[string]any{
				{"daemon_id": "d-alpha", "session_id": "s-100", "workdir": "/work/repo", "status": "active", "started_at": "2026-07-08T11:00:00Z", "last_activity": "2026-07-08T11:59:00Z", "cost_usd": 0.4242},
			},
			"total": 1, "page": 1, "page_size": 50, "active_ttl_sec": 300,
		})
	}))
	defer coordSrv.Close()
	coordAPI = coordSrv.URL

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	req.Header.Set("Authorization", "Bearer op-token")
	rr := httptest.NewRecorder()
	handleSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"d-alpha", "s-100", "/work/repo", "active", "$0.4242", "Active total:", "Freshness window:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("live sessions body missing %q: %q", want, body)
		}
	}
}

// TestSessionsPageSurfacesCoordError: a 403 from coord-api is rendered as an
// honest error (with the reason) and no synthetic rows.
func TestSessionsPageSurfacesCoordError(t *testing.T) {
	prevCoordAPI := coordAPI
	t.Cleanup(func() { coordAPI = prevCoordAPI })

	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "operator role required"})
	}))
	defer coordSrv.Close()
	coordAPI = coordSrv.URL

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	req.Header.Set("Authorization", "Bearer member-token")
	rr := httptest.NewRecorder()
	handleSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (page renders the error, does not 500)", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Could not read live sessions") || !strings.Contains(body, "operator role required") {
		t.Fatalf("error not surfaced honestly: %q", body)
	}
	if !strings.Contains(body, "status 403") {
		t.Fatalf("expected coord status in error: %q", body)
	}
}

// TestDashboardActiveSessionsCardLiveCount: with an operator bearer and a
// coord-api that answers /healthz + /v1/sessions, the dashboard card shows
// the real active-session total instead of "Unavailable".
func TestDashboardActiveSessionsCardLiveCount(t *testing.T) {
	prevCoordAPI := coordAPI
	t.Cleanup(func() { coordAPI = prevCoordAPI })

	coordSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "r1-coord-api", "env": "prod", "version": "v1", "uptime_sec": 10})
		case "/v1/sessions":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": []map[string]any{}, "total": 7, "page": 1, "page_size": 1, "active_ttl_sec": 300})
		default:
			http.NotFound(w, r)
		}
	}))
	defer coordSrv.Close()
	coordAPI = coordSrv.URL

	snapshot := buildDashboardSnapshot(context.Background(), startedAt.Add(time.Second), coordSrv.Client(), "Bearer op-token")
	if !snapshot.Sessions.Available {
		t.Fatalf("expected sessions available with bearer, got %+v", snapshot.Sessions)
	}
	if snapshot.Sessions.Count != 7 || snapshot.Sessions.Value != "7" {
		t.Fatalf("sessions count=%d value=%q, want 7", snapshot.Sessions.Count, snapshot.Sessions.Value)
	}
}

// TestVerifyAdminJWTTamperedFinalSigCharFails: replacing the FINAL signature
// character with ANY other base64url character must fail verifyAdminJWT. An
// HS256 signature (32 bytes) encodes to 43 unpadded chars with 2 unused
// trailing bits; the lenient default decoder ignores those bits, so the 3
// other alphabet chars sharing the final char's top 4 bits decoded to the
// IDENTICAL signature and verified — token malleability. Strict() decoding
// rejects them.
func TestVerifyAdminJWTTamperedFinalSigCharFails(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	cfg := adminJWTConfig{
		issuer:   "iss-1",
		audience: "aud-1",
		secret:   strings.Repeat("s", 32),
	}
	token := mustSignAdminToken(t, cfg.issuer, cfg.audience, cfg.secret, map[string]any{
		"roles": []string{"operator"},
		"sub":   "operator-1",
	})
	if _, err := verifyAdminJWT(token, cfg); err != nil {
		t.Fatalf("untampered token must verify: %v", err)
	}
	last := token[len(token)-1]
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] == last {
			continue
		}
		tampered := token[:len(token)-1] + string(alphabet[i])
		if _, err := verifyAdminJWT(tampered, cfg); err == nil {
			t.Errorf("final sig char %q -> %q: verifyAdminJWT accepted a tampered token", string(last), string(alphabet[i]))
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
