//go:build !linux

package main

import "os/exec"

// setSidOnCmd is a no-op on non-linux platforms.
func setSidOnCmd(cmd *exec.Cmd) {}
