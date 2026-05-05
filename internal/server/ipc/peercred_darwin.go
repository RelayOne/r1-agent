//go:build darwin
// +build darwin

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// CheckPeerCred — macOS implementation. Uses `syscall.Getpeereid`
// (LOCAL_PEERCRED-equivalent) to read the peer UID. macOS reports
// only `(uid, gid)`; pid is unavailable from this syscall.
//
// See peercred_linux.go for the contract / sentinel errors.
func CheckPeerCred(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ErrPeerNotUnix
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("ipc: SyscallConn: %w", err)
	}
	var uid uint32
	var sockErr error
	cerr := raw.Control(func(fd uintptr) {
		// LOCAL_PEERCRED returns Xucred{Version, Uid, Ngroups, Groups}.
		// Pid is unavailable on macOS via this syscall; we only use
		// Uid for the auth decision.
		var x *unix.Xucred
		x, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if sockErr == nil && x != nil {
			uid = x.Uid
		}
	})
	if cerr != nil {
		return fmt.Errorf("ipc: raw.Control: %w", cerr)
	}
	if sockErr != nil {
		return fmt.Errorf("ipc: Getpeereid: %w", sockErr)
	}
	want := expectedUID()
	if int(uid) != want {
		return fmt.Errorf("%w: peer uid=%d, want %d", ErrPeerCredMismatch, uid, want)
	}
	return nil
}

func expectedUID() int {
	if v := os.Getenv("R1_EXPECTED_UID"); v != "" {
		var n int
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

var ErrPeerNotUnix = errors.New("ipc: peer-cred: connection is not a unix socket")
var ErrPeerCredMismatch = errors.New("ipc: peer-cred: uid mismatch")
