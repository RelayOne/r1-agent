//go:build !linux

// memlimit_other.go — no-op fallback for non-Linux builds
// (darwin, windows, freebsd, etc.). The Go runtime soft cap
// from debug.SetMemoryLimit still applies on every platform;
// only the hard OS-side RLIMIT_AS is Linux-specific.
//
// The exported applyOSMemoryLimit signature stays identical so
// the cross-platform applyMemoryLimit wiring in memlimit.go
// compiles on every supported target. Operators running r1 on
// a non-Linux host see Go's GC pacer as their only ceiling —
// document that in docs/integrations/relaygate-r1-stage.md.
//
// Spec: specs/oneshot-production-hardening.md §T1.2.
package oneshot

// applyOSMemoryLimit is a deliberate no-op on non-Linux builds.
// The compiler is satisfied; the runtime's soft cap continues
// to apply through debug.SetMemoryLimit.
func applyOSMemoryLimit(bytes uint64) error {
	_ = bytes
	return nil
}
