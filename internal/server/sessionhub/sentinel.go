// Package sessionhub will host the multi-session daemon's per-session
// state in Phase D of the r1d-server spec (specs/r1d-server.md §11.21–24).
//
// This file lands in Phase A — long before sessionhub.go itself — because
// the sentinel it provides (assertCwd) is the runtime safety backstop for
// the os.Chdir audit gate (spec §10.1–10.5, items 1–10). The audit gate
// is BLOCKING for Phase E (multi-session enable). The sentinel converts a
// missed cwd-leak from a silent cross-session contamination into an
// immediate, loud crash.
//
// Why panic instead of error-return?
//
//   The whole point of the sentinel is to detect a CLASS of bug — a stray
//   os.Chdir on a goroutine path that the lint+audit failed to catch — and
//   make it un-ignorable. An error-return would let the calling handler
//   carry on serving the wrong session's files, which is the exact
//   catastrophic outcome (risk R1, spec §11.5) the gate exists to prevent.
//   A panic propagates up the goroutine, gets caught by the supervisor's
//   panic-recover wrapper around session goroutines, and terminates the
//   contaminated session with a stack trace pointing at the offending
//   call. That is fail-closed.
//
// Usage (when Phase D lands):
//
//	// At entry to a session-bound handler that must run in s.Workspace:
//	defer assertCwd(s.Workspace) // optional: verify on return too
//	// ... handler work that MUST NOT call os.Chdir ...
//
// The expected string MUST be an absolute path; callers should use
// filepath.Abs at session creation and stash the result in Session.Workspace.
package sessionhub

import (
	"fmt"
	"os"
	"path/filepath"
)

// assertCwd verifies that the process's current working directory matches
// `expected`. On mismatch, it panics with a message naming both paths.
//
// This is the runtime safety backstop for the r1d-server Phase A os.Chdir
// audit gate. If the audit and lint together missed a stray cwd-mutating
// call somewhere in a session-bound code path — and the daemon is running
// in multi-session mode — this panic converts the silent leak into an
// immediate crash with a stack trace pointing at the contaminated handler.
//
// Sentinel calls are advisory by design: callers wrap them around the
// boundary of session-bound work. Phase D will deploy them at the entrypoints
// of Session.Run and at every public API on the SessionHub that can dispatch
// session-scoped work to a goroutine.
//
// `expected` should be an absolute path; if the runtime cwd resolves to a
// different absolute path (after symlink/`.` normalisation), the sentinel
// panics. We do NOT call filepath.EvalSymlinks here — it can block on
// stale-NFS / dead mounts and the sentinel must remain cheap.
//
// The panic message is intentionally verbose ("leaked workdir" plus both
// paths) so an on-call operator reading a stack trace can immediately
// identify what happened.
//
//   - LINT-ALLOW chdir-stdlib: the sentinel itself MUST call os.Getwd to
//     check the live cwd; that is exactly its purpose, not a leak.
func assertCwd(expected string) {
	// LINT-ALLOW chdir-stdlib: sentinel inspects the live cwd; cannot be replaced by a parameter — see func doc.
	got, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("r1/server/sessionhub: os.Getwd failed during sentinel check (expected=%s): %v — leaked workdir", expected, err))
	}
	// Normalise both sides to clean absolute form before comparing so that
	// a benign trailing-slash or `./` from the caller doesn't trip the
	// sentinel.
	wantAbs, wantErr := filepath.Abs(expected)
	if wantErr != nil {
		panic(fmt.Sprintf("r1/server/sessionhub: filepath.Abs(expected=%s) failed: %v — sentinel cannot validate", expected, wantErr))
	}
	gotAbs := filepath.Clean(got)
	wantClean := filepath.Clean(wantAbs)
	if gotAbs == wantClean {
		return
	}
	panic(fmt.Sprintf("r1/server/sessionhub: cwd drifted: got %s, want %s — leaked workdir (see specs/r1d-server.md §10 D-D4 audit gate)", gotAbs, wantClean))
}
