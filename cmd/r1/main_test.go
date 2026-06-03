package main

import (
	"os"
	"strings"
	"testing"
)

// TestMain is a defense-in-depth trap against the recursive test-binary
// fan-out. Production realSpawnDaemon (cmd/r1/daemon_http.go:113) execs
// os.Executable() with a "serve" arg to launch the background daemon. In a
// test build, os.Executable() resolves to the *test binary*, so the spawned
// `<test-bin> serve` would re-run the entire test suite. Each re-run can
// itself spawn another daemon, fanning out into a cascade of orphaned
// `r1.test serve` processes (~120 leaked in the wild).
//
// To break the recursion we intercept the daemon subcommand BEFORE m.Run():
// if os.Args[1:] names a daemon serve command (a bare `serve`, or the
// `mcp serve` / `agent serve` two-token forms), we exit 0 immediately and
// never run any tests. Normal `go test` invocations only pass `-test.*`
// flags, which are ignored here, so the suite runs as usual.
func TestMain(m *testing.M) {
	if isDaemonServeArgs(os.Args[1:]) {
		// Spawned as a daemon by realSpawnDaemon: do NOT run the suite.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// isDaemonServeArgs reports whether args name a daemon serve subcommand in the
// *production* re-exec shape, i.e. the bare `<exe> serve` invocation emitted by
// realSpawnDaemon — NOT a developer's `go test`.
//
// The distinguishing signal: a real `go test ./cmd/r1` run always passes one or
// more `-test.*` flags to the test binary (e.g. -test.run, -test.timeout,
// -test.paniconexit0). The production daemon re-exec passes none. So if ANY arg
// begins with "-test.", we treat this as a genuine test run and do NOT trap —
// which lets `go test ./cmd/r1 -args serve` exercise the suite normally.
//
// When no `-test.*` flag is present, it locates the first non-flag token
// (skipping anything beginning with "-") and traps on:
//
//	serve
//	mcp serve
//	agent serve
//
// Anything else (including no non-flag token) is treated as a normal test run.
func isDaemonServeArgs(args []string) bool {
	// A real `go test` run always passes -test.* flags to the binary. The
	// production daemon re-exec (`<exe> serve`) passes none. If we see any
	// -test.* flag, this is NOT the daemon shape: run the suite.
	for _, a := range args {
		if strings.HasPrefix(a, "-test.") {
			return false
		}
	}
	// Find the first positional (non-flag) token.
	i := 0
	for i < len(args) && len(args[i]) > 0 && args[i][0] == '-' {
		i++
	}
	if i >= len(args) {
		return false
	}
	first := args[i]
	if first == "serve" {
		return true
	}
	if first == "mcp" || first == "agent" {
		// Two-token form: the next positional must be exactly "serve".
		if i+1 < len(args) && args[i+1] == "serve" {
			return true
		}
	}
	return false
}
