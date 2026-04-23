package deploy

import "time"

// DeployExecutor spec (specs/deploy-executor.md §Auto-Rollback Decision
// Tree, D24/D25) calls for a strict triple-condition predicate before
// Stoke auto-reverts a live deploy. Any two-of-three signal — a 502
// alone, a handful of console errors alone, or a long-running machine
// alone — is treated as warm-up noise or transient badness, NOT a
// rollback trigger. Only when all three fire simultaneously does Stoke
// pull the previous image.
//
// Keeping the predicate here as a pure function (rather than inlining
// it in the executor) gives the rest of the codebase one canonical
// implementation to call and one surface for unit tests (see
// auto_rollback_test.go colocated alongside this file for the branch
// matrix: each single-factor failure, each two-factor near-miss, the
// three-factor trigger, and the warm-up edge at exactly 30s).
//
// Spec invariant: timing is measured from Deploy() call start, NOT
// from submit time. Matches operator intuition — "I hit ship; 30s
// later it's still 500ing → revert." The caller is responsible for
// passing the correct elapsed; AutoRollback itself does not touch
// time.Now so callers are trivially testable with a fake clock.

// AutoRollbackWarmup is the wall-clock grace period during which a
// failing deploy is presumed to still be warming up (machine cold
// start, Fly.io wildcard DNS propagation, buildpack first-run caches,
// etc.). Deploys that fail verification before this window elapses do
// not trigger auto-rollback regardless of how bad status / console
// errors look — flyctl's own checks report pass/fail on a similar
// cadence (30s interval, 10s grace) so anchoring here keeps Stoke in
// lockstep with the platform's own health gate.
//
// Exported as a constant rather than a config field because
// adjusting it would change the contract the spec documents; a caller
// who genuinely wants a different window should compute the elapsed
// differently before calling AutoRollback, not tune a global.
const AutoRollbackWarmup = 30 * time.Second

// AutoRollback reports whether a verification result warrants an
// auto-rollback per specs/deploy-executor.md §Auto-Rollback Decision
// Tree. The three conditions must ALL be true:
//
//  1. statusCode != 200 — the deployed URL is not serving a healthy
//     2xx on GET /. A 502/503 mid-warm-up is covered by the elapsed
//     gate below; this clause just ensures we never rollback a deploy
//     that is visibly fine from the public internet.
//
//  2. consoleErrCount > 0 — the browser verify step (or a synthesized
//     "TTFB exceeds SLA" pseudo-error) logged at least one console
//     error. This is the signal that differentiates "slow but working"
//     from "actually broken" — a page that 200s with zero JS errors
//     is not worth reverting even if it's slow, whereas a page that
//     500s AND throws console errors is reliably bad.
//
//  3. elapsed > AutoRollbackWarmup — we waited past the 30s warm-up
//     window before checking. Reverting a 10-second-old deploy because
//     the first request hit a cold machine would cause flapping: the
//     rolled-back deploy would itself cold-start, fail verify, and
//     tempt a caller into rolling THAT back too. The 30s floor breaks
//     that loop.
//
// Returns false (no rollback) when any single condition fails. This
// is intentionally strict: the spec calls out that "single-factor
// failure does NOT rollback" and lists the triple as an AND, not an
// OR. Callers who want a looser policy should layer their own check
// on top rather than edit this function — the predicate is a public
// contract the executor tests verify.
func AutoRollback(statusCode, consoleErrCount int, elapsed time.Duration) bool {
	if statusCode == 200 {
		return false
	}
	if consoleErrCount <= 0 {
		return false
	}
	if elapsed <= AutoRollbackWarmup {
		return false
	}
	return true
}

// AutoRollbackReason returns a short, stable string identifying why
// AutoRollback returned false (or "trigger" when it returned true).
// Used by the executor's event emitter so operators reading the
// stream-json log can tell a warm-up skip from a single-factor skip
// without digging into payloads.
//
// The returned string is one of:
//
//   - "trigger"          — all three conditions met; rollback
//   - "status_ok"        — statusCode == 200
//   - "no_console_errs"  — consoleErrCount == 0
//   - "within_warmup"    — elapsed <= AutoRollbackWarmup
//
// Order matters: we report the FIRST condition that disqualified the
// trigger, reading left-to-right the same way AutoRollback checks
// them. That ordering is documented so operators can rely on it when
// debugging ("the event said within_warmup, so I know status and
// errs weren't even checked").
func AutoRollbackReason(statusCode, consoleErrCount int, elapsed time.Duration) string {
	if statusCode == 200 {
		return "status_ok"
	}
	if consoleErrCount <= 0 {
		return "no_console_errs"
	}
	if elapsed <= AutoRollbackWarmup {
		return "within_warmup"
	}
	return "trigger"
}
