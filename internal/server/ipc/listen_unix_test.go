//go:build !windows
// +build !windows

package ipc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempRuntime points R1_RUNTIME_DIR at a tmpdir for the duration
// of t. The unix listener resolves the socket dir from this env var
// when it's set (production never sets it).
func withTempRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, hadPrev := os.LookupEnv("R1_RUNTIME_DIR")
	if err := os.Setenv("R1_RUNTIME_DIR", dir); err != nil {
		t.Fatalf("setenv R1_RUNTIME_DIR: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("R1_RUNTIME_DIR", prev)
		} else {
			_ = os.Unsetenv("R1_RUNTIME_DIR")
		}
	})
	return dir
}

func TestSocketBindAndConnect(t *testing.T) {
	dir := withTempRuntime(t)

	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	wantPath := filepath.Join(dir, SocketDirName, SocketName)
	if ln.Path != wantPath {
		t.Errorf("Path = %q, want %q", ln.Path, wantPath)
	}

	c, err := net.DialTimeout("unix", ln.Path, 1*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Round-trip a byte to confirm the listener accepts.
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		_, _ = conn.Write([]byte("R"))
		_ = conn.Close()
	}()
	buf := make([]byte, 1)
	c.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read after accept: %v", err)
	}
	if buf[0] != 'R' {
		t.Errorf("got %q, want %q", string(buf), "R")
	}
}

func TestSocketChmod0600(t *testing.T) {
	withTempRuntime(t)
	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	info, err := os.Stat(ln.Path)
	if err != nil {
		t.Fatalf("stat sock: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode = %v, want 0600", mode)
	}
	parent := filepath.Dir(ln.Path)
	pinfo, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if mode := pinfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("parent dir mode = %v, want 0700", mode)
	}
}

func TestSocketStaleButLive(t *testing.T) {
	withTempRuntime(t)

	ln1, err := Listen()
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	// Drain the accept loop in the background so isLive's dial
	// completes the handshake instead of hanging in SYN-SENT.
	go func() {
		for {
			c, err := ln1.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	defer ln1.Close()

	ln2, err := Listen()
	if err == nil {
		ln2.Close()
		t.Fatal("second Listen succeeded with live owner; want ErrStaleButLive")
	}
	if !errors.Is(err, ErrStaleButLive) {
		t.Errorf("err = %v, want ErrStaleButLive", err)
	}
}

func TestSocketStaleAfterUnlink(t *testing.T) {
	withTempRuntime(t)

	ln1, err := Listen()
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	// Hard-close so the socket file lingers (some Go versions don't
	// unlink under abnormal close).
	stalePath := ln1.Path
	if err := ln1.UnixListener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	// Re-create just the file (simulating a crashed daemon that
	// didn't unlink) so isLive sees ECONNREFUSED.
	if _, err := os.Stat(stalePath); os.IsNotExist(err) {
		f, cerr := os.OpenFile(stalePath, os.O_CREATE|os.O_WRONLY, 0o600)
		if cerr != nil {
			t.Fatalf("recreate stale: %v", cerr)
		}
		_ = f.Close()
	}

	ln2, err := Listen()
	if err != nil {
		t.Fatalf("second Listen with stale sock: %v", err)
	}
	defer ln2.Close()
	if ln2.Path != stalePath {
		t.Errorf("rebind path = %q, want %q", ln2.Path, stalePath)
	}
}
