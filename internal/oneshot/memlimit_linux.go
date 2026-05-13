//go:build linux

// memlimit_linux.go — Linux-specific RLIMIT_AS enforcement via
// prlimit(2). RLIMIT_AS caps the process's total virtual-memory
// address space; the kernel returns ENOMEM (which surfaces in Go
// as a runtime panic) when an allocation would exceed it.
//
// We use prlimit on PID 0 (current process) rather than the
// older setrlimit so we can change both Cur and Max in one
// syscall. Setting Max==Cur means we can never re-raise the
// ceiling later in the same process, which is exactly what we
// want: the cap is established at startup and immutable for the
// remainder of the verb's lifetime.
//
// Spec: specs/oneshot-production-hardening.md §T1.2.
package oneshot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// applyOSMemoryLimit pins RLIMIT_AS to the supplied byte count.
// On allocation past the cap, the Go runtime panics with
// "runtime: out of memory" or "cannot allocate memory"; the
// deferred handler in runOneShotCmd recovers and exits 3.
func applyOSMemoryLimit(bytes uint64) error {
	rl := unix.Rlimit{Cur: bytes, Max: bytes}
	if err := unix.Prlimit(0, unix.RLIMIT_AS, &rl, nil); err != nil {
		return fmt.Errorf("oneshot: prlimit RLIMIT_AS: %w", err)
	}
	return nil
}
