// Package daemondisco implements the per-user daemon discovery file
// at `~/.r1/daemon.json` and the auth token vault used by the
// `r1 serve` daemon (specs/r1d-server.md Phase B, items 12 + 13).
//
// On startup, `r1 serve` writes a JSON record describing how to reach
// it: pid, control-socket path, loopback HTTP/WS port, bearer token,
// and version string. Every CLI client (`r1 ctl`, `r1 chat`, the
// desktop UI) reads this file to find the running daemon. The file is
// authoritative — losing it during a crash means clients can't reach
// the daemon even though it's running, so we treat the write as a
// startup-critical step (see TASK-17 for the server-side wiring that
// invokes WriteDiscovery after `127.0.0.1:0` resolves to a port).
//
// # Security model
//
// The discovery file contains a 32-byte bearer token that grants full
// daemon control to anyone who can read it. We therefore:
//
//   - Write atomically (tmp + rename) so partial reads never leak a
//     half-written token.
//   - Set mode 0600 on the final file (owner-only).
//   - **REFUSE** to read the file if its mode is wider than 0600
//     (fail-closed: a world-readable token is treated as compromised
//     and the reader returns an error rather than silently accepting
//     it). The spec calls this out as a hard requirement.
//
// On Windows, file-mode bits don't map cleanly to ACLs; we still write
// 0600 and read it back tolerantly. The Windows control surface is
// the named-pipe ACL (TASK-15), which restricts the pipe to the
// owning SID — the discovery file is a fallback for HTTP clients on
// loopback.
//
// # Atomic write
//
// We write to `daemon.json.tmp` in the same directory, fsync, then
// `os.Rename` it onto `daemon.json`. Same-directory rename is atomic
// on every supported OS.
package daemondisco

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the basename of the discovery file under ~/.r1/.
// Exported so callers (e.g. daemonlock) can reference the same name
// without drift.
const FileName = "daemon.json"

// MaxAllowedMode is the strictest mode bits accepted by ReadDiscovery.
// Anything wider (e.g. group-readable 0640, world-readable 0644)
// causes ReadDiscovery to return ErrUnsafeMode — the file's bearer
// token is treated as compromised.
const MaxAllowedMode os.FileMode = 0o600

// ErrUnsafeMode is returned by ReadDiscovery when the discovery file
// is more permissive than 0600. Callers can `errors.Is` against it to
// distinguish a fail-closed mode rejection from real I/O errors.
var ErrUnsafeMode = errors.New("daemondisco: discovery file mode too permissive")

// Discovery is the on-disk schema of `~/.r1/daemon.json`. Field tags
// are the wire contract — adding fields is safe; renaming or removing
// them is not (it would silently break running clients).
type Discovery struct {
	// PID is the daemon's process id. Used by `r1 ctl` to attach
	// debuggers and by daemonlock to print the contention message.
	PID int `json:"pid"`

	// SockPath is the absolute path of the unix-domain control
	// socket on POSIX, or the named-pipe path
	// (`\\.\pipe\r1-<USER>`) on Windows.
	SockPath string `json:"sock_path"`

	// Port is the loopback HTTP/WS port (random ephemeral, captured
	// from `127.0.0.1:0` after Listen returns).
	Port int `json:"port"`

	// Token is the 32-byte hex-encoded bearer token (see
	// MintToken). Required on all HTTP/WS requests.
	Token string `json:"token"`

	// Version is the daemon's release string (e.g. `r1-v1.4.2`).
	// Clients can refuse to talk to a too-old daemon.
	Version string `json:"version"`
}

// WriteDiscovery writes the per-user discovery file under `~/.r1/`
// atomically with mode 0600. The home dir is auto-resolved (or
// overridden by R1_HOME for tests).
//
// Returns the absolute path written on success.
func WriteDiscovery(pid int, sockPath string, port int, token, version string) (string, error) {
	dir, err := r1Dir()
	if err != nil {
		return "", fmt.Errorf("daemondisco: resolve home: %w", err)
	}
	return WriteDiscoveryTo(dir, pid, sockPath, port, token, version)
}

// WriteDiscoveryTo writes the discovery file under an explicit
// directory. Callers should normally use WriteDiscovery; this entry
// point exists so tests (and the daemonlock contention reader) can
// point at a sandbox without setting R1_HOME globally.
func WriteDiscoveryTo(dir string, pid int, sockPath string, port int, token, version string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("daemondisco: mkdir %s: %w", dir, err)
	}
	d := Discovery{
		PID:      pid,
		SockPath: sockPath,
		Port:     port,
		Token:    token,
		Version:  version,
	}
	body, err := json.MarshalIndent(&d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("daemondisco: marshal: %w", err)
	}

	final := filepath.Join(dir, FileName)
	tmp := final + ".tmp"

	// Create-or-truncate, OWNER-ONLY (0600). Note: O_EXCL would race
	// against a leftover .tmp from a prior crash; we accept the
	// truncate.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("daemondisco: open %s: %w", tmp, err)
	}
	if _, werr := f.Write(body); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("daemondisco: write %s: %w", tmp, werr)
	}
	// fsync the data before rename so a crash between rename and the
	// next reader doesn't surface a zero-length file.
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("daemondisco: sync %s: %w", tmp, serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("daemondisco: close %s: %w", tmp, cerr)
	}
	// Some umasks widen 0600 to 0644 silently on the OpenFile path
	// (rare, but seen on misconfigured shared hosts). Re-chmod
	// explicitly to enforce the contract.
	if cerr := os.Chmod(tmp, 0o600); cerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("daemondisco: chmod %s: %w", tmp, cerr)
	}
	if rerr := os.Rename(tmp, final); rerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("daemondisco: rename %s -> %s: %w", tmp, final, rerr)
	}
	return final, nil
}

// ReadDiscovery reads `~/.r1/daemon.json` and returns its parsed
// contents. Refuses to read a world- or group-readable file
// (fail-closed against token leakage).
func ReadDiscovery() (*Discovery, error) {
	dir, err := r1Dir()
	if err != nil {
		return nil, fmt.Errorf("daemondisco: resolve home: %w", err)
	}
	return ReadDiscoveryFrom(dir)
}

// ReadDiscoveryFrom reads the discovery file under an explicit
// directory. See ReadDiscovery for semantics.
func ReadDiscoveryFrom(dir string) (*Discovery, error) {
	path := filepath.Join(dir, FileName)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// Mode check — fail-closed on widened modes. Note: on Windows,
	// info.Mode().Perm() is synthesized from the ACL and is often
	// 0666; we relax the check there because the named-pipe ACL is
	// the real boundary on that platform.
	if !isWindows() {
		if mode := info.Mode().Perm(); mode&^MaxAllowedMode != 0 {
			return nil, fmt.Errorf("%w: path=%s mode=%v want<=%v", ErrUnsafeMode, path, mode, MaxAllowedMode)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Discovery
	if uerr := json.Unmarshal(body, &d); uerr != nil {
		return nil, fmt.Errorf("daemondisco: parse %s: %w", path, uerr)
	}
	return &d, nil
}

// r1Dir resolves `~/.r1/` (or the test override under $R1_HOME).
// Mirrors the helper in the daemonlock package; duplicated here
// rather than imported to avoid an import cycle (daemonlock reads
// daemon.json on contention).
func r1Dir() (string, error) {
	if home := os.Getenv("R1_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".r1"), nil
}

// isWindows reports whether we're running on Windows. Defined as a
// helper rather than importing runtime so the cost is zero on POSIX
// (the linker dead-strips the constant comparison). See TASK-15 for
// the named-pipe-based control plane that supersedes file-mode
// checks on that platform.
func isWindows() bool {
	return os.PathSeparator == '\\' && os.PathListSeparator == ';'
}
