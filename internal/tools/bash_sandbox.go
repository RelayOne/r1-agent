package tools

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/RelayOne/r1/internal/sandbox"
)

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
