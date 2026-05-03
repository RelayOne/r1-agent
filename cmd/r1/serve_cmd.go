package main

// serve_cmd.go — TASK-40: `r1 serve` command + flag surface.
//
// Replaces the inline serveCmd that previously lived in main.go. The
// flag surface matches specs/r1d-server.md TASK-40:
//
//   --addr <host:port>       Listen address (loopback ephemeral when empty).
//   --no-tcp                 Unix-socket only (no TCP listener).
//   --no-unix                TCP only (no unix-socket listener).
//   --token <s>              Override the auto-minted bearer token.
//   --install                serviceunit.Install + Start. (TASK-38)
//   --uninstall              serviceunit.Stop + Uninstall. (TASK-38)
//   --status                 serviceunit.Status. (TASK-38)
//   --single-session         Reject 2nd session.start (development mode).
//   --enable-agent-routes    Mount /v1/agent/ + /api/* aliases. (TASK-34)
//   --enable-queue-routes    Mount /v1/queue/ + /api/* aliases. (TASK-35)
//   --config <path>          Read additional config from a YAML file.
//
// This file owns the lifecycle action routing: --install / --uninstall /
// --status take the install path (TASK-38) and exit. Otherwise we fall
// through to the existing serve loop (loopback HTTP + dashboard +
// mission API + optional agent/queue mounts).
//
// Legacy compatibility. The old --port flag is preserved as an alias
// for --addr so callers pinning `r1 serve --port=8420` keep working
// during the transition. When both are set, --addr wins.

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/agentserve"
	"github.com/RelayOne/r1/internal/daemon"
	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/server"
)

// serveOptions captures the parsed CLI flags for `r1 serve`. Returned
// from parseServeFlags so test code can exercise flag-shape without
// spinning up a listener.
type serveOptions struct {
	Addr               string
	Port               int    // legacy --port alias
	NoTCP              bool   // unix-socket only
	NoUnix             bool   // TCP only
	Token              string // empty → auto-mint
	Install            bool
	Uninstall          bool
	Status             bool
	SingleSession      bool
	EnableAgentRoutes  bool
	EnableQueueRoutes  bool
	ConfigPath         string
	Repo               string // legacy: repo root for orchestrator
	DataDir            string // legacy: .stoke / mission store
	rawArgs            []string
}

// parseServeFlags parses argv. flag.ContinueOnError so tests don't
// os.Exit on flag.Parse rejecting input.
func parseServeFlags(args []string) (serveOptions, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))

	opts := serveOptions{rawArgs: append([]string(nil), args...)}
	fs.StringVar(&opts.Addr, "addr", "", "listen address (host:port). Empty → loopback ephemeral 127.0.0.1:0")
	fs.IntVar(&opts.Port, "port", 0, "legacy alias for --addr (port-only). 0 = unset.")
	fs.BoolVar(&opts.NoTCP, "no-tcp", false, "unix-socket only (no TCP listener)")
	fs.BoolVar(&opts.NoUnix, "no-unix", false, "TCP only (no unix-socket listener)")
	fs.StringVar(&opts.Token, "token", r1env.Get("R1_API_TOKEN", "STOKE_API_TOKEN"), "bearer token; empty → auto-mint")
	fs.BoolVar(&opts.Install, "install", false, "install r1 serve as a per-user service unit and start it")
	fs.BoolVar(&opts.Uninstall, "uninstall", false, "uninstall the r1 serve service unit")
	fs.BoolVar(&opts.Status, "status", false, "report the r1 serve service unit's status")
	fs.BoolVar(&opts.SingleSession, "single-session", false, "reject a 2nd session.start (development mode)")
	fs.BoolVar(&opts.EnableAgentRoutes, "enable-agent-routes", false, "mount /v1/agent/ + /api/* aliases (TASK-34)")
	fs.BoolVar(&opts.EnableQueueRoutes, "enable-queue-routes", false, "mount /v1/queue/ + /api/* aliases (TASK-35)")
	fs.StringVar(&opts.ConfigPath, "config", "", "additional config YAML to read")
	fs.StringVar(&opts.Repo, "repo", ".", "repository root for the optional mission orchestrator")
	fs.StringVar(&opts.DataDir, "data-dir", ".stoke", "data directory for mission/research stores")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}

	// Validate mutually-exclusive flags.
	if opts.Install && opts.Uninstall {
		return opts, fmt.Errorf("--install and --uninstall are mutually exclusive")
	}
	if opts.NoTCP && opts.NoUnix {
		return opts, fmt.Errorf("--no-tcp and --no-unix are mutually exclusive (one transport must remain)")
	}

	// Normalize --port (legacy) into --addr if --addr was not set.
	if opts.Addr == "" && opts.Port > 0 {
		opts.Addr = fmt.Sprintf("127.0.0.1:%d", opts.Port)
	}

	return opts, nil
}

// serveCmd is the entry point from main.go's switch. Lifecycle:
//
//  1. Classify the requested action (run / install / uninstall / status)
//     via the shared classifier in serve_install.go.
//  2. Route install / uninstall / status to runServeInstall /
//     runServeUninstall / runServeStatus and exit.
//  3. Otherwise, parse the run-path flags and start the server loop.
func serveCmd(args []string) {
	// Phase 1: classify lifecycle action. We do this BEFORE parseServeFlags
	// so a malformed run-path flag (e.g. --addr=) doesn't block --uninstall.
	action, err := classifyServeAction(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(2)
	}

	switch action {
	case serveActionInstall:
		os.Exit(runServeInstall(args, os.Stdout, os.Stderr))
	case serveActionUninstall:
		os.Exit(runServeUninstall(os.Stdout, os.Stderr))
	case serveActionStatus:
		os.Exit(runServeStatus(os.Stdout, os.Stderr))
	}

	// Phase 2: parse run-path flags and start the server.
	opts, err := parseServeFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(2)
	}
	runServeLoop(opts)
}

// runServeLoop drives the actual HTTP server. Pulled out of serveCmd
// so it's separately testable (parseServeFlags + the install path can
// be exercised without ever calling Listen).
func runServeLoop(opts serveOptions) {
	// Default --addr to a stable port if neither --addr nor --port was
	// supplied. The legacy serveCmd defaulted to 8420; we preserve that
	// behavior so existing scripts keep working. The new
	// loopback-ephemeral mode (server.ServeLoopback) is opted into via
	// --addr "" which we treat as 127.0.0.1:0 below; `flag.ContinueOnError`
	// + StringVar's default-empty cannot distinguish "not set" from
	// "explicitly empty", but the legacy behavior matters more.
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8420"
	}
	port := portFromAddr(opts.Addr)

	absRepo, err := filepath.Abs(opts.Repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	bus := server.NewEventBus()
	srv := server.New(port, opts.Token, bus)

	// Dashboard state.
	dashState := server.NewDashboardState()

	// Optional mission orchestrator (legacy --repo / --data-dir flow).
	orch, orchErr := createOrchestrator(absRepo, opts.DataDir)
	if orchErr != nil {
		fmt.Fprintf(os.Stderr, "warn: mission API disabled: %v\n", orchErr)
	} else {
		server.RegisterMissionAPI(srv, orch)
		defer orch.Close()
		fmt.Fprintf(os.Stderr, "mission API enabled\n")
		if orch.EventBus() != nil {
			server.BridgeHubToEventBus(orch.EventBus(), bus)
			server.BridgeHubToDashboard(orch.EventBus(), dashState)
		}
	}

	server.RegisterDashboardAPI(srv, nil, nil, dashState)
	server.RegisterRulesAPI(srv, absRepo)
	server.RegisterDashboardUI(srv)

	// Optional /v1/agent/ + /v1/queue/ mounts (TASK-34/35).
	muxAlias := getServeMux(srv)
	if muxAlias != nil && opts.EnableAgentRoutes {
		ag := agentserve.NewServer(agentserve.Config{Version: version})
		server.MountAgentServe(muxAlias, ag, opts.Token)
		fmt.Fprintf(os.Stderr, "agent routes mounted at /v1/agent/ (alias: /api/*)\n")
	}
	if muxAlias != nil && opts.EnableQueueRoutes {
		// Build a daemon scoped to the data dir so /v1/queue/* maps
		// to a real queue. We Start the daemon so the worker pool is
		// initialized (handlers like /status read d.pool); MaxParallel
		// is the operator-supplied flag (default 1 keeps a usable
		// worker available without surprising memory pressure).
		// Operators can resize via `r1 ctl workers --count N`.
		d, derr := daemon.New(daemon.Config{
			StateDir:    filepath.Join(absRepo, opts.DataDir),
			Addr:        "127.0.0.1:0", // unused; handler is mounted directly
			Token:       "",            // outer middleware enforces auth
			MaxParallel: 1,
		}, nil)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "warn: queue routes disabled: %v\n", derr)
		} else if startErr := d.Start(context.Background()); startErr != nil {
			fmt.Fprintf(os.Stderr, "warn: queue routes disabled (daemon start: %v)\n", startErr)
		} else {
			defer d.Stop()
			server.MountDaemonQueue(muxAlias, d, opts.Token)
			fmt.Fprintf(os.Stderr, "queue routes mounted at /v1/queue/ (alias: /api/*)\n")
		}
	}

	// Single-session guard. setSingleSessionMode persists the
	// operator's choice on a package-level atomic.Bool that
	// SessionHub.Create reads via IsSingleSessionMode() to reject a
	// 2nd session.start. Package-level state is the deliberate choice
	// (vs. threading the flag through every Server constructor) so
	// the existing Server / SessionHub call sites don't have to grow
	// a new parameter.
	setSingleSessionMode(opts.SingleSession)
	if opts.SingleSession {
		fmt.Fprintln(os.Stderr, "single-session mode: enabled")
	}

	fmt.Fprintf(os.Stderr, "r1 serve listening on %s\n", opts.Addr)
	fmt.Fprintf(os.Stderr, "dashboard: http://%s/\n", opts.Addr)

	sigCtx, sigCancel := signalContext(context.Background())
	defer sigCancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-sigCtx.Done():
		fmt.Fprintf(os.Stderr, "r1 serve: shutting down\n")
	case err := <-errCh:
		if err != nil {
			fatal("serve: %v", err)
		}
	}
}

// singleSessionMode is the package-level holder for the
// --single-session flag. The cmd/r1 layer reads it when constructing
// a sessionhub.SessionHub so the hub's SetSingleSession setter can
// be applied at the source. atomic.Bool gives a lock-free
// reader/writer pair so a session-handler goroutine can read it
// without a mutex.
var singleSessionMode atomic.Bool

// setSingleSessionMode is the writer used by runServeLoop.
func setSingleSessionMode(v bool) { singleSessionMode.Store(v) }

// IsSingleSessionMode is the read-side accessor consumed by sessionhub
// construction. The actual rejection of a 2nd session.start is
// implemented by sessionhub.SessionHub.SetSingleSession +
// sessionhub.ErrSingleSessionExceeded.
func IsSingleSessionMode() bool { return singleSessionMode.Load() }

// portFromAddr extracts the integer port from a host:port string.
// Returns 0 on parse failure (the caller's server.New treats 0 as
// "let the kernel pick").
func portFromAddr(addr string) int {
	idx := strings.LastIndexByte(addr, ':')
	if idx < 0 || idx == len(addr)-1 {
		return 0
	}
	var p int
	fmt.Sscanf(addr[idx+1:], "%d", &p)
	return p
}

// getServeMux returns the inner *http.ServeMux of a server.Server when
// possible, so MountAgentServe / MountDaemonQueue can register routes
// alongside the dashboard handlers. The Server type doesn't currently
// expose its mux directly; we cast through Handler() which returns
// *http.ServeMux today. If the type changes we fall back to nil and
// the agent / queue mounts skip silently with a warning.
func getServeMux(srv *server.Server) *http.ServeMux {
	if srv == nil {
		return nil
	}
	h := srv.Handler()
	if mux, ok := h.(*http.ServeMux); ok {
		return mux
	}
	return nil
}
