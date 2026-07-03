package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/RelayOne/r1/internal/sandbox"
)

// errSandboxDeniedCron / errSandboxDeniedNotebook are returned by the
// cron_create and notebook_cell_run handlers when the OS sandbox is engaged.
// Both tools exec on the host OUTSIDE the containment wrapped around the bash
// tool (cron runs later via crontab; notebook_cell_run shells out to
// jupyter), so allowing them while a sandbox is active would be a silent
// escape hatch. Denying is the fail-closed choice; the operator drops the
// sandbox (R1_NATIVE_SANDBOX=off) to use them. See docs/native-sandbox.md.
var (
	errSandboxDeniedCron = errors.New(
		"cron_create is disabled while the native OS sandbox is engaged: a cron entry runs later on the host, outside the sandbox. Set R1_NATIVE_SANDBOX=off to schedule jobs.")
	errSandboxDeniedNotebook = errors.New(
		"notebook_cell_run is disabled while the native OS sandbox is engaged: it executes on the host via jupyter, outside the sandbox. Run Python through the bash tool (which IS sandboxed), or set R1_NATIVE_SANDBOX=off.")
)

// sandboxActive reports whether an OS-level sandbox is wired on the registry
// (set via SetSandbox with a non-off policy). Handlers that exec OUTSIDE the
// bash containment consult it to fail closed rather than silently escape.
func (r *Registry) sandboxActive() bool { return r.sbx != nil }

// SetSandbox wires OS-level containment around the bash tool. Backend
// selection runs eagerly so an unenforceable policy fails here — at wiring
// time, before any command runs — never mid-mission. A policy whose Mode
// is "off" clears any previously configured sandbox and returns nil (the
// registry runs bash directly on the host, the historical behavior).
func (r *Registry) SetSandbox(p sandbox.Policy) error {
	w, err := sandbox.Select(p)
	if err != nil {
		return err
	}
	r.sbx = w
	r.sbxPolicy = p
	return nil
}

// buildBashCmd constructs the bash invocation for handleBash. Without a
// sandbox it reproduces the direct host exec unchanged; with one it
// delegates to the backend wrapper. A non-nil error means the command must
// NOT run: containment was requested and the wrapper could not produce a
// contained command, and silently degrading to host exec would fail open.
// Either way the caller owns process-group, Cancel, and WaitDelay plumbing.
func (r *Registry) buildBashCmd(ctx context.Context, command string) (*exec.Cmd, error) {
	if r.sbx == nil {
		cmd := exec.CommandContext(ctx, "bash", "-c", command) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
		cmd.Dir = r.workDir
		return cmd, nil
	}
	cmd, err := r.sbx.Command(ctx, command, r.workDir, r.sbxPolicy)
	if err != nil {
		return nil, fmt.Errorf("sandbox (%s) required but unavailable: %w (set R1_NATIVE_SANDBOX=off to run unsandboxed)", r.sbx.Name(), err)
	}
	return cmd, nil
}
