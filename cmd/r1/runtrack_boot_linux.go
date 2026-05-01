//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// setSidOnCmd detaches the child into its own process group so that
// SIGINT delivered to stoke doesn't cascade to the daemon. Linux-only
// — on other platforms setSidOnCmd is a no-op (stubbed in the generic
// file).
func setSidOnCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
