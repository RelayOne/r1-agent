//go:build windows
// +build windows

package ipc

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
)

// PipePrefix is the canonical Windows named-pipe prefix.
const PipePrefix = `\\.\pipe\`

// pipeName builds the per-user pipe name `\\.\pipe\r1-<USERNAME>`.
// USERNAME is sanitized: backslashes (DOMAIN\user) become hyphens
// because backslash is reserved inside a pipe name. Empty USERNAME
// (some service contexts) falls back to the SID-derived "r1-svc".
func pipeName() string {
	user := os.Getenv("USERNAME")
	if user == "" {
		// LocalSystem and other service contexts may not have
		// USERNAME set. Fall through to a stable, single-instance
		// fallback; the daemonlock + named-pipe SECURITY_ATTRIBUTES
		// (current SID) still bounds visibility to the running
		// account.
		user = "svc"
	}
	user = strings.ReplaceAll(user, `\`, "-")
	user = strings.ReplaceAll(user, "/", "-")
	return PipePrefix + "r1-" + user
}

// Listen opens the per-user named pipe with a SECURITY_ATTRIBUTES
// SDDL string granting full access only to the current user's SID
// and to LocalSystem (so a service-installed daemon can still admin
// itself). Other principals (including Administrators on a different
// account) are denied — the pipe is per-user, not per-machine.
//
// The SDDL fragment used is:
//
//	D:P(A;;GA;;;OW)(A;;GA;;;SY)
//
// where:
//
//   - D:P     — DACL, protected (no inheritance from parent).
//   - (A;;GA;;;OW) — Allow Generic-All to OW (the current owner).
//   - (A;;GA;;;SY) — Allow Generic-All to SY (LocalSystem).
//
// PipeConfig zero-initializes everything else; we explicitly set
// MessageMode=false (byte stream) to match the JSON-RPC framing the
// dispatcher expects (length-prefixed, not message-mode).
func Listen() (*Listener, error) {
	name := pipeName()
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;OW)(A;;GA;;;SY)",
		MessageMode:        false,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	}
	ln, err := winio.ListenPipe(name, cfg)
	if err != nil {
		return nil, fmt.Errorf("ipc: ListenPipe %s: %w", name, err)
	}
	return &Listener{Listener: ln, Path: name}, nil
}

// Listener wraps the bound pipe listener. Path holds the pipe name
// (e.g. `\\.\pipe\r1-alice`) so the daemon can write it into the
// discovery file for `r1 ctl` to dial.
type Listener struct {
	net.Listener
	Path string
}

// Close stops the listener. Pipes are torn down by the OS as soon as
// the last reference goes away, so there is no equivalent of
// "unlink" on Windows.
func (l *Listener) Close() error {
	if l == nil || l.Listener == nil {
		return nil
	}
	return l.Listener.Close()
}

// ErrStaleButLive — kept for cross-platform parity with listen_unix.go.
// On Windows, ListenPipe returns ERROR_PIPE_BUSY when another process
// holds the same pipe name; we don't model that as a separate
// sentinel because callers should treat any ListenPipe failure as
// fatal. Defined here so cross-package consumers can `errors.Is`
// without a build-tag dance.
var ErrStaleButLive = fmt.Errorf("ipc: control pipe has a live owner")
