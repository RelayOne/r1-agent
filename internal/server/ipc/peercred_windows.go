//go:build windows
// +build windows

package ipc

import (
	"errors"
	"net"
)

// CheckPeerCred — Windows. The named-pipe SECURITY_ATTRIBUTES bound
// the pipe to the current user's SID at Listen time (TASK-15), so
// every connection that reaches Accept is already proven to be the
// same user. We could call `GetNamedPipeClientProcessId` for finer-
// grained logging, but the auth decision is already enforced by the
// kernel — return nil.
//
// Cross-platform parity: callers use the same CheckPeerCred(conn)
// signature on every OS; this is the Windows accept-everything path.
func CheckPeerCred(c net.Conn) error {
	return nil
}

var (
	ErrPeerNotUnix         = errors.New("ipc: peer-cred: connection is not a unix socket")
	ErrPeerCredMismatch    = errors.New("ipc: peer-cred: uid mismatch")
	ErrPeerCredUnsupported = errors.New("ipc: peer-cred: not used on Windows (pipe ACL enforces)")
)
