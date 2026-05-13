// supervisor.go: Chromium subprocess launcher + CDP proxy for the
// r1-browser service.
//
// The launcher exec()s Chromium with the args spec §T3 requires:
//
//   chromium \
//     --headless=new \
//     --remote-debugging-port=9222 \
//     --remote-debugging-address=127.0.0.1 \
//     --no-sandbox \
//     --disable-gpu \
//     --disable-dev-shm-usage \
//     --user-data-dir=/tmp/userdata-<uuid> \
//     about:blank
//
// The proxy then forwards inbound /devtools/browser/<id> WS upgrades
// to localhost:9222's identical WS endpoint. Bearer token verification
// runs BEFORE the upgrade — failed auth returns 401 without ever
// touching Chromium.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// chromiumBinary returns the chromium executable path. CHROMIUM_BINARY
// env override wins; otherwise the well-known Debian path.
func chromiumBinary() string {
	if v := os.Getenv("CHROMIUM_BINARY"); v != "" {
		return v
	}
	return "/usr/bin/chromium"
}

// startChromium launches Chromium and pings localhost:9222 until it
// responds. Sets chromiumReady on success. On failure, logs and
// retries every 5 seconds — Cloud Run will kill the container after
// the configured readiness probe deadline.
func startChromium() {
	for {
		if err := launchOnce(); err != nil {
			log.Printf("chromium launch failed: %v; retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		// launchOnce blocks until Chromium exits; we loop to relaunch.
		log.Printf("chromium exited; relaunching")
		chromiumReady.Store(false)
	}
}

// launchOnce starts one Chromium instance and waits for it.
func launchOnce() error {
	uuid := randomUUID()
	args := []string{
		"--headless=new",
		"--remote-debugging-port=9222",
		"--remote-debugging-address=127.0.0.1",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--user-data-dir=/tmp/userdata-" + uuid,
		"about:blank",
	}
	cmd := exec.Command(chromiumBinary(), args...)
	cmd.Stdout = os.Stderr // Chromium is chatty; route to stderr for Cloud Logging
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := waitDevtoolsReady(30 * time.Second); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	chromiumReady.Store(true)
	return cmd.Wait()
}

// waitDevtoolsReady pings localhost:9222 until it returns 200 or the
// timeout elapses.
func waitDevtoolsReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:9222", 1*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("chromium devtools not ready within timeout")
}

// randomUUID returns a 16-hex random string — good enough for
// per-launch user-data-dir suffixes.
func randomUUID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newCDPProxy returns the http.Handler that authenticates inbound
// CDP-over-WS upgrades and forwards them to localhost:9222.
func newCDPProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !verifyBearer(r) {
			http.Error(w, "auth rejected", http.StatusUnauthorized)
			return
		}
		// Strip the Authorization header before forwarding upstream
		// (Chromium ignores it, but defense in depth).
		r.Header.Del("Authorization")
		proxyToDevtools(w, r)
	})
}

// verifyBearer returns true when the Authorization header carries a
// well-formed Bearer token. v1: presence check + JWT structural
// validation. Full audience + signature verification lives in v2
// alongside the JWKS cache; v1 ships with the container in a
// VPC-internal Cloud Run service where IAM's run.invoker grant is
// the load-bearing authz check (T10).
func verifyBearer(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	tok := strings.TrimPrefix(auth, "Bearer ")
	if len(tok) < 16 {
		return false
	}
	// Structural sanity: JWT has three dot-delimited segments.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		// Allow non-JWT bearer tokens in dev (e.g., the StubMinter
		// case used by integration tests). The IAM check upstream
		// still gates production traffic.
		return os.Getenv("R1_BROWSER_DEV_ANY_BEARER") != ""
	}
	return true
}

// proxyToDevtools forwards a request to localhost:9222 preserving
// the WS upgrade headers. For non-WS GETs (the devtools JSON
// catalog) it does a plain HTTP roundtrip.
func proxyToDevtools(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		proxyWS(w, r)
		return
	}
	// Plain HTTP forward.
	upstream := "http://127.0.0.1:9222" + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// proxyWS hijacks the inbound connection and bidirectionally streams
// it to localhost:9222.
func proxyWS(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	upstream, err := net.DialTimeout("tcp", "127.0.0.1:9222", 5*time.Second)
	if err != nil {
		http.Error(w, "dial upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Replay the inbound request to upstream verbatim.
	if err := r.Write(upstream); err != nil {
		upstream.Close()
		http.Error(w, "write upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		http.Error(w, "hijack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	// Either direction closing tears the pair down.
	<-done
}
