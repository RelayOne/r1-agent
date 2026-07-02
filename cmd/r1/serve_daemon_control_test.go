// serve_daemon_control_test.go — integration test for the plain-HTTP
// daemon control plane mounted by runServeLoop (O1). Uses the SAME
// helper the serve loop uses (mountDaemonControlRoutes) so the auth
// chain (RequireBearer), the GET /v1/daemon/info projection, and — the
// load-bearing assertion — the POST /v1/daemon/shutdown → shutdownReqCh
// signal that unwinds the serve loop's select are proven end-to-end
// over a real HTTP server.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/server/jsonrpc"
)

// TestDaemonControl_ShutdownUnwindsLoop is the O1 activation proof: a
// POST to /v1/daemon/shutdown lands a value on shutdownReqCh — the
// exact channel runServeLoop's select reads to tear the daemon down —
// and a fake serve-loop select observes it and returns.
func TestDaemonControl_ShutdownUnwindsLoop(t *testing.T) {
	shutdownReqCh := make(chan int, 1)
	info := func(context.Context) (jsonrpc.DaemonInfoResponse, error) {
		return jsonrpc.DaemonInfoResponse{PID: 4242, Version: "v-test", SessionCount: 2}, nil
	}

	mux := http.NewServeMux()
	mountDaemonControlRoutes(mux, info, shutdownReqCh, testBearer)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Stand up a fake serve loop: it blocks in a select on shutdownReqCh
	// exactly like runServeLoop, and signals `unwound` when the shutdown
	// arrives. This is what "unwinds the loop" means concretely.
	unwound := make(chan int, 1)
	go func() {
		select {
		case grace := <-shutdownReqCh:
			unwound <- grace
		case <-time.After(3 * time.Second):
			unwound <- -1
		}
	}()

	// POST without bearer → 401 (RequireBearer holds; no shutdown fires).
	respNoAuth, err := ts.Client().Post(ts.URL+"/v1/daemon/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("post no-auth: %v", err)
	}
	respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token shutdown status = %d, want 401", respNoAuth.StatusCode)
	}

	// POST with bearer + a grace budget → 200 + accepted_at, and the
	// grace value must reach the serve loop.
	body, _ := json.Marshal(map[string]any{"grace_seconds": 7})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/daemon/shutdown", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("shutdown post: %v", err)
	}
	var ack struct {
		AcceptedAt   string `json:"accepted_at"`
		GraceSeconds int    `json:"grace_seconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("shutdown status = %d, want 200", resp.StatusCode)
	}
	if ack.AcceptedAt == "" {
		t.Errorf("ack missing accepted_at: %+v", ack)
	}
	if ack.GraceSeconds != 7 {
		t.Errorf("ack grace_seconds = %d, want 7", ack.GraceSeconds)
	}

	// The load-bearing assertion: the serve loop select observed the
	// shutdown signal (with the operator-supplied grace) and returned.
	select {
	case grace := <-unwound:
		if grace != 7 {
			t.Fatalf("serve loop unwound with grace=%d, want 7", grace)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve loop did not unwind after POST /v1/daemon/shutdown")
	}
}

// TestDaemonControl_InfoProjection proves GET /v1/daemon/info returns
// the daemon metadata (bearer-gated) without --enable-queue-routes.
func TestDaemonControl_InfoProjection(t *testing.T) {
	shutdownReqCh := make(chan int, 1)
	info := func(context.Context) (jsonrpc.DaemonInfoResponse, error) {
		return jsonrpc.DaemonInfoResponse{PID: 99, Version: "v9", SessionCount: 3}, nil
	}
	mux := http.NewServeMux()
	mountDaemonControlRoutes(mux, info, shutdownReqCh, testBearer)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// No token → 401.
	resp401, err := ts.Client().Get(ts.URL + "/v1/daemon/info")
	if err != nil {
		t.Fatalf("info no-auth: %v", err)
	}
	resp401.Body.Close()
	if resp401.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token info status = %d, want 401", resp401.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/v1/daemon/info", nil)
	req.Header.Set("Authorization", "Bearer "+testBearer)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("info get: %v", err)
	}
	var got jsonrpc.DaemonInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("info status = %d, want 200", resp.StatusCode)
	}
	if got.PID != 99 || got.Version != "v9" || got.SessionCount != 3 {
		t.Errorf("info = %+v, want pid=99 version=v9 sessions=3", got)
	}

	// Nothing fired on the shutdown channel (info is read-only).
	select {
	case g := <-shutdownReqCh:
		t.Fatalf("info fired a shutdown signal (grace=%d)", g)
	default:
	}
}
