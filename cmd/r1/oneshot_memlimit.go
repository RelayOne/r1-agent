// oneshot_memlimit.go — CLI-side wrapper around the
// internal/oneshot memory limit helper. The internal helper is
// package-private (it's an implementation detail of the verb
// runtime); this file is the indirection point so the CLI can
// invoke it without exporting platform-specific symbols.
//
// We re-implement the very thin glue here rather than exporting
// the package-private symbol because cmd/r1 already imports
// internal/oneshot for the rest of the public surface; adding
// an exported "ApplyMemoryLimit" to the package would force
// every alternate caller to deal with the cross-platform
// build-tag dance. Keeping the dance contained to internal/
// oneshot and replicating ~6 lines here is the cleaner split.
//
// Spec: specs/oneshot-production-hardening.md §T1.3.
package main

import (
	"fmt"
	"runtime/debug"

	"github.com/RelayOne/r1/internal/oneshot"
)

// applyMemLimitForCLI wires the requested ceiling into both the
// Go runtime (debug.SetMemoryLimit, soft) and the OS (via the
// platform-specific applyOSMemLimit, hard). Returns wrapped
// errors so the caller can route them into the
// oneshot.EventMemoryLimitHit stderr envelope.
//
// HeadroomRatio is sourced from internal/oneshot so the doc-
// test and the unit test agree on the constant.
func applyMemLimitForCLI(maxMemMiB int) error {
	hardBytes := uint64(maxMemMiB) << 20
	softBytes := int64(float64(hardBytes) * oneshot.HeadroomRatio)
	debug.SetMemoryLimit(softBytes)
	if err := applyOSMemLimit(hardBytes); err != nil {
		return fmt.Errorf("oneshot: applyOSMemoryLimit: %w", err)
	}
	return nil
}
