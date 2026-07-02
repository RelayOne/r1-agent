// Package main implements the r1 coordination API SaaS surface.
//
// Endpoints:
//
//	GET /healthz       — liveness probe; returns {"ok":true,"service":"r1-coord-api","env":"<env>","version":"<sha>"}
//	GET /v1/version    — version metadata
//	POST /v1/license/verify  — license-key shape stub; valid iff key length >= 8 (shape check only, no key store)
//	POST /v1/telemetry/opt-in  — accepts an opt-in record; returns {accepted:true,seq:<int>}
//
// Deployment:
//
//	gcloud run deploy r1-coord-api-prod --image=us-central1-docker.pkg.dev/relayone-488319/r1/r1-coord-api:<sha> ...
//
// Spec: Goodventures GCP standing rules — Cloud Run service, min instances 1,
// Instance-based billing, region us-central1.
package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1-coord-api/internal/auth"
	"github.com/RelayOne/r1-coord-api/internal/tracking"
)

// trackingClients groups the three vendor clients. Each is a no-op when
// its env vars are unset, so dev environments don't need any tracking
// credentials.
type trackingClients struct {
	posthog    *tracking.PostHog
	customerio *tracking.CustomerIO
	coderadar  *tracking.CodeRadar
}

func newTrackingClients() *trackingClients {
	return &trackingClients{
		posthog: tracking.NewPostHog(
			os.Getenv("POSTHOG_API_KEY"),
			os.Getenv("POSTHOG_HOST"),
		),
		customerio: tracking.NewCustomerIO(
			os.Getenv("CUSTOMERIO_SITE_ID"),
			os.Getenv("CUSTOMERIO_API_KEY"),
			getenv("CUSTOMERIO_REGION", "us"),
		),
		coderadar: tracking.NewCodeRadar(
			os.Getenv("CODERADAR_DSN"),
			serviceName,
			envName,
			versionStr,
		),
	}
}

// captureFunnel sends a single business event to the hosted tracking
// vendors. CodeRadar is the canonical backend analytics transport; the
// other vendors remain best-effort mirrors.
//
// Errors from individual vendors are logged but never block the caller.
func (tc *trackingClients) captureFunnel(ctx context.Context, distinctID, event string, props map[string]any) {
	if err := tc.coderadar.Track(ctx, distinctID, event, props); err != nil {
		log.Printf("coderadar track(%s): %v", event, err)
	}
	if err := tc.posthog.Capture(ctx, distinctID, event, props); err != nil {
		log.Printf("posthog capture(%s): %v", event, err)
	}
	if err := tc.customerio.Track(ctx, distinctID, event, props); err != nil {
		log.Printf("customerio track(%s): %v", event, err)
	}
}

const serviceName = "r1-coord-api"

type healthz struct {
	OK        bool   `json:"ok"`
	Service   string `json:"service"`
	Env       string `json:"env"`
	Version   string `json:"version"`
	UptimeSec int64  `json:"uptime_sec"`
}

type telemetryAttribution struct {
	TS          string `json:"ts"`
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMTerm     string `json:"utm_term"`
	UTMContent  string `json:"utm_content"`
	Ref         string `json:"ref"`
	GCLID       string `json:"gclid"`
	FBCLID      string `json:"fbclid"`
	MSCLKID     string `json:"msclkid"`
	Referrer    string `json:"referrer"`
	LandingPath string `json:"landing_path"`
}

type telemetryOptInRequest struct {
	DistinctID     string               `json:"distinct_id"`
	Source         string               `json:"source"`
	Enabled        *bool                `json:"enabled"`
	InstallChannel string               `json:"install_channel"`
	SessionID      string               `json:"session_id"`
	Device         string               `json:"device"`
	UserAgent      string               `json:"user_agent"`
	Region         string               `json:"region"`
	Attribution    telemetryAttribution `json:"attribution"`
	UTMSource      string               `json:"utm_source"`
	UTMMedium      string               `json:"utm_medium"`
	UTMCampaign    string               `json:"utm_campaign"`
	UTMTerm        string               `json:"utm_term"`
	UTMContent     string               `json:"utm_content"`
	Ref            string               `json:"ref"`
	GCLID          string               `json:"gclid"`
	FBCLID         string               `json:"fbclid"`
	MSCLKID        string               `json:"msclkid"`
	Referrer       string               `json:"referrer"`
	LandingPath    string               `json:"landing_path"`
	AttributionTS  string               `json:"ts"`
}

var (
	startedAt  = time.Now()
	telSeqCtr  atomic.Int64
	envName    = getenv("R1_ENV", "dev")
	versionStr = getenv("R1_VERSION", "dev")
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

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthz{
		OK:        true,
		Service:   serviceName,
		Env:       envName,
		Version:   versionStr,
		UptimeSec: int64(time.Since(startedAt).Seconds()),
	})
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service": serviceName,
		"env":     envName,
		"version": versionStr,
	})
}

func handleLicenseVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	// io.EOF (empty body) falls through to a 200 {valid:false}, matching
	// the telemetry handler below. errors.Is against fmt.Errorf("EOF")
	// could never match — a freshly allocated error is its own identity.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON body"})
		return
	}
	valid := len(req.Key) >= 8
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"valid":  valid,
		"mode":   "shape-check",
		"reason": map[bool]string{true: "shape check only (stub, no key store): key >= 8 chars", false: "key shorter than 8 chars"}[valid],
	})
}

func handleTelemetryOptIn(tc *trackingClients) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		var req telemetryOptInRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON body"})
			return
		}
		seq := telSeqCtr.Add(1)
		distinctID := req.DistinctID
		if distinctID == "" {
			distinctID = fmt.Sprintf("telemetry-%d", seq)
		}
		props := map[string]any{
			"env":     envName,
			"version": versionStr,
		}
		if req.Source != "" {
			props["source"] = req.Source
		}
		if req.Enabled != nil {
			props["enabled"] = *req.Enabled
		}
		if req.InstallChannel != "" {
			props["install_channel"] = req.InstallChannel
		}
		if req.SessionID != "" {
			props["session_id"] = req.SessionID
		}
		if req.Device != "" {
			props["device"] = req.Device
		}
		if req.UserAgent != "" {
			props["user_agent"] = req.UserAgent
		} else if ua := r.UserAgent(); ua != "" {
			props["user_agent"] = ua
		}
		if req.Region != "" {
			props["region"] = req.Region
		}
		addTelemetryAttributionProps(props, req)
		// Best-effort fan-out to PostHog/Customer.io/CodeRadar. The
		// caller doesn't wait on this and it can't fail the response.
		go tc.captureFunnel(context.Background(), distinctID, "telemetry_opt_in", props)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"accepted": true,
			"seq":      seq,
		})
	}
}

func addTelemetryAttributionProps(props map[string]any, req telemetryOptInRequest) {
	for _, field := range []struct {
		prop   string
		nested string
		top    string
	}{
		{prop: "utm_source", nested: req.Attribution.UTMSource, top: req.UTMSource},
		{prop: "utm_medium", nested: req.Attribution.UTMMedium, top: req.UTMMedium},
		{prop: "utm_campaign", nested: req.Attribution.UTMCampaign, top: req.UTMCampaign},
		{prop: "utm_term", nested: req.Attribution.UTMTerm, top: req.UTMTerm},
		{prop: "utm_content", nested: req.Attribution.UTMContent, top: req.UTMContent},
		{prop: "ref", nested: req.Attribution.Ref, top: req.Ref},
		{prop: "gclid", nested: req.Attribution.GCLID, top: req.GCLID},
		{prop: "fbclid", nested: req.Attribution.FBCLID, top: req.FBCLID},
		{prop: "msclkid", nested: req.Attribution.MSCLKID, top: req.MSCLKID},
		{prop: "referrer", nested: req.Attribution.Referrer, top: req.Referrer},
		{prop: "landing_path", nested: req.Attribution.LandingPath, top: req.LandingPath},
		{prop: "attribution_ts", nested: req.Attribution.TS, top: req.AttributionTS},
	} {
		if value := firstNonEmpty(field.top, field.nested); value != "" {
			props[field.prop] = value
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]string{
			"service": serviceName,
			"env":     envName,
			"docs":    "https://platform.r1.run/docs",
		})
		return
	}
	http.NotFound(w, r)
}

// authService builds the JwtService from env.
//   - Prod: AUTH_JWT_SECRET MUST be set; fatal if missing.
//   - Dev:  if AUTH_JWT_SECRET is empty, mint a random per-process key.
//     This means tokens issued by one dev process don't verify on
//     another, which is the right failure mode for a dev surface.
func authService() *auth.JwtService {
	key := []byte(os.Getenv("AUTH_JWT_SECRET"))
	if len(key) == 0 {
		if envName == "prod" {
			log.Fatalf("AUTH_JWT_SECRET must be set in prod (got empty)")
		}
		buf := make([]byte, 32)
		if _, err := cryptorand.Read(buf); err != nil {
			log.Fatalf("generate per-process JWT key: %v", err)
		}
		key = buf
		log.Printf("WARNING: AUTH_JWT_SECRET unset; minted a random %d-byte per-process key (dev only)", len(key))
	}
	issuer := getenv("AUTH_JWT_ISSUER", "r1-coord-api")
	audience := getenv("AUTH_JWT_AUDIENCE", "r1-coord-api")
	return auth.NewJwtServiceHS256(issuer, audience, key)
}

// ssoClient builds the RelayOneSsoClient from env. Returns nil when the
// SSO env block is unset — the auth handlers will return 503 in that case.
func ssoClient() *auth.RelayOneSsoClient {
	base := os.Getenv("RELAYONE_SSO_BASE")
	id := os.Getenv("RELAYONE_SSO_CLIENT_ID")
	secret := os.Getenv("RELAYONE_SSO_CLIENT_SECRET")
	redirect := os.Getenv("RELAYONE_SSO_REDIRECT_URI")
	if base == "" || id == "" || secret == "" || redirect == "" {
		return nil
	}
	return auth.NewRelayOneSsoClient(base, id, secret, redirect)
}

// handleSsoStart redirects the user to the RelayOne SSO authorize URL.
func handleSsoStart(sso *auth.RelayOneSsoClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sso == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":    false,
				"error": "SSO not configured (RELAYONE_SSO_* env unset)",
			})
			return
		}
		state, err := auth.GenerateState()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		// Stash state in a short-lived cookie for CSRF protection.
		http.SetCookie(w, &http.Cookie{
			Name: "r1_sso_state", Value: state, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: true, MaxAge: 600,
		})
		url, err := sso.AuthorizeURL(state)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	}
}

// handleSsoCallback completes the OIDC handshake and issues an r1 JWT.
func handleSsoCallback(sso *auth.RelayOneSsoClient, jwt *auth.JwtService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sso == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":    false,
				"error": "SSO not configured (RELAYONE_SSO_* env unset)",
			})
			return
		}
		// Validate state matches the cookie we set in /sso/start.
		c, err := r.Cookie("r1_sso_state")
		if err != nil || c.Value == "" || c.Value != r.URL.Query().Get("state") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "state mismatch (CSRF)"})
			return
		}
		// Clear the cookie.
		http.SetCookie(w, &http.Cookie{Name: "r1_sso_state", Value: "", Path: "/", MaxAge: -1})

		code := r.URL.Query().Get("code")
		if code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "missing code"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		tok, ui, err := sso.Login(ctx, code, jwt)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"jwt":   tok,
			"sub":   ui.Sub,
			"email": ui.Email,
			"msp":   ui.MSP,
			"org":   ui.Org,
			"roles": ui.Roles,
		})
	}
}

// handleAuthRefresh extends a JWT's expiry without re-authenticating.
func handleAuthRefresh(jwt *auth.JwtService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "missing token"})
			return
		}
		fresh, err := jwt.Refresh(req.Token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jwt": fresh})
	}
}

func main() {
	port := getenv("PORT", "8080")
	jwt := authService()
	sso := ssoClient()
	tc := newTrackingClients()
	log.Printf("tracking enabled: posthog=%v customerio=%v coderadar=%v",
		tc.posthog.Enabled(), tc.customerio.Enabled(), tc.coderadar.Enabled())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/livez", handleHealthz)
	mux.HandleFunc("/readyz", handleHealthz)
	mux.HandleFunc("/v1/version", handleVersion)
	mux.HandleFunc("/v1/license/verify", handleLicenseVerify)
	mux.HandleFunc("/v1/telemetry/opt-in", handleTelemetryOptIn(tc))
	mux.HandleFunc("/v1/auth/sso/start", handleSsoStart(sso))
	mux.HandleFunc("/v1/auth/sso/callback", handleSsoCallback(sso, jwt))
	mux.HandleFunc("/v1/auth/refresh", handleAuthRefresh(jwt))
	mux.HandleFunc("/", handleRoot)

	// Wrap the mux in the auth middleware. Public paths (health probes,
	// version, license verify, telemetry, SSO start/callback, auth
	// refresh) bypass the middleware via the optional list.
	publicPaths := []string{
		"/", "/healthz", "/livez", "/readyz",
		"/v1/version",
		"/v1/license/verify",
		"/v1/telemetry/opt-in",
		"/v1/auth/sso/start",
		"/v1/auth/sso/callback",
		"/v1/auth/refresh",
	}
	handler := auth.Middleware(jwt, publicPaths...)(mux)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("%s listening on :%s (env=%s version=%s sso=%v)", serviceName, port, envName, versionStr, sso != nil)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
