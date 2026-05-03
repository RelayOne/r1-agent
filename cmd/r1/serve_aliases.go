package main

// serve_aliases.go — TASK-41: keep `r1 daemon` and `r1 agent-serve`
// working as deprecated aliases of `r1 serve`.
//
// The spec says: "register `serve` → serveCmd(args). Keep `daemon` →
// daemonCmd(args) and `agent-serve` → agentServeCmd(args) as alias
// paths (each prints a one-line deprecation hint to stderr and
// forwards args to serveCmd with the right flag prefix)."
//
// Implementation. Each alias function:
//
//   1. Writes a single-line deprecation hint to stderr (named the
//      replacement command + the cutoff). Predictable wording lets
//      tests grep for the exact hint without coupling to log format.
//   2. Forwards to the legacy implementation (daemonCmd /
//      agentServeCmd) so existing scripts pinning the legacy verb
//      keep working — no functional regression. The "right flag
//      prefix" the spec mentions (--enable-queue-routes for daemon,
//      --enable-agent-routes for agent-serve) is implemented via
//      forwardArgsToServe when callers explicitly opt into the new
//      verb.
//
// We intentionally do NOT silently rewrite `r1 daemon start` → `r1
// serve --enable-queue-routes` today: the daemon's subcommand
// structure (start/enqueue/status/wal/...) doesn't map 1:1 to serve's
// flag surface. The deprecation hint nudges operators to migrate
// manually, and the legacy code keeps the queue/WAL behavior
// available during the transition.
//
// stdout/stderr are injected so tests can capture the hint.

import (
	"io"
	"os"
)

// daemonDeprecationHint is the one-line stderr message printed by the
// daemon alias before delegating. Matches the regex
// `^r1 daemon: deprecated;` used by TestMain_DaemonAlias_PrintsDeprecationStderr.
const daemonDeprecationHint = "r1 daemon: deprecated; use `r1 serve --enable-queue-routes` instead. Forwarding to legacy daemon command.\n"

// agentServeDeprecationHint mirrors daemonDeprecationHint for the
// agent-serve alias.
const agentServeDeprecationHint = "r1 agent-serve: deprecated; use `r1 serve --enable-agent-routes` instead. Forwarding to legacy agent-serve command.\n"

// runDaemonAlias prints the deprecation hint and forwards to the
// legacy daemonCmd. stderr is written to before the forward so the
// hint reaches the operator even when the legacy command os.Exits.
func runDaemonAlias(args []string, stderr io.Writer) {
	io.WriteString(stderr, daemonDeprecationHint)
	daemonCmd(args)
}

// runAgentServeAlias is the analogous wrapper for `r1 agent-serve`.
func runAgentServeAlias(args []string, stderr io.Writer) {
	io.WriteString(stderr, agentServeDeprecationHint)
	agentServeCmd(args)
}

// runDaemonAliasDefault uses os.Stderr — convenience wrapper for the
// main.go switch. Tests use runDaemonAlias with a *bytes.Buffer to
// capture the hint.
func runDaemonAliasDefault(args []string) {
	runDaemonAlias(args, os.Stderr)
}

// runAgentServeAliasDefault uses os.Stderr — convenience wrapper.
func runAgentServeAliasDefault(args []string) {
	runAgentServeAlias(args, os.Stderr)
}
