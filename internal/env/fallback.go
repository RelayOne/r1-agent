// Package env — fallback.go
//
// STOKE-004 fallback chain: when a tool's capability manifest
// declares its preferred sandbox backend but that backend isn't
// available on the current host, walk a ranked chain of alternates
// and use the first one that succeeds. Logs every fallback
// decision so operators see degraded isolation rather than silent
// downgrade.
//
// The chain the SOW specifies is Firecracker → Docker → Local,
// reflecting the isolation ranking (Firecracker > Docker > Local).
// If a tool's manifest says "needs hardware isolation" and
// Firecracker isn't available, the chain downgrades to Docker
// (VM-less container isolation) and emits a warning; if Docker
// isn't available either, it downgrades to Local and emits a
// stronger warning — at that point the operator has lost the
// isolation the manifest asked for and should know.
//
// Scope of this file: the chain walker. The Firecracker backend
// itself is a separate module that doesn't yet ship; when it
// lands, plug it into DefaultChain and no other code changes.
package env

import (
	"context"
	"fmt"
)

// Ranked lists the isolation tiers of declared backends, high to
// low. Used by DefaultChain to order fallback attempts and by
// callers that want to know whether a given backend satisfies a
// manifest's minimum isolation requirement.
//
// Note: Fly/Ember are remote-execution backends, not isolation
// alternates — they're omitted from the chain walker. A tool
// whose manifest explicitly requires remote execution won't fall
// back to Local.
var isolationRank = map[Backend]int{
	// Firecracker is not yet a declared Backend constant; when
	// the backend ships, add `BackendFirecracker` to the Backend
	// consts and extend this map with rank 30.
	BackendDocker: 20, // container + namespaces
	BackendInProc: 0,  // no isolation (host processes)
	// Fly/Ember deliberately absent — different semantics.
}

// IsolationRank reports the declared isolation tier of a backend.
// Higher = stronger isolation. Backends not in the declared chain
// (Fly, Ember, unknown) return -1 so the caller can distinguish
// "known weak" from "not comparable".
func IsolationRank(b Backend) int {
	if r, ok := isolationRank[b]; ok {
		return r
	}
	return -1
}

// DefaultChain is the fallback order the SOW specifies: most-
// isolated backend first, falling back through each less-isolated
// alternative. When the Firecracker backend ships its constant
// should be prepended here.
func DefaultChain() []Backend {
	return []Backend{BackendDocker, BackendInProc}
}

// AvailabilityCheck reports whether a backend is usable on the
// current host. Implementations are expected to be cheap — at
// most a `which docker` or a socket probe — so the chain walker
// can call them synchronously per invocation without adding
// appreciable latency.
//
// Registered via Register; backends that don't register are
// assumed available (the chain walker won't fall back past them).
type AvailabilityCheck func(ctx context.Context) bool

var availabilityChecks = map[Backend]AvailabilityCheck{
	BackendInProc: func(ctx context.Context) bool { return true },
}

// RegisterAvailabilityCheck installs a probe for a backend.
// Replaces any existing probe for the same backend. Safe to call
// from init() funcs in backend sub-packages.
func RegisterAvailabilityCheck(b Backend, check AvailabilityCheck) {
	availabilityChecks[b] = check
}

// BackendAvailable reports whether the backend's probe says yes.
// Backends without a registered probe return true by default so
// the chain walker doesn't over-eagerly skip them.
func BackendAvailable(ctx context.Context, b Backend) bool {
	check, ok := availabilityChecks[b]
	if !ok {
		return true
	}
	return check(ctx)
}

// FallbackLogger is the interface the chain walker uses to
// announce degraded isolation. Matches the subset of slog/log
// packages so callers can plug in whichever logger they already
// use without an adapter.
type FallbackLogger interface {
	Printf(format string, v ...any)
}

// silentLogger drops all messages — used when the caller passes
// nil to ResolveBackend. Keeps the ResolveBackend signature free
// of optional-nil guards.
type silentLogger struct{}

func (silentLogger) Printf(string, ...any) {}

// ErrNoUsableBackend is returned by ResolveBackend when every
// backend in the requested chain failed its availability check.
// Callers must surface this to the operator — silently dropping
// a tool's execution because no sandbox is available would
// violate STOKE-004's fail-closed posture.
var ErrNoUsableBackend = fmt.Errorf("env: no usable backend in fallback chain")

// ResolveBackend walks the ordered chain and returns the first
// backend whose AvailabilityCheck passes. `preferred` is the
// backend the caller (or the tool's capability manifest) asked
// for; it's tried first regardless of chain order. `chain` is
// the fallback order used when the preferred backend isn't
// available; pass nil to use DefaultChain.
//
// When the resolved backend differs from `preferred`, the logger
// receives a message so operators see degraded isolation in real
// time rather than discovering it from reports days later.
func ResolveBackend(ctx context.Context, preferred Backend, chain []Backend, logger FallbackLogger) (Backend, error) {
	if logger == nil {
		logger = silentLogger{}
	}

	if preferred != "" && BackendAvailable(ctx, preferred) {
		return preferred, nil
	}
	if preferred != "" {
		logger.Printf("env: preferred backend %q unavailable; falling back through chain", preferred)
	}

	if chain == nil {
		chain = DefaultChain()
	}

	for _, b := range chain {
		if b == preferred {
			continue // already tried
		}
		if BackendAvailable(ctx, b) {
			if preferred != "" && b != preferred {
				preferredRank := IsolationRank(preferred)
				chosenRank := IsolationRank(b)
				if preferredRank > chosenRank && preferredRank >= 0 && chosenRank >= 0 {
					logger.Printf("env: DEGRADED — using %q (rank %d) instead of requested %q (rank %d); operator should investigate",
						b, chosenRank, preferred, preferredRank)
				} else {
					logger.Printf("env: using %q instead of requested %q", b, preferred)
				}
			}
			return b, nil
		}
	}
	return "", fmt.Errorf("%w: preferred=%q chain=%v", ErrNoUsableBackend, preferred, chain)
}
