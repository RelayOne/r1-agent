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
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/agentserve"
	"github.com/RelayOne/r1/internal/daemon"
	"github.com/RelayOne/r1/internal/daemondisco"
	"github.com/RelayOne/r1/internal/daemonlock"
	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/server"
	"github.com/RelayOne/r1/internal/server/jsonrpc"
	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/server/sse"
	"github.com/RelayOne/r1/internal/server/ws"
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
	// Phase 0: single-instance enforcement (TASK-50). Acquire the
	// per-user `~/.r1/daemon.lock` BEFORE binding any listener so a
	// second `r1 serve` exits non-zero with a discoverable
	// "already running" message and does NOT race the first instance
	// to bind a port. The lock auto-releases on process exit; we keep
	// a reference in dlock so deferred Release fires on graceful
	// shutdown.
	dlock, lockErr := daemonlock.Acquire()
	if lockErr != nil {
		// On contention, daemonlock returns ErrAlreadyRunning wrapping
		// a multi-line message ("daemon already running, pid=N,
		// sock=...\nuse 'r1 ctl' to talk to it."). Print the message
		// and exit 1 — non-zero per spec item 50.
		if errors.Is(lockErr, daemonlock.ErrAlreadyRunning) {
			fmt.Fprintln(os.Stderr, lockErr.Error())
			os.Exit(1)
		}
		// Other errors (mkdir failure, flock IO error) are also
		// fatal; print and exit 1 with the wrapped message.
		fmt.Fprintf(os.Stderr, "serve: %v\n", lockErr)
		os.Exit(1)
	}
	defer func() { _ = dlock.Release() }()

	// C3: build the per-tool throttle gate. The gate is constructed
	// once at startup; daemon.reload_config swaps its policy via the
	// reload callback wired below. Failure to parse the operator's
	// config path is non-fatal at this stage — we install the
	// bundled defaults and surface the error to the operator at the
	// first reload attempt.
	serveThrottler = newServeThrottler(opts.ConfigPath)
	defer serveThrottler.Stop()

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
	muxAlias := srv.Mux()
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

	// shutdownReqCh carries daemon.shutdown RPC signals into the
	// serve loop's select below. Buffered=1 so a flood of concurrent
	// shutdowns coalesces into one wakeup. The handler's callback
	// (installed below alongside RegisterDaemonAPI) does a
	// non-blocking send to drop duplicates.
	shutdownReqCh := make(chan int, 1)

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

	// JSON-RPC over WebSocket: construct the SessionHub-backed handler,
	// wire it onto a fresh Dispatcher, and mount the WS endpoint at
	// /v1/rpc. This is the surface external clients (r1 ctl, the
	// desktop app, headless integrations) call when they invoke
	// session.start / session.cancel / etc.
	//
	// The hub is per-daemon: a single SessionHub registers every live
	// session so the WS handler can route session-id-bearing JSON-RPC
	// frames to the right Session via HubHandler.lookupSession.
	//
	// We tolerate hub-construction errors by logging the failure and
	// leaving the JSON-RPC endpoint unmounted — the rest of the daemon
	// (dashboard, mission API, optional agent/queue routes) keeps
	// working. A missing JSON-RPC endpoint is a degraded-but-running
	// mode rather than a fatal one because existing scripts may still
	// rely on the dashboard alone.
	if muxAlias != nil {
		hub, hubErr := sessionhub.NewHub()
		if hubErr != nil {
			fmt.Fprintf(os.Stderr, "warn: JSON-RPC endpoint disabled (hub init: %v)\n", hubErr)
		} else {
			hub.SetSingleSession(opts.SingleSession)
			rpcHandler := jsonrpc.NewHubHandler(hub)
			rpcHandler.PID = os.Getpid()
			rpcHandler.Version = version
			rpcHandler.StartedAt = time.Now()
			rpcHandler.HTTPPort = port

			disp := jsonrpc.NewDispatcher()
			jsonrpc.RegisterDaemonAPI(disp, rpcHandler)

			// Wire the WS bridge so session.subscribe / unsubscribe
			// can pull the live conn out of the dispatch ctx.
			// *ws.Conn satisfies jsonrpc.SubscriptionRegistry
			// structurally via its Register/Unregister methods.
			jsonrpc.ConnFromContextFunc = func(ctx context.Context) jsonrpc.SubscriptionRegistry {
				if c := ws.ConnFromContext(ctx); c != nil {
					return c
				}
				return nil
			}

			// Wire daemon.shutdown to the serve loop's shutdown channel.
			// The channel is buffered=1 + non-blocking send so a flood of
			// concurrent shutdown RPCs coalesces into one signal; the
			// serve loop reads it and unwinds.
			//
			// daemonShutdownAckDelay (package const) gives the WS layer a
			// brief window to flush the daemon.shutdown response back to
			// the caller before the listener tears down. Without this
			// delay the handler can return success, the serve loop can
			// fire on shutdownReqCh, and the listener can close before the
			// dispatcher's conn.WriteResponse completes — clients then see
			// a closed connection rather than the success envelope. Found
			// by codex review of commit 671ed37c (P1). The plain-HTTP
			// /v1/daemon/shutdown route (O1) reuses the same delay.
			rpcHandler.SetShutdownFunc(func(graceSeconds int) {
				time.Sleep(daemonShutdownAckDelay)
				select {
				case shutdownReqCh <- graceSeconds:
				default:
					// channel already has a pending signal; drop.
				}
			})

			// Wire daemon.reload_config to the throttle policy reloader
			// (C3 T14). The callback reads the operator-supplied path
			// (or auto-discovers a default) and applies the new
			// throttling block to the live limiter. Buckets keep their
			// current token count across reload; only Limit / Burst are
			// swapped. Validation errors keep the OLD policy in place
			// and surface to the caller via the RPC error envelope.
			rpcHandler.SetReloadConfigFunc(makeThrottleReloader(serveThrottler))

			wsHandler := &ws.Handler{Dispatcher: disp, Token: opts.Token}
			muxAlias.Handle("/v1/rpc", wsHandler)
			fmt.Fprintf(os.Stderr, "JSON-RPC over WS mounted at /v1/rpc\n")

			// Plain-HTTP daemon control plane (O1): the real
			// daemon.shutdown signal used to be reachable only over the
			// /v1/rpc WebSocket, which no CLI client dials, and the
			// queue-mux control verbs are gated behind
			// --enable-queue-routes. Mount POST /v1/daemon/shutdown and
			// GET /v1/daemon/info on the shared mux so `r1 ctl
			// shutdown`/`info` reach a real transport against a default
			// `r1 serve`. shutdown reuses the SAME shutdownReqCh
			// non-blocking send as the daemon.shutdown RPC callback above.
			mountDaemonControlRoutes(muxAlias, rpcHandler.DaemonInfo, shutdownReqCh, opts.Token)
			fmt.Fprintf(os.Stderr, "daemon control mounted at POST /v1/daemon/shutdown + GET /v1/daemon/info\n")

			// Session event journals + subscribe replay (audit A069).
			// Sessions created via session.start get a journal-first
			// event pipe; subscribers (WS $/event, SSE, web bridge)
			// replay from `~/.r1/journals/<id>.jsonl` then go live.
			if jd, jerr := serveJournalDir(); jerr != nil {
				fmt.Fprintf(os.Stderr, "warn: session journals disabled: %v\n", jerr)
			} else {
				hub.SetJournalDir(jd)
				rpcHandler.SetJournalPathFn(func(sessionID string) string {
					return filepath.Join(jd, sessionID+".jsonl")
				})
			}

			// Web chat surface (audit A008): POST /auth/ws-ticket
			// mints short-lived WS tokens (bearer-authenticated), and
			// /ws serves the typed-frame bridge the embedded SPA
			// dials (web/src/lib/api/r1d.ts default wsUrl).
			tickets := server.NewWSTicketStore(server.DefaultWSTicketTTL)
			var ticketHandler http.Handler = tickets.Handler()
			if opts.Token != "" {
				ticketHandler = server.RequireBearer(opts.Token)(ticketHandler)
			}
			muxAlias.Handle("/auth/ws-ticket", ticketHandler)

			daemonToken := opts.Token
			webWS := &ws.WebHandler{
				API: rpcHandler,
				ValidateToken: func(tok string) bool {
					if daemonToken == "" {
						return true // dev mode: no bearer configured
					}
					if len(tok) == len(daemonToken) &&
						subtle.ConstantTimeCompare([]byte(tok), []byte(daemonToken)) == 1 {
						return true
					}
					return tickets.Validate(tok)
				},
			}
			muxAlias.Handle("/ws", webWS)
			fmt.Fprintf(os.Stderr, "web chat WS mounted at /ws (+ POST /auth/ws-ticket)\n")

			// Read-only SSE bridge (audit A069; specs/r1d-server.md
			// Phase E item 33). AttachAuthFromQuery runs BEFORE
			// requireBearer so browser EventSource clients can pass
			// ?token= (the middleware split in auth_middleware.go was
			// designed for exactly this).
			muxAlias.Handle("GET /v1/sessions/{id}/sse", buildSessionSSEHandler(rpcHandler, opts.Token))
			fmt.Fprintf(os.Stderr, "session SSE mounted at /v1/sessions/{id}/sse\n")

			// Lane control over the lanes WS (audit A040): route
			// r1.lanes.list/kill/pin (+ killAll as iterated kill) by
			// delegating to the MCP lane tool semantics against each
			// session's cortex workspace, resolved at call time.
			srv.WithLanes(&server.LanesWiring{Tools: newHubLanesTools(hub)})
		}
	}

	// Phase C (specs/r1d-server.md items 14-16, audit A031): bind the
	// per-user unix control socket unless --no-unix. It serves the
	// SAME mux as the TCP listener; auth on this transport is the
	// kernel peer-cred check (peerCredListener in serve_ipc.go)
	// instead of the bearer token. A bind failure is non-fatal when
	// TCP remains available, but fatal under --no-tcp — the daemon
	// would otherwise serve nothing.
	unixLn, ipcErr := bindUnixControl(opts.NoUnix)
	if ipcErr != nil {
		if opts.NoTCP {
			fatal("serve: --no-tcp requires the unix control socket, which failed to bind: %v", ipcErr)
		}
		fmt.Fprintf(os.Stderr, "warn: unix control socket not bound: %v\n", ipcErr)
	}
	sockPath := ""
	if unixLn != nil {
		sockPath = unixLn.Path
		defer unixLn.Close()
	}

	if opts.NoTCP {
		fmt.Fprintf(os.Stderr, "r1 serve listening on unix socket only (--no-tcp)\n")
	} else {
		fmt.Fprintf(os.Stderr, "r1 serve listening on %s\n", opts.Addr)
		fmt.Fprintf(os.Stderr, "dashboard: http://%s/\n", opts.Addr)
	}
	if sockPath != "" {
		fmt.Fprintf(os.Stderr, "control socket: %s\n", sockPath)
	}

	// Write the discovery file so external clients (`r1 ctl`, the
	// desktop app, headless test harnesses) can find this daemon.
	// `daemondisco.WriteDiscovery` writes ~/.r1/daemon.json atomically
	// (tmp + rename). Failure here is non-fatal — the dashboard +
	// JSON-RPC endpoints still work for clients that already know the
	// port; it just means `r1 ctl` won't be able to auto-discover.
	// The socket path activates r1 ctl's unixSocketAlive/dialUnix
	// branch (ctl_daemon_cmd.go pickClientAndURL).
	if discPath, derr := daemondisco.WriteDiscovery(os.Getpid(), sockPath, port, opts.Token, version); derr != nil {
		fmt.Fprintf(os.Stderr, "warn: discovery file not written: %v\n", derr)
	} else {
		fmt.Fprintf(os.Stderr, "discovery file: %s\n", discPath)
	}

	sigCtx, sigCancel := signalContext(context.Background())
	defer sigCancel()

	errCh := make(chan error, 2)
	if !opts.NoTCP {
		go func() { errCh <- srv.ListenAndServe() }()
	}
	if unixLn != nil {
		unixSrv := newUnixControlServer(srv.Handler(), opts.Token)
		go func() { errCh <- unixSrv.Serve(&peerCredListener{Listener: unixLn}) }()
	}

	select {
	case <-sigCtx.Done():
		fmt.Fprintf(os.Stderr, "r1 serve: shutting down\n")
	case grace := <-shutdownReqCh:
		fmt.Fprintf(os.Stderr, "r1 serve: shutting down (daemon.shutdown RPC, grace=%ds)\n", grace)
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

// daemonShutdownAckDelay gives the response layer a brief window to
// flush the shutdown ack before the serve loop tears the listener down.
// Shared by the daemon.shutdown RPC callback (SetShutdownFunc) and the
// plain-HTTP POST /v1/daemon/shutdown route (O1).
const daemonShutdownAckDelay = 100 * time.Millisecond

// daemonInfoFunc reports daemon metadata for GET /v1/daemon/info. In
// runServeLoop this is rpcHandler.DaemonInfo; tests pass a stub.
type daemonInfoFunc func(context.Context) (jsonrpc.DaemonInfoResponse, error)

// mountDaemonControlRoutes wires the plain-HTTP daemon control plane
// (O1) onto the shared serve mux: POST /v1/daemon/shutdown and GET
// /v1/daemon/info.
//
// Before O1 the real daemon.shutdown signal was reachable ONLY over the
// /v1/rpc WebSocket (which no CLI client dials), and every queue-backed
// ctl verb 404s against a default `r1 serve` because MountDaemonQueue is
// gated behind --enable-queue-routes. These two plain routes give
// `r1 ctl shutdown`/`info` a reachable transport with no extra flags.
//
// Auth: both routes are wrapped with RequireBearer(token). On the
// loopback TCP listener the CLI supplies the bearer from daemon.json; on
// the unix control socket the caller sends no bearer but the peer-cred
// check authenticates the connection and unixControlHandler injects the
// bearer before dispatch — so a single RequireBearer wrap is correct for
// both transports.
//
// shutdown reuses the SAME non-blocking send into shutdownReqCh that the
// daemon.shutdown RPC callback uses, so the serve loop's select unwinds
// identically whichever surface the operator hits. The signal fires
// AFTER the ack flushes (daemonShutdownAckDelay) so the caller observes
// the success envelope before the listener closes.
func mountDaemonControlRoutes(mux *http.ServeMux, info daemonInfoFunc, shutdownReqCh chan<- int, token string) {
	var shutdownH http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			GraceSeconds int `json:"grace_seconds"`
		}
		// Tolerate an empty body (curl -X POST with no payload); bound
		// the read so a hostile client cannot stream unbounded JSON.
		if r.Body != nil {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted_at":   time.Now().UTC().Format(time.RFC3339Nano),
			"grace_seconds": req.GraceSeconds,
		})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func(grace int) {
			time.Sleep(daemonShutdownAckDelay)
			select {
			case shutdownReqCh <- grace:
			default:
				// channel already has a pending signal; drop.
			}
		}(req.GraceSeconds)
	})
	var infoH http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := info(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	if token != "" {
		shutdownH = server.RequireBearer(token)(shutdownH)
		infoH = server.RequireBearer(token)(infoH)
	}
	mux.Handle("POST /v1/daemon/shutdown", shutdownH)
	mux.Handle("GET /v1/daemon/info", infoH)
}


// serveJournalDir resolves and creates `~/.r1/journals` (or
// `$R1_HOME/journals` in sandboxes), the per-session event journal
// directory the subscribe replay path reads (audit A069).
func serveJournalDir() (string, error) {
	dir := os.Getenv("R1_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		dir = filepath.Join(home, ".r1")
	}
	jd := filepath.Join(dir, "journals")
	if err := os.MkdirAll(jd, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", jd, err)
	}
	return jd, nil
}

// buildSessionSSEHandler assembles the GET /v1/sessions/{id}/sse
// middleware chain (audit A069): sse.AttachAuthFromQuery promotes
// ?token= into the Authorization header BEFORE requireBearer runs, so
// browser EventSource clients (which cannot set headers) authenticate
// with the same bearer CLI clients use. The Subscribe binding is the
// shared fanout engine: journal replay from since_seq / Last-Event-ID,
// then live events, per the replay-before-live contract.
func buildSessionSSEHandler(rpcHandler *jsonrpc.HubHandler, token string) http.Handler {
	var inner http.Handler = &sse.Handler{
		Subscribe:            rpcHandler.SubscribeSessionWithSink,
		SessionIDFromRequest: sse.PathSessionID,
		FilterFromRequest:    sse.QueryFilter,
	}
	if token != "" {
		inner = server.RequireBearer(token)(inner)
	}
	return sse.AttachAuthFromQuery(inner)
}

// hubLanesTools adapts the multi-session SessionHub to
// server.LaneToolInvoker (audit A040). The MCP lane tools
// (internal/mcp/lanes_server.go) are written against a single
// session's cortex workspace; the daemon hosts N sessions, so this
// adapter resolves the session named by args["session_id"] at call
// time, casts its Workspace to mcp.LanesBackend (*cortex.Workspace
// satisfies it), and delegates to a per-session LanesServer. Sessions
// without a cortex workspace — every session until the workspace
// attach glue (audit A073/A041) lands — get an honest not_found
// envelope instead of -32601.
type hubLanesTools struct {
	hub     *sessionhub.SessionHub
	mu      sync.Mutex
	servers map[string]*mcp.LanesServer
}

func newHubLanesTools(hub *sessionhub.SessionHub) *hubLanesTools {
	return &hubLanesTools{hub: hub, servers: map[string]*mcp.LanesServer{}}
}

// laneToolErrorEnvelope builds the spec §7 error envelope shape the
// WS route unwraps (matching mcp.LanesServer.envelopeError).
func laneToolErrorEnvelope(code, msg string) string {
	b, _ := json.Marshal(map[string]any{
		"ok":            false,
		"error_code":    code,
		"error_message": msg,
	})
	return string(b)
}

// HandleToolCall implements server.LaneToolInvoker.
func (t *hubLanesTools) HandleToolCall(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	sid, _ := args["session_id"].(string)
	if sid == "" {
		return laneToolErrorEnvelope("internal", "session_id is required"), nil
	}
	sess, err := t.hub.Get(sid)
	if err != nil {
		t.mu.Lock()
		delete(t.servers, sid)
		t.mu.Unlock()
		return laneToolErrorEnvelope("not_found", fmt.Sprintf("session %q not found", sid)), nil
	}
	backend, ok := sess.Workspace.(mcp.LanesBackend)
	if !ok || backend == nil {
		return laneToolErrorEnvelope("not_found",
			fmt.Sprintf("session %q has no cortex workspace (lanes unavailable)", sid)), nil
	}
	t.mu.Lock()
	ls := t.servers[sid]
	if ls == nil {
		ls = mcp.NewLanesServer(backend, nil)
		t.servers[sid] = ls
	}
	t.mu.Unlock()
	return ls.HandleToolCall(ctx, toolName, args)
}
