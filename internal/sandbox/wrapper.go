package sandbox

import (
	"context"
	"fmt"
	"os/exec"
)

// Wrapper is a sandbox backend. Implementations build an *exec.Cmd that
// runs `bash -c shellCmd` under OS-level containment.
type Wrapper interface {
	// Name identifies the backend ("bwrap", "landlock", "docker").
	Name() string
	// Available reports whether this backend can enforce the given policy
	// on this host. Called eagerly at wiring time so misconfiguration
	// fails before any command runs — never mid-mission.
	Available(p Policy) error
	// Command builds the wrapped command. The returned cmd has Dir set to
	// workDir; callers still own process-group, Cancel, and WaitDelay
	// plumbing (the wrapper must not preclude group-kill semantics).
	Command(ctx context.Context, shellCmd, workDir string, p Policy) (*exec.Cmd, error)
}

// Select resolves a policy to a usable backend.
//
//   - Mode "off" returns (nil, nil): the caller runs unsandboxed. This is
//     the ONLY silent-passthrough mode.
//   - An explicitly named mode returns that backend or an error — it never
//     falls through to another backend, so a request for bwrap on a host
//     without bwrap fails closed instead of quietly degrading.
//   - "auto" (or empty) tries bwrap then landlock and reports both probe
//     failures when neither works. Docker is never auto-selected.
//   - Unknown modes are an error (a typo must not mean "auto").
func Select(p Policy) (Wrapper, error) {
	mode := p.Mode
	if mode == "" {
		mode = ModeAuto
	}
	switch mode {
	case ModeOff:
		return nil, nil
	case ModeBwrap:
		return require(&bwrapWrapper{}, p)
	case ModeLandlock:
		return require(&landlockWrapper{}, p)
	case ModeDocker:
		return require(&dockerWrapper{}, p)
	case ModeAuto:
		bw := &bwrapWrapper{}
		bwErr := bw.Available(p)
		if bwErr == nil {
			return bw, nil
		}
		ll := &landlockWrapper{}
		llErr := ll.Available(p)
		if llErr == nil {
			return ll, nil
		}
		return nil, fmt.Errorf("no sandbox backend available: bwrap: %v; landlock: %v", bwErr, llErr)
	default:
		return nil, fmt.Errorf("unknown sandbox mode %q (want %s|%s|%s|%s|%s)",
			mode, ModeAuto, ModeBwrap, ModeLandlock, ModeDocker, ModeOff)
	}
}

func require(w Wrapper, p Policy) (Wrapper, error) {
	if err := w.Available(p); err != nil {
		return nil, fmt.Errorf("sandbox mode %q requested but unavailable: %w", w.Name(), err)
	}
	return w, nil
}
