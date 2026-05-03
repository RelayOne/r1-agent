//go:build !windows
// +build !windows

// Package ipc implements the per-user control-plane listener used by
// the `r1 serve` daemon (specs/r1d-server.md Phase C, items 14–16).
//
// On POSIX: a unix-domain stream socket under
// `$XDG_RUNTIME_DIR/r1/r1.sock` (Linux) or `$TMPDIR/r1-$UID/r1.sock`
// (macOS, or Linux without XDG_RUNTIME_DIR), chmod 0600, with the
// parent directory chmodded 0700. The control surface is auth'd by
// kernel-enforced peer-credential check (see TASK-16) — no Bearer
// token needed on the unix path because the kernel proves caller
// identity.
//
// On Windows: see listen_windows.go (named pipe + SECURITY_ATTRIBUTES).
//
// # Stale-but-live detection
//
// On startup, an existing socket file may mean either:
//
//  1. A previous daemon crashed without unlink — stale, safe to remove
//     and re-bind.
//  2. A live daemon is already listening — second invocation must
//     abort (single-instance — daemonlock catches this too, but a
//     belt-and-suspenders dial here surfaces the failure faster on
//     filesystems where flock has lag, e.g. NFS).
//
// We dial the existing socket first. If the dial succeeds, we abort
// (live owner). If it fails with `ECONNREFUSED` (or `ENOENT`), we
// `unlink` + `bind`. Any other error (permission, bad-perm,
// disconnected) is surfaced unmodified.
package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// SocketName is the basename of the control socket inside the
// per-user runtime dir.
const SocketName = "r1.sock"

// SocketDirName is the subdir under XDG_RUNTIME_DIR (or $TMPDIR/r1-$UID/)
// that holds the socket. We use a dedicated subdir so chmod 0700 on
// the *parent* doesn't disturb other tools sharing the runtime dir.
const SocketDirName = "r1"

// ErrStaleButLive is returned by Listen when the socket path already
// has a live owner. Callers can `errors.Is` to distinguish "another
// daemon is already serving" from real I/O errors.
var ErrStaleButLive = errors.New("ipc: control socket has a live owner")

// Listener pairs the bound *net.UnixListener with the absolute socket
// path so callers can write the discovery file and clean up on
// shutdown.
type Listener struct {
	*net.UnixListener
	Path string
}

// Close stops the listener AND unlinks the socket file. Idempotent.
func (l *Listener) Close() error {
	if l == nil || l.UnixListener == nil {
		return nil
	}
	err := l.UnixListener.Close()
	// Best-effort unlink. UnixListener.Close already does this on
	// most Go versions; the explicit Remove tolerates the file
	// being gone.
	if rerr := os.Remove(l.Path); rerr != nil && !os.IsNotExist(rerr) {
		if err == nil {
			err = rerr
		}
	}
	return err
}

// Listen opens the per-user control socket. Returns the bound
// listener (whose Accept loop should be drained by the caller) and
// the resolved socket path.
//
// Side effects:
//
//   - Creates the parent directory at mode 0700 if missing.
//   - Chmods an existing parent directory to 0700 (the spec is
//     explicit: "parent dir 0700"; we enforce it on each Listen).
//   - Chmods the socket file to 0600 after bind.
//   - On contention: dials the socket first. Live owner → returns
//     ErrStaleButLive. ECONNREFUSED/ENOENT → unlink + bind.
func Listen() (*Listener, error) {
	dir, err := runtimeSocketDir()
	if err != nil {
		return nil, fmt.Errorf("ipc: resolve runtime dir: %w", err)
	}
	if err := ensureDir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ipc: ensure dir %s: %w", dir, err)
	}
	sockPath := filepath.Join(dir, SocketName)

	if alive, derr := isLive(sockPath); derr == nil && alive {
		return nil, fmt.Errorf("%w: %s", ErrStaleButLive, sockPath)
	}
	// Either no socket, or stale (ECONNREFUSED) — clean up before bind.
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("ipc: unlink stale %s: %w", sockPath, err)
	}

	addr := &net.UnixAddr{Net: "unix", Name: sockPath}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("ipc: bind %s: %w", sockPath, err)
	}
	// Some kernels (and most macOS umasks) widen the bound socket
	// to 0755 by default. Re-chmod after bind to enforce 0600.
	if cerr := os.Chmod(sockPath, 0o600); cerr != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil, fmt.Errorf("ipc: chmod %s: %w", sockPath, cerr)
	}
	return &Listener{UnixListener: ln, Path: sockPath}, nil
}

// runtimeSocketDir resolves the per-user runtime dir for the control
// socket. Preference order:
//
//   1. $R1_RUNTIME_DIR (test override; production code never sets it).
//   2. $XDG_RUNTIME_DIR/r1/ (Linux per-user tmpfs, lifetime = login).
//   3. $TMPDIR/r1-$UID/ (macOS, headless Linux without XDG).
//   4. /tmp/r1-$UID/ (last-resort).
func runtimeSocketDir() (string, error) {
	if override := os.Getenv("R1_RUNTIME_DIR"); override != "" {
		return filepath.Join(override, SocketDirName), nil
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, SocketDirName), nil
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	uid := strconv.Itoa(os.Getuid())
	return filepath.Join(tmp, "r1-"+uid), nil
}

// ensureDir creates dir at mode if missing, then chmods it to mode if
// already present. The chmod-on-existing branch enforces the spec's
// "parent dir 0700" requirement even when an older r1 daemon left the
// dir at a wider mode.
func ensureDir(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}

// isLive dials the socket with a short timeout. Returns true if a
// process is accepting connections. Returns false (with no error) on
// ECONNREFUSED or ENOENT — both mean "safe to unlink and rebind".
// Other errors are surfaced verbatim.
func isLive(path string) (bool, error) {
	c, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		_ = c.Close()
		return true, nil
	}
	// Map Go's wrapped errors back to errno equivalents. We
	// specifically want ECONNREFUSED ("socket file present, nobody
	// listening") and ENOENT ("file gone — never bound or already
	// cleaned").
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		if sysErr == syscall.ECONNREFUSED || sysErr == syscall.ENOENT {
			return false, nil
		}
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	// Some Go versions wrap the connection-refused as a *net.OpError
	// without exposing the syscall.Errno; string-compare as a
	// fallback so the stale-but-live check works on every platform
	// the spec targets.
	if msg := err.Error(); contains(msg, "connection refused") || contains(msg, "no such file") {
		return false, nil
	}
	return false, err
}

func contains(s, substr string) bool {
	// Local helper to avoid importing strings just for one call.
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
