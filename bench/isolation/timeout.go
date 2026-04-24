package isolation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// TimeoutRunner wraps command execution with a SIGTERM-then-SIGKILL timeout
// pattern, giving the process a grace period to shut down cleanly.
type TimeoutRunner struct {
	// GracePeriod is how long to wait after SIGTERM before sending SIGKILL.
	// Defaults to 10 seconds if zero.
	GracePeriod time.Duration
}

// TimeoutResult captures the outcome of a timed command execution.
type TimeoutResult struct {
	ExitCode    int
	Stdout      string
	Stderr      string
	TimedOut    bool
	GraceKilled bool
}

// Run executes the given command. If ctx is cancelled or its deadline expires,
// SIGTERM is sent to the process group first. If the process does not exit
// within GracePeriod, SIGKILL is sent.
func (t *TimeoutRunner) Run(ctx context.Context, name string, args ...string) (TimeoutResult, error) {
	grace := t.GracePeriod
	if grace == 0 {
		grace = 10 * time.Second
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...) // #nosec G204 -- benchmark harness binary with Stoke-generated args.
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return TimeoutResult{ExitCode: -1}, fmt.Errorf("start %s: %w", name, err)
	}

	// Wait for either the process to finish or the context to be done.
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- cmd.Wait()
	}()

	select {
	case err := <-doneCh:
		// Process finished on its own.
		res := TimeoutResult{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
		}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				res.ExitCode = exitErr.ExitCode()
			} else {
				res.ExitCode = -1
				return res, err
			}
		}
		return res, nil

	case <-ctx.Done():
		// Context cancelled or deadline exceeded. Send SIGTERM to process group.
		res := TimeoutResult{TimedOut: true}
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}

		// Wait for graceful shutdown.
		graceTimer := time.NewTimer(grace)
		defer graceTimer.Stop()

		select {
		case waitErr := <-doneCh:
			res.Stdout = stdout.String()
			res.Stderr = stderr.String()
			if waitErr != nil {
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					res.ExitCode = exitErr.ExitCode()
				} else {
					res.ExitCode = -1
				}
			}
			return res, nil

		case <-graceTimer.C:
			// Grace period expired, send SIGKILL.
			res.GraceKilled = true
			if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
				_ = cmd.Process.Kill()
			}

			waitErr := <-doneCh
			res.Stdout = stdout.String()
			res.Stderr = stderr.String()
			if waitErr != nil {
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					res.ExitCode = exitErr.ExitCode()
				} else {
					res.ExitCode = -1
				}
			}
			return res, nil
		}
	}
}
