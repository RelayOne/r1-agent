//go:build linux

// oneshot_memlimit_linux.go — Linux RLIMIT_AS application for
// the CLI-side wrapper. Mirrors internal/oneshot/memlimit_linux.go
// — the duplication is intentional (see comment in
// oneshot_memlimit.go).
//
// Spec: specs/oneshot-production-hardening.md §T1.3.
package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// applyOSMemLimit pins RLIMIT_AS to the supplied byte count.
func applyOSMemLimit(bytes uint64) error {
	rl := unix.Rlimit{Cur: bytes, Max: bytes}
	if err := unix.Prlimit(0, unix.RLIMIT_AS, &rl, nil); err != nil {
		return fmt.Errorf("oneshot: prlimit RLIMIT_AS: %w", err)
	}
	return nil
}
