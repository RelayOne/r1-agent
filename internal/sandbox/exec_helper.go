package sandbox

import (
	"encoding/json"
	"fmt"
	"io"
)

// HelperSubcommand is the hidden argv[1] the Landlock backend re-execs the
// current binary with. Deliberately un-typeable-looking and excluded from
// help output: it is an internal protocol, not a user command. Host
// binaries (cmd/r1/main.go) route it to RunExecHelper before any other
// dispatch.
const HelperSubcommand = "__sandbox-exec"

// helperExitCode is returned for ANY helper failure. 125 mirrors the
// docker/timeout convention for "the wrapper itself failed, the payload
// never ran" — distinguishable from the payload's own exit codes.
const helperExitCode = 125

// RunExecHelper implements the __sandbox-exec subcommand:
//
//	<binary> __sandbox-exec --policy <json> -- <argv...>
//
// It applies the Landlock policy to this process and execs argv. Fail-
// closed: on any error (bad args, malformed policy, kernel refusal) it
// writes the reason to stderr and returns helperExitCode WITHOUT running
// the payload. On success it never returns (the process image is
// replaced).
func RunExecHelper(args []string, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "sandbox-exec: "+format+"\n", a...)
		return helperExitCode
	}
	// --probe is a routing self-test: a binary that embeds RunExecHelper
	// returns 0 here without touching the kernel, so landlockWrapper.Available
	// can confirm at wiring time that THIS binary actually routes the
	// __sandbox-exec subcommand. A binary that does not embed the helper
	// (r1-server/r1-bench without the cmd/r1 dispatch) instead hits its own
	// CLI parser, which rejects the unknown subcommand non-zero — turning a
	// guaranteed mid-mission bash failure into a clear fail-closed error at
	// wiring time. Must be the first token after the subcommand.
	if len(args) > 0 && args[0] == "--probe" {
		return 0
	}
	var policyJSON string
	var argv []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--policy":
			i++
			if i >= len(args) {
				return fail("--policy requires a value")
			}
			policyJSON = args[i]
		case "--":
			argv = args[i+1:]
			i = len(args)
		default:
			return fail("unknown argument %q", args[i])
		}
	}
	if policyJSON == "" {
		return fail("missing --policy")
	}
	if len(argv) == 0 {
		return fail("missing payload argv after --")
	}
	var p Policy
	if err := json.Unmarshal([]byte(policyJSON), &p); err != nil {
		return fail("malformed policy: %v", err)
	}
	// Only returns on error; success replaces the process image.
	err := applyAndExec(p, argv)
	return fail("cannot enforce sandbox: %v", err)
}
