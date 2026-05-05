package server

// agent_mount_test.go — TASK-34 tests:
//
//   TestAgentServeMount_BearerFlows           Bearer token gates /v1/agent/.
//   TestAgentServeAlias_DeprecationHeader     /api/* alias stamps Deprecation: true.
//
// Both tests construct an agentserve.Server with no internal Bearer
// (we rely on the outer requireBearer middleware). The capabilities
// endpoint is the chosen probe because it requires no executor wiring
// and returns deterministic JSON.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/agentserve"
)

// newTestAgentServer builds a minimal agentserve.Server suitable for
// mount tests. Bearer is nil (no internal auth) so the outer
// requireBearer is the only gate.
func newTestAgentServer() *agentserve.Server {
	return agentserve.NewServer(agentserve.Config{
		Version: "test",
		Capabilities: agentserve.Capabilities{
			TaskTypes: []string{"research"},
		},
	})
}

func TestAgentServeMount_BearerFlows(t *testing.T) {
	const token = "tk-mount-bearer-test"
	mux := http.NewServeMux()
	MountAgentServe(mux, newTestAgentServer(), token)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Missing Authorization → 401.
	resp, err := http.Get(srv.URL + "/v1/agent/api/capabilities")
	if err != nil {
		t.Fatalf("get without auth: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing auth: got status %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Wrong token → 401.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/agent/api/capabilities", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get with wrong auth: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: got status %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Correct token → 200 + body.
	req, _ = http.NewRequest("GET", srv.URL+"/v1/agent/api/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get with correct auth: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("correct token: got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var caps agentserve.Capabilities
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatalf("parse caps: %v body=%s", err, string(body))
	}
	if caps.Version != "test" {
		t.Errorf("version: got %q, want test", caps.Version)
	}

	// Canonical mount must NOT stamp Deprecation header.
	if v := resp.Header.Get("Deprecation"); v != "" {
		t.Errorf("canonical /v1/agent/: should not have Deprecation header; got %q", v)
	}
}

func TestAgentServeAlias_DeprecationHeader(t *testing.T) {
	const token = "tk-alias-deprecation-test"
	mux := http.NewServeMux()
	MountAgentServe(mux, newTestAgentServer(), token)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Hit the /api/capabilities alias with a valid bearer.
	req, _ := http.NewRequest("GET", srv.URL+"/api/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get alias: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("alias auth: got status %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Deprecation"); !strings.EqualFold(got, "true") {
		t.Errorf("Deprecation header: got %q, want true", got)
	}
	if got := resp.Header.Get("Sunset"); got == "" {
		t.Errorf("Sunset header: should be set on alias")
	}

	// Alias must still gate auth.
	resp, err = http.Get(srv.URL + "/api/capabilities")
	if err != nil {
		t.Fatalf("get alias without auth: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("alias missing auth: got status %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}
