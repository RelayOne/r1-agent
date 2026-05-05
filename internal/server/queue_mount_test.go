package server

// queue_mount_test.go — TASK-35 tests:
//
//   TestQueueMount_BearerFlows           Bearer token gates /v1/queue/.
//   TestQueueAlias_DeprecationHeader     /api/<verb> alias stamps Deprecation: true.
//
// We exercise the daemon's /health endpoint as the unauthenticated
// probe (it's the only daemon route that bypasses auth in the inner
// handler) and /status as the authenticated probe.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/daemon"
)

// newTestDaemon builds a daemon.Daemon scoped to a tempdir. The
// daemon is NOT Started — Handler() works without a running pool.
// Token is empty so the inner auth wrapper is permissive; the outer
// requireBearer in MountDaemonQueue is the gate under test.
func newTestDaemon(t *testing.T) *daemon.Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := daemon.New(daemon.Config{
		StateDir:    filepath.Join(dir, "state"),
		Addr:        "127.0.0.1:0",
		Token:       "",
		MaxParallel: 1,
	}, nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	// Start the daemon so the worker pool is initialized; otherwise
	// /status panics on a nil pool. We don't actually serve HTTP from
	// the daemon's own listener — we just need .pool to exist for
	// handler probes. Stop on cleanup.
	if err := d.Start(t.Context()); err != nil {
		t.Fatalf("daemon.Start: %v", err)
	}
	t.Cleanup(d.Stop)
	return d
}

func TestQueueMount_BearerFlows(t *testing.T) {
	const token = "tk-queue-bearer-test"
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	MountDaemonQueue(mux, d, token)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Missing Authorization → 401.
	resp, err := http.Get(srv.URL + "/v1/queue/status")
	if err != nil {
		t.Fatalf("get without auth: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing auth: got status %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Wrong token → 401.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/queue/status", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get with wrong auth: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: got status %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Correct token → 200.
	req, _ = http.NewRequest("GET", srv.URL+"/v1/queue/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get with correct auth: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("correct token: got status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Canonical mount must NOT stamp Deprecation header.
	if got := resp.Header.Get("Deprecation"); got != "" {
		t.Errorf("canonical /v1/queue/: should not have Deprecation; got %q", got)
	}
}

func TestQueueAlias_DeprecationHeader(t *testing.T) {
	const token = "tk-queue-alias-test"
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	MountDaemonQueue(mux, d, token)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// /api/status alias with valid bearer should succeed and stamp deprecation.
	req, _ := http.NewRequest("GET", srv.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get alias: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("alias /api/status: got status %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Deprecation"); !strings.EqualFold(got, "true") {
		t.Errorf("Deprecation header: got %q, want true", got)
	}
	if got := resp.Header.Get("Sunset"); got == "" {
		t.Error("Sunset header: should be set on alias")
	}

	// Alias must still gate auth.
	resp, err = http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("get alias without auth: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("alias missing auth: got status %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}
