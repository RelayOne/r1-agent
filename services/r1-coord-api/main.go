// Package main implements the r1 coordination API SaaS surface.
//
// Endpoints:
//
//	GET /healthz       — liveness probe; returns {"ok":true,"service":"r1-coord-api","env":"<env>","version":"<sha>"}
//	GET /v1/version    — version metadata
//	POST /v1/license/verify  — license-key shape stub; returns {valid:true} for any non-empty key
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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const serviceName = "r1-coord-api"

type healthz struct {
	OK        bool   `json:"ok"`
	Service   string `json:"service"`
	Env       string `json:"env"`
	Version   string `json:"version"`
	UptimeSec int64  `json:"uptime_sec"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, fmt.Errorf("EOF")) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON body"})
		return
	}
	valid := len(req.Key) >= 8
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"valid":  valid,
		"reason": map[bool]string{true: "well-formed key", false: "key shorter than 8 chars"}[valid],
	})
}

func handleTelemetryOptIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	seq := telSeqCtr.Add(1)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"accepted": true,
		"seq":      seq,
	})
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

func main() {
	port := getenv("PORT", "8080")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/livez", handleHealthz)
	mux.HandleFunc("/readyz", handleHealthz)
	mux.HandleFunc("/v1/version", handleVersion)
	mux.HandleFunc("/v1/license/verify", handleLicenseVerify)
	mux.HandleFunc("/v1/telemetry/opt-in", handleTelemetryOptIn)
	mux.HandleFunc("/", handleRoot)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("%s listening on :%s (env=%s version=%s)", serviceName, port, envName, versionStr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
