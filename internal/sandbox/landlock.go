package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// landlockWrapper contains commands with the Linux Landlock LSM, applied
// via raw syscalls on the vendored x/sys constants (x/sys v0.43.0 has the
// constants and structs but no high-level wrappers — zero new deps).
//
// Landlock is allow-list-only and landlock_restrict_self binds the CALLING
// process, so the wrapper re-execs the current binary with the hidden
// `__sandbox-exec` subcommand: that child applies the ruleset to itself
// and then execs bash. Consequence: embedders whose binary does not route
// the `__sandbox-exec` argv to RunExecHelper cannot use this backend — the
// child exits without running the payload (fail-closed, but confusing);
// cmd/r1 and this package's own test binary both route it.
type landlockWrapper struct{}

// landlockABIProbe is a seam so portable tests can simulate hosts with a
// given Landlock ABI (including 0 = unsupported/seccomp-blocked). The real
// probe lives in landlock_linux.go; non-Linux builds stub it to 0.
var landlockABIProbe = probeLandlockABI

func (l *landlockWrapper) Name() string { return ModeLandlock }

// Available requires Landlock ABI >= 1, and ABI >= 4 when the policy
// denies egress: on older ABIs the kernel cannot restrict TCP at all, and
// silently allowing egress that the policy denies would be a fail-open.
func (l *landlockWrapper) Available(p Policy) error {
	abi := landlockABIProbe()
	if abi < 1 {
		return fmt.Errorf("landlock unsupported (kernel too old, LSM disabled, or syscall seccomp-blocked)")
	}
	if !p.AllowEgress && abi < 4 {
		return fmt.Errorf("landlock ABI %d cannot restrict egress (need >= 4); refusing to enforce a weaker policy than requested", abi)
	}
	if _, err := os.Executable(); err != nil {
		return fmt.Errorf("cannot resolve own executable for re-exec helper: %w", err)
	}
	return nil
}

// Command re-execs the current binary as the __sandbox-exec helper, which
// applies the ruleset to itself and execs bash -c shellCmd.
func (l *landlockWrapper) Command(ctx context.Context, shellCmd, workDir string, p Policy) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve own executable: %w", err)
	}
	pj, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal sandbox policy: %w", err)
	}
	cmd := exec.CommandContext(ctx, exe, HelperSubcommand,
		"--policy", string(pj), "--", "bash", "-c", shellCmd) // #nosec G204 -- re-exec of our own binary with harness-constructed argv.
	cmd.Dir = workDir
	return cmd, nil
}
