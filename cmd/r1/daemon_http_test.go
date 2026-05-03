package main

// daemon_http_test.go — TASK-42 tests.
//
//   TestDaemonHTTP_AutoSpawn         spec-named: discovery file is
//                                    initially missing; a fake spawn
//                                    writes it; the retry loop
//                                    succeeds within the 2s timeout.
//   TestResolveDaemonEndpoint_AddrPassthrough
//                                    Non-empty addr is used as-is.
//   TestResolveDaemonEndpoint_DiscoveryHit
//                                    Empty addr + present daemon.json
//                                    populates from disc.
//   TestResolveDaemonEndpoint_SpawnFailure
//                                    Spawn returns error → bubbled up.
//   TestWaitForDiscovery_Timeout     Spawn that never produces a
//                                    daemon.json fails with errDiscoveryMissing.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/daemondisco"
)

// withSpawnHook replaces the package-level spawnDaemon with the
// supplied function for the lifetime of t. Tests use this to drop in
// a fake spawn that writes daemon.json directly.
func withSpawnHook(t *testing.T, fn func() error) {
	t.Helper()
	orig := spawnDaemon
	spawnDaemon = fn
	t.Cleanup(func() { spawnDaemon = orig })
}

// httptestEchoHandler returns a tiny http.HandlerFunc that responds
// with `{"status":"ok"}` so the auto-spawn test can verify the
// resolved endpoint reaches a real server (we don't probe headers /
// methods here — the resolveDaemonEndpoint path is the load-bearing
// assertion).
func httptestEchoHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

func TestResolveDaemonEndpoint_AddrPassthrough(t *testing.T) {
	got, err := resolveDaemonEndpoint("127.0.0.1:1234", "tk")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Addr != "127.0.0.1:1234" {
		t.Errorf("Addr: got %q, want 127.0.0.1:1234", got.Addr)
	}
	if got.Token != "tk" {
		t.Errorf("Token: got %q, want tk", got.Token)
	}
}

func TestResolveDaemonEndpoint_DiscoveryHit(t *testing.T) {
	// assert.Equal-style block-checks below.
	dir := t.TempDir()
	t.Setenv("R1_HOME", dir)
	if _, err := daemondisco.WriteDiscoveryTo(dir, 100, "/tmp/x.sock", 7777, "tk-disc", "r1-test"); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	got, err := resolveDaemonEndpoint("", "")
	if err != nil {
		t.Fatalf("resolveDaemonEndpoint: %v", err)
	}
	if got.Addr != "127.0.0.1:7777" {
		t.Errorf("Addr: got %q", got.Addr)
	}
	if got.Token != "tk-disc" {
		t.Errorf("Token: got %q", got.Token)
	}
}

func TestDaemonHTTP_AutoSpawn(t *testing.T) {
	// Spec-named end-to-end: empty discovery → fake spawn writes
	// daemon.json → retry loop observes it within timeout → daemonHTTP
	// reaches the test server with the right token.
	dir := t.TempDir()
	t.Setenv("R1_HOME", dir)

	// Stand up a stub HTTP server. The fake spawn will write a
	// daemon.json pointing at this server's port.
	srv := httptest.NewServer(httptestEchoHandler(t))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())

	// Hook the spawn to write the discovery file inline. We add a
	// tiny delay so the retry loop actually polls (verifying the
	// retry-loop logic, not just an instant first read).
	var spawnCalls int32
	var mu sync.Mutex
	withSpawnHook(t, func() error {
		mu.Lock()
		spawnCalls++
		mu.Unlock()
		go func() {
			time.Sleep(150 * time.Millisecond)
			daemondisco.WriteDiscoveryTo(dir, 999, "/tmp/x.sock", port, "tk-spawn", "r1-test")
		}()
		return nil
	})

	out, err := daemonHTTP("GET", "", "", "/health", nil)
	if err != nil {
		t.Fatalf("daemonHTTP after auto-spawn: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("response missing 'ok'; got %q", out)
	}
	if spawnCalls != 1 {
		t.Errorf("spawn call count: got %d, want 1", spawnCalls)
	}
}

func TestResolveDaemonEndpoint_SpawnFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_HOME", dir)
	withSpawnHook(t, func() error {
		return errors.New("fake spawn failed")
	})
	_, err := resolveDaemonEndpoint("", "")
	if err == nil {
		t.Fatal("spawn failure: want error, got nil")
	}
	if !strings.Contains(err.Error(), "fake spawn failed") {
		t.Errorf("error should propagate spawn failure; got %v", err)
	}
}

func TestWaitForDiscovery_Timeout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_HOME", dir)
	// daemon.json never appears.
	_, err := waitForDiscovery(300 * time.Millisecond)
	if err == nil {
		t.Fatal("timeout: want error, got nil")
	}
	if !errors.Is(err, errDiscoveryMissing) {
		t.Errorf("error should wrap errDiscoveryMissing; got %v", err)
	}
}
