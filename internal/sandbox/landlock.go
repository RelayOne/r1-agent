package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

// reachableDaemonSocket is a seam so tests can simulate a host with (or
// without) a reachable daemon control socket without touching the real /run.
var reachableDaemonSocket = ReachableDaemonSocket

// landlockHelperProbeTimeout bounds the re-exec routing self-test in
// Available. The probe just re-execs this binary and expects an immediate
// exit(0); a couple of seconds is generous even on a loaded host.
const landlockHelperProbeTimeout = 3 * time.Second

// landlockHelperProbe is a seam so tests can exercise the Available error
// path without depending on how the test binary routes __sandbox-exec. It
// re-execs the current binary with `__sandbox-exec --probe`; a binary that
// embeds RunExecHelper exits 0, one that does not fails closed here.
var landlockHelperProbe = func() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve own executable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), landlockHelperProbeTimeout)
	defer cancel()
	// #nosec G204 -- re-exec of our own binary with a fixed argv.
	if out, err := exec.CommandContext(ctx, exe, HelperSubcommand, "--probe").CombinedOutput(); err != nil {
		return fmt.Errorf("this binary (%s) does not route the %s helper subcommand "+
			"(embed sandbox.RunExecHelper, or select bwrap/docker instead): %v: %s",
			filepath.Base(exe), HelperSubcommand, err, out)
	}
	return nil
}

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
	// Landlock does not mediate connect()/bind() on AF_UNIX pathname sockets
	// (landlock(7)), so a reachable daemon control socket (docker/podman/…)
	// is a full host escape it CANNOT close — unlike bwrap, which /dev/null-
	// masks it. When the operator asked for containment (egress denied),
	// fail closed rather than hand back false assurance; bwrap is the backend
	// that actually contains this.
	if !p.AllowEgress {
		if sock := reachableDaemonSocket(); sock != "" {
			return fmt.Errorf("landlock cannot contain the reachable daemon socket %s "+
				"(it does not mediate AF_UNIX connect); use the bwrap backend "+
				"(R1_NATIVE_SANDBOX=bwrap), which masks it", sock)
		}
	}
	if _, err := os.Executable(); err != nil {
		return fmt.Errorf("cannot resolve own executable for re-exec helper: %w", err)
	}
	// Routing self-test: confirm THIS binary actually dispatches the
	// __sandbox-exec subcommand to RunExecHelper. Embedders (r1-server,
	// r1-bench) that build NativeRunner without the cmd/r1 helper dispatch
	// would otherwise select landlock at wiring time and then fail every
	// bash command mid-mission (the child exits without running the
	// payload). Failing here converts that into a clear, actionable error
	// before any command runs.
	if err := landlockHelperProbe(); err != nil {
		return err
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
