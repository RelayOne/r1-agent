package main

// ctl_bootstrap_test.go — CDC-15 tests.
//
// These tests exercise startSessionCtlServer end-to-end:
//   - it actually binds a Unix socket
//   - the default Status handler returns a snapshot whose Mode matches
//     the caller's mode argument
//   - R1_CTL_DIR overrides the default /tmp socket directory
//
// All tests use t.Setenv which forbids t.Parallel; that's fine because
// the helper is fast (single socket, no I/O).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/sessionctl"
)

// TestStartSessionCtlServer_Listens checks that the helper binds a real
// socket and answers a `status` round-trip with OK=true.
func TestStartSessionCtlServer_Listens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_CTL_DIR", dir)

	srv, sessID := startSessionCtlServer("run", "")
	if srv == nil {
		t.Fatalf("startSessionCtlServer returned nil; expected listener bound under %s", dir)
	}
	defer srv.Close()
	if sessID == "" {
		t.Fatalf("expected non-empty session id")
	}

	sock := filepath.Join(dir, "stoke-"+sessID+".sock")
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not created at %s: %v", sock, err)
	}

	resp, err := sessionctl.Call(sock, sessionctl.Request{
		Verb:      sessionctl.VerbStatus,
		RequestID: "ctl-bootstrap-test-listens",
	})
	if err != nil {
		t.Fatalf("Call(status): %v", err)
	}
	if !resp.OK {
		t.Fatalf("status not OK: error=%q", resp.Error)
	}
}

// TestStartSessionCtlServer_DefaultStatus_ModeMatches verifies the
// default Status callback embeds the mode the caller passed in. This
// is what `stoke status` renders in the MODE column.
func TestStartSessionCtlServer_DefaultStatus_ModeMatches(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_CTL_DIR", dir)

	srv, sessID := startSessionCtlServer("chat", "")
	if srv == nil {
		t.Fatal("startSessionCtlServer returned nil")
	}
	defer srv.Close()

	sock := filepath.Join(dir, "stoke-"+sessID+".sock")
	resp, err := sessionctl.Call(sock, sessionctl.Request{
		Verb:      sessionctl.VerbStatus,
		RequestID: "ctl-bootstrap-test-mode",
	})
	if err != nil {
		t.Fatalf("Call(status): %v", err)
	}
	if !resp.OK {
		t.Fatalf("status not OK: error=%q", resp.Error)
	}

	var snap sessionctl.StatusSnapshot
	if err := json.Unmarshal(resp.Data, &snap); err != nil {
		t.Fatalf("decode StatusSnapshot: %v (raw=%s)", err, string(resp.Data))
	}
	if snap.Mode != "chat" {
		t.Errorf("snap.Mode = %q; want %q", snap.Mode, "chat")
	}
	// The default status handler always reports executing — we don't
	// care about exact wording but it must be non-empty so the table
	// view renders a sensible STATE column.
	if snap.State == "" {
		t.Errorf("snap.State empty; expected non-empty state")
	}
	// Sanity: the JSON should contain "mode":"chat" so anyone scraping
	// the raw blob (e.g. `stoke status --json`) sees it directly.
	if !strings.Contains(string(resp.Data), `"mode":"chat"`) {
		t.Errorf("raw status missing \"mode\":\"chat\": %s", string(resp.Data))
	}
}

// TestStartSessionCtlServer_CustomDir verifies R1_CTL_DIR routes the
// socket file into the requested directory and not /tmp.
func TestStartSessionCtlServer_CustomDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_CTL_DIR", dir)

	srv, sessID := startSessionCtlServer("ship", "")
	if srv == nil {
		t.Fatal("startSessionCtlServer returned nil")
	}
	defer srv.Close()

	want := filepath.Join(dir, "stoke-"+sessID+".sock")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("socket not created in custom dir: %s: %v", want, err)
	}
	// Negative: the helper must NOT have written into /tmp.
	tmpPath := filepath.Join("/tmp", "stoke-"+sessID+".sock")
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("socket leaked into /tmp at %s; R1_CTL_DIR override ignored", tmpPath)
		_ = os.Remove(tmpPath)
	}
}
