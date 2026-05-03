//go:build windows
// +build windows

package main

// daemon_http_windows.go — Windows implementation of applyDetachAttrs
// used by spawnDaemonInBackground (TASK-42).
//
// Windows equivalent of POSIX setsid is the CREATE_NEW_PROCESS_GROUP
// creation flag (0x00000200), which puts the child in its own
// console-process-group so a Ctrl-C in the parent's console doesn't
// propagate to the daemon. This matches the detachment guarantee the
// POSIX path provides via Setsid.

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup is the Windows CREATE_NEW_PROCESS_GROUP flag
// from MSDN's CreateProcess docs. Defined inline so we don't depend
// on golang.org/x/sys for a single constant.
const createNewProcessGroup = 0x00000200

func applyDetachAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}
