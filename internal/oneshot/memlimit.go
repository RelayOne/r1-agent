// memlimit.go — bounded memory enforcement for the `--one-shot`
// CLI. Combines Go's runtime/debug.SetMemoryLimit (GOMEMLIMIT,
// a soft target the GC pacer tries to stay near) with the OS
// hard cap (Linux RLIMIT_AS via prlimit; no-op on darwin /
// windows so the package still compiles).
//
// Design note: GOMEMLIMIT is a soft target — the runtime can
// exceed it on allocation spikes. RLIMIT_AS is the hard cap.
// Setting GOMEMLIMIT to ~87% of the hard cap gives the GC pacer
// room to react before the kernel kills the process. The 13%
// headroom rule comes from the Weaviate GOMEMLIMIT post and the
// KimMachineGun/automemlimit defaults; Go runtime proposal
// golang/go#75164 tracks making GOMEMLIMIT cgroup-aware so that
// once it lands we can drop the explicit prlimit call. Until
// then we set both.
//
// Spec: specs/oneshot-production-hardening.md §T1.2.
package oneshot

import (
	"fmt"
	"runtime/debug"
)

// HeadroomRatio is the fraction of the hard ceiling we feed to
// debug.SetMemoryLimit. 0.87 = 13% headroom, picked so the GC
// pacer kicks in well before the kernel sends SIGKILL. Exported
// so unit tests can pin the value.
const HeadroomRatio = 0.87

// applyMemoryLimit wires the requested ceiling into both the Go
// runtime (soft, via debug.SetMemoryLimit) and the OS (hard, via
// applyOSMemoryLimit). Returns the prlimit error wrapped on
// Linux; nil elsewhere. maxMemMiB is in mebibytes (MiB, 1<<20).
//
// Pre-condition: caller has range-checked maxMemMiB into the
// [32, 16384] band — the CLI does that at flag-parse time.
func applyMemoryLimit(maxMemMiB int) error {
	hardBytes := uint64(maxMemMiB) << 20
	softBytes := int64(float64(hardBytes) * HeadroomRatio)
	debug.SetMemoryLimit(softBytes)
	if err := applyOSMemoryLimit(hardBytes); err != nil {
		return fmt.Errorf("oneshot: applyOSMemoryLimit: %w", err)
	}
	return nil
}
