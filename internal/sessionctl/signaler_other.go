//go:build !unix

package sessionctl

import "errors"

// unsupportedSignaler is the non-POSIX fallback: takeover handoff via
// SIGSTOP/SIGCONT only works on unix-family systems.
type unsupportedSignaler struct{}

// NewPGIDSignaler returns a Signaler that fails closed on non-unix builds.
func NewPGIDSignaler() Signaler { return unsupportedSignaler{} }

func (unsupportedSignaler) Pause(int) error {
	return errors.New("sessionctl: takeover unsupported on this OS")
}
func (unsupportedSignaler) Resume(int) error {
	return errors.New("sessionctl: takeover unsupported on this OS")
}
