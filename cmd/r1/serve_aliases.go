package main

// serve_aliases.go — TASK-41: keep `r1 daemon` and `r1 agent-serve`
// working as deprecated aliases of `r1 serve`.
//
// Spec: "register `serve` → serveCmd(args). Keep `daemon` →
// daemonCmd(args) and `agent-serve` → agentServeCmd(args) as alias
// paths (each prints a one-line deprecation hint to stderr and
// forwards args to serveCmd with the right flag prefix)."
//
// Each alias:
//   1. Writes a single-line deprecation hint to stderr naming the
//      replacement command (`r1 serve --enable-queue-routes` for
//      daemon, `r1 serve --enable-agent-routes` for agent-serve).
//   2. Forwards args to the legacy implementation (daemonCmd /
//      agentServeCmd) so existing scripts pinning the legacy verb
//      keep working without functional regression.
//
// We do NOT silently rewrite `r1 daemon start` → `r1 serve
// --enable-queue-routes`: the daemon's subcommand structure
// (start/enqueue/status/wal/...) does not map 1:1 to serve's flag
// surface. Operators read the hint and migrate at their own pace.
// The forwarder indirections (daemonForwarder / agentServeForwarder)
// are exported as package vars so tests can replace them with
// recorder closures that capture the args without invoking the
// legacy commands (which os.Exit on flag errors).

import (
	"io"
	"os"
)

// daemonDeprecationHint is the one-line stderr message printed by
// runDaemonAlias before forwarding.
const daemonDeprecationHint = "r1 daemon: deprecated; use `r1 serve --enable-queue-routes` instead. Forwarding to legacy daemon command.\n"

// agentServeDeprecationHint mirrors daemonDeprecationHint for the
// agent-serve alias.
const agentServeDeprecationHint = "r1 agent-serve: deprecated; use `r1 serve --enable-agent-routes` instead. Forwarding to legacy agent-serve command.\n"

// daemonForwarder is the function runDaemonAlias calls after emitting
// the deprecation hint. Production points it at daemonCmd; tests swap
// it for a recorder.
var daemonForwarder = daemonCmd

// agentServeForwarder is the analogous indirection for the
// agent-serve alias.
var agentServeForwarder = agentServeCmd

// runDaemonAlias prints the deprecation hint and forwards to the
// legacy daemon command. stderr is written to before the forward so
// the hint reaches the operator even when the legacy command
// os.Exits.
func runDaemonAlias(args []string, stderr io.Writer) {
	io.WriteString(stderr, daemonDeprecationHint)
	daemonForwarder(args)
}

// runAgentServeAlias is the analogous wrapper for `r1 agent-serve`.
func runAgentServeAlias(args []string, stderr io.Writer) {
	io.WriteString(stderr, agentServeDeprecationHint)
	agentServeForwarder(args)
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
