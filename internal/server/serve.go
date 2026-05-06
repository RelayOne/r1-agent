package server

// Loopback HTTP+WS listener for the `r1 serve` per-user daemon.
// Implements specs/r1d-server.md Phase C, item 17: bind
// 127.0.0.1:0, capture the resolved ephemeral port, and write it
// (with the bearer token) to ~/.r1/daemon.json so clients can find
// the daemon.
//
// This file is intentionally additive: it does not touch the
// existing Server type or `internal/server/server.go`'s SSE wiring.
// The new ServeLoopback helper is the entry point a future
// `cmd/r1/serve_cmd.go` will call. Existing callers keep working
// unchanged.
//
// # Why 127.0.0.1:0
//
// Binding to port 0 lets the kernel pick an unused ephemeral port,
// avoiding port-collision races with other users' r1 daemons (or
// with anything else on the box). The resolved port is captured
// from `net.Listener.Addr()` AFTER Listen returns, then committed
// to the discovery file under mode 0600. Clients dial loopback
// using the port from the file; the discovery file therefore IS
// the rendezvous point.
//
// # Discovery file lifecycle
//
// ServeLoopback writes daemon.json on startup (success path only —
// a failed Listen returns before any write). On graceful shutdown,
// daemon.json is left in place so a restart can read its prior
// PID + port for diagnostics; the next ServeLoopback overwrites it
// atomically (TASK-12 contract). Crash leftovers are also fine —
// daemonlock's contention message tolerates them.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/daemondisco"
)

// LoopbackHost is the address ServeLoopback binds to. Always
// loopback — the daemon must NOT listen on a public interface
// (browser-extension exfiltration, neighbour-network attack).
const LoopbackHost = "127.0.0.1"

// LoopbackListener pairs the bound *net.TCPListener with the
// resolved port and the absolute path of the discovery file the
// daemon wrote. Callers serve HTTP off Listener and call Close on
// shutdown.
type LoopbackListener struct {
	Listener      *net.TCPListener
	Port          int
	Token         string
	DiscoveryPath string
	SockPath      string
	Version       string
	PID           int
}

// Addr returns the loopback "host:port" string the listener
// resolved to (e.g. "127.0.0.1:50321"). Useful for logging and for
// the `Host` allow-list (TASK-19).
func (l *LoopbackListener) Addr() string {
	if l == nil || l.Listener == nil {
		return ""
	}
	return l.Listener.Addr().String()
}

// Close stops the listener AND removes the discovery file. The
// removal is best-effort; an error there is logged but doesn't
// fail Close because the listener-close half is the load-bearing
// step.
func (l *LoopbackListener) Close() error {
	if l == nil {
		return nil
	}
	var err error
	if l.Listener != nil {
		err = l.Listener.Close()
	}
	if l.DiscoveryPath != "" {
		_ = os.Remove(l.DiscoveryPath)
	}
	return err
}

// LoopbackOptions tune ServeLoopback.
type LoopbackOptions struct {
	// SockPath is the absolute control-socket path the unix-domain
	// listener bound to (TASK-14). Recorded in the discovery file
	// so `r1 ctl` can reach the daemon over the unix path. Empty
	// means "no unix listener active" (TCP-only mode).
	SockPath string

	// Token, if non-empty, overrides the auto-minted bearer. Used
	// by tests so they can predict the token without parsing
	// daemon.json. Production always leaves this empty so
	// MintToken runs once per process.
	Token string

	// Version is recorded in the discovery file for client
	// version-skew checks. Empty defaults to "r1-dev".
	Version string

	// PID is recorded in the discovery file. Defaults to
	// os.Getpid() when zero.
	PID int

	// HomeDir lets tests inject an alternate ~/.r1/ location.
	// Empty falls through to the default $R1_HOME / ~/.r1
	// resolution implemented by the daemondisco package.
	HomeDir string
}

// ServeLoopback opens a loopback TCP listener on 127.0.0.1:0,
// captures the resolved port, mints (or accepts) a bearer token,
// and writes the discovery file. Returns the LoopbackListener; the
// caller is expected to drive `http.Serve(ln.Listener, handler)`
// on it.
//
// Errors:
//
//   - "ipc: bind 127.0.0.1:0: ..." — kernel-level Listen failure
//     (extremely rare; usually means the loopback interface is
//     misconfigured).
//   - "daemondisco: ..." — discovery file write failure.
func ServeLoopback(opts LoopbackOptions) (*LoopbackListener, error) {
	addr := &net.TCPAddr{IP: net.ParseIP(LoopbackHost), Port: 0}
	ln, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("server: bind %s:0: %w", LoopbackHost, err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, errors.New("server: ListenTCP did not return *net.TCPAddr")
	}
	port := tcpAddr.Port

	tok := opts.Token
	if tok == "" {
		minted, terr := daemondisco.MintToken()
		if terr != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("server: mint token: %w", terr)
		}
		tok = minted
	}

	pid := opts.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	ver := opts.Version
	if ver == "" {
		ver = "r1-dev"
	}

	var discPath string
	if opts.HomeDir != "" {
		discPath, err = daemondisco.WriteDiscoveryTo(opts.HomeDir, pid, opts.SockPath, port, tok, ver)
	} else {
		discPath, err = daemondisco.WriteDiscovery(pid, opts.SockPath, port, tok, ver)
	}
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("server: write discovery: %w", err)
	}

	return &LoopbackListener{
		Listener:      ln,
		Port:          port,
		Token:         tok,
		DiscoveryPath: discPath,
		SockPath:      opts.SockPath,
		Version:       ver,
		PID:           pid,
	}, nil
}

// ServeHTTP runs http.Server.Serve on the listener, gracefully
// shutting down when ctx is cancelled. Returns the first error from
// Serve other than http.ErrServerClosed. Helper used by tests; the
// production daemon may want to assemble its own *http.Server with
// custom timeouts.
func (l *LoopbackListener) ServeHTTP(ctx context.Context, handler http.Handler) error {
	if l == nil || l.Listener == nil {
		return errors.New("server: nil LoopbackListener")
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	var serveErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Serve(l.Listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	wg.Wait()
	return serveErr
}
