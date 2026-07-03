//go:build !linux

package sandbox

import "fmt"

// probeLandlockABI: Landlock is Linux-only; report unsupported so
// Available() fails closed and auto-selection skips this backend.
func probeLandlockABI() int { return 0 }

// applyAndExec is unreachable on non-Linux (Available always errors), but
// the helper subcommand still needs a definition to compile.
func applyAndExec(Policy, []string) error {
	return fmt.Errorf("landlock sandbox is linux-only")
}
