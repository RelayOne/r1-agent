package main

// serve_ipc.go — unix-domain control transport for `r1 serve`
// (specs/r1d-server.md Phase C items 14-16, wired by audit A031).
//
// runServeLoop binds the per-user control socket via ipc.Listen()
// unless --no-unix is set, and serves the SAME mux as the TCP
// listener on it. Auth on this transport is the kernel peer-cred
// check (SO_PEERCRED on Linux, LOCAL_PEERCRED on macOS, pipe SDDL on
// Windows) enforced at accept time by peerCredListener — no bearer
// token, matching `r1 ctl`'s pickClientAndURL which sends no
// Authorization header on the unix path (ctl_daemon_cmd.go).

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/RelayOne/r1/internal/server/ipc"
)

// bindUnixControl binds the per-user control socket unless the
// operator passed --no-unix. Returns (nil, nil) when the transport is
// disabled by flag so the caller can distinguish "off" from "failed".
func bindUnixControl(noUnix bool) (*ipc.Listener, error) {
	if noUnix {
		return nil, nil
	}
	return ipc.Listen()
}

// peerCredListener wraps the ipc control listener and enforces the
// kernel peer-credential check on every accepted connection: a
// same-UID peer passes, anything else is closed and the accept loop
// continues. This is the unix transport's entire auth boundary —
// the kernel-reported peer identity is non-spoofable, so no bearer
// token is required on this path.
type peerCredListener struct {
	net.Listener
}

// Accept filters accepted conns through ipc.CheckPeerCred.
func (l *peerCredListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if cerr := ipc.CheckPeerCred(conn); cerr != nil {
			fmt.Fprintf(os.Stderr, "serve: unix control conn rejected: %v\n", cerr)
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

// unixControlHandler adapts the shared serve mux for the peer-cred
// transport: the accepted connection was already authenticated by
// CheckPeerCred, so the bearer header the TCP-side authWrap demands
// is injected here rather than required from the client.
func unixControlHandler(h http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		h.ServeHTTP(w, r)
	})
}

// newUnixControlServer builds the http.Server fronting the unix
// control socket. Timeouts mirror server.Server.ListenAndServe so
// both transports share the same slow-client budget.
func newUnixControlServer(h http.Handler, token string) *http.Server {
	return &http.Server{
		Handler:           unixControlHandler(h, token),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
