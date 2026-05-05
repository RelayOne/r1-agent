//go:build linux
// +build linux

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// CheckPeerCred verifies that the connecting process is running as
// the same UID as the daemon. It is the unix-domain control plane's
// auth boundary — if the kernel says "this peer is uid 1000 too",
// the daemon trusts the request. No bearer token is required on the
// unix socket because the kernel's `SO_PEERCRED` ucred is
// non-spoofable.
//
// Linux implementation: pull the underlying `*net.UnixConn`, get the
// raw fd via `SyscallConn`, then call `getsockopt(SOL_SOCKET,
// SO_PEERCRED)` for a `Ucred{ Pid, Uid, Gid }`. Reject with a
// non-nil error when `Ucred.Uid != os.Getuid()`.
//
// Returns:
//
//   - nil — peer is the same UID as the daemon (accept).
//   - ErrPeerNotUnix — connection wasn't a UnixConn (caller must
//     enforce auth a different way; this normally means a TCP
//     hijacker reached the IPC dispatcher, which is a routing bug).
//   - ErrPeerCredMismatch — peer UID didn't match (reject — close).
//
// `os.Getuid` is read once per call rather than cached because tests
// inject an alternate expected UID via SetExpectedUID below.
func CheckPeerCred(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ErrPeerNotUnix
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("ipc: SyscallConn: %w", err)
	}
	var ucred *syscall.Ucred
	var sockErr error
	cerr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if cerr != nil {
		return fmt.Errorf("ipc: raw.Control: %w", cerr)
	}
	if sockErr != nil {
		return fmt.Errorf("ipc: SO_PEERCRED: %w", sockErr)
	}
	want := expectedUID()
	if int(ucred.Uid) != want {
		return fmt.Errorf("%w: peer uid=%d, want %d (pid=%d)", ErrPeerCredMismatch, ucred.Uid, want, ucred.Pid)
	}
	return nil
}

// expectedUID returns the UID the kernel-reported peer must match.
// Honors the R1_EXPECTED_UID env override (tests only); falls back
// to os.Getuid() in production.
func expectedUID() int {
	if v := os.Getenv("R1_EXPECTED_UID"); v != "" {
		var n int
		// Hand-rolled atoi to avoid importing strconv just for one
		// call site; whitespace/sign not supported (env vars are
		// always positive integers in tests).
		for _, r := range v {
			if r < '0' || r > '9' {
				return os.Getuid()
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return os.Getuid()
}

// ErrPeerNotUnix is returned by CheckPeerCred when the conn isn't a
// unix-domain socket (cross-platform parity with peercred_darwin.go).
var ErrPeerNotUnix = errors.New("ipc: peer-cred: connection is not a unix socket")

// ErrPeerCredMismatch is returned by CheckPeerCred when the peer's
// UID doesn't match the daemon's UID. Callers can `errors.Is` to
// distinguish auth rejection from real I/O errors.
var ErrPeerCredMismatch = errors.New("ipc: peer-cred: uid mismatch")
