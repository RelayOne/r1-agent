//go:build !linux && !darwin && !windows
// +build !linux,!darwin,!windows

package ipc

import (
	"errors"
	"net"
)

// CheckPeerCred is a stub on platforms that don't have a portable
// peer-credential syscall. The control plane's defense in depth is:
//
//  1. The unix socket is bound under a 0700 dir owned by the user, so
//     other users cannot connect in the first place (file-mode
//     boundary).
//  2. The discovery file is mode 0600.
//
// On these platforms the daemon must require Bearer auth even on the
// unix path. Callers detect this branch via errors.Is(err, ErrPeerCredUnsupported).
func CheckPeerCred(c net.Conn) error {
	return ErrPeerCredUnsupported
}

// ErrPeerCredUnsupported signals "this platform has no peer-cred
// syscall, fall back to bearer auth." Callers use errors.Is to
// branch on this without a build-tag dance.
var ErrPeerCredUnsupported = errors.New("ipc: peer-cred not supported on this platform")

// Cross-platform parity: the linux/darwin builds export these
// directly; on platforms without a peer-cred path, callers shouldn't
// see them in returned errors, but the symbols must exist so generic
// errors.Is(err, ipc.ErrPeerCredMismatch) call sites still compile.
var ErrPeerNotUnix = errors.New("ipc: peer-cred: connection is not a unix socket")
var ErrPeerCredMismatch = errors.New("ipc: peer-cred: uid mismatch")
