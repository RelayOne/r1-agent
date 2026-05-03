//go:build !windows
// +build !windows

package main

// daemon_http_unix.go — POSIX implementation of applyDetachAttrs used
// by spawnDaemonInBackground (TASK-42). Setsid puts the child in its
// own session + process group so a Ctrl-C of the parent doesn't
// reach the daemon.

import (
	"os/exec"
	"syscall"
)

func applyDetachAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
