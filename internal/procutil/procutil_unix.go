//go:build !windows

package procutil

import (
	"os/exec"
	"syscall"
)

func ConfigureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func ConfigureDetachedProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func CurrentProcessGroupID() int {
	return syscall.Getpgrp()
}

func Terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return syscall.Kill(-pgid, syscall.SIGTERM)
}

func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// KillGroup sends SIGKILL to the process group led by pid. Unlike Kill it
// does not need a Getpgid lookup, so it still works after the leader has
// been reaped by Wait: with Setpgid the leader's pid IS the pgid, and the
// group survives its leader.
func KillGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// GroupAlive reports whether any process remains in the group led by pid
// (signal 0 probe). Used by tests and post-cancel escalation checks.
func GroupAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(-pid, 0) == nil
}
