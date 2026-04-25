package main

// ctl_bootstrap.go — CDC-15: wire sessionctl.StartServer into stoke's
// long-lived entry points (run/ship/chat) so every running session is
// discoverable through the existing CDC-13/14 CLI (stoke status,
// approve, override, budget, pause, resume, inject, takeover).
//
// startSessionCtlServer is the single helper each entry point calls
// near the top, paired with `defer srv.Close()`. Failure to bind the
// socket is non-fatal: we print a one-line warning to stderr and
// continue, because losing operator-control should never crash the
// agent.
//
// Wiring is intentionally minimal: Router/Signaler/PGID/Status/Takeover
// today; InjectTask/BudgetAdd/Emit are stubs that handlers report as
// "unavailable" via the graceful fallback in handlers.go. Full wiring
// of those callbacks lands in CDC-16/17 alongside the scheduler and
// bus integration.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"github.com/RelayOne/r1-agent/internal/r1env"
	"github.com/RelayOne/r1-agent/internal/sessionctl"
)

// startSessionCtlServer is called at the top of every long-lived entry
// point (run/ship/chat). Returns the server + session ID; the caller
// MUST defer srv.Close() so the socket is unlinked on clean exit.
//
// Failure to start is non-fatal — we emit a warning to stderr and
// return (nil, sessID). Callers must nil-check srv before deferring
// Close. The session ID is always returned so downstream code can use
// it for correlation even when the socket couldn't bind.
//
// repoRoot, when non-empty, enables the takeover handler by
// constructing a TakeoverManager. Pass "" to disable takeover for
// shorter-lived entry points (we currently always have a repo root,
// but the parameter keeps the helper honest).
func startSessionCtlServer(mode string, repoRoot string) (*sessionctl.Server, string) {
	sessID := newSessionID(mode)
	deps := sessionctl.Deps{
		SessionID: sessID,
		Router:    sessionctl.NewApprovalRouter(),
		Signaler:  sessionctl.NewPGIDSignaler(),
		PGID:      syscall.Getpgrp(),
		Status: func() sessionctl.StatusSnapshot {
			return sessionctl.StatusSnapshot{
				State: "executing",
				Mode:  mode,
			}
		},
		Emit: func(kind string, payload any) string {
			// CDC-16/17 will wire this to the bus/eventlog. Until
			// then return "" so handlers report no event id but
			// still complete (handlers.go uses the emit() helper
			// which tolerates empty returns).
			return ""
		},
	}
	if repoRoot != "" {
		deps.Takeover = sessionctl.NewTakeoverManager(
			sessID, deps.PGID, deps.Signaler, deps.Emit, repoRoot,
		)
	}

	ctlDir := r1env.Get("R1_CTL_DIR", "STOKE_CTL_DIR")
	if ctlDir == "" {
		ctlDir = "/tmp"
	}

	srv, err := sessionctl.StartServer(sessionctl.Opts{
		SocketDir: ctlDir,
		SessionID: sessID,
		Handlers:  sessionctl.DefaultHandlers(deps),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"sessionctl: not listening (%v); stoke status/approve/etc. unavailable for this session\n",
			err)
		return nil, sessID
	}
	fmt.Fprintf(os.Stderr,
		"sessionctl: listening on %s/stoke-%s.sock\n",
		ctlDir, sessID)
	return srv, sessID
}

// newSessionID returns a "<mode>-<12hex>" identifier. crypto/rand
// failure is treated as fatal since the alternative (collisions in
// /tmp) leaks across sessions; we panic with a clear message rather
// than fall back to a weaker source.
func newSessionID(mode string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand on Linux/macOS is backed by getrandom/getentropy
		// and effectively cannot fail; if it does the system is in a
		// state where we shouldn't be running anyway.
		panic("sessionctl: crypto/rand: " + err.Error())
	}
	return mode + "-" + hex.EncodeToString(b[:])
}
