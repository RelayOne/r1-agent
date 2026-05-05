//go:build linux
// +build linux

package ipc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPeerCredAcceptsSelf — connect to a unix socket from the same
// process and verify CheckPeerCred returns nil (because the peer's
// UID matches os.Getuid()).
func TestPeerCredAcceptsSelf(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "peercred.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: sock})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	// Server side: accept once, run CheckPeerCred, send result on chan.
	resultCh := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			resultCh <- aerr
			return
		}
		defer conn.Close()
		resultCh <- CheckPeerCred(conn)
	}()

	c, err := net.DialTimeout("unix", sock, 1*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("CheckPeerCred(self) = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CheckPeerCred timed out")
	}
}

// TestPeerCredRejectsForeignUID — set R1_EXPECTED_UID to something
// other than os.Getuid() and verify CheckPeerCred returns
// ErrPeerCredMismatch. This simulates the "peer was a different
// user" branch without needing setuid in the test process.
func TestPeerCredRejectsForeignUID(t *testing.T) {
	prev, hadPrev := os.LookupEnv("R1_EXPECTED_UID")
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("R1_EXPECTED_UID", prev)
		} else {
			_ = os.Unsetenv("R1_EXPECTED_UID")
		}
	})
	if err := os.Setenv("R1_EXPECTED_UID", "999999"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "peercred.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: sock})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	resultCh := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			resultCh <- aerr
			return
		}
		defer conn.Close()
		resultCh <- CheckPeerCred(conn)
	}()

	c, err := net.DialTimeout("unix", sock, 1*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrPeerCredMismatch) {
			t.Fatalf("CheckPeerCred = %v, want ErrPeerCredMismatch", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CheckPeerCred timed out")
	}
}

// TestPeerCredRejectsNonUnix — pipe a *net.TCPConn through and
// verify ErrPeerNotUnix.
func TestPeerCredRejectsNonUnix(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen tcp: %v", err)
	}
	defer ln.Close()

	resultCh := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			resultCh <- aerr
			return
		}
		defer conn.Close()
		resultCh <- CheckPeerCred(conn)
	}()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), 1*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrPeerNotUnix) {
			t.Fatalf("CheckPeerCred(tcp) = %v, want ErrPeerNotUnix", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CheckPeerCred timed out")
	}
}
