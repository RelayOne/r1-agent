//go:build !linux

// oneshot_memlimit_other.go — no-op fallback for non-Linux
// platforms. The Go runtime soft cap from debug.SetMemoryLimit
// still applies; only the OS-side hard cap is Linux-specific.
//
// Spec: specs/oneshot-production-hardening.md §T1.3.
package main

// applyOSMemLimit is a deliberate no-op outside Linux.
func applyOSMemLimit(bytes uint64) error {
	_ = bytes
	return nil
}
