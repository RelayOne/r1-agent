// serve_ipc_test.go — regression tests for audit A031: `r1 serve`
// must actually bind (and serve on) the unix control socket, honor
// --no-unix, and authenticate the transport with peer-cred instead
// of the bearer token.

package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/server"
	"github.com/RelayOne/r1/internal/server/ipc"
)

// TestBindUnixControlNoUnixSkipsSocket asserts --no-unix yields no
// listener and no socket file.
func TestBindUnixControlNoUnixSkipsSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_RUNTIME_DIR", dir)

	ln, err := bindUnixControl(true)
	if err != nil {
		t.Fatalf("bindUnixControl(--no-unix): %v", err)
	}
	if ln != nil {
		t.Fatalf("bindUnixControl(--no-unix) = %v, want nil listener", ln)
	}
	sock := filepath.Join(dir, ipc.SocketDirName, ipc.SocketName)
	if _, statErr := os.Stat(sock); !os.IsNotExist(statErr) {
		t.Errorf("socket file %s exists despite --no-unix (stat err = %v)", sock, statErr)
	}
}

// TestBindUnixControlBindsSocket asserts the default path binds the
// per-user control socket at the ipc-resolved location.
func TestBindUnixControlBindsSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket file assertions are POSIX-only")
	}
	dir := t.TempDir()
	t.Setenv("R1_RUNTIME_DIR", dir)

	ln, err := bindUnixControl(false)
	if err != nil {
		t.Fatalf("bindUnixControl: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	want := filepath.Join(dir, ipc.SocketDirName, ipc.SocketName)
	if ln.Path != want {
		t.Errorf("socket path = %q, want %q", ln.Path, want)
	}
	if _, statErr := os.Stat(ln.Path); statErr != nil {
		t.Errorf("socket file missing: %v", statErr)
	}
}

// TestUnixControlServesSharedMuxWithPeerCredAuth is the end-to-end
// A031 regression: a same-UID client dialing the control socket with
// NO bearer token must reach an authWrap'd route on the shared serve
// mux — peer-cred replaces bearer auth on this transport.
func TestUnixControlServesSharedMuxWithPeerCredAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket test is POSIX-only")
	}
	t.Setenv("R1_RUNTIME_DIR", t.TempDir())

	ln, err := bindUnixControl(false)
	if err != nil {
		t.Fatalf("bindUnixControl: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	srv := server.New(0, "secret-token", server.NewEventBus())
	us := newUnixControlServer(srv.Handler(), "secret-token")
	go func() { _ = us.Serve(&peerCredListener{Listener: ln}) }()
	t.Cleanup(func() { _ = us.Close() })

	// dialUnix is the exact client `r1 ctl` uses on this path
	// (ctl_daemon_cmd.go) — it sends no Authorization header.
	client := dialUnix(ln.Path)
	client.Timeout = 5 * time.Second

	resp, err := client.Get("http://unix/api/status")
	if err != nil {
		t.Fatalf("GET /api/status over unix socket: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q) — peer-cred auth did not replace bearer", resp.StatusCode, body)
	}
}
