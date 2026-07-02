//go:build windows

package procutil

import (
	"os/exec"
	"syscall"
)

func ConfigureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func ConfigureDetachedProcess(cmd *exec.Cmd) {
	ConfigureProcessGroup(cmd)
}

func CurrentProcessGroupID() int {
	return 0
}

func Terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// KillGroup is a no-op on Windows: there is no POSIX pgid to signal, and
// cmd.Cancel (Process.Kill) has already terminated the direct child.
func KillGroup(pid int) error { return nil }

// GroupAlive always reports false on Windows (no pgid probe available).
func GroupAlive(pid int) bool { return false }
