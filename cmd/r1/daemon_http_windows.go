//go:build windows
// +build windows

package main

// daemon_http_windows.go — Windows stub for applyDetachAttrs.
// CREATE_NEW_PROCESS_GROUP would be the equivalent of POSIX setsid;
// today we rely on the shell to detach (running `r1 daemon status`
// from a console rarely needs the daemon to outlive the console).

import "os/exec"

func applyDetachAttrs(_ *exec.Cmd) {
	// No-op on Windows for now.
}
