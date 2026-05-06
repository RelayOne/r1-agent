// Package sessionhub: tool-dispatch sentinel guard.
//
// =============================================================================
// Why this file exists
// =============================================================================
//
// The r1d-server multi-session daemon hosts MANY sessions inside ONE process.
// Each session has a workdir; tool runners do their work relative to that
// workdir. The shared-process model means there is exactly one
// `os.Getwd()` for the whole daemon — so if any tool runner (or any
// goroutine on the dispatch path) calls `os.Chdir`, it changes the cwd
// for every session, not just the one it was invoked from.
//
// The Phase A audit gate (specs/r1d-server.md §10) eliminates `os.Chdir`
// from session-bound code paths. The sentinel (sessionhub/sentinel.go)
// is the runtime backstop: it runs before each tool dispatch and
// `panic`s if the live cwd has drifted from the session's
// SessionRoot. A panic is the right shape because:
//
//  1. The panic propagates up through the agent loop's recover wrapper
//     and aborts the contaminated session with a stack trace pointing
//     at the offending dispatch.
//
//  2. An error-return would let the contaminated handler keep
//     serving requests against the wrong session's filesystem — the
//     EXACT catastrophic outcome (spec §11.5 risk R1) the gate exists
//     to prevent.
//
// This file wires the sentinel into the session's tool-dispatch path
// (TASK-25). Every Session.Run that omits an explicit DispatchHook
// gets `defaultDispatchHook` installed automatically — so the default
// configuration is fail-loud on cwd drift, no opt-in required.
//
// Tests can override the hook via SetDispatchHook (e.g. to capture
// invocations or to inject a panic for the ChdirSentinel test in
// cmd/r1/serve_integration_test.go).
//
// =============================================================================
// Why the assertion runs BEFORE the inner handler
// =============================================================================
//
// The sentinel must fire BEFORE the tool runner reads/writes anything.
// Once a Read or Bash call lands in the wrong cwd, the bytes are
// already misrouted — there is no recovery path that restores the
// per-session boundary. The wrapHandler ordering (TASK-22) makes this
// invariant structural: the hook is the FIRST line of code the tool
// dispatch executes, with no intervening branches.
//
// =============================================================================
package sessionhub

// defaultDispatchHook is the production tool-dispatch hook. It runs
// `assertCwd(s.SessionRoot)` (sentinel.go) on every tool call. A cwd
// drift panics with the SessionRoot vs. live-cwd labels, which the
// supervisor's panic-recover wrapper around session goroutines turns
// into a session-terminate with a stack trace.
func defaultDispatchHook(s *Session, _ string) {
	// SessionRoot is set at Create time and never mutated afterward,
	// so reading it here without locking is race-free.
	assertCwd(s.SessionRoot)
}
