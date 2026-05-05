//go:build windows
// +build windows

package ipc

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestPipeBindAndConnect — Windows-only. On non-Windows it's
// invisible (build tag). The Linux/macOS test stub is in
// listen_unix_test.go so this name only compiles on the platform
// being tested.
func TestPipeBindAndConnect(t *testing.T) {
	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if !strings.HasPrefix(ln.Path, PipePrefix) {
		t.Errorf("pipe path %q missing prefix %q", ln.Path, PipePrefix)
	}

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		_, _ = conn.Write([]byte("R"))
		_ = conn.Close()
	}()

	c, err := net.DialTimeout("pipe", ln.Path, 1*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	buf := make([]byte, 1)
	c.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if buf[0] != 'R' {
		t.Errorf("got %q, want %q", string(buf), "R")
	}
}
