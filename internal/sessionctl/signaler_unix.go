//go:build unix

package sessionctl

import "syscall"

// pgidSignaler uses SIGSTOP/SIGCONT to pause and resume an entire process
// group. Negative pid argument to kill(2) targets the pgid.
type pgidSignaler struct{}

// NewPGIDSignaler returns a Signaler that pauses/resumes process groups via
// SIGSTOP/SIGCONT. Only wired on unix builds.
func NewPGIDSignaler() Signaler { return pgidSignaler{} }

func (pgidSignaler) Pause(pgid int) error  { return syscall.Kill(-pgid, syscall.SIGSTOP) }
func (pgidSignaler) Resume(pgid int) error { return syscall.Kill(-pgid, syscall.SIGCONT) }
