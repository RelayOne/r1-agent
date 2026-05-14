// exit_codes.go — exported integer exit codes used by the
// `r1 --one-shot` CLI. The CLI's own switch statement in
// cmd/r1/oneshot_cmd.go references these constants rather than
// magic numbers so the docs/integrations/relaygate-r1-stage.md
// exit-code table can be cross-checked against the code by the
// doctest in doctest_test.go.
//
// Spec: specs/oneshot-production-hardening.md §T6.2.
package oneshot

// Exit codes for the `--one-shot` CLI. The 0..2 range is
// inherited from the original three-verb CLI; the 3..4 range
// is added by the A3 hardening spec. 130 / 143 are the
// conventional Unix signal codes (128+SIGINT, 128+SIGTERM).
const (
	// ExitOK — verb ran to completion. Response written to
	// stdout. Note that StatusError responses also use this
	// code: the program ran successfully even though the verb
	// could not produce a result.
	ExitOK = 0

	// ExitRuntime — internal I/O / marshal / dispatch failure.
	// A machine-readable JSON envelope is written to stderr.
	ExitRuntime = 1

	// ExitUsage — bad flag, unknown verb, or argument out of
	// range. Stdout is never written on this path.
	ExitUsage = 2

	// ExitMemory — the process hit a memory ceiling. Either
	// the Linux RLIMIT_AS was exceeded (kernel killed the
	// allocation with ENOMEM, recovered by the deferred panic
	// handler) or the prlimit call itself failed at startup.
	// Stderr carries an oneshot.memory.limit_hit envelope.
	ExitMemory = 3

	// ExitTimeout — --timeout fired before the verb returned.
	// The staging buffer is dropped (stdout stays empty) and
	// stderr carries an oneshot.timeout envelope.
	ExitTimeout = 4

	// ExitSIGINT — process received SIGINT (Ctrl-C). Stdout
	// stays empty; stderr carries an oneshot.shutdown envelope
	// with "signal":"SIGINT".
	ExitSIGINT = 130

	// ExitSIGTERM — process received SIGTERM (orchestrator).
	// Stdout stays empty; stderr carries an oneshot.shutdown
	// envelope with "signal":"SIGTERM".
	ExitSIGTERM = 143
)
