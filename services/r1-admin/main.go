// Package main implements the r1 operator admin panel as a server-
// rendered Go service. We chose Go-with-server-templates over a full
// Next.js admin app because:
//
//  1. The other 9 r1 services are Go; one toolchain to operate.
//  2. Cloud Run cold-start on a 4-MB distroless static is sub-100 ms;
//     a Next.js admin would be 60-200 MB and 1-3 s cold-start.
//  3. The admin surface is read-mostly and operator-only — the
//     interactive richness needed for end users is not needed here.
//
// When richer interactivity is needed, individual pages can be promoted
// to React fragments served from a /static/ path; the shell stays Go.
//
// Routes:
//
//	GET /                       → operator dashboard (live counts)
//	GET /sessions               → all r1 sessions across all daemons
//	GET /lanes                  → live lane overview
//	GET /users                  → user list
//	GET /license-keys           → license-key management
//	GET /usage                  → cost + usage metrics
//	GET /revenue                → revenue dashboard
//	GET /antitrunc              → anti-truncation verifier output
//	GET /settings               → operator settings
//	GET /livez /readyz /v1/version  → health
//	GET /api/...                → JSON-only endpoints for the dashboard widgets
//
// Auth: every non-health page requires Bearer JWT with role=operator
// in the claims.msp namespace. Auth is delegated to the same JWT
// service the coord-api uses (shared AUTH_JWT_SECRET via Secret Manager).
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const serviceName = "r1-admin"

var (
	startedAt  = time.Now()
	envName    = getenv("R1_ENV", "dev")
	versionStr = getenv("R1_VERSION", "dev")
	coordAPI   = getenv("R1_COORD_API_URL", "http://r1-coord-api:8080")
	coordHTTP  = &http.Client{Timeout: 2 * time.Second}
)

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

const layoutTpl = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<title>{{.Title}} — r1 admin</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; script-src 'self'; frame-ancestors 'none';">
<style>
:root{color-scheme:light dark}
body{font:14px/1.55 system-ui,-apple-system,'Segoe UI',sans-serif;margin:0;color:#222;background:#f6f7fa}
@media (prefers-color-scheme:dark){body{color:#e8e8e8;background:#0e0f12}.shell{background:#15161a;border-color:#2a2c33}a{color:#88c0ff}.kv b{color:#9cd9ff}}
.shell{max-width:1280px;margin:0 auto;display:flex;min-height:100vh;background:#fff;border-left:1px solid #ddd;border-right:1px solid #ddd}
nav{width:200px;flex-shrink:0;border-right:1px solid #ddd;padding:1rem;background:#fafafa}
@media (prefers-color-scheme:dark){nav{background:#0b0c0f;border-color:#2a2c33}}
nav h1{font-size:14px;margin:0 0 1rem 0;letter-spacing:.04em;color:#666}
nav a{display:block;padding:.4rem .6rem;border-radius:.4rem;text-decoration:none;color:inherit;margin-bottom:.1rem}
nav a:hover,nav a.active{background:#eef0f3}
@media (prefers-color-scheme:dark){nav a:hover,nav a.active{background:#1c1e23}}
main{flex:1;padding:1.5rem 2rem;min-width:0}
main h1{font-size:1.4rem;margin-top:0}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:1rem;margin:1rem 0}
.card{padding:1rem;border:1px solid #ddd;border-radius:.5rem;background:#fff}
@media (prefers-color-scheme:dark){.card{background:#15161a;border-color:#2a2c33}}
.card .label{font-size:.8rem;color:#888;letter-spacing:.04em;text-transform:uppercase}
.card .value{font-size:1.6rem;font-weight:600;margin-top:.3rem}
table{width:100%;border-collapse:collapse;margin:1rem 0}
th,td{text-align:left;padding:.5rem .8rem;border-bottom:1px solid #eee}
@media (prefers-color-scheme:dark){th,td{border-color:#2a2c33}}
th{font-weight:600;color:#555;font-size:.85rem;letter-spacing:.04em;text-transform:uppercase}
.kv{display:flex;flex-direction:column;gap:.4rem}
.kv b{display:inline-block;min-width:9rem}
code{background:#f0f1f4;padding:.05rem .35rem;border-radius:.25rem;font-size:.9em}
@media (prefers-color-scheme:dark){code{background:#1c1e23}}
.tag{display:inline-block;padding:.05rem .5rem;border-radius:.4rem;font-size:.8em;background:#eef0f3}
@media (prefers-color-scheme:dark){.tag{background:#1c1e23}}
footer{padding:.8rem 2rem;color:#888;font-size:.8em;border-top:1px solid #ddd;background:#fafafa}
@media (prefers-color-scheme:dark){footer{background:#0b0c0f;border-color:#2a2c33}}
</style>
</head><body>
<div class="shell">
<nav>
<h1>r1 admin / {{.Env}}</h1>
<a href="/" class="{{if eq .Path "/"}}active{{end}}">Dashboard</a>
<a href="/sessions" class="{{if eq .Path "/sessions"}}active{{end}}">Sessions</a>
<a href="/lanes" class="{{if eq .Path "/lanes"}}active{{end}}">Lanes</a>
<a href="/users" class="{{if eq .Path "/users"}}active{{end}}">Users</a>
<a href="/license-keys" class="{{if eq .Path "/license-keys"}}active{{end}}">License keys</a>
<a href="/usage" class="{{if eq .Path "/usage"}}active{{end}}">Usage</a>
<a href="/revenue" class="{{if eq .Path "/revenue"}}active{{end}}">Revenue</a>
<a href="/antitrunc" class="{{if eq .Path "/antitrunc"}}active{{end}}">Anti-truncation</a>
<a href="/settings" class="{{if eq .Path "/settings"}}active{{end}}">Settings</a>
</nav>
<main>
<h1>{{.Title}}</h1>
{{.Body}}
</main>
</div>
<footer>r1-admin · env=<code>{{.Env}}</code> · version=<code>{{.Version}}</code> · uptime=<code>{{.Uptime}}</code></footer>
</body></html>
`

var pageTpl = template.Must(template.New("layout").Parse(layoutTpl))

type page struct {
	Title   string
	Path    string
	Env     string
	Version string
	Uptime  string
	Body    template.HTML
}

func render(w http.ResponseWriter, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	p.Env = envName
	p.Version = versionStr
	p.Uptime = time.Since(startedAt).Round(time.Second).String()
	_ = pageTpl.Execute(w, p)
}

type operatorClaimsContextKey struct{}

type operatorScopeSnapshot struct {
	AuthMode string
	Subject  string
	Email    string
	Tenant   string
	MSP      string
	Org      string
	Roles    string
	Scope    string
	Source   string
}

// --- handlers ---------------------------------------------------------

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service": serviceName, "env": envName, "version": versionStr,
		"uptime_sec": int64(time.Since(startedAt).Seconds()),
	})
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		handleNotFound(w, r)
		return
	}
	snapshot := buildDashboardSnapshot(r.Context(), time.Now(), coordHTTP)
	body := template.HTML(fmt.Sprintf(`
<div class="cards">
  <div class="card"><div class="label">Admin version</div><div class="value">%s</div></div>
  <div class="card"><div class="label">Admin uptime</div><div class="value">%s</div></div>
  <div class="card"><div class="label">Coord API</div><div class="value">%s</div></div>
  <div class="card"><div class="label">Auth mode</div><div class="value">%s</div></div>
</div>
<p>This dashboard now reflects live hosted runtime truth for the admin service, operator auth mode, and coord-api reachability. Sessions, lanes, usage, revenue, and user data still require their backing stores/endpoints.</p>
<h2>Runtime summary</h2>
<div class="kv">
<span><b>Admin service:</b> <code>%s</code></span>
<span><b>Admin env:</b> <code>%s</code></span>
<span><b>Admin started:</b> <code>%s</code></span>
<span><b>Coord API URL:</b> <code>%s</code></span>
<span><b>Coord API service:</b> <code>%s</code></span>
<span><b>Coord API env:</b> <code>%s</code></span>
<span><b>Coord API version:</b> <code>%s</code></span>
<span><b>JWT issuer:</b> <code>%s</code></span>
<span><b>JWT audience:</b> <code>%s</code></span>
<span><b>JWT secret:</b> <span class="tag">%s</span></span>
</div>
%s
<h2>Quick links</h2>
<ul>
<li><a href="/sessions">All sessions</a> — every r1d daemon's session catalog</li>
<li><a href="/lanes">Live lanes</a> — every running cognitive thread + tool call</li>
<li><a href="/users">Users</a> — RelayOne MSP-tenanted user list</li>
<li><a href="/license-keys">License keys</a> — issue, revoke, audit</li>
<li><a href="/usage">Usage</a> — per-org / per-user cost + token metrics</li>
<li><a href="/revenue">Revenue</a> — per-tier MRR + ARR + churn</li>
<li><a href="/antitrunc">Anti-truncation</a> — output of <code>r1 antitrunc verify</code></li>
</ul>
`,
		template.HTMLEscapeString(snapshot.Admin.Version),
		template.HTMLEscapeString(snapshot.Admin.Uptime),
		template.HTMLEscapeString(snapshot.Coord.Status),
		template.HTMLEscapeString(snapshot.Auth.Mode),
		template.HTMLEscapeString(snapshot.Admin.Service),
		template.HTMLEscapeString(snapshot.Admin.Env),
		template.HTMLEscapeString(snapshot.Admin.StartedAt),
		template.HTMLEscapeString(snapshot.Coord.URL),
		template.HTMLEscapeString(snapshot.Coord.Service),
		template.HTMLEscapeString(snapshot.Coord.Env),
		template.HTMLEscapeString(snapshot.Coord.Version),
		template.HTMLEscapeString(snapshot.Auth.Issuer),
		template.HTMLEscapeString(snapshot.Auth.Audience),
		template.HTMLEscapeString(snapshot.Auth.Secret),
		template.HTMLEscapeString(snapshot.Coord.Note),
	))
	render(w, page{Title: "Dashboard", Path: "/", Body: body})
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>This page surfaces every active session across every daemon (one row per session). Backed by <code>` + template.HTMLEscapeString(coordAPI) + `/v1/sessions</code>.</p>
<table><thead><tr><th>Daemon</th><th>Session</th><th>Workdir</th><th>Status</th><th>Last activity</th><th>Cost USD</th></tr></thead>
<tbody><tr><td colspan=6 style="color:#888;text-align:center;padding:2rem">Wiring depends on the coord-api Sessions endpoint (post-deploy iteration).</td></tr></tbody></table>`)
	render(w, page{Title: "Sessions", Path: "/sessions", Body: body})
}

func handleLanes(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>Live lane overview. Each row corresponds to a single lane (cognitive thread, tool call, mission task) on any session in any daemon.</p>
<table><thead><tr><th>Lane</th><th>Session</th><th>Kind</th><th>State</th><th>Started</th><th>Notes</th></tr></thead>
<tbody><tr><td colspan=6 style="color:#888;text-align:center;padding:2rem">Wiring depends on the coord-api Lanes endpoint (post-deploy iteration).</td></tr></tbody></table>`)
	render(w, page{Title: "Lanes", Path: "/lanes", Body: body})
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	scope := buildOperatorScopeSnapshot(r)
	body := template.HTML(fmt.Sprintf(`
<p>Current operator scope. This page reflects the verified bearer claims the admin service is enforcing right now, so operators can confirm tenant boundaries and identity without relying on a future user-store integration.</p>
<div class="cards">
  <div class="card"><div class="label">Auth mode</div><div class="value">%s</div></div>
  <div class="card"><div class="label">Tenant scope</div><div class="value">%s</div></div>
  <div class="card"><div class="label">MSP scope</div><div class="value">%s</div></div>
  <div class="card"><div class="label">Org scope</div><div class="value">%s</div></div>
</div>
<div class="kv">
<span><b>Claim source:</b> <code>%s</code></span>
<span><b>Scope summary:</b> %s</span>
</div>
<table><thead><tr><th>Sub</th><th>Email</th><th>Tenant</th><th>MSP</th><th>Org</th><th>Roles</th></tr></thead>
<tbody><tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr></tbody></table>`,
		template.HTMLEscapeString(scope.AuthMode),
		template.HTMLEscapeString(scope.Tenant),
		template.HTMLEscapeString(scope.MSP),
		template.HTMLEscapeString(scope.Org),
		template.HTMLEscapeString(scope.Source),
		template.HTMLEscapeString(scope.Scope),
		template.HTMLEscapeString(scope.Subject),
		template.HTMLEscapeString(scope.Email),
		template.HTMLEscapeString(scope.Tenant),
		template.HTMLEscapeString(scope.MSP),
		template.HTMLEscapeString(scope.Org),
		template.HTMLEscapeString(scope.Roles),
	))
	render(w, page{Title: "Users", Path: "/users", Body: body})
}

func handleLicenseKeys(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>License-key issuance, rotation, revocation, audit. Backed by Cloud SQL <code>license_keys</code> table (schema in <code>services/r1-coord-api/migrations/</code> — pending).</p>
<table><thead><tr><th>Key fingerprint</th><th>Owner</th><th>Tier</th><th>Issued</th><th>Last verified</th><th>Status</th></tr></thead>
<tbody><tr><td colspan=6 style="color:#888;text-align:center;padding:2rem">Real key store + UI wired post-deploy.</td></tr></tbody></table>`)
	render(w, page{Title: "License keys", Path: "/license-keys", Body: body})
}

func handleUsage(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>Per-org / per-user cost + token metrics. Source: aggregated <code>cost.tick</code> events from each daemon's WAL replicated to the central coord-api.</p>
<div class="kv">
<span><b>Total spend (30d):</b> —</span>
<span><b>Total tokens (30d):</b> —</span>
<span><b>Top org by spend:</b> —</span>
<span><b>Median session cost:</b> —</span>
</div>`)
	render(w, page{Title: "Usage", Path: "/usage", Body: body})
}

func handleRevenue(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>Per-tier MRR + ARR + churn dashboard. Hooks into Stripe (or whichever billing vendor lands; not yet wired).</p>
<div class="cards">
<div class="card"><div class="label">MRR</div><div class="value">—</div></div>
<div class="card"><div class="label">ARR</div><div class="value">—</div></div>
<div class="card"><div class="label">Net new revenue (30d)</div><div class="value">—</div></div>
<div class="card"><div class="label">Churn (30d)</div><div class="value">—</div></div>
</div>`)
	render(w, page{Title: "Revenue", Path: "/revenue", Body: body})
}

func handleAntitrunc(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>Output of the most recent <code>r1 antitrunc verify -n 100</code> run across each env. Each commit is classified Verified / Unverified / Lying. Lying counts > 0 are paged to oncall.</p>
<table><thead><tr><th>Env</th><th>Window</th><th>Verified</th><th>Unverified</th><th>Lying</th><th>Last run</th></tr></thead>
<tbody>
<tr><td>prod</td><td>last 100 commits</td><td>—</td><td>—</td><td>—</td><td>—</td></tr>
<tr><td>staging</td><td>last 100 commits</td><td>—</td><td>—</td><td>—</td><td>—</td></tr>
<tr><td>dev</td><td>last 100 commits</td><td>—</td><td>—</td><td>—</td><td>—</td></tr>
</tbody></table>
<p style="color:#888">Numbers populated by a Cloud Scheduler job running <code>r1 antitrunc verify</code> per env and writing results to a small Cloud SQL table read by this page (post-deploy iteration).</p>`)
	render(w, page{Title: "Anti-truncation", Path: "/antitrunc", Body: body})
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	body := template.HTML(`
<p>Operator settings.</p>
<div class="kv">
<span><b>JWT issuer:</b> <code>` + template.HTMLEscapeString(getenv("AUTH_JWT_ISSUER", "r1-coord-api")) + `</code></span>
<span><b>JWT audience:</b> <code>` + template.HTMLEscapeString(getenv("AUTH_JWT_AUDIENCE", "r1-coord-api")) + `</code></span>
<span><b>SSO base URL:</b> <code>` + template.HTMLEscapeString(getenv("RELAYONE_SSO_BASE", "(unset)")) + `</code></span>
<span><b>Coord API URL:</b> <code>` + template.HTMLEscapeString(coordAPI) + `</code></span>
<span><b>Tracking — PostHog:</b> <span class="tag">` + envOrUnset("POSTHOG_API_KEY") + `</span></span>
<span><b>Tracking — Customer.io:</b> <span class="tag">` + envOrUnset("CUSTOMERIO_SITE_ID") + `</span></span>
<span><b>Tracking — CodeRadar:</b> <span class="tag">` + envOrUnset("CODERADAR_DSN") + `</span></span>
</div>`)
	render(w, page{Title: "Settings", Path: "/settings", Body: body})
}

func envOrUnset(k string) string {
	if v := os.Getenv(k); v != "" {
		return "configured"
	}
	return "unset"
}

func buildOperatorScopeSnapshot(r *http.Request) operatorScopeSnapshot {
	scope := operatorScopeSnapshot{
		AuthMode: "dev bypass",
		Subject:  "(unverified)",
		Email:    "(unset)",
		Tenant:   "(global)",
		MSP:      "(unset)",
		Org:      "(unset)",
		Roles:    "(none)",
		Scope:    "dev bypass active; no verified bearer claims on this request",
		Source:   "request context",
	}

	claims, ok := operatorClaimsFromRequest(r)
	if !ok {
		if envName != "dev" {
			scope.AuthMode = "jwt unavailable"
			scope.Scope = "request reached the users panel without verified operator claims"
		}
		return scope
	}

	scope.AuthMode = "jwt enforced"
	scope.Subject = stringClaim(claims, "sub")
	scope.Email = stringClaim(claims, "email")
	scope.Tenant = stringClaim(claims, "tenant_id")
	scope.MSP = nestedStringClaim(claims, "msp", "id")
	scope.Org = firstNonEmpty(stringClaim(claims, "org_id", "org"), nestedStringClaim(claims, "msp", "org_id", "org"))
	scope.Roles = strings.Join(claimRoles(claims), ", ")

	if scope.Subject == "" {
		scope.Subject = "(unset)"
	}
	if scope.Email == "" {
		scope.Email = "(unset)"
	}
	if scope.Tenant == "" {
		scope.Tenant = "(global)"
	}
	if scope.MSP == "" {
		scope.MSP = "(unset)"
	}
	if scope.Org == "" {
		scope.Org = "(unset)"
	}
	if scope.Roles == "" {
		scope.Roles = "(none)"
	}
	scope.Scope = fmt.Sprintf("tenant=%s · msp=%s · org=%s", scope.Tenant, scope.MSP, scope.Org)
	return scope
}

type dashboardSnapshot struct {
	Admin dashboardServiceStatus
	Coord dashboardCoordStatus
	Auth  dashboardAuthStatus
}

type dashboardServiceStatus struct {
	Service   string
	Env       string
	Version   string
	Uptime    string
	StartedAt string
}

type dashboardCoordStatus struct {
	URL     string
	Status  string
	Service string
	Env     string
	Version string
	Note    string
	Error   string
}

type dashboardAuthStatus struct {
	Mode     string
	Issuer   string
	Audience string
	Secret   string
}

type healthzPayload struct {
	OK        bool   `json:"ok"`
	Service   string `json:"service"`
	Env       string `json:"env"`
	Version   string `json:"version"`
	UptimeSec int64  `json:"uptime_sec"`
}

func buildDashboardSnapshot(ctx context.Context, now time.Time, client *http.Client) dashboardSnapshot {
	snapshot := dashboardSnapshot{
		Admin: dashboardServiceStatus{
			Service:   serviceName,
			Env:       envName,
			Version:   versionStr,
			Uptime:    now.Sub(startedAt).Round(time.Second).String(),
			StartedAt: startedAt.UTC().Format(time.RFC3339),
		},
		Coord: dashboardCoordStatus{
			URL:     coordAPI,
			Status:  "unreachable",
			Service: "unknown",
			Env:     "unknown",
			Version: "unknown",
			Note:    "coord-api health probe failed",
		},
		Auth: dashboardAuthStatus{
			Mode:     "jwt unavailable",
			Issuer:   getenv("AUTH_JWT_ISSUER", "(unset)"),
			Audience: getenv("AUTH_JWT_AUDIENCE", "(unset)"),
			Secret:   envOrUnset("AUTH_JWT_SECRET"),
		},
	}

	if envName == "dev" {
		snapshot.Auth.Mode = "dev bypass"
	} else if _, err := loadAdminJWTConfig(); err == nil {
		snapshot.Auth.Mode = "jwt enforced"
	}

	health, err := fetchCoordHealth(ctx, client, coordAPI)
	if err != nil {
		snapshot.Coord.Error = err.Error()
		return snapshot
	}
	snapshot.Coord.Status = "healthy"
	snapshot.Coord.Service = health.Service
	snapshot.Coord.Env = health.Env
	snapshot.Coord.Version = health.Version
	snapshot.Coord.Note = fmt.Sprintf("healthz ok · uptime=%ds", health.UptimeSec)
	return snapshot
}

func fetchCoordHealth(ctx context.Context, client *http.Client, baseURL string) (healthzPayload, error) {
	if client == nil {
		client = coordHTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/healthz", nil)
	if err != nil {
		return healthzPayload{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return healthzPayload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return healthzPayload{}, fmt.Errorf("coord-api healthz status %d", resp.StatusCode)
	}
	var payload healthzPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return healthzPayload{}, err
	}
	if payload.Service == "" {
		payload.Service = "unknown"
	}
	if payload.Env == "" {
		payload.Env = "unknown"
	}
	if payload.Version == "" {
		payload.Version = "unknown"
	}
	return payload, nil
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	body := template.HTML(`<p>Path <code>` + template.HTMLEscapeString(r.URL.Path) + `</code> not found.</p><p><a href="/">← Dashboard</a></p>`)
	render(w, page{Title: "404", Path: r.URL.Path, Body: body})
}

type adminJWTConfig struct {
	issuer   string
	audience string
	secret   string
}

var errAdminJWTConfigMissing = errors.New("admin jwt config missing")

// requireOperator keeps dev-mode friction low but verifies production
// operator tokens locally so the hosted admin cannot be unlocked by a
// bare Authorization prefix.
func requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Allow unauthenticated access in dev for now; switch to JWT
		// verification once the shared auth package is extracted.
		if envName == "dev" {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := bearerTokenFromHeader(r.Header.Get("Authorization"))
		if !ok {
			http.Redirect(w, r, "/v1/auth/sso/start", http.StatusFound)
			return
		}
		cfg, err := loadAdminJWTConfig()
		if err != nil {
			http.Error(w, "admin auth unavailable", http.StatusServiceUnavailable)
			return
		}
		claims, err := verifyAdminJWT(token, cfg)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if !hasOperatorRole(claims) {
			http.Error(w, "operator role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), operatorClaimsContextKey{}, claims)))
	})
}

func operatorClaimsFromRequest(r *http.Request) (map[string]any, bool) {
	if r == nil {
		return nil, false
	}
	claims, ok := r.Context().Value(operatorClaimsContextKey{}).(map[string]any)
	return claims, ok
}

func bearerTokenFromHeader(authHeader string) (string, bool) {
	if authHeader == "" {
		return "", false
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if token == "" {
		return "", true
	}
	return token, true
}

func loadAdminJWTConfig() (adminJWTConfig, error) {
	cfg := adminJWTConfig{
		issuer:   os.Getenv("AUTH_JWT_ISSUER"),
		audience: os.Getenv("AUTH_JWT_AUDIENCE"),
		secret:   os.Getenv("AUTH_JWT_SECRET"),
	}
	if cfg.issuer == "" || cfg.audience == "" || cfg.secret == "" {
		return adminJWTConfig{}, errAdminJWTConfigMissing
	}
	return cfg, nil
}

func verifyAdminJWT(token string, cfg adminJWTConfig) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("jwt must have three parts")
	}

	encoding := base64.RawURLEncoding
	headerBytes, err := encoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, err
	}
	if header.Alg != "HS256" {
		return nil, errors.New("unsupported jwt alg")
	}

	payloadBytes, err := encoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, err
	}

	signature, err := encoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(cfg.secret))
	if _, err := mac.Write([]byte(parts[0] + "." + parts[1])); err != nil {
		return nil, err
	}
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(signature, expected) != 1 {
		return nil, errors.New("invalid jwt signature")
	}

	if iss, _ := claims["iss"].(string); iss != cfg.issuer {
		return nil, errors.New("invalid issuer")
	}
	if !audienceMatches(claims["aud"], cfg.audience) {
		return nil, errors.New("invalid audience")
	}
	if !claimTimeValid(claims["exp"], time.Now()) {
		return nil, errors.New("token expired")
	}
	return claims, nil
}

func audienceMatches(raw any, want string) bool {
	switch aud := raw.(type) {
	case string:
		return aud == want
	case []any:
		for _, value := range aud {
			if audValue, ok := value.(string); ok && audValue == want {
				return true
			}
		}
	}
	return false
}

func claimTimeValid(raw any, now time.Time) bool {
	if raw == nil {
		return true
	}
	switch value := raw.(type) {
	case float64:
		return int64(value) > now.Unix()
	case json.Number:
		parsed, err := value.Int64()
		return err == nil && parsed > now.Unix()
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return err == nil && parsed > now.Unix()
	default:
		return false
	}
}

func hasOperatorRole(claims map[string]any) bool {
	if containsRole(claims["roles"], "operator") {
		return true
	}
	msp, ok := claims["msp"].(map[string]any)
	if !ok {
		return false
	}
	return containsRole(msp["roles"], "operator")
}

func containsRole(raw any, want string) bool {
	switch roles := raw.(type) {
	case []any:
		for _, role := range roles {
			if roleValue, ok := role.(string); ok && roleValue == want {
				return true
			}
		}
	case []string:
		for _, role := range roles {
			if role == want {
				return true
			}
		}
	}
	return false
}

func claimRoles(claims map[string]any) []string {
	roles := collectRoles(nil, claims["roles"])
	if msp, ok := claims["msp"].(map[string]any); ok {
		roles = collectRoles(roles, msp["roles"])
	}
	if len(roles) == 0 {
		return nil
	}
	return roles
}

func collectRoles(dst []string, raw any) []string {
	switch roles := raw.(type) {
	case []any:
		for _, role := range roles {
			if roleValue, ok := role.(string); ok {
				dst = appendUnique(dst, roleValue)
			}
		}
	case []string:
		for _, role := range roles {
			dst = appendUnique(dst, role)
		}
	}
	return dst
}

func appendUnique(dst []string, value string) []string {
	if value == "" {
		return dst
	}
	for _, existing := range dst {
		if existing == value {
			return dst
		}
	}
	return append(dst, value)
}

func stringClaim(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nestedStringClaim(claims map[string]any, parent string, keys ...string) string {
	nested, ok := claims[parent].(map[string]any)
	if !ok {
		return ""
	}
	return stringClaim(nested, keys...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isPublic(p string) bool {
	switch p {
	case "/healthz", "/livez", "/readyz", "/v1/version":
		return true
	}
	return false
}

func main() {
	port := getenv("PORT", "8080")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/livez", handleHealthz)
	mux.HandleFunc("/readyz", handleHealthz)
	mux.HandleFunc("/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": serviceName, "env": envName, "version": versionStr})
	})

	mux.HandleFunc("/", handleDashboard)
	mux.HandleFunc("/sessions", handleSessions)
	mux.HandleFunc("/lanes", handleLanes)
	mux.HandleFunc("/users", handleUsers)
	mux.HandleFunc("/license-keys", handleLicenseKeys)
	mux.HandleFunc("/usage", handleUsage)
	mux.HandleFunc("/revenue", handleRevenue)
	mux.HandleFunc("/antitrunc", handleAntitrunc)
	mux.HandleFunc("/settings", handleSettings)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           requireOperator(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("%s listening on :%s (env=%s version=%s coord_api=%s)", serviceName, port, envName, versionStr, coordAPI)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
	_ = fmt.Sprintf // keep fmt used after refactor
}
